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
	Editable bool             `json:"editable"`
	Group    string           `json:"group"`
	Listing  *listingOut      `json:"listing"`
	Meta     model.Meta       `json:"meta"`
	Scan     *model.Scan      `json:"scan"`
	Issues   []analysis.Issue `json:"issues"`
	Muted    []analysis.Issue `json:"muted"`
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

func (s *Server) analysisSettings(set map[string]string) analysis.Settings {
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
		Settings: s.analysisSettings(z.Settings), Now: s.Now(), Loc: s.Cfg.Timezone})
	return z, nil
}

func (z *zoneState) find(id string) *model.Record {
	for i := range z.Records {
		if z.Records[i].ID == id {
			return &z.Records[i]
		}
	}
	return nil
}

func (z *zoneState) out(r model.Record) recordOut {
	o := recordOut{Record: r, Editable: dnsrec.Editable[r.Type], Meta: z.Meta[r.ID], Issues: []analysis.Issue{}, Muted: []analysis.Issue{}}
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
	pending := 0
	if reqs, err := s.Store.Requests(ctx); err == nil {
		for _, q := range reqs {
			if q.Status == "pending" {
				pending++
			}
		}
	}
	var lastSnap float64
	if list, err := s.Snaps.List(); err == nil && len(list) > 0 {
		lastSnap = list[0].TS
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
		return m, &dnsrec.ValidationError{Msg: "備註最多 2000 字"}
	}
	m.ExpiresAt = strings.TrimSpace(m.ExpiresAt)
	if m.ExpiresAt != "" && !validDate(m.ExpiresAt) {
		return m, &dnsrec.ValidationError{Msg: "到期日格式應為 YYYY-MM-DD"}
	}
	if m.AutoDelete && m.ExpiresAt == "" {
		return m, &dnsrec.ValidationError{Msg: "自動刪除需設定到期日"}
	}
	return m, nil
}

// ---- create / update / delete ----

type createBody struct {
	model.RecordInput
	Meta *model.Meta `json:"meta"`
}

func (s *Server) checkConflict(ctx context.Context, in model.RecordInput, exclude string) error {
	recs, _, err := s.CF.List(ctx, true)
	if err != nil {
		return err
	}
	return dnsrec.Conflict(in, recs, exclude)
}

