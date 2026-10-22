package httpapi

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/analysis"
	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/model"
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
	// TargetKey is the canonical target used by "targets" (A/AAAA/CNAME).
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

func atoiDef(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func analysisSettings(set map[string]string) analysis.Settings {
	return analysis.Settings{ZombieDays: atoiDef(set["zombie_days"], 7), ExpiryWarnDays: atoiDef(set["expiry_warn_days"], 14), NameRule: set["name_rule"]}
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
		o.Issues, o.Muted, o.Group = ar.Issues, ar.Muted, ar.Group
	}
	if l, ok := z.Listings[r.ID]; ok {
		o.Listing = toListingOut(&l)
	}
	if sc, ok := z.Scans[r.ID]; ok {
		o.Scan = &sc
	}
	return o
}

func (s *Server) apiRecords(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	z, err := s.loadZone(ctx, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		writeAnyErr(w, err)
		return
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
	kinds := map[string]analysis.Kind{}
	for _, k := range analysis.Kinds {
		kinds[k.Key] = k
	}
	pending, _ := s.Store.PendingRequests(ctx)
	var lastSnap float64
	if t := s.Snaps.Latest(); !t.IsZero() {
		lastSnap = float64(t.Unix())
	}
	set := z.Settings
	writeJSON(w, http.StatusOK, map[string]any{
		"zone": s.Cfg.CFZoneName, "today": s.today(), "synced_at": float64(z.SyncedAt.Unix()), "now": float64(s.Now().Unix()),
		"records": out, "orphans": orphans, "summary": z.Analysis.Summary, "muted_summary": z.Analysis.MutedSummary,
		"kinds": kinds, "kind_order": kindOrder(), "groups": z.Analysis.Groups, "targets": z.Analysis.Targets,
		"scan": s.Scanner.Status(), "pending_requests": pending, "last_snapshot": lastSnap,
		"settings": map[string]string{"zombie_days": set["zombie_days"], "expiry_warn_days": set["expiry_warn_days"], "scan_ports": set["scan_ports"],
			"name_rule_hint": set["name_rule_hint"], "name_rule": set["name_rule"], "requests_enabled": set["requests_enabled"]},
	})
}

func kindOrder() []string {
	out := make([]string, len(analysis.Kinds))
	for i, k := range analysis.Kinds {
		out[i] = k.Key
	}
	return out
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

// prepareCreate validates a new record and its meta and checks conflicts.
func (s *Server) prepareCreate(ctx context.Context, in *model.RecordInput, meta *model.Meta) error {
	if err := dnsrec.Validate(in, s.Cfg.CFZoneName); err != nil {
		return err
	}
	m, err := cleanMeta(*meta)
	if err != nil {
		return err
	}
	*meta = m
	return s.checkConflict(ctx, *in, "")
}

// commitCreate creates a validated record, stores its meta and audits it.
// The caller queues the connection check.
func (s *Server) commitCreate(r *http.Request, in model.RecordInput, meta model.Meta, action string, extra map[string]any) (*model.Record, error) {
	ctx := r.Context()
	rec, err := s.CF.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := s.Store.PutMeta(ctx, rec.ID, meta); err != nil {
		log.Printf("put meta %s: %v", rec.Name, err)
	}
	d := map[string]any{"record_id": rec.ID, "after": rec.Input(), "meta": meta}
	for k, v := range extra {
		d[k] = v
	}
	s.auditLog(r, action, rec.Name, d)
	return rec, nil
}

// createRecord is prepareCreate, commitCreate and a connection check.
// It reports whether a check was queued.
func (s *Server) createRecord(r *http.Request, in model.RecordInput, meta model.Meta, action string, extra map[string]any) (*model.Record, bool, error) {
	if err := s.prepareCreate(r.Context(), &in, &meta); err != nil {
		return nil, false, err
	}
	rec, err := s.commitCreate(r, in, meta, action, extra)
	if err != nil {
		return nil, false, err
	}
	return rec, len(s.Scanner.ScanSoon(*rec)) > 0, nil
}

// updateDNS writes a validated change to Cloudflare and follows up: keeps
// the listing hostname in sync and queues the record and service checks.
// It reports whether a connection check was queued.
func (s *Server) updateDNS(ctx context.Context, id string, before, in model.RecordInput) (*model.Record, bool, error) {
	if err := s.checkConflict(ctx, in, id); err != nil {
		return nil, false, err
	}
	rec, err := s.CF.Update(ctx, id, in)
	if err != nil {
		return nil, false, err
	}
	if !strings.EqualFold(before.Name, rec.Name) {
		s.Store.SetListingHostname(ctx, id, rec.Name)
	}
	if l, err := s.Store.Listing(ctx, id); err == nil {
		s.Health.CheckSoon(*l)
	}
	return rec, len(s.Scanner.ScanSoon(*rec)) > 0, nil
}

// putMeta replaces a record's meta and audits the change as meta_update.
func (s *Server) putMeta(r *http.Request, id string, before, after model.Meta, extra map[string]any) error {
	if after == before {
		return nil
	}
	ctx := r.Context()
	if err := s.Store.PutMeta(ctx, id, after); err != nil {
		return err
	}
	d := map[string]any{"record_id": id, "meta_before": before, "meta_after": after}
	for k, v := range extra {
		d[k] = v
	}
	s.auditLog(r, "meta_update", s.recordName(ctx, id), d)
	if before.NoScan && !after.NoScan {
		if rec, err := s.CF.Get(ctx, id); err == nil {
			s.Scanner.ScanSoon(*rec)
		}
	}
	return nil
}

// ---- create / update / delete ----

type createBody struct {
	model.RecordInput
	Meta *model.Meta `json:"meta"`
}

func (s *Server) apiCreateRecord(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	var meta model.Meta
	if b.Meta != nil {
		meta = *b.Meta
	}
	rec, scanning, err := s.createRecord(r, b.RecordInput, meta, "record_create", nil)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"record": rec, "scanning": scanning})
}

