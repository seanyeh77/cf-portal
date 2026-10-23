package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/seanyeh77/cf-portal/internal/model"
	"github.com/seanyeh77/cf-portal/testdata/mockcf"
)

func (e *env) ids(names ...string) []string {
	var out []string
	for _, n := range names {
		for _, typ := range []string{"A", "CNAME", "TXT", "SRV"} {
			if r := e.mock.Find(typ, n); r != nil {
				out = append(out, r.ID)
				break
			}
		}
	}
	return out
}

func results(t *testing.T, m map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range m["results"].([]any) {
		r := r.(map[string]any)
		out[strings.TrimSuffix(r["name"].(string), "."+mockcf.Zone)] = r
	}
	return out
}

func TestBulkDNS(t *testing.T) {
	e := newEnv(t)
	e.login()
	ids := e.ids("host001", "host002", "_minecraft._tcp", "_dmarc")
	target := mockcf.TunnelOK + ".cfargotunnel.com"
	body := map[string]any{"ids": ids, "set": map[string]any{"content": target, "proxied": true}, "dry_run": true}
	plan := results(t, e.mustOK(e.do("POST", "/api/records/bulk-dns", body)))
	if p := plan["host001"]; p["after"].(map[string]any)["type"] != "CNAME" || !strings.Contains(strings.Join(toStrings(p["changed"]), ","), "content") {
		t.Errorf("host001 plan %v", p)
	}
	if plan["_minecraft._tcp"]["skipped"] != "SRV 請於 Cloudflare 編輯" || plan["_dmarc"]["skipped"] != "TXT 不適用指向、Proxy" {
		t.Errorf("skips %v / %v", plan["_minecraft._tcp"], plan["_dmarc"])
	}
	if e.mock.Find("A", "host001") == nil {
		t.Fatal("dry run changed the zone")
	}

	body["dry_run"] = false
	out := e.mustOK(e.do("POST", "/api/records/bulk-dns", body))
	if out["ok"] != 2.0 || out["failed"] != 0.0 || out["snapshot"] == "" {
		t.Fatalf("apply %v", out)
	}
	if r := e.mock.Find("CNAME", "host002"); r == nil || r.Content != target || !r.Proxied {
		t.Fatalf("host002 %+v", r)
	}
	// Each record is audited on its own and can be reverted.
	n := 0
	for _, a := range e.audits() {
		if a["action"] == "record_update" && strings.HasPrefix(a["target"].(string), "host00") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("audits %d", n)
	}

	// TTL applies to every editable type.
	ids = e.ids("git", "gitlab")
	plan = results(t, e.mustOK(e.do("POST", "/api/records/bulk-dns", map[string]any{"ids": ids, "set": map[string]any{"ttl": 300}, "dry_run": true})))
	if plan["git"]["error"] != nil || plan["gitlab"]["after"].(map[string]any)["ttl"] != 300.0 {
		t.Errorf("ttl plan %v", plan)
	}
	if w := e.do("POST", "/api/records/bulk-dns", map[string]any{"ids": ids, "set": map[string]any{}}); w.Code != 422 {
		t.Error("empty set accepted")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestBulkListings(t *testing.T) {
	e := newEnv(t)
	e.login()
	ids := e.ids("grafana", "gitlab")
	items := []map[string]any{{"record_id": ids[0], "title": "Grafana"}, {"record_id": ids[1], "title": "GitLab", "url": "https://gitlab.example.org/explore"}}
	out := e.mustOK(e.do("POST", "/api/listings/bulk-create", map[string]any{"items": items, "category": "工具", "tags": []string{"需 VPN"}, "icon": "none"}))
	if out["ok"] != 2.0 {
		t.Fatalf("create %v", out)
	}
	// Listing again fails per record instead of replacing.
	if out := e.mustOK(e.do("POST", "/api/listings/bulk-create", map[string]any{"items": items[:1], "icon": "none"})); out["failed"] != 1.0 {
		t.Fatalf("relist %v", out)
	}
	out = e.mustOK(e.do("POST", "/api/listings/bulk-update", map[string]any{"ids": append(ids, e.ids("jupyter")...), "category": "監控", "featured": true,
		"add_tags": []string{"內部使用"}, "remove_tags": []string{"需 VPN"}}))
	if out["ok"] != 2.0 || out["failed"] != 1.0 {
		t.Fatalf("update %v", out)
	}
	l, _ := e.srv.Store.Listing(context.Background(), ids[1])
	if l.Category != "監控" || !l.Featured || strings.Join(l.Tags, ",") != "內部使用" || l.Title != "GitLab" || l.URL == nil || *l.URL != "https://gitlab.example.org/explore" {
		t.Fatalf("listing %+v", l)
	}
	if out := e.mustOK(e.do("POST", "/api/listings/bulk-delete", map[string]any{"ids": ids})); out["ok"] != 2.0 {
		t.Fatalf("delete %v", out)
	}
	if ls, _ := e.srv.Store.Listings(context.Background()); len(ls) != 0 {
		t.Fatalf("left %v", ls)
	}
}

func TestBulkMuteScanExport(t *testing.T) {
	e := newEnv(t)
	e.login()
	ids := e.ids("host003", "host004")
	out := e.mustOK(e.do("POST", "/api/issues/bulk-mute", map[string]any{"ids": ids, "mute": []string{"no_owner", "expired"}, "note": "測試機，無需負責人"}))
	if out["ok"] != 2.0 {
		t.Fatalf("mute %v", out)
	}
	mutes, _ := e.srv.Store.Mutes(context.Background())
	m, _ := e.srv.Store.Meta(context.Background(), ids[0])
	if !mutes[ids[0]]["no_owner"] || mutes[ids[0]]["expired"] || m.Notes != "測試機，無需負責人" {
		t.Fatalf("mutes %v notes %q", mutes[ids[0]], m.Notes)
	}
	// Muting again does not repeat the note.
	e.mustOK(e.do("POST", "/api/issues/bulk-mute", map[string]any{"ids": ids, "unmute": []string{"no_owner"}}))
	e.mustOK(e.do("POST", "/api/issues/bulk-mute", map[string]any{"ids": ids, "mute": []string{"no_owner"}, "note": "測試機，無需負責人"}))
	if m, _ := e.srv.Store.Meta(context.Background(), ids[0]); m.Notes != "測試機，無需負責人" {
		t.Fatalf("notes %q", m.Notes)
	}
	if w := e.do("POST", "/api/issues/bulk-mute", map[string]any{"ids": ids, "mute": []string{"nope"}}); w.Code != 422 {
		t.Error("unknown kind accepted")
	}

	if out := e.mustOK(e.do("POST", "/api/records/bulk-scan", map[string]any{"ids": append(ids, "missing")})); out["ok"] != 2.0 || out["failed"] != 1.0 {
		t.Fatalf("scan %v", out)
	}

	w := e.do("GET", "/api/export.csv?ids="+strings.Join(ids, ","), nil)
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if w.Code != 200 || len(lines) != 3 || !strings.HasPrefix(lines[1], "host003.") || !strings.HasPrefix(lines[2], "host004.") {
		t.Fatalf("export %d %q", w.Code, lines)
	}
}

func TestBulkRequests(t *testing.T) {
	e := newEnv(t)
	e.login()
	ctx := context.Background()
	var ids []int64
	for i := 0; i < 3; i++ {
		id, _ := e.srv.Store.AddRequest(ctx, modelRequest(i))
		ids = append(ids, id)
	}
	// The second request asks for a name the first one creates.
	dup := modelRequest(9)
	dup.Name, dup.Type, dup.Content = "bulk0.example.org", "CNAME", "x.example.com"
	dupID, _ := e.srv.Store.AddRequest(ctx, dup)
	out := e.mustOK(e.do("POST", "/api/requests/bulk-approve", map[string]any{"ids": []int64{ids[0], dupID, ids[1]}, "note": "已開通"}))
	if out["ok"] != 2.0 || out["failed"] != 1.0 {
		t.Fatalf("approve %v", out)
	}
	if r := e.mock.Find("A", "bulk1"); r == nil || r.Proxied {
		t.Fatalf("bulk1 %+v", r)
	}
	if q, _ := e.srv.Store.Request(ctx, dupID); q.Status != "pending" {
		t.Fatal("conflicting request decided")
	}
	if w := e.do("POST", "/api/requests/bulk-reject", map[string]any{"ids": []int64{ids[2]}}); w.Code != 422 {
		t.Fatal("reject without reason accepted")
	}
	out = e.mustOK(e.do("POST", "/api/requests/bulk-reject", map[string]any{"ids": []int64{ids[2], ids[0]}, "note": "名額已滿"}))
	if out["ok"] != 1.0 || out["failed"] != 1.0 {
		t.Fatalf("reject %v", out)
	}
	if q, _ := e.srv.Store.Request(ctx, ids[2]); q.Status != "rejected" || q.AdminNote != "名額已滿" {
		t.Fatalf("rejected %+v", q)
	}
}

func TestPlanDNSConflicts(t *testing.T) {
	e := newEnv(t)
	a := func(id, ip string) model.Record {
		return model.Record{ID: id, RecordInput: model.RecordInput{Type: "A", Name: "x." + mockcf.Zone, Content: ip, TTL: 1}}
	}
	target := "y.example.com"
	plan := e.srv.planDNS([]model.Record{a("1", "10.0.0.1"), a("2", "10.0.0.2")}, []string{"1", "2"}, dnsSet{Content: &target})
	// Either record turned into a CNAME would share its name with the other.
	for _, c := range plan {
		if c.After != nil || !strings.Contains(c.Error, "CNAME 不可與其他 record 同名") {
			t.Errorf("%s: %+v", c.ID, c)
		}
	}
	ip := "10.0.0.9"
	plan = e.srv.planDNS([]model.Record{a("1", "10.0.0.1"), a("2", "10.0.0.2")}, []string{"1", "2"}, dnsSet{Content: &ip})
	// The first change makes the second a duplicate of it.
	if plan[0].After == nil || !strings.Contains(plan[1].Error, "已有相同的 A record") {
		t.Errorf("duplicate: %+v", plan)
	}
}
