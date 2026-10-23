package httpapi

import (
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/seanyeh77/cf-portal/internal/analysis"
	"github.com/seanyeh77/cf-portal/internal/dnsrec"
	"github.com/seanyeh77/cf-portal/internal/loop"
	"github.com/seanyeh77/cf-portal/internal/model"
	"github.com/seanyeh77/cf-portal/internal/store"
)

// Bulk operations take the selected record ids and report one result per
// record. Each change is audited per record, like its single-record form,
// so it shows in that record's history and can be undone on its own.

func requireIDs(ids []string) error {
	switch {
	case len(ids) == 0:
		return httpErr{http.StatusUnprocessableEntity, "未選擇任何 record"}
	case len(ids) > maxBatch:
		return httpErr{http.StatusUnprocessableEntity, "一次最多處理 500 筆"}
	}
	return nil
}

func countOK(results []itemResult) int {
	n := 0
	for _, r := range results {
		if r.OK {
			n++
		}
	}
	return n
}

// bulkEach runs f for every id with the current record, concurrently up to
// cfWorkers, and collects the results.
func (s *Server) bulkEach(r *http.Request, ids []string, f func(rec model.Record) error) ([]itemResult, error) {
	recs, _, err := s.CF.List(r.Context(), true)
	if err != nil {
		return nil, err
	}
	byID := map[string]model.Record{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}
	results := make([]itemResult, len(ids))
	loop.ForEach(len(ids), cfWorkers, func(i int) {
		rec, ok := byID[ids[i]]
		res := itemResult{ID: ids[i], Name: rec.Name}
		switch {
		case !ok:
			res.Name, res.Error = ids[i], "record 已不存在"
		default:
			if err := f(rec); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
		}
		results[i] = res
	})
	return results, nil
}

func writeBulk(w http.ResponseWriter, results []itemResult, extra map[string]any) {
	ok := countOK(results)
	out := map[string]any{"results": results, "ok": ok, "failed": len(results) - ok}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
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
	byID := map[string]model.Record{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}
	zone := slices.Clone(recs)
	plan := make([]dnsChange, len(ids))
	for i, id := range ids {
		cur, ok := byID[id]
		c := dnsChange{ID: id, Name: cur.Name, Before: cur.Input(), Changed: []string{}}
		plan[i] = c
		if !ok {
			plan[i].Name, plan[i].Error = id, "record 已不存在"
			continue
		}
		if !dnsrec.Editable[cur.Type] {
			plan[i].Skipped = cur.Type + " 請於 Cloudflare 編輯"
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
			plan[i].Error = err.Error()
			continue
		}
		plan[i].Changed = model.ChangedFields(in, c.Before)
		if len(plan[i].Changed) == 0 {
			plan[i].Skipped = "無需變更"
			if len(na) > 0 {
				plan[i].Skipped = cur.Type + " 不適用" + strings.Join(na, "、")
			}
			continue
		}
		if err := dnsrec.Conflict(in, zone, id); err != nil {
			plan[i].Error = err.Error()
			continue
		}
		plan[i].After = &in
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
	recs, _, err := s.CF.List(r.Context(), true)
	if err != nil {
		return err
	}
	plan := s.planDNS(recs, b.IDs, b.Set)
	if b.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{"results": plan})
		return nil
	}
	info, _, err := s.Maint.Snapshot(r.Context(), "manual")
	if err != nil {
		return httpErr{http.StatusBadGateway, "無法建立快照，已取消變更：" + err.Error()}
	}
	s.auditLog(r, "snapshot_create", info.Name, map[string]any{"reason": "bulk_dns", "count": info.Count})
	byID := map[string]model.Record{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}
	loop.ForEach(len(plan), cfWorkers, func(i int) {
		c := &plan[i]
		if c.After == nil {
			return
		}
		// No meta change: before and after are both empty.
		if _, _, err := s.writeUpdate(r, byID[c.ID], *c.After, model.Meta{}, model.Meta{}, "record_update"); err != nil {
			c.Error = err.Error()
		} else {
			c.OK = true
		}
	})
	ok, failed := 0, 0
	for _, c := range plan {
		if c.OK {
			ok++
		} else if c.Error != "" {
			failed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": plan, "ok": ok, "failed": failed, "snapshot": info.Name})
	return nil
}

// ---- connection checks and issues ----

func (s *Server) apiBulkScan(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	results, err := s.bulkEach(r, b.IDs, func(rec model.Record) error {
		s.Scanner.ScanSoon(rec)
		return nil
	})
	if err != nil {
		return err
	}
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
	results, err := s.bulkEach(r, b.IDs, func(rec model.Record) error {
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
		return s.putMeta(r, rec, before, after, nil)
	})
	if err != nil {
		return err
	}
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
	items := map[string]int{}
	for i, id := range ids {
		items[id] = i
	}
	ctx := r.Context()
	results, err := s.bulkEach(r, ids, func(rec model.Record) error {
		it := b.Items[items[rec.ID]]
		if prev, err := s.Store.Listing(ctx, rec.ID); err == nil && prev != nil {
			return httpErr{http.StatusConflict, "已上架"}
		} else if err != nil && err != store.ErrNotFound {
			return err
		}
		_, err := s.saveListing(r, rec, nil, listingBody{Title: it.Title, URL: it.URL, Category: b.Category, Tags: b.Tags, Featured: b.Featured, Icon: b.Icon})
		return err
	})
	if err != nil {
		return err
	}
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
	add, remove := splitTagList(b.AddTags), splitTagList(b.RemoveTags)
	if b.Category == nil && b.Featured == nil && len(add)+len(remove) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "請勾選要修改的欄位"}
	}
	ctx := r.Context()
	results, err := s.bulkEach(r, b.IDs, func(rec model.Record) error {
		prev, err := s.Store.Listing(ctx, rec.ID)
		if err != nil {
			return httpErr{http.StatusNotFound, "尚未上架"}
		}
		body := listingBody{Title: prev.Title, Description: prev.Description, Category: prev.Category, SortOrder: prev.SortOrder,
			Featured: prev.Featured, Icon: prev.Icon}
		if prev.URL != nil {
			body.URL = *prev.URL
		}
		tags := splitTags(prev.Tags)
		tags = slices.DeleteFunc(tags, func(t string) bool { return slices.Contains(remove, t) })
		for _, t := range add {
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
		body.Tags = tags
		if b.Category != nil {
			body.Category = *b.Category
		}
		if b.Featured != nil {
			body.Featured = *b.Featured
		}
		_, err = s.saveListing(r, rec, prev, body)
		return err
	})
	if err != nil {
		return err
	}
	writeBulk(w, results, nil)
	return nil
}

