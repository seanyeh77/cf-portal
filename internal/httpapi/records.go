package httpapi

import (
	"context"
	"log"
	"maps"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/seanyeh77/cf-portal/internal/analysis"
	"github.com/seanyeh77/cf-portal/internal/assets"
	"github.com/seanyeh77/cf-portal/internal/dnsrec"
	"github.com/seanyeh77/cf-portal/internal/loop"
	"github.com/seanyeh77/cf-portal/internal/model"
	"github.com/seanyeh77/cf-portal/internal/store"
)

type listingOut struct {
	model.Listing
	EffectiveURL string `json:"effective_url"`
	IconURL      string `json:"icon_url"`
}

func toListingOut(l *model.Listing) *listingOut {
	if l == nil {
		return nil
	}
	return &listingOut{Listing: *l, EffectiveURL: l.EffectiveURL(), IconURL: iconURL(*l)}
}

type recordOut struct {
	model.Record
	Editable bool   `json:"editable"`
	Group    string `json:"group"`
	// Level is the row level: the most severe non-info issue, or "".
	Level string `json:"level"`
	// TargetKey is the canonical target of A/AAAA/CNAME records, for grouping.
	TargetKey string           `json:"target_key"`
	Listing   *listingOut      `json:"listing"`
	Meta      model.Meta       `json:"meta"`
	Scan      *model.Scan      `json:"scan"`
	Issues    []analysis.Issue `json:"issues"`
	Muted     []analysis.Issue `json:"muted"`
}

// zoneState is everything known about the zone at one moment.
type zoneState struct {
	Records  []model.Record
	SyncedAt time.Time
	Meta     map[string]model.Meta
	Scans    map[string]model.Scan
	Mutes    map[string]map[string]bool
	Settings map[string]string
	Listings map[string]model.Listing
	Analysis *analysis.Result
}

func analysisSettings(set map[string]string) analysis.Settings {
	return analysis.Settings{ZombieDays: store.SettingInt(set, "zombie_days"), ExpiryWarnDays: store.SettingInt(set, "expiry_warn_days"), NameRule: set["name_rule"]}
}

func (s *Server) loadZone(ctx context.Context, fresh bool) (*zoneState, error) {
	recs, at, err := s.CF.List(ctx, fresh)
	if err != nil {
		return nil, err
	}
	z := &zoneState{Records: recs, SyncedAt: at, Settings: s.settings(ctx), Listings: map[string]model.Listing{}}
	if z.Meta, err = s.Store.AllMeta(ctx); err != nil {
		return nil, err
	}
	if z.Scans, err = s.Store.AllScans(ctx); err != nil {
		return nil, err
	}
	if z.Mutes, err = s.Store.Mutes(ctx); err != nil {
		return nil, err
	}
	ls, err := s.Store.Listings(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range ls {
		z.Listings[l.RecordID] = l
	}
	z.Analysis = analysis.Analyze(analysis.Input{Zone: s.Cfg.CFZoneName, Records: recs, Meta: z.Meta, Scans: z.Scans, Mutes: z.Mutes,
		Settings: analysisSettings(z.Settings), Now: s.Now(), Loc: s.Cfg.Timezone})
	return z, nil
}

func (z *zoneState) out(r model.Record) recordOut {
	o := recordOut{Record: r, Editable: dnsrec.Editable[r.Type], TargetKey: analysis.TargetKey(r), Meta: z.Meta[r.ID], Issues: []analysis.Issue{}, Muted: []analysis.Issue{}}
	if ar := z.Analysis.Records[r.ID]; ar != nil {
		o.Issues, o.Muted, o.Group, o.Level = ar.Issues, ar.Muted, ar.Group, ar.Level()
	}
	if l, ok := z.Listings[r.ID]; ok {
		o.Listing = toListingOut(&l)
	}
	if sc, ok := z.Scans[r.ID]; ok {
		o.Scan = &sc
	}
	return o
}

func (s *Server) apiRecords(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	z, err := s.loadZone(ctx, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		return err
	}
	out := make([]recordOut, 0, len(z.Records))
	present := map[string]bool{}
	for _, rec := range z.Records {
		present[rec.ID] = true
		if l, ok := z.Listings[rec.ID]; ok && !strings.EqualFold(l.Hostname, rec.Name) {
			if err := s.Store.SetListingHostname(ctx, rec.ID, rec.Name); err == nil {
				l.Hostname = rec.Name
				z.Listings[rec.ID] = l
			}
		}
		out = append(out, z.out(rec))
	}
	orphans := []map[string]any{}
	for id, l := range z.Listings {
		if !present[id] {
			orphans = append(orphans, map[string]any{"record_id": id, "hostname": l.Hostname, "title": l.Title})
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i]["hostname"].(string) < orphans[j]["hostname"].(string) })
	pending, _ := s.Store.PendingRequests(ctx)
	var lastSnap float64
	if t := s.Snaps.Latest(); !t.IsZero() {
		lastSnap = float64(t.Unix())
	}
	set := z.Settings
	writeJSON(w, http.StatusOK, map[string]any{
		"zone": s.Cfg.CFZoneName, "today": s.today(), "synced_at": float64(z.SyncedAt.Unix()), "now": float64(s.Now().Unix()),
		"records": out, "orphans": orphans, "summary": z.Analysis.Summary,
		"kinds": analysis.Kinds, "groups": z.Analysis.Groups,
		"scan": s.Scanner.Status(), "pending_requests": pending, "last_snapshot": lastSnap,
		"settings": map[string]string{"zombie_days": set["zombie_days"], "expiry_warn_days": set["expiry_warn_days"], "scan_ports": set["scan_ports"],
			"name_rule_hint": set["name_rule_hint"], "name_rule": set["name_rule"], "requests_enabled": set["requests_enabled"]},
	})
	return nil
}