func (s *Server) apiCreateRecord(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if err := decode(r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	in := b.RecordInput
	if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
		writeAnyErr(w, err)
		return
	}
	var meta model.Meta
	if b.Meta != nil {
		var err error
		if meta, err = cleanMeta(*b.Meta); err != nil {
			writeAnyErr(w, err)
			return
		}
	}
	ctx := r.Context()
	if err := s.checkConflict(ctx, in, ""); err != nil {
		writeAnyErr(w, err)
		return
	}
	rec, err := s.CF.Create(ctx, in)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	if err := s.Store.PutMeta(ctx, rec.ID, meta); err != nil {
		log.Printf("put meta: %v", err)
	}
	s.auditLog(r, "record_create", rec.Name, map[string]any{"record_id": rec.ID, "after": rec.Input(), "meta": meta})
	s.Scanner.ScanSoon(*rec)
	writeJSON(w, http.StatusCreated, map[string]any{"record": rec, "meta": meta})
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

func sameInput(a, b model.RecordInput) bool {
	pa, pb := -1, -1
	if a.Priority != nil {
		pa = *a.Priority
	}
	if b.Priority != nil {
		pb = *b.Priority
	}
	return a.Type == b.Type && strings.EqualFold(a.Name, b.Name) && a.Content == b.Content && a.TTL == b.TTL &&
		a.Proxied == b.Proxied && pa == pb && a.Comment == b.Comment
}

func (s *Server) apiUpdateRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var b updateBody
	if err := decode(r, &b); err != nil {
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
	in := before
	dnsSent := b.Type != nil || b.Name != nil || b.Content != nil || b.TTL != nil || b.Proxied != nil || b.Priority != nil || b.Comment != nil
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
	dnsChanged := false
	if dnsSent && !sameInput(in, before) {
		if !dnsrec.Editable[cur.Type] {
			writeErr(w, http.StatusUnprocessableEntity, cur.Type+" 請於 Cloudflare 編輯")
			return
		}
		if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
			writeAnyErr(w, err)
			return
		}
		dnsChanged = !sameInput(in, before)
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
	metaChanged := metaAfter != metaBefore
	rec := cur
	if dnsChanged {
		if err := s.checkConflict(ctx, in, id); err != nil {
			writeAnyErr(w, err)
			return
		}
		if rec, err = s.CF.Update(ctx, id, in); err != nil {
			writeAnyErr(w, err)
			return
		}
	}
	if metaChanged {
		if err := s.Store.PutMeta(ctx, id, metaAfter); err != nil {
			writeAnyErr(w, err)
			return
		}
	}
	switch {
	case dnsChanged:
		d := map[string]any{"record_id": id, "before": before, "after": rec.Input()}
		if metaChanged {
			d["meta_before"], d["meta_after"] = metaBefore, metaAfter
		}
		s.auditLog(r, "record_update", rec.Name, d)
		if !strings.EqualFold(before.Name, rec.Name) {
			s.Store.SetListingHostname(ctx, id, rec.Name)
		}
		s.Scanner.ScanSoon(*rec)
		if l, err := s.Store.Listing(ctx, id); err == nil {
			s.Health.CheckSoon(*l)
		}
	case metaChanged:
		s.auditLog(r, "meta_update", rec.Name, map[string]any{"record_id": id, "meta_before": metaBefore, "meta_after": metaAfter})
		if metaBefore.NoScan && !metaAfter.NoScan {
			s.Scanner.ScanSoon(*rec)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec, "meta": metaAfter, "dns_changed": dnsChanged})
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
	l, _ := s.Store.Listing(ctx, rec.ID)
	if err := s.CF.Delete(ctx, rec.ID); err != nil {
		return err
	}
	s.forget(ctx, rec.ID)
	d := map[string]any{"record_id": rec.ID, "before": rec.Input(), "meta": meta}
	if l != nil {
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
	if err := decode(r, &p); err != nil {
		writeAnyErr(w, err)
		return
	}
	ctx := r.Context()
	before, err := s.Store.Meta(ctx, id)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	after, err := cleanMeta(p.Apply(before))
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	if after != before {
		if err := s.Store.PutMeta(ctx, id, after); err != nil {
			writeAnyErr(w, err)
			return
		}
		s.auditLog(r, "meta_update", s.recordName(ctx, id), map[string]any{"record_id": id, "meta_before": before, "meta_after": after})
		if before.NoScan && !after.NoScan {
			if rec, err := s.CF.Get(ctx, id); err == nil {
				s.Scanner.ScanSoon(*rec)
			}
		}
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
	if err := decode(r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	if len(b.IDs) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "未選擇任何 record")
		return
	}
	ctx := r.Context()
	befores := map[string]model.Meta{}
	afters := map[string]model.Meta{}
	for _, id := range b.IDs {
		before, err := s.Store.Meta(ctx, id)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		after, err := cleanMeta(b.Set.Apply(before))
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		befores[id], afters[id] = before, after
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
	if err := decode(r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	if len(b.IDs) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "未選擇任何 record")
		return
	}
	ctx := r.Context()
	info, err := s.Maint.Snapshot(ctx, "manual")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "無法建立快照，已取消刪除："+err.Error())
		return
	}
	s.auditLog(r, "snapshot_create", info.Name, map[string]any{"reason": "bulk_delete", "count": info.Count})
	recs, _, err := s.CF.List(ctx, true)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
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
	if err := decode(r, &b); err != nil {
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
	pseudo := append([]model.Record(nil), existing...)
	for i, row := range b.Records {
		in := row.RecordInput
		res := batchResult{Index: i, Name: dnsrec.NormalizeName(in.Name, s.Cfg.CFZoneName), Type: strings.ToUpper(in.Type), Content: in.Content}
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
			pseudo = append(pseudo, model.Record{ID: "batch-" + strconv.Itoa(i), Type: in.Type, Name: in.Name, Content: in.Content})
		}
		if msg := dnsrec.NameRuleError(in.Name, s.Cfg.CFZoneName, rule); msg != "" {
			res.Warning = "名稱不符規則：" + msg
		}
		inputs[i] = in
		results[i] = res
	}
	created := 0
	if !b.DryRun {
		for i := range results {
			if !results[i].OK {
				continue
			}
			rec, err := s.CF.Create(ctx, inputs[i])
			if err != nil {
				results[i].OK = false
				results[i].Error = err.Error()
				continue
			}
			s.Store.PutMeta(ctx, rec.ID, metas[i])
			s.auditLog(r, "record_create", rec.Name, map[string]any{"record_id": rec.ID, "after": rec.Input(), "meta": metas[i], "batch": true})
			s.Scanner.ScanSoon(*rec)
			results[i].RecordID = rec.ID
			created++
		}
	}
	ok := 0
	for _, res := range results {
		if res.OK {
			ok++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "ok": ok, "skipped": len(results) - ok, "created": created, "dry_run": b.DryRun})
}