type updateBody struct {
	Type     *string          `json:"type"`
	Name     *string          `json:"name"`
	Content  *string          `json:"content"`
	TTL      *int             `json:"ttl"`
	Proxied  *bool            `json:"proxied"`
	Priority *int             `json:"priority"`
	Comment  *string          `json:"comment"`
	Meta     *model.MetaPatch `json:"meta"`
}

// apply overlays the sent DNS fields on in.
func (b updateBody) apply(in model.RecordInput) model.RecordInput {
	if b.Type != nil {
		in.Type = *b.Type
	}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Content != nil {
		in.Content = *b.Content
	}
	if b.TTL != nil {
		in.TTL = *b.TTL
	}
	if b.Proxied != nil {
		in.Proxied = *b.Proxied
	}
	if b.Priority != nil {
		in.Priority = b.Priority
	}
	if b.Comment != nil {
		in.Comment = *b.Comment
	}
	return in
}

func (s *Server) apiUpdateRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var b updateBody
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	ctx := r.Context()
	cur, err := s.CF.Get(ctx, id)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	before := cur.Input()
	in := b.apply(before)
	dnsChanged := len(model.ChangedFields(in, before)) > 0
	if dnsChanged {
		if !dnsrec.Editable[cur.Type] {
			writeErr(w, http.StatusUnprocessableEntity, cur.Type+" 請於 Cloudflare 編輯")
			return
		}
		if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
			writeAnyErr(w, err)
			return
		}
		// Normalization can turn a sent change back into the current value.
		dnsChanged = len(model.ChangedFields(in, before)) > 0
	}
	metaBefore, err := s.Store.Meta(ctx, id)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	metaAfter := metaBefore
	if b.Meta != nil {
		if metaAfter, err = cleanMeta(b.Meta.Apply(metaBefore)); err != nil {
			writeAnyErr(w, err)
			return
		}
	}
	if !dnsChanged {
		if err := s.putMeta(r, id, metaBefore, metaAfter, nil); err != nil {
			writeAnyErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"record": cur, "dns_changed": false, "scanning": false})
		return
	}
	// The meta is written first so the queued scan sees the new no_scan.
	metaChanged := metaAfter != metaBefore
	if metaChanged {
		if err := s.Store.PutMeta(ctx, id, metaAfter); err != nil {
			writeAnyErr(w, err)
			return
		}
	}
	rec, scanning, err := s.updateDNS(ctx, id, before, in)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	d := map[string]any{"record_id": id, "before": before, "after": rec.Input()}
	if metaChanged {
		d["meta_before"], d["meta_after"] = metaBefore, metaAfter
	}
	s.auditLog(r, "record_update", rec.Name, d)
	writeJSON(w, http.StatusOK, map[string]any{"record": rec, "dns_changed": true, "scanning": scanning})
}

// forget removes local data of a deleted record, including icon files.
func (s *Server) forget(ctx context.Context, id string) {
	l, err := s.Store.ForgetRecord(ctx, id)
	if err != nil {
		log.Printf("forget %s: %v", id, err)
		return
	}
	if l != nil {
		s.removeIconFile(ctx, l.IconFile)
	}
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
	l, err := s.Store.ForgetRecord(ctx, rec.ID)
	if err != nil {
		log.Printf("forget %s: %v", rec.ID, err)
	}
	d := map[string]any{"record_id": rec.ID, "before": rec.Input(), "meta": meta}
	if l != nil {
		s.removeIconFile(ctx, l.IconFile)
		d["listing"] = l.Title
	}
	s.auditLog(r, action, rec.Name, d)
	return nil
}