// ---- meta validation ----

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func cleanMeta(m model.Meta) (model.Meta, error) {
	m.Owner = trimLen(m.Owner, 80)
	m.Purpose = trimLen(m.Purpose, 300)
	m.Notes = strings.TrimSpace(m.Notes)
	if len([]rune(m.Notes)) > 2000 {
		return m, invalidf("備註最多 2000 字")
	}
	m.ExpiresAt = strings.TrimSpace(m.ExpiresAt)
	if m.ExpiresAt != "" && !validDate(m.ExpiresAt) {
		return m, invalidf("到期日格式應為 YYYY-MM-DD")
	}
	if m.AutoDelete && m.ExpiresAt == "" {
		return m, invalidf("自動刪除需設定到期日")
	}
	return m, nil
}

// ---- shared create / update / meta steps ----

func (s *Server) checkConflict(ctx context.Context, in model.RecordInput, exclude string) error {
	recs, _, err := s.CF.List(ctx, true)
	if err != nil {
		return err
	}
	return dnsrec.Conflict(in, recs, exclude)
}

// prepareCreate validates a new record and its meta. Conflicts are checked
// separately, against the zone or (in a batch) the zone plus earlier rows.
func (s *Server) prepareCreate(in model.RecordInput, meta model.Meta) (model.RecordInput, model.Meta, error) {
	if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
		return in, meta, err
	}
	meta, err := cleanMeta(meta)
	return in, meta, err
}

// commitCreate creates a prepared record, stores its meta and audits it.
// The caller queues the connection check once all writes are done.
func (s *Server) commitCreate(r *http.Request, in model.RecordInput, meta model.Meta, action string, extra map[string]any) (*model.Record, error) {
	ctx, err := writeContext(r)
	if err != nil {
		return nil, err
	}
	rec, err := s.CF.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := s.Store.PutMeta(ctx, rec.ID, meta); err != nil {
		log.Printf("put meta %s: %v", rec.Name, err)
	}
	d := map[string]any{"record_id": rec.ID, "after": rec.Input(), "meta": meta}
	maps.Copy(d, extra)
	s.auditLog(r, action, rec.Name, d)
	return rec, nil
}

// createRecord prepares, conflict-checks, commits and queues a connection
// check for one record, reporting whether a check was queued. newName
// applies the naming rule; restores pass false to bring back old names.
func (s *Server) createRecord(r *http.Request, in model.RecordInput, meta model.Meta, newName bool, action string, extra map[string]any) (*model.Record, bool, error) {
	in, meta, err := s.prepareCreate(in, meta)
	if err == nil && newName {
		err = s.checkNameRule(r.Context(), in.Name)
	}
	if err == nil {
		err = s.checkConflict(r.Context(), in, "")
	}
	if err != nil {
		return nil, false, err
	}
	rec, err := s.commitCreate(r, in, meta, action, extra)
	if err != nil {
		return nil, false, err
	}
	return rec, s.Scanner.ScanSoon(*rec), nil
}

