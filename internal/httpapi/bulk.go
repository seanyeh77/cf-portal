package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/seanyeh77/cf-portal/internal/analysis"
	"github.com/seanyeh77/cf-portal/internal/dnsrec"
	"github.com/seanyeh77/cf-portal/internal/loop"
	"github.com/seanyeh77/cf-portal/internal/model"
	"github.com/seanyeh77/cf-portal/internal/snapshots"
	"github.com/seanyeh77/cf-portal/internal/store"
)

// Bulk operations take the selected ids and report one result per item.
// Each change is audited per record, like its single-record form, so it
// shows in that record's history and can be undone on its own.

type itemResult struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func requireIDs[T any](ids []T) error {
	switch {
	case len(ids) == 0:
		return httpErr{http.StatusUnprocessableEntity, "未選擇任何項目"}
	case len(ids) > maxBatch:
		return httpErr{http.StatusUnprocessableEntity, "一次最多處理 500 筆"}
	}
	return nil
}

// collect runs f for items 0..n-1, up to workers at a time. f names the
// item on res; its error, if any, marks the item failed.
func collect(n, workers int, f func(i int, res *itemResult) error) []itemResult {
	out := make([]itemResult, n)
	loop.ForEach(n, workers, func(i int) {
		if err := f(i, &out[i]); err != nil {
			out[i].Error = err.Error()
		} else {
			out[i].OK = true
		}
	})
	return out
}

func recordsByID(recs []model.Record) map[string]model.Record {
	m := make(map[string]model.Record, len(recs))
	for _, rec := range recs {
		m[rec.ID] = rec
	}
	return m
}

// bulkEach runs f for the record of every id (the i-th), looked up in recs,
// concurrently up to cfWorkers.
func bulkEach(recs []model.Record, ids []string, f func(i int, rec model.Record) error) []itemResult {
	byID := recordsByID(recs)
	return collect(len(ids), cfWorkers, func(i int, res *itemResult) error {
		rec, ok := byID[ids[i]]
		res.ID, res.Name = ids[i], ids[i]
		if !ok {
			return errors.New("record 已不存在")
		}
		res.Name = rec.Name
		return f(i, rec)
	})
}

// okItems returns the items whose result succeeded; items[i] belongs to
// results[i].
func okItems[T any](results []itemResult, items []T) []T {
	var out []T
	for i, r := range results {
		if r.OK {
			out = append(out, items[i])
		}
	}
	return out
}

func writeBulk(w http.ResponseWriter, results []itemResult, extra map[string]any) {
	ok := 0
	for _, r := range results {
		if r.OK {
			ok++
		}
	}
	out := map[string]any{"results": results, "ok": ok, "failed": len(results) - ok}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// safetySnapshot saves the zone before a bulk change and returns the
// records it saved; without it the change is cancelled.
func (s *Server) safetySnapshot(r *http.Request, reason string) (snapshots.Info, []model.Record, error) {
	info, recs, err := s.Maint.Snapshot(r.Context(), "manual")
	if err != nil {
		return info, nil, httpErr{http.StatusBadGateway, "無法建立快照，已取消變更：" + err.Error()}
	}
	s.auditLog(r, "snapshot_create", info.Name, map[string]any{"reason": reason, "count": info.Count})
	return info, recs, nil
}

func (s *Server) apiBulkDelete(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	info, recs, err := s.safetySnapshot(r, "bulk_delete")
	if err != nil {
		return err
	}
	results := bulkEach(recs, b.IDs, func(_ int, rec model.Record) error { return s.deleteRecord(r, rec, "record_delete") })
	writeBulk(w, results, map[string]any{"snapshot": info.Name})
	return nil
}

// ---- DNS ----

type dnsSet struct {
	Content *string `json:"content"`
	Proxied *bool   `json:"proxied"`
	TTL     *int    `json:"ttl"`
}

// dnsChange is the plan for, and then the outcome of, one record.
type dnsChange struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Before  model.RecordInput  `json:"before"`
	After   *model.RecordInput `json:"after,omitempty"`
	Changed []string           `json:"changed"`
	// Skipped explains why the record is left as it is.
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	OK      bool   `json:"ok"`
	rec     model.Record
}