// ---- restore ----

type restoreBody struct {
	Mode     string             `json:"mode"`
	RecordID string             `json:"record_id"`
	Data     *model.RecordInput `json:"data"`
	Meta     *model.Meta        `json:"meta"`
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) {
	var b restoreBody
	if err := decode(r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	ctx := r.Context()
	switch b.Mode {
	case "recreate":
		if b.Data == nil {
			writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
			return
		}
		in := *b.Data
		if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
			writeAnyErr(w, err)
			return
		}
		if err := s.checkConflict(ctx, in, ""); err != nil {
			writeAnyErr(w, err)
			return
		}
		rec, err := s.CF.Create(ctx, in)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		var meta model.Meta
		if b.Meta != nil {
			meta = *b.Meta
			s.Store.PutMeta(ctx, rec.ID, meta)
		}
		s.auditLog(r, "record_restore", rec.Name, map[string]any{"record_id": rec.ID, "after": rec.Input(), "meta": meta, "from_record_id": b.RecordID})
		s.Scanner.ScanSoon(*rec)
		writeJSON(w, http.StatusOK, map[string]any{"record": rec})
	case "revert":
		if b.Data == nil || b.RecordID == "" {
			writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
			return
		}
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
		if err := s.checkConflict(ctx, in, b.RecordID); err != nil {
			writeAnyErr(w, err)
			return
		}
		rec, err := s.CF.Update(ctx, b.RecordID, in)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		d := map[string]any{"record_id": rec.ID, "before": cur.Input(), "after": rec.Input()}
		if b.Meta != nil {
			mb, _ := s.Store.Meta(ctx, rec.ID)
			s.Store.PutMeta(ctx, rec.ID, *b.Meta)
			d["meta_before"], d["meta_after"] = mb, *b.Meta
		}
		if !strings.EqualFold(cur.Name, rec.Name) {
			s.Store.SetListingHostname(ctx, rec.ID, rec.Name)
		}
		s.auditLog(r, "record_restore", rec.Name, d)
		s.Scanner.ScanSoon(*rec)
		writeJSON(w, http.StatusOK, map[string]any{"record": rec})
	case "delete":
		if b.RecordID == "" {
			writeErr(w, http.StatusUnprocessableEntity, "缺少 record_id")
			return
		}
		cur, err := s.CF.Get(ctx, b.RecordID)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		if err := s.deleteRecord(r, *cur, "record_restore_delete"); err != nil {
			writeAnyErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "meta":
		if b.RecordID == "" || b.Meta == nil {
			writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
			return
		}
		before, _ := s.Store.Meta(ctx, b.RecordID)
		after, err := cleanMeta(*b.Meta)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		if err := s.Store.PutMeta(ctx, b.RecordID, after); err != nil {
			writeAnyErr(w, err)
			return
		}
		s.auditLog(r, "meta_update", s.recordName(ctx, b.RecordID), map[string]any{"record_id": b.RecordID, "meta_before": before, "meta_after": after, "restore": true})
		writeJSON(w, http.StatusOK, map[string]any{"meta": after})
	default:
		writeErr(w, http.StatusUnprocessableEntity, "mode 須為 recreate、revert 或 delete")
	}
}

func (s *Server) apiMute(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RecordID string `json:"record_id"`
		Kind     string `json:"kind"`
		Muted    bool   `json:"muted"`
	}
	if err := decode(r, &b); err != nil {
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