// updateRecord writes a validated DNS change and the new meta, audits both
// as action and then queues the checks. The local meta is written first and
// rolled back if Cloudflare rejects the change, so a failure leaves nothing
// changed. Once writing starts, client disconnects are ignored so the
// sequence is never abandoned halfway.
func (s *Server) updateRecord(r *http.Request, cur model.Record, in model.RecordInput, metaBefore, metaAfter model.Meta, action string) (*model.Record, bool, error) {
	if err := s.checkConflict(r.Context(), in, cur.ID); err != nil {
		return nil, false, err
	}
	ctx, err := writeContext(r)
	if err != nil {
		return nil, false, err
	}
	metaChanged := metaAfter != metaBefore
	if metaChanged {
		if err := s.Store.PutMeta(ctx, cur.ID, metaAfter); err != nil {
			return nil, false, err
		}
	}
	rec, err := s.CF.Update(ctx, cur.ID, in)
	if err != nil {
		if metaChanged {
			s.rollbackMeta(ctx, r, cur, metaBefore, metaAfter)
		}
		return nil, false, err
	}
	d := map[string]any{"record_id": cur.ID, "before": cur.Input(), "after": rec.Input()}
	if metaChanged {
		d["meta_before"], d["meta_after"] = metaBefore, metaAfter
	}
	if !strings.EqualFold(cur.Name, rec.Name) {
		s.Store.SetListingHostname(ctx, cur.ID, rec.Name)
	}
	s.auditLog(r, action, rec.Name, d)
	if l, err := s.Store.Listing(ctx, cur.ID); err == nil {
		s.Health.CheckSoon(*l)
	}
	return rec, s.Scanner.ScanSoon(*rec), nil
}

// rollbackMeta restores metaBefore after Cloudflare rejected an update,
// unless the meta changed again meanwhile. If the restore fails, the meta
// change stays and is audited so the log matches the data.
func (s *Server) rollbackMeta(ctx context.Context, r *http.Request, cur model.Record, metaBefore, metaAfter model.Meta) {
	if _, err := s.Store.RestoreMeta(ctx, cur.ID, metaAfter, metaBefore); err != nil {
		log.Printf("roll back meta %s: %v", cur.Name, err)
		s.auditMeta(r, cur, metaBefore, metaAfter, map[string]any{"note": "DNS 更新失敗，管理資訊未能還原"})
	}
}

// writeContext returns the context for a multi-step write: the request must
// still be live when writing starts, but a disconnect afterwards must not
// abandon the sequence halfway. Outbound calls keep their own timeouts.
func writeContext(r *http.Request) (context.Context, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	return context.WithoutCancel(r.Context()), nil
}

// auditMeta writes a meta_update audit entry.
func (s *Server) auditMeta(r *http.Request, rec model.Record, before, after model.Meta, extra map[string]any) {
	d := map[string]any{"record_id": rec.ID, "meta_before": before, "meta_after": after}
	maps.Copy(d, extra)
	s.auditLog(r, "meta_update", rec.Name, d)
}

// putMeta replaces a record's meta and audits the change as meta_update.
func (s *Server) putMeta(r *http.Request, rec model.Record, before, after model.Meta, extra map[string]any) error {
	if after == before {
		return nil
	}
	if err := s.Store.PutMeta(r.Context(), rec.ID, after); err != nil {
		return err
	}
	s.auditMeta(r, rec, before, after, extra)
	return nil
}

// reenabled reports whether a meta change turned the connection check back
// on, which needs a fresh scan.
func reenabled(before, after model.Meta) bool { return before.NoScan && !after.NoScan }

// setMeta is a nextMeta change that replaces the meta with m.
func setMeta(m model.Meta) func(model.Meta) model.Meta {
	return func(model.Meta) model.Meta { return m }
}

// nextMeta loads a record's meta and returns it with change (nil keeps it)
// applied and validated.
func (s *Server) nextMeta(ctx context.Context, id string, change func(model.Meta) model.Meta) (before, after model.Meta, err error) {
	if before, err = s.Store.Meta(ctx, id); err != nil {
		return
	}
	after = before
	if change != nil {
		after = change(before)
	}
	after, err = cleanMeta(after)
	return
}

// ---- create / update / delete ----

// recordBody is a record to create, with optional meta.
type recordBody struct {
	model.RecordInput
	Meta model.Meta `json:"meta"`
}