// targetType is the record type for a new target: A or AAAA for an
// address, CNAME for a host name.
func targetType(content string) string {
	if a, err := netip.ParseAddr(strings.TrimSpace(content)); err == nil {
		if a.Unmap().Is4() {
			return "A"
		}
		return "AAAA"
	}
	return "CNAME"
}

// planDNS works out each record's new values. Conflicts are checked against
// the zone with the earlier records of the plan already changed.
func (s *Server) planDNS(recs []model.Record, ids []string, set dnsSet) []dnsChange {
	byID := recordsByID(recs)
	zone := slices.Clone(recs)
	plan := make([]dnsChange, len(ids))
	for i, id := range ids {
		cur, ok := byID[id]
		c := &plan[i]
		*c = dnsChange{ID: id, Name: cur.Name, Before: cur.Input(), Changed: []string{}, rec: cur}
		if !ok {
			c.Name, c.Error = id, "record 已不存在"
			continue
		}
		if !dnsrec.Editable[cur.Type] {
			c.Skipped = cur.Type + " 請於 Cloudflare 編輯"
			continue
		}
		in := cur.Input()
		var na []string
		if set.Content != nil {
			if dnsrec.Proxiable[cur.Type] {
				in.Content = *set.Content
				in.Type = targetType(*set.Content)
			} else {
				na = append(na, "指向")
			}
		}
		if set.Proxied != nil {
			if dnsrec.Proxiable[in.Type] {
				in.Proxied = *set.Proxied
			} else {
				na = append(na, "Proxy")
			}
		}
		if set.TTL != nil {
			in.TTL = *set.TTL
		}
		if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
			c.Error = err.Error()
			continue
		}
		c.Changed = model.ChangedFields(in, c.Before)
		if len(c.Changed) == 0 {
			c.Skipped = "無需變更"
			if len(na) > 0 {
				c.Skipped = cur.Type + " 不適用" + strings.Join(na, "、")
			}
			continue
		}
		if err := dnsrec.Conflict(in, zone, id); err != nil {
			c.Error = err.Error()
			continue
		}
		c.After = &in
		for j := range zone {
			if zone[j].ID == id {
				zone[j].RecordInput = in
			}
		}
	}
	return plan
}