func (s *Server) apiBulkListingDelete(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if err := requireIDs(b.IDs); err != nil {
		return err
	}
	// A listing may outlive its record, so this does not need the zone.
	results := make([]itemResult, len(b.IDs))
	for i, id := range b.IDs {
		res := itemResult{ID: id, Name: id}
		if l, err := s.Store.Listing(r.Context(), id); err == nil {
			res.Name = l.Hostname
		}
		if err := s.removeListing(r, id); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		results[i] = res
	}
	writeBulk(w, results, nil)
	return nil
}

func splitTagList(in []string) []string {
	var out []string
	for _, t := range in {
		for _, p := range splitTags(t) {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// ---- requests ----

func (s *Server) apiBulkApprove(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs     []int64 `json:"ids"`
		Proxied bool    `json:"proxied"`
		Note    string  `json:"note"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if len(b.IDs) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "未選擇任何申請"}
	}
	// One at a time, so each approval sees the records the earlier ones created.
	results := make([]itemResult, len(b.IDs))
	for i, id := range b.IDs {
		res := itemResult{ID: strconv.FormatInt(id, 10)}
		q, err := s.pendingRequest(r.Context(), id)
		if err == nil {
			res.Name = q.Name
			in := q.Input()
			in.Proxied = b.Proxied && dnsrec.Proxiable[in.Type]
			_, err = s.approveRequest(r, q, in, requestMeta(q), b.Note)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		results[i] = res
	}
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
	if len(b.IDs) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "未選擇任何申請"}
	}
	if strings.TrimSpace(b.Note) == "" {
		return httpErr{http.StatusUnprocessableEntity, "請填寫駁回原因"}
	}
	results := make([]itemResult, len(b.IDs))
	for i, id := range b.IDs {
		res := itemResult{ID: strconv.FormatInt(id, 10)}
		q, err := s.pendingRequest(r.Context(), id)
		if err == nil {
			res.Name = q.Name
			err = s.rejectRequest(r, q, b.Note)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		results[i] = res
	}
	writeBulk(w, results, nil)
	return nil
}