// checkNameRule refuses a (validated) name that breaks the naming rule. It
// applies to new names only; existing and restored names may keep breaking
// it, and batch imports only warn.
func (s *Server) checkNameRule(ctx context.Context, name string) error {
	if msg := dnsrec.NameRuleError(name, s.Cfg.CFZoneName, s.settings(ctx)["name_rule"]); msg != "" {
		return invalidf("名稱格式不符：" + msg)
	}
	return nil
}

func (s *Server) apiCreateRecord(w http.ResponseWriter, r *http.Request) error {
	var b recordBody
	if err := decode(w, r, &b); err != nil {
		return err
	}
	rec, scanning, err := s.createRecord(r, b.RecordInput, b.Meta, true, "record_create", nil)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"record": rec, "scanning": scanning})
	return nil
}

func (s *Server) apiUpdateRecord(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	ctx := r.Context()
	cur, err := s.CF.Get(ctx, id)
	if err != nil {
		return err
	}
	before := cur.Input()
	// Decoding over the current values changes only the fields sent. The
	// priority is copied first: JSON decodes into an existing pointer.
	b := struct {
		model.RecordInput
		Meta *model.MetaPatch `json:"meta"`
	}{RecordInput: before}
	if before.Priority != nil {
		b.Priority = new(*before.Priority)
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	in := b.RecordInput
	dnsChanged := len(model.ChangedFields(in, before)) > 0
	if dnsChanged {
		if !dnsrec.Editable[cur.Type] {
			return httpErr{http.StatusUnprocessableEntity, cur.Type + " 請於 Cloudflare 編輯"}
		}
		if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
			return err
		}
		// Normalization can turn a sent change back into the current value.
		dnsChanged = len(model.ChangedFields(in, before)) > 0
		if !strings.EqualFold(in.Name, before.Name) {
			if err := s.checkNameRule(ctx, in.Name); err != nil {
				return err
			}
		}
	}
	var change func(model.Meta) model.Meta
	if b.Meta != nil {
		change = b.Meta.Apply
	}
	metaBefore, metaAfter, err := s.nextMeta(ctx, id, change)
	if err != nil {
		return err
	}
	rec, scanning := cur, false
	if dnsChanged {
		rec, scanning, err = s.updateRecord(r, *cur, in, metaBefore, metaAfter, "record_update")
	} else {
		if err = s.putMeta(r, *cur, metaBefore, metaAfter, nil); err == nil && reenabled(metaBefore, metaAfter) {
			scanning = s.Scanner.ScanSoon(*cur)
		}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec, "dns_changed": dnsChanged, "scanning": scanning})
	return nil
}

// forget removes local data of a deleted record, including icon files,
// and returns the listing that was removed.
func (s *Server) forget(ctx context.Context, id string) *model.Listing {
	l, err := s.Store.ForgetRecord(ctx, id)
	if err != nil {
		log.Printf("forget %s: %v", id, err)
		return nil
	}
	if l != nil {
		s.removeIconFile(ctx, l.IconFile)
	}
	return l
}

func (s *Server) removeIconFile(ctx context.Context, f *string) {
	if f == nil || *f == "" || strings.HasPrefix(*f, "si-") {
		return
	}
	if used, _ := s.Store.IconFileInUse(ctx, *f); !used {
		assets.Remove(s.Icons.Dir, *f)
	}
}

func (s *Server) deleteRecord(r *http.Request, rec model.Record, action string) error {
	ctx := r.Context()
	meta, _ := s.Store.Meta(ctx, rec.ID)
	if err := s.CF.Delete(ctx, rec.ID); err != nil {
		return err
	}
	d := map[string]any{"record_id": rec.ID, "before": rec.Input(), "meta": meta}
	if l := s.forget(ctx, rec.ID); l != nil {
		d["listing"] = l.Title
	}
	s.auditLog(r, action, rec.Name, d)
	return nil
}