func (s *Server) apiBulkDNS(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs    []string `json:"ids"`
		Set    dnsSet   `json:"set"`
		DryRun bool     `json:"dry_run"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	if b.Set.Content == nil && b.Set.Proxied == nil && b.Set.TTL == nil {
		return httpErr{http.StatusUnprocessableEntity, "請勾選要修改的欄位"}
	}
	if b.DryRun {
		recs, _, err := s.CF.List(r.Context(), true)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": s.planDNS(recs, b.IDs, b.Set)})
		return nil
	}
	// Planned on the snapshot's records, so the plan matches what was saved.
	info, recs, err := s.safetySnapshot(r, "bulk_dns")
	if err != nil {
		return err
	}
	plan := s.planDNS(recs, b.IDs, b.Set)
	loop.ForEach(len(plan), cfWorkers, func(i int) {
		c := &plan[i]
		if c.After == nil {
			return
		}
		// No meta change: before and after are both empty.
		rec, err := s.writeUpdate(r, c.rec, *c.After, model.Meta{}, model.Meta{}, "record_update")
		if err != nil {
			c.Error = err.Error()
		} else {
			c.OK, c.rec = true, *rec
		}
	})
	var changed []model.Record
	failed := 0
	for _, c := range plan {
		if c.OK {
			changed = append(changed, c.rec)
		} else if c.Error != "" {
			failed++
		}
	}
	ok := len(changed)
	s.checkAfterWrite(r.Context(), changed...)
	writeJSON(w, http.StatusOK, map[string]any{"results": plan, "ok": ok, "failed": failed, "snapshot": info.Name})
	return nil
}

// ---- connection checks and issues ----

// idsBody decodes {"ids": [...]} and checks the count.
func idsBody(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &b); err != nil {
		return nil, err
	}
	return b.IDs, requireIDs(b.IDs)
}

// cachedRecords lists the zone, reusing a recent list: for bulk actions
// that do not write DNS.
func (s *Server) cachedRecords(ctx context.Context) ([]model.Record, error) {
	recs, _, err := s.CF.List(ctx, false)
	return recs, err
}

func (s *Server) apiBulkScan(w http.ResponseWriter, r *http.Request) error {
	ids, err := idsBody(w, r)
	if err != nil {
		return err
	}
	recs, err := s.cachedRecords(r.Context())
	if err != nil {
		return err
	}
	found := make([]model.Record, len(ids))
	results := bulkEach(recs, ids, func(i int, rec model.Record) error {
		found[i] = rec
		return nil
	})
	s.Scanner.ScanSoon(okItems(results, found)...)
	writeBulk(w, results, nil)
	return nil
}

func (s *Server) apiBulkMute(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs    []string `json:"ids"`
		Mute   []string `json:"mute"`
		Unmute []string `json:"unmute"`
		// Note is appended to each muted record's notes.
		Note string `json:"note"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	if len(b.Mute)+len(b.Unmute) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "請勾選要忽略或還原的項目"}
	}
	for _, k := range append(slices.Clone(b.Mute), b.Unmute...) {
		if _, ok := analysis.KindByKey[k]; !ok {
			return httpErr{http.StatusUnprocessableEntity, "未知的項目：" + k}
		}
	}
	note := trimLen(b.Note, 300)
	ctx := r.Context()
	z, err := s.loadZone(ctx, false)
	if err != nil {
		return err
	}
	// Only issues a record actually has are changed.
	has := func(id, kind string, muted bool) bool {
		rr := z.Analysis.Records[id]
		if rr == nil {
			return false
		}
		list := rr.Issues
		if muted {
			list = rr.Muted
		}
		return slices.ContainsFunc(list, func(i analysis.Issue) bool { return i.Kind == kind })
	}
	results := bulkEach(z.Records, b.IDs, func(_ int, rec model.Record) error {
		changed := false
		for _, set := range []struct {
			kinds []string
			muted bool
		}{{b.Mute, true}, {b.Unmute, false}} {
			for _, k := range set.kinds {
				if !has(rec.ID, k, !set.muted) {
					continue
				}
				if err := s.Store.SetMute(ctx, rec.ID, k, set.muted); err != nil {
					return err
				}
				action := map[bool]string{true: "issue_mute", false: "issue_unmute"}[set.muted]
				s.auditLog(r, action, rec.Name, map[string]any{"record_id": rec.ID, "kind": k, "label": analysis.KindByKey[k].Label})
				changed = changed || set.muted
			}
		}
		if note == "" || !changed {
			return nil
		}
		before, after, err := s.nextMeta(ctx, rec.ID, func(m model.Meta) model.Meta {
			if !strings.Contains(m.Notes, note) {
				m.Notes = strings.TrimSpace(m.Notes + "\n" + note)
			}
			return m
		})
		if err != nil {
			return err
		}
		return s.putMeta(r, "meta_update", rec, before, after, nil)
	})
	writeBulk(w, results, nil)
	return nil
}

// ---- portal listings ----

func (s *Server) apiBulkListingCreate(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Items []struct {
			RecordID string `json:"record_id"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"items"`
		Category string   `json:"category"`
		Tags     []string `json:"tags"`
		Featured bool     `json:"featured"`
		Icon     string   `json:"icon"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	ids := make([]string, len(b.Items))
	for i, it := range b.Items {
		ids[i] = it.RecordID
	}
	if err := requireIDs(ids); err != nil {
		return err
	}
	if b.Icon != "auto" && b.Icon != "none" {
		b.Icon = "auto"
	}
	ctx := r.Context()
	recs, err := s.cachedRecords(ctx)
	if err != nil {
		return err
	}
	saved := make([]model.Listing, len(ids))
	results := bulkEach(recs, ids, func(i int, rec model.Record) error {
		it := b.Items[i]
		if prev, err := s.Store.Listing(ctx, rec.ID); err == nil && prev != nil {
			return httpErr{http.StatusConflict, "已上架"}
		} else if err != nil && err != store.ErrNotFound {
			return err
		}
		l, _, err := s.saveListing(r, rec, nil, listingBody{Title: it.Title, URL: it.URL, Category: b.Category, Tags: b.Tags, Featured: b.Featured, Icon: b.Icon})
		saved[i] = l
		return err
	})
	s.Health.CheckSoon(okItems(results, saved)...)
	writeBulk(w, results, nil)
	return nil
}

func (s *Server) apiBulkListingUpdate(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs        []string `json:"ids"`
		Category   *string  `json:"category"`
		Featured   *bool    `json:"featured"`
		AddTags    []string `json:"add_tags"`
		RemoveTags []string `json:"remove_tags"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	add, remove := splitList(b.AddTags...), splitList(b.RemoveTags...)
	if b.Category == nil && b.Featured == nil && len(add)+len(remove) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "請勾選要修改的欄位"}
	}
	ctx := r.Context()
	recs, err := s.cachedRecords(ctx)
	if err != nil {
		return err
	}
	saved := make([]model.Listing, len(b.IDs))
	results := bulkEach(recs, b.IDs, func(i int, rec model.Record) error {
		prev, err := s.Store.Listing(ctx, rec.ID)
		if err != nil {
			return httpErr{http.StatusNotFound, "尚未上架"}
		}
		body := listingBody{Title: prev.Title, Description: prev.Description, Category: prev.Category, SortOrder: prev.SortOrder,
			Featured: prev.Featured, Icon: prev.Icon}
		if prev.URL != nil {
			body.URL = *prev.URL
		}
		tags := slices.DeleteFunc(slices.Clone(prev.Tags), func(t string) bool { return slices.Contains(remove, t) })
		body.Tags = append(tags, add...) // cleanListing drops duplicates
		if b.Category != nil {
			body.Category = *b.Category
		}
		if b.Featured != nil {
			body.Featured = *b.Featured
		}
		saved[i], _, err = s.saveListing(r, rec, prev, body)
		return err
	})
	s.Health.CheckSoon(okItems(results, saved)...)
	writeBulk(w, results, nil)
	return nil
}