func (s *Server) apiDeleteRecord(w http.ResponseWriter, r *http.Request) {
	rec, err := s.CF.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	if err := s.deleteRecord(r, *rec, "record_delete"); err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) recordName(ctx context.Context, id string) string {
	recs, _, err := s.CF.List(ctx, false)
	if err == nil {
		for _, r := range recs {
			if r.ID == id {
				return r.Name
			}
		}
	}
	return id
}

func (s *Server) apiPutMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p model.MetaPatch
	if err := decode(w, r, &p); err != nil {
		writeAnyErr(w, err)
		return
	}
	before, err := s.Store.Meta(r.Context(), id)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	after, err := cleanMeta(p.Apply(before))
	if err == nil {
		err = s.putMeta(r, id, before, after, nil)
	}
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": after})
}

func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.URL.Query().Get("name")
	if name == "" {
		name = s.recordName(r.Context(), id)
	}
	rows, err := s.Store.AuditForRecord(r.Context(), id, name)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": rows})
}

func (s *Server) apiBulkMeta(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs []string        `json:"ids"`
		Set model.MetaPatch `json:"set"`
	}
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	if len(b.IDs) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "未選擇任何 record")
		return
	}
	ctx := r.Context()
	all, err := s.Store.AllMeta(ctx)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	befores := map[string]model.Meta{}
	afters := map[string]model.Meta{}
	for _, id := range b.IDs {
		after, err := cleanMeta(b.Set.Apply(all[id]))
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		befores[id], afters[id] = all[id], after
	}
	changed := 0
	for _, id := range b.IDs {
		if afters[id] != befores[id] {
			if err := s.Store.PutMeta(ctx, id, afters[id]); err != nil {
				writeAnyErr(w, err)
				return
			}
			changed++
		}
	}
	s.auditLog(r, "bulk_meta", strconv.Itoa(len(b.IDs))+" 筆", map[string]any{"ids": b.IDs, "set": b.Set, "before": befores})
	writeJSON(w, http.StatusOK, map[string]any{"updated": changed})
}

type itemResult struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) apiBulkDelete(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	if len(b.IDs) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "未選擇任何 record")
		return
	}
	info, recs, err := s.Maint.Snapshot(r.Context(), "manual")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "無法建立快照，已取消刪除："+err.Error())
		return
	}
	s.auditLog(r, "snapshot_create", info.Name, map[string]any{"reason": "bulk_delete", "count": info.Count})
	byID := map[string]model.Record{}
	for _, rec := range recs {
		byID[rec.ID] = rec
	}
	results := []itemResult{}
	okN := 0
	for _, id := range b.IDs {
		rec, ok := byID[id]
		if !ok {
			results = append(results, itemResult{ID: id, Name: id, Error: "record 已不存在"})
			continue
		}
		if err := s.deleteRecord(r, rec, "record_delete"); err != nil {
			results = append(results, itemResult{ID: id, Name: rec.Name, Error: err.Error()})
			continue
		}
		okN++
		results = append(results, itemResult{ID: id, Name: rec.Name, OK: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "deleted": okN, "failed": len(b.IDs) - okN, "snapshot": info.Name})
}

// ---- batch ----

type batchRow struct {
	model.RecordInput
	Meta *model.Meta `json:"meta"`
}