func (s *Server) apiDeleteRecord(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.CF.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := s.deleteRecord(r, *rec, "record_delete"); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// cachedRecord finds a record in the (possibly cached) zone list.
func (s *Server) cachedRecord(ctx context.Context, id string) (*model.Record, error) {
	recs, _, err := s.CF.List(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// recordName returns a record's name for audit entries, or id if unknown.
func (s *Server) recordName(ctx context.Context, id string) string {
	if rec, err := s.cachedRecord(ctx, id); err == nil {
		return rec.Name
	}
	return id
}

func (s *Server) apiPutMeta(w http.ResponseWriter, r *http.Request) error {
	var p model.MetaPatch
	if err := decode(w, r, &p); err != nil {
		return err
	}
	return s.replaceMeta(w, r, r.PathValue("id"), p.Apply, nil)
}

// replaceMeta applies change to a record's meta and saves it. The record
// comes from the cached zone list; meta for unknown records is refused.
func (s *Server) replaceMeta(w http.ResponseWriter, r *http.Request, id string, change func(model.Meta) model.Meta, extra map[string]any) error {
	ctx := r.Context()
	rec, err := s.cachedRecord(ctx, id)
	if err != nil {
		return err
	}
	before, after, err := s.nextMeta(ctx, id, change)
	if err == nil {
		err = s.putMeta(r, *rec, before, after, extra)
	}
	if err != nil {
		return err
	}
	scanning := reenabled(before, after) && s.Scanner.ScanSoon(*rec)
	writeJSON(w, http.StatusOK, map[string]any{"meta": after, "scanning": scanning})
	return nil
}

func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	name := r.URL.Query().Get("name")
	if name == "" {
		name = s.recordName(r.Context(), id)
	}
	rows, err := s.Store.AuditForRecord(r.Context(), id, name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": rows})
	return nil
}

func (s *Server) apiBulkMeta(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs []string        `json:"ids"`
		Set model.MetaPatch `json:"set"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if len(b.IDs) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "未選擇任何 record"}
	}
	ctx := r.Context()
	all, err := s.Store.AllMeta(ctx)
	if err != nil {
		return err
	}
	befores := map[string]model.Meta{}
	afters := map[string]model.Meta{}
	for _, id := range b.IDs {
		after, err := cleanMeta(b.Set.Apply(all[id]))
		if err != nil {
			return err
		}
		befores[id], afters[id] = all[id], after
	}
	// One audit entry per changed record, so each record's history shows
	// it and it can be undone like a single meta change.
	changed := 0
	for _, id := range b.IDs {
		if afters[id] == befores[id] {
			continue
		}
		if err := s.Store.PutMeta(ctx, id, afters[id]); err != nil {
			return err
		}
		s.auditLog(r, "bulk_meta", s.recordName(ctx, id), map[string]any{"record_id": id, "meta_before": befores[id], "meta_after": afters[id]})
		changed++
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": changed})
	return nil
}

type itemResult struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) apiBulkDelete(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if len(b.IDs) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "未選擇任何 record"}
	}
	info, recs, err := s.Maint.Snapshot(r.Context(), "manual")
	if err != nil {
		return httpErr{http.StatusBadGateway, "無法建立快照，已取消刪除：" + err.Error()}
	}
	s.auditLog(r, "snapshot_create", info.Name, map[string]any{"reason": "bulk_delete", "count": info.Count})
	byID := map[string]model.Record{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}
	results := make([]itemResult, len(b.IDs))
	loop.ForEach(len(b.IDs), cfWorkers, func(i int) {
		id := b.IDs[i]
		rec, ok := byID[id]
		if !ok {
			results[i] = itemResult{ID: id, Name: id, Error: "record 已不存在"}
		} else if err := s.deleteRecord(r, rec, "record_delete"); err != nil {
			results[i] = itemResult{ID: id, Name: rec.Name, Error: err.Error()}
		} else {
			results[i] = itemResult{ID: id, Name: rec.Name, OK: true}
		}
	})
	okN := 0
	for _, res := range results {
		if res.OK {
			okN++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "deleted": okN, "failed": len(b.IDs) - okN, "snapshot": info.Name})
	return nil
}

func (s *Server) apiMute(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		RecordID string `json:"record_id"`
		Kind     string `json:"kind"`
		Muted    bool   `json:"muted"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if _, ok := analysis.KindByKey[b.Kind]; !ok || b.RecordID == "" {
		return httpErr{http.StatusUnprocessableEntity, "未知的項目"}
	}
	if err := s.Store.SetMute(r.Context(), b.RecordID, b.Kind, b.Muted); err != nil {
		return err
	}
	action := "issue_mute"
	if !b.Muted {
		action = "issue_unmute"
	}
	s.auditLog(r, action, s.recordName(r.Context(), b.RecordID), map[string]any{"record_id": b.RecordID, "kind": b.Kind, "label": analysis.KindByKey[b.Kind].Label})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) apiScan(w http.ResponseWriter, r *http.Request) error {
	started := s.Scanner.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "scan": s.Scanner.Status()})
	return nil
}

func (s *Server) apiScanOne(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.CF.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	s.Scanner.ScanSoon(*rec)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
	return nil
}