func (s *Server) apiBulkListingDelete(w http.ResponseWriter, r *http.Request) error {
	ids, err := idsBody(w, r)
	if err != nil {
		return err
	}
	// A listing may outlive its record, so this does not need the zone.
	writeBulk(w, collect(len(ids), 1, func(i int, res *itemResult) error {
		res.ID, res.Name = ids[i], ids[i]
		if l, err := s.Store.Listing(r.Context(), ids[i]); err == nil {
			res.Name = l.Hostname
		}
		return s.removeListing(r, ids[i])
	}), nil)
	return nil
}

// ---- requests ----

// eachRequest runs f, one at a time, for every request that still awaits
// a decision.
func (s *Server) eachRequest(r *http.Request, ids []int64, f func(q *model.Request) error) []itemResult {
	return collect(len(ids), 1, func(i int, res *itemResult) error {
		res.ID = strconv.FormatInt(ids[i], 10)
		q, err := s.pendingRequest(r.Context(), ids[i])
		if err != nil {
			return err
		}
		res.Name = q.Name
		return f(q)
	})
}

func (s *Server) apiBulkApprove(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs     []int64 `json:"ids"`
		Proxied bool    `json:"proxied"`
		Note    string  `json:"note"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	// The zone is listed once; each approval adds its record to this copy so
	// later ones are checked against it.
	ctx := r.Context()
	zone, _, err := s.CF.List(ctx, true)
	if err != nil {
		return err
	}
	rule := s.nameRule(ctx)
	note := trimLen(b.Note, 500)
	var created []model.Record
	results := s.eachRequest(r, b.IDs, func(q *model.Request) error {
		in := q.Input()
		in.Proxied = b.Proxied && dnsrec.Proxiable[in.Type]
		in, meta, err := s.vetNew(in, requestMeta(q), true, rule, zone)
		if err != nil {
			return err
		}
		rec, err := s.commitCreate(r, in, meta, "request_approve", approvalAudit(q, note))
		if err != nil {
			return err
		}
		zone = append(zone, *rec)
		created = append(created, *rec)
		return s.markApproved(ctx, q, rec, note)
	})
	s.Scanner.ScanSoon(created...)
	writeBulk(w, results, nil)
	return nil
}

func (s *Server) apiBulkReject(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs  []int64 `json:"ids"`
		Note string  `json:"note"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	note := trimLen(b.Note, 500)
	if note == "" {
		return httpErr{http.StatusUnprocessableEntity, "請填寫駁回原因"}
	}
	writeBulk(w, s.eachRequest(r, b.IDs, func(q *model.Request) error {
		if err := s.Store.DecideRequest(r.Context(), q.ID, "rejected", note, nil); err != nil {
			return err
		}
		s.auditLog(r, "request_reject", q.Name, map[string]any{"request_id": q.ID, "note": note})
		return nil
	}), nil)
	return nil
}