type batchResult struct {
	Index    int    `json:"index"`
	OK       bool   `json:"ok"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	Error    string `json:"error,omitempty"`
	Warning  string `json:"warning,omitempty"`
	RecordID string `json:"record_id,omitempty"`
}

const maxBatch = 500

func (s *Server) apiBatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Records []batchRow `json:"records"`
		DryRun  bool       `json:"dry_run"`
	}
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	if len(b.Records) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "沒有可建立的資料")
		return
	}
	if len(b.Records) > maxBatch {
		writeErr(w, http.StatusUnprocessableEntity, "一次最多建立 500 筆")
		return
	}
	ctx := r.Context()
	existing, _, err := s.CF.List(ctx, true)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	rule := s.settings(ctx)["name_rule"]
	results := make([]batchResult, len(b.Records))
	inputs := make([]model.RecordInput, len(b.Records))
	metas := make([]model.Meta, len(b.Records))
	// Earlier rows count as existing, so duplicates within the batch are caught.
	pseudo := append([]model.Record(nil), existing...)
	ok := 0
	for i, row := range b.Records {
		in := row.RecordInput
		res := batchResult{Index: i, Type: strings.ToUpper(in.Type)}
		err := dnsrec.Validate(&in, s.Cfg.CFZoneName)
		if err == nil && row.Meta != nil {
			metas[i], err = cleanMeta(*row.Meta)
		}
		if err == nil {
			err = dnsrec.Conflict(in, pseudo, "")
		}
		res.Name, res.Content = in.Name, in.Content
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			ok++
			pseudo = append(pseudo, model.Record{ID: "batch-" + strconv.Itoa(i), Type: in.Type, Name: in.Name, Content: in.Content})
		}
		if msg := dnsrec.NameRuleError(in.Name, s.Cfg.CFZoneName, rule); msg != "" {
			res.Warning = "名稱不符規則：" + msg
		}
		inputs[i] = in
		results[i] = res
	}
	var created []model.Record
	if !b.DryRun {
		for i := range results {
			if !results[i].OK {
				continue
			}
			rec, err := s.commitCreate(r, inputs[i], metas[i], "record_create", map[string]any{"batch": true})
			if err != nil {
				results[i].OK = false
				results[i].Error = err.Error()
				ok--
				continue
			}
			results[i].RecordID = rec.ID
			created = append(created, *rec)
		}
		s.Scanner.ScanSoon(created...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "ok": ok, "skipped": len(results) - ok, "created": len(created), "dry_run": b.DryRun})
}

// ---- restore ----

type restoreBody struct {
	Mode     string             `json:"mode"`
	RecordID string             `json:"record_id"`
	Data     *model.RecordInput `json:"data"`
	Meta     *model.Meta        `json:"meta"`
}

var restoreModes = map[string]func(s *Server, w http.ResponseWriter, r *http.Request, b restoreBody){
	"recreate": (*Server).restoreRecreate,
	"revert":   (*Server).restoreRevert,
	"delete":   (*Server).restoreDelete,
	"meta":     (*Server).restoreMeta,
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) {
	var b restoreBody
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	h, ok := restoreModes[b.Mode]
	if !ok {
		writeErr(w, http.StatusUnprocessableEntity, "mode 須為 recreate、revert、delete 或 meta")
		return
	}
	h(s, w, r, b)
}

func (s *Server) restoreRecreate(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.Data == nil {
		writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
		return
	}
	var meta model.Meta
	if b.Meta != nil {
		meta = *b.Meta
	}
	rec, _, err := s.createRecord(r, *b.Data, meta, "record_restore", map[string]any{"from_record_id": b.RecordID})
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
}

func (s *Server) restoreRevert(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.Data == nil || b.RecordID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
		return
	}
	ctx := r.Context()
	cur, err := s.CF.Get(ctx, b.RecordID)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	in := *b.Data
	if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
		writeAnyErr(w, err)
		return
	}
	d := map[string]any{"record_id": b.RecordID, "before": cur.Input()}
	if b.Meta != nil {
		after, err := cleanMeta(*b.Meta)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		mb, _ := s.Store.Meta(ctx, b.RecordID)
		if err := s.Store.PutMeta(ctx, b.RecordID, after); err != nil {
			writeAnyErr(w, err)
			return
		}
		d["meta_before"], d["meta_after"] = mb, after
	}
	rec, _, err := s.updateDNS(ctx, b.RecordID, cur.Input(), in)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	d["after"] = rec.Input()
	s.auditLog(r, "record_restore", rec.Name, d)
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
}

func (s *Server) restoreDelete(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.RecordID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "缺少 record_id")
		return
	}
	cur, err := s.CF.Get(r.Context(), b.RecordID)
	if err == nil {
		err = s.deleteRecord(r, *cur, "record_restore_delete")
	}
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) restoreMeta(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.RecordID == "" || b.Meta == nil {
		writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
		return
	}
	before, err := s.Store.Meta(r.Context(), b.RecordID)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	after, err := cleanMeta(*b.Meta)
	if err == nil {
		err = s.putMeta(r, b.RecordID, before, after, map[string]any{"restore": true})
	}
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": after})
}

func (s *Server) apiMute(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RecordID string `json:"record_id"`
		Kind     string `json:"kind"`
		Muted    bool   `json:"muted"`
	}
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	if _, ok := analysis.KindByKey[b.Kind]; !ok || b.RecordID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "未知的項目")
		return
	}
	if err := s.Store.SetMute(r.Context(), b.RecordID, b.Kind, b.Muted); err != nil {
		writeAnyErr(w, err)
		return
	}
	action := "issue_mute"
	if !b.Muted {
		action = "issue_unmute"
	}
	s.auditLog(r, action, s.recordName(r.Context(), b.RecordID), map[string]any{"record_id": b.RecordID, "kind": b.Kind, "label": analysis.KindByKey[b.Kind].Label})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiScan(w http.ResponseWriter, r *http.Request) {
	started := s.Scanner.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "scan": s.Scanner.Status()})
}

func (s *Server) apiScanOne(w http.ResponseWriter, r *http.Request) {
	rec, err := s.CF.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	s.Scanner.ScanSoon(*rec)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}
