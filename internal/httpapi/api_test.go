package httpapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/seanyeh77/cf-portal/testdata/mockcf"
)

func findRec(t *testing.T, data map[string]any, name string) map[string]any {
	t.Helper()
	for _, r := range data["records"].([]any) {
		m := r.(map[string]any)
		if m["name"] == name+"."+mockcf.Zone || m["name"] == name {
			return m
		}
	}
	return nil
}

func TestRecordsListing(t *testing.T) {
	e := newEnv(t)
	e.login()
	data := e.mustOK(e.do("GET", "/api/records", nil))
	if n := len(data["records"].([]any)); n != len(mockcf.Seed()) {
		t.Fatalf("records %d", n)
	}
	if data["zone"] != mockcf.Zone || data["today"] == "" {
		t.Fatal("zone/today")
	}
	chain := findRec(t, data, "chain")
	issues := chain["issues"].([]any)
	if issues[0].(map[string]any)["kind"] != "upstream_broken" {
		t.Errorf("chain issues %v", issues)
	}
	srv := findRec(t, data, "_minecraft._tcp")
	if srv["editable"] != false {
		t.Error("SRV editable")
	}
	groups := fmt.Sprint(data["groups"])
	if !strings.Contains(groups, "host 20") {
		t.Errorf("groups %s", groups)
	}
}

func TestRecordsTunnels(t *testing.T) {
	e := newEnv(t)
	e.login()
	data := e.mustOK(e.do("GET", "/api/records", nil))
	app, gone := findRec(t, data, "tun-app"), findRec(t, data, "tun-gone")
	if tn := app["tunnel"].(map[string]any); tn["name"] != "home-lab" || tn["status"] != "healthy" || len(app["issues"].([]any)) != 1 {
		t.Errorf("tun-app: %v %v", tn, app["issues"]) // only no_owner
	}
	if tn := gone["tunnel"].(map[string]any); tn["deleted"] != true || gone["level"] != "high" {
		t.Errorf("tun-gone: %v %v", tn, gone["issues"])
	}
	if data["tunnel_error"] != "" || findRec(t, data, "docs")["tunnel"] != nil {
		t.Error("tunnel fields on a plain record")
	}

	// Without the permission the names are unknown but nothing is flagged.
	e2 := newEnv(t)
	e2.mock.SetTunnelsForbidden(true)
	e2.login()
	data = e2.mustOK(e2.do("GET", "/api/records", nil))
	gone = findRec(t, data, "tun-gone")
	if tn := gone["tunnel"].(map[string]any); tn["deleted"] != false || tn["name"] != "" || gone["level"] == "high" {
		t.Errorf("forbidden: %v %v", tn, gone["issues"])
	}
	if !strings.Contains(data["tunnel_error"].(string), "Cloudflare Tunnel › Read") {
		t.Errorf("tunnel_error %v", data["tunnel_error"])
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	e := newEnv(t)
	e.login()
	// validation and conflicts
	if w := e.do("POST", "/api/records", map[string]any{"type": "A", "name": "x", "content": "127.0.0.1"}); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "不可指向本機位址") {
		t.Fatalf("loopback: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/records", map[string]any{"type": "CNAME", "name": "gitlab", "content": "x.example.com"}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "gitlab.example.org 已有 A record，CNAME 不可與其他 record 同名") {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/records", map[string]any{"type": "A", "name": "git", "content": "10.0.1.21"}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "已有相同的 A record") {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/records", map[string]any{"type": "A", "name": "p", "content": "10.0.0.1", "meta": map[string]any{"auto_delete": true}}); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "自動刪除需設定到期日") {
		t.Fatalf("auto delete without date: %d", w.Code)
	}
	// Cloudflare errors pass through with the 'Cloudflare:' prefix.
	w := e.do("POST", "/api/records", map[string]any{"type": "A", "name": "prx", "content": "10.0.0.1", "proxied": true})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Cloudflare: 9003") {
		t.Fatalf("cloudflare error: %d %s", w.Code, w.Body.String())
	}
	// create
	res := e.mustOK(e.do("POST", "/api/records", map[string]any{"type": "A", "name": "Grafana2", "content": "10.0.1.50", "ttl": 300,
		"meta": map[string]any{"owner": "王小明", "purpose": "監控", "expires_at": "2026-12-31"}}), 201)
	rec := res["record"].(map[string]any)
	id := rec["id"].(string)
	if rec["name"] != "grafana2.example.org" {
		t.Fatalf("name %v", rec["name"])
	}
	// meta-only update does not call Cloudflare
	patches := e.mock.Calls["PATCH"]
	e.mustOK(e.do("PATCH", "/api/records/"+id, map[string]any{"type": "A", "name": "grafana2", "content": "10.0.1.50", "ttl": 300, "proxied": false,
		"meta": map[string]any{"notes": "機房 A"}}))
	if e.mock.Calls["PATCH"] != patches {
		t.Fatal("unchanged DNS fields must not call Cloudflare")
	}
	// DNS update
	out := e.mustOK(e.do("PATCH", "/api/records/"+id, map[string]any{"content": "10.0.1.99", "meta": map[string]any{"owner": "李小華"}}))
	if out["record"].(map[string]any)["content"] != "10.0.1.99" || e.mock.Calls["PATCH"] != patches+1 {
		t.Fatal("update")
	}
	a := e.audits()
	if a[0]["action"] != "record_update" {
		t.Fatalf("audit %v", a[0])
	}
	d := a[0]["detail"].(map[string]any)
	if d["record_id"] != id || d["before"].(map[string]any)["content"] != "10.0.1.50" || d["after"].(map[string]any)["content"] != "10.0.1.99" ||
		d["meta_before"].(map[string]any)["owner"] != "王小明" || d["meta_after"].(map[string]any)["owner"] != "李小華" {
		t.Fatalf("update audit %v", d)
	}
	if a[1]["action"] != "meta_update" {
		t.Fatalf("meta audit %v", a[1])
	}
	// meta endpoint
	e.mustOK(e.do("PUT", "/api/records/"+id+"/meta", map[string]any{"no_scan": true}))
	meta, _ := e.srv.Store.Meta(context.Background(), id)
	if !meta.NoScan || meta.Owner != "李小華" || meta.Notes != "機房 A" {
		t.Fatalf("partial meta %+v", meta)
	}
	// SRV cannot be edited
	srv := e.mock.Find("SRV", "_minecraft._tcp")
	if w := e.do("PATCH", "/api/records/"+srv.ID, map[string]any{"content": "0 1 x.example.com"}); w.Code != 422 || !strings.Contains(w.Body.String(), "SRV 請於 Cloudflare 編輯") {
		t.Fatalf("SRV edit: %d %s", w.Code, w.Body.String())
	}
	// listing then delete removes listing and meta
	e.mustOK(e.do("PUT", "/api/listings/"+id, map[string]any{"title": "G2", "icon": "none"}))
	e.mustOK(e.do("DELETE", "/api/records/"+id, nil))
	if e.mock.Find("A", "grafana2") != nil {
		t.Fatal("not deleted")
	}
	if l, _ := e.srv.Store.Listing(context.Background(), id); l != nil {
		t.Fatal("listing kept")
	}
	if m, _ := e.srv.Store.Meta(context.Background(), id); m.Owner != "" {
		t.Fatal("meta kept")
	}
	a = e.audits()
	if a[0]["action"] != "record_delete" || a[0]["detail"].(map[string]any)["meta"].(map[string]any)["owner"] != "李小華" {
		t.Fatalf("delete audit %v", a[0])
	}
	// history finds all audit rows of the record
	hist := e.mustOK(e.do("GET", "/api/records/"+id+"/history?name=grafana2.example.org", nil))
	if n := len(hist["entries"].([]any)); n < 5 {
		t.Fatalf("history %d", n)
	}
}

func TestBatchAndBulk(t *testing.T) {
	e := newEnv(t)
	e.login()
	rows := []map[string]any{}
	for i := 1; i <= 4; i++ {
		rows = append(rows, map[string]any{"type": "A", "name": fmt.Sprintf("vm%02d", i), "content": fmt.Sprintf("10.0.9.%d", i), "meta": map[string]any{"owner": "TA"}})
	}
	rows = append(rows, map[string]any{"type": "A", "name": "vm01", "content": "10.0.9.1"})     // duplicate within batch
	rows = append(rows, map[string]any{"type": "A", "name": "Bad_Name", "content": "10.0.9.9"}) // warning only
	rows = append(rows, map[string]any{"type": "CNAME", "name": "grafana", "content": "x.example.com"})
	dry := e.mustOK(e.do("POST", "/api/records/batch", map[string]any{"records": rows, "dry_run": true}))
	if dry["ok"].(float64) != 5 || dry["skipped"].(float64) != 2 || dry["created"].(float64) != 0 {
		t.Fatalf("dry run %v", dry)
	}
	res := dry["results"].([]any)
	if r := res[4].(map[string]any); r["ok"] != false || !strings.Contains(r["error"].(string), "已有相同的 A record") {
		t.Errorf("dup row %v", r)
	}
	if r := res[5].(map[string]any); r["ok"] != true || !strings.HasPrefix(r["warning"].(string), "名稱不符規則") {
		t.Errorf("warn row %v", r)
	}
	if e.mock.Calls["POST"] != 0 {
		t.Fatal("dry run created records")
	}
	real := e.mustOK(e.do("POST", "/api/records/batch", map[string]any{"records": rows}))
	if real["created"].(float64) != 5 || e.mock.Find("A", "vm03") == nil {
		t.Fatalf("batch %v", real)
	}
	vm := e.mock.Find("A", "vm02")
	if m, _ := e.srv.Store.Meta(context.Background(), vm.ID); m.Owner != "TA" {
		t.Fatal("batch meta")
	}
	// bulk meta
	ids := []string{e.mock.Find("A", "vm01").ID, vm.ID}
	e.mustOK(e.do("POST", "/api/records/bulk-meta", map[string]any{"ids": ids, "set": map[string]any{"purpose": "專案", "expires_at": "2027-01-31"}}))
	m, _ := e.srv.Store.Meta(context.Background(), vm.ID)
	if m.Owner != "TA" || m.Purpose != "專案" || m.ExpiresAt != "2027-01-31" {
		t.Fatalf("bulk meta %+v", m)
	}
	// bulk delete takes a snapshot first
	out := e.mustOK(e.do("POST", "/api/records/bulk-delete", map[string]any{"ids": append(ids, "missing")}))
	if out["ok"].(float64) != 2 || out["failed"].(float64) != 1 || out["snapshot"] == "" {
		t.Fatalf("bulk delete %v", out)
	}
	snap, err := e.srv.Snaps.Load(out["snapshot"].(string))
	if err != nil || len(snap.Records) != len(mockcf.Seed())+5 || snap.Source != "manual" {
		t.Fatalf("snapshot before delete: %v", err)
	}
	found := false
	for _, r := range snap.Records {
		if r.ID == vm.ID && r.Meta.Purpose == "專案" {
			found = true
		}
	}
	if !found {
		t.Fatal("snapshot lacks deleted record meta")
	}
}

func TestRestoreModes(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "grafana")
	e.mustOK(e.do("PUT", "/api/records/"+g.ID+"/meta", map[string]any{"owner": "甲"}))
	e.mustOK(e.do("DELETE", "/api/records/"+g.ID, nil))
	del := e.audits()[0]["detail"].(map[string]any)
	// recreate from the delete audit
	out := e.mustOK(e.do("POST", "/api/records/restore", map[string]any{"mode": "recreate", "record_id": g.ID, "data": del["before"], "meta": del["meta"]}))
	nid := out["record"].(map[string]any)["id"].(string)
	if re := e.mock.Find("A", "grafana"); re == nil || re.Content != "10.0.1.20" || re.TTL != 300 {
		t.Fatal("recreate")
	}
	if m, _ := e.srv.Store.Meta(context.Background(), nid); m.Owner != "甲" {
		t.Fatal("recreate meta")
	}
	if a := e.audits()[0]; a["action"] != "record_restore" || a["detail"].(map[string]any)["before"] != nil {
		t.Fatalf("restore audit %v", a)
	}
	// revert an update
	e.mustOK(e.do("PATCH", "/api/records/"+nid, map[string]any{"content": "10.0.1.77"}))
	upd := e.audits()[0]["detail"].(map[string]any)
	e.mustOK(e.do("POST", "/api/records/restore", map[string]any{"mode": "revert", "record_id": nid, "data": upd["before"]}))
	if e.mock.Find("A", "grafana").Content != "10.0.1.20" {
		t.Fatal("revert")
	}
	if d := e.audits()[0]["detail"].(map[string]any); d["before"].(map[string]any)["content"] != "10.0.1.77" {
		t.Fatalf("revert audit %v", d)
	}
	// delete (undo of a recreate)
	e.mustOK(e.do("POST", "/api/records/restore", map[string]any{"mode": "delete", "record_id": nid}))
	if e.mock.Find("A", "grafana") != nil || e.audits()[0]["action"] != "record_restore_delete" {
		t.Fatal("restore delete")
	}
	// conflicts are checked on restore
	gl := e.mock.Find("A", "gitlab")
	if w := e.do("POST", "/api/records/restore", map[string]any{"mode": "recreate", "data": map[string]any{"type": "A", "name": gl.Name, "content": gl.Content, "ttl": 1}}); w.Code != 409 {
		t.Fatalf("restore conflict: %d", w.Code)
	}
	if w := e.do("POST", "/api/records/restore", map[string]any{"mode": "nope"}); w.Code != 422 {
		t.Fatal("bad mode")
	}
}

func TestSnapshotsAPI(t *testing.T) {
	e := newEnv(t)
	e.login()
	info := e.mustOK(e.do("POST", "/api/snapshots", nil), 201)
	name := info["name"].(string)
	g := e.mock.Find("A", "grafana")
	e.mustOK(e.do("DELETE", "/api/records/"+g.ID, nil))
	e.mustOK(e.do("PATCH", "/api/records/"+e.mock.Find("A", "gitlab").ID, map[string]any{"content": "10.0.1.29"}))
	e.mustOK(e.do("POST", "/api/records", map[string]any{"type": "A", "name": "fresh", "content": "10.0.1.31"}), 201)
	diff := e.mustOK(e.do("GET", "/api/snapshots/"+name+"/diff", nil))["diff"].(map[string]any)
	if len(diff["removed"].([]any)) != 1 || len(diff["changed"].([]any)) != 1 || len(diff["added"].([]any)) != 1 {
		t.Fatalf("diff %v", diff)
	}
	w := e.do("GET", "/api/snapshots/"+name+"/download?format=zone", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "$ORIGIN example.org.") || !strings.Contains(w.Body.String(), "grafana.example.org.\t300\tIN\tA\t10.0.1.20") {
		t.Fatalf("zone %s", w.Body.String())
	}
	if w := e.do("GET", "/api/snapshots/..%2f..%2fportal.db/download", nil); w.Code == 200 {
		t.Fatal("traversal")
	}
	list := e.mustOK(e.do("GET", "/api/snapshots", nil))
	if len(list["snapshots"].([]any)) != 1 || list["keep"].(float64) != 30 {
		t.Fatalf("list %v", list)
	}
}

func TestExportCSV(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "grafana")
	e.mustOK(e.do("PUT", "/api/records/"+g.ID+"/meta", map[string]any{"owner": "陳, 同學", "purpose": "監控", "notes": "=HYPERLINK(\"x\")"}))
	w := e.do("GET", "/api/export.csv", nil)
	body := w.Body.String()
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Fatal("missing BOM")
	}
	lines := strings.Split(strings.TrimPrefix(body, "\xEF\xBB\xBF"), "\n")
	if lines[0] != strings.Join(exportHeader, ",") {
		t.Fatalf("header %q", lines[0])
	}
	if !strings.Contains(body, `grafana.example.org,A,10.0.1.20,false,300,,"陳, 同學",監控`) {
		t.Fatalf("row missing:\n%s", body)
	}
	if !strings.Contains(body, `"'=HYPERLINK(""x"")"`) {
		t.Fatalf("formula not neutralized:\n%s", body)
	}
	if !strings.Contains(body, "host019.example.org,A,10.0.3.119,false,1,,,,,,false,false,,,host,,,未指派負責人") {
		t.Fatalf("host row:\n%s", body)
	}
}

func TestRequestFlow(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"requester": {"陳同學"}, "contact": {"chen@example.com"}, "name": {"chen-blog"}, "type": {"CNAME"},
		"content": {"chen.github.io"}, "purpose": {"個人部落格"}, "expires_at": {""}}
	// validation errors
	w := e.do("POST", "/request", url.Values{"type": {"A"}, "name": {"Bad_Name"}}, from("10.0.0.50"))
	if w.Code != 422 {
		t.Fatalf("invalid form: %d", w.Code)
	}
	for _, msg := range []string{"請填寫姓名", "請填寫聯絡方式", "請填寫指向內容", "請填寫用途", "名稱格式不符："} {
		if !strings.Contains(w.Body.String(), msg) {
			t.Errorf("missing %q", msg)
		}
	}
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("type", "A")
	bad.Set("content", "127.0.0.1")
	if w := e.do("POST", "/request", bad, from("10.0.0.50")); w.Code != 422 || !strings.Contains(w.Body.String(), "不可指向本機位址") {
		t.Fatalf("content validation: %d", w.Code)
	}
	// submit
	w = e.do("POST", "/request", form, from("10.0.0.50"))
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/request/") {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if w := e.do("GET", loc, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"pending"`) || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("status page: %s", w.Body.String())
	}
	// throttle: 5 per hour per IP
	for i := 0; i < 4; i++ {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set("name", fmt.Sprintf("t%d", i))
		if w := e.do("POST", "/request", f, from("10.0.0.50")); w.Code != 303 {
			t.Fatalf("submit %d: %d", i, w.Code)
		}
	}
	if w := e.do("POST", "/request", form, from("10.0.0.50")); w.Code != 422 || !strings.Contains(w.Body.String(), "每小時最多申請 5 筆，請稍後再試。") {
		t.Fatalf("throttle: %d", w.Code)
	}
	// admin list: pending first, no token
	e.login()
	w = e.do("GET", "/api/requests", nil)
	if strings.Contains(w.Body.String(), `"token"`) {
		t.Fatal("token leaked")
	}
	list := e.mustOK(w)["requests"].([]any)
	var id float64
	for _, q := range list {
		m := q.(map[string]any)
		if m["name"] == "chen-blog.example.org" {
			id = m["id"].(float64)
			if m["conflict"] != "" || m["name_ok"] != true {
				t.Errorf("request flags %v", m)
			}
		}
	}
	// approve with defaults
	e.mustOK(e.do("POST", fmt.Sprintf("/api/requests/%d/approve", int(id)), map[string]any{"note": "已建立"}))
	rec := e.mock.Find("CNAME", "chen-blog")
	if rec == nil {
		t.Fatal("approve did not create record")
	}
	m, _ := e.srv.Store.Meta(context.Background(), rec.ID)
	if m.Owner != "陳同學（chen@example.com）" || m.Purpose != "個人部落格" {
		t.Fatalf("approve meta %+v", m)
	}
	if a := e.audits()[0]; a["action"] != "request_approve" || a["detail"].(map[string]any)["record_id"] != rec.ID {
		t.Fatalf("approve audit %v", a)
	}
	if w := e.do("POST", fmt.Sprintf("/api/requests/%d/approve", int(id)), map[string]any{}); w.Code != 409 {
		t.Fatalf("double approve: %d", w.Code)
	}
	if w := e.do("GET", loc, nil); !strings.Contains(w.Body.String(), `"status":"approved"`) || !strings.Contains(w.Body.String(), "已建立") {
		t.Fatal("status page after approve")
	}
	// reject requires a note
	other := list[1].(map[string]any)["id"].(float64)
	if w := e.do("POST", fmt.Sprintf("/api/requests/%d/reject", int(other)), map[string]any{"note": " "}); w.Code != 422 {
		t.Fatalf("reject without note: %d", w.Code)
	}
	e.mustOK(e.do("POST", fmt.Sprintf("/api/requests/%d/reject", int(other)), map[string]any{"note": "名稱重複"}))
	// disabled
	e.mustOK(e.do("PUT", "/api/settings", map[string]any{"requests_enabled": "0"}))
	if w := e.do("GET", "/request", nil); w.Code != 404 {
		t.Fatalf("closed form: %d", w.Code)
	}
	// pending cap
	e.mustOK(e.do("PUT", "/api/settings", map[string]any{"requests_enabled": "1"}))
	for i := 0; i < 100; i++ {
		e.srv.Store.AddRequest(context.Background(), modelRequest(i))
	}
	if w := e.do("POST", "/request", form, from("10.0.0.51")); !strings.Contains(w.Body.String(), "待審申請已滿，請聯絡管理者。") {
		t.Fatalf("pending cap: %d", w.Code)
	}
}

func TestAutoDeleteViaAPI(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.srv.Now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, e.srv.Cfg.Timezone) }
	s := e.mock.Find("A", "host005")
	e.mustOK(e.do("PUT", "/api/listings/"+s.ID, map[string]any{"title": "S5", "icon": "none"}))
	e.mustOK(e.do("PUT", "/api/records/"+s.ID+"/meta", map[string]any{"expires_at": "2026-10-01", "auto_delete": true}))
	n, err := e.srv.Maint.AutoDelete(context.Background(), e.srv.Now())
	if err != nil || n != 1 || e.mock.Find("A", "host005") != nil {
		t.Fatalf("auto delete %d %v", n, err)
	}
	if l, _ := e.srv.Store.Listing(context.Background(), s.ID); l != nil {
		t.Fatal("listing kept")
	}
	a := e.audits()[0]
	d := a["detail"].(map[string]any)
	if a["action"] != "record_auto_delete" || d["record_id"] != s.ID || d["before"] == nil || d["meta"].(map[string]any)["auto_delete"] != true {
		t.Fatalf("audit %v", a)
	}
}

func TestListingsAndSettings(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "gitlab")
	// Simple Icons
	out := e.mustOK(e.do("PUT", "/api/listings/"+g.ID, map[string]any{"title": "GitLab", "category": "開發工具", "icon": "si:gitlab", "tags": []string{"需帳號", "成員限定"}}))
	if out["icon_error"] != "" || out["listing"].(map[string]any)["icon_url"] != "/icons/si-gitlab.svg" {
		t.Fatalf("si %v", out)
	}
	out = e.mustOK(e.do("PUT", "/api/listings/"+g.ID, map[string]any{"title": "GitLab", "category": "開發工具", "icon": "si:missing", "tags": []string{"需帳號", "成員限定"}}))
	if out["icon_error"] != "Simple Icons 查無 missing，暫以文字圖示顯示" {
		t.Fatalf("si missing %v", out)
	}
	if w := e.do("PUT", "/api/listings/"+g.ID, map[string]any{"title": "x", "tags": []string{"1", "2", "3", "4", "5", "6", "7"}}); w.Code != 422 {
		t.Fatal("tag limit")
	}
	// upload validates by header
	if w := e.upload("/api/listings/"+g.ID+"/icon", "x.png", []byte("<html>")); w.Code != 422 {
		t.Fatalf("html as png: %d", w.Code)
	}
	out = e.mustOK(e.upload("/api/listings/"+g.ID+"/icon", "x.gif", mockcf.PNG1))
	if !strings.HasSuffix(out["listing"].(map[string]any)["icon_url"].(string), ".png") {
		t.Fatalf("upload %v", out)
	}
	// portal.json lists it
	p := e.mustOK(e.do("GET", "/portal.json", nil))
	svc := p["services"].([]any)[0].(map[string]any)
	if svc["title"] != "GitLab" || svc["url"] != "https://gitlab.example.org" || len(svc["tags"].([]any)) != 2 {
		t.Fatalf("portal %v", svc)
	}
	if _, ok := p["site"].(map[string]any)["name_rule"]; ok {
		t.Fatal("rule settings leaked")
	}
	// settings
	if w := e.do("PUT", "/api/settings", map[string]any{"name_rule": "(["}); w.Code != 422 {
		t.Fatal("invalid regexp accepted")
	}
	s := e.mustOK(e.do("PUT", "/api/settings", map[string]any{"scan_ports": "22, 443 22", "title": "Lab"}))
	if s["scan_ports"] != "22,443" || s["title"] != "Lab" || s["accent"] != "#2dd4bf" {
		t.Fatalf("settings %v", s)
	}
	s = e.mustOK(e.do("PUT", "/api/settings", map[string]any{"expiry_cycle_name": "學期末", "expiry_cycle_dates": "06-30，01-31 06-30", "preset_tags": "需 VPN, , 測試中、需 VPN"}))
	if s["expiry_cycle_dates"] != "01-31, 06-30" || s["preset_tags"] != "需 VPN, 測試中" {
		t.Fatalf("cycle/tags %v %v", s["expiry_cycle_dates"], s["preset_tags"])
	}
	for _, bad := range []map[string]any{{"expiry_cycle_dates": "02-29"}, {"expiry_cycle_dates": "1-31"}, {"expiry_cycle_name": ""}} {
		if w := e.do("PUT", "/api/settings", bad); w.Code != 422 {
			t.Errorf("%v accepted: %d", bad, w.Code)
		}
	}
	if w := e.upload("/api/settings/media/hero", "h.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); w.Code != 422 {
		t.Fatal("svg hero accepted")
	}
	s = e.mustOK(e.upload("/api/settings/media/logo", "l.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)))
	if !strings.HasPrefix(s["logo_url"].(string), "/media/logo-") {
		t.Fatalf("logo %v", s)
	}
	if w := e.do("GET", s["logo_url"].(string), nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatal("serve logo")
	}
	e.mustOK(e.do("DELETE", "/api/settings/media/logo", nil))
	e.mustOK(e.do("DELETE", "/api/listings/"+g.ID, nil))
	if a := e.audits()[0]; a["action"] != "listing_remove" {
		t.Fatal("listing_remove audit")
	}
	if w := e.do("GET", "/api/audit", nil); len(e.mustOK(w)["entries"].([]any)) < 6 {
		t.Fatal("audit list")
	}
}

func TestMuteAndScan(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "grafana")
	e.mustOK(e.do("POST", "/api/issues/mute", map[string]any{"record_id": g.ID, "kind": "private_ip", "muted": true}))
	rec := findRec(t, e.mustOK(e.do("GET", "/api/records", nil)), "grafana")
	if len(rec["muted"].([]any)) != 1 {
		t.Fatalf("muted %v", rec["muted"])
	}
	if w := e.do("POST", "/api/issues/mute", map[string]any{"record_id": g.ID, "kind": "bogus", "muted": true}); w.Code != 422 {
		t.Fatal("unknown kind")
	}
	e.mustOK(e.do("POST", "/api/scan", nil), 202)
	deadline := time.Now().Add(10 * time.Second)
	for e.srv.Scanner.Status().Running && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if st := e.srv.Scanner.Status(); st.Running || st.Count == 0 {
		t.Fatalf("scan %+v", st)
	}
}

func TestFailedUpdateWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "gitlab")
	audits := len(e.audits())
	// Renaming gitlab onto the docs CNAME conflicts; the meta must not be saved.
	w := e.do("PATCH", "/api/records/"+g.ID, map[string]any{"name": "docs", "meta": map[string]any{"owner": "不該寫入"}})
	if w.Code != 409 {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
	if m, _ := e.srv.Store.Meta(context.Background(), g.ID); m.Owner != "" {
		t.Fatalf("meta written despite failed update: %+v", m)
	}
	if n := len(e.audits()); n != audits {
		t.Fatalf("audit written despite failed update: %d", n)
	}
}

func TestOversizedUpload(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "gitlab")
	e.mustOK(e.do("PUT", "/api/listings/"+g.ID, map[string]any{"title": "GitLab", "icon": "none"}))
	big := append(append([]byte{}, mockcf.PNG1...), make([]byte, 2<<20)...)
	if w := e.upload("/api/listings/"+g.ID+"/icon", "x.png", big); w.Code != 413 || !strings.Contains(w.Body.String(), "1 MB") {
		t.Fatalf("oversized upload: %d %s", w.Code, w.Body.String())
	}
}

func TestCloudflareRejectionRollsBackMeta(t *testing.T) {
	e := newEnv(t)
	e.login()
	g := e.mock.Find("A", "grafana")
	e.mustOK(e.do("PUT", "/api/records/"+g.ID+"/meta", map[string]any{"owner": "原本"}))
	// The mock rejects proxying a private address after validation passed.
	w := e.do("PATCH", "/api/records/"+g.ID, map[string]any{"proxied": true, "meta": map[string]any{"owner": "新的"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Cloudflare: 9003") {
		t.Fatalf("expected Cloudflare rejection: %d %s", w.Code, w.Body.String())
	}
	if m, _ := e.srv.Store.Meta(context.Background(), g.ID); m.Owner != "原本" {
		t.Fatalf("meta not rolled back: %+v", m)
	}
}

func TestMetaForMissingRecord(t *testing.T) {
	e := newEnv(t)
	e.login()
	if w := e.do("PUT", "/api/records/does-not-exist/meta", map[string]any{"owner": "x"}); w.Code != 404 {
		t.Fatalf("meta for missing record: %d", w.Code)
	}
	if w := e.do("POST", "/api/records/restore", map[string]any{"mode": "meta", "record_id": "gone", "meta": map[string]any{"owner": "x"}}); w.Code != 404 {
		t.Fatalf("meta restore for missing record: %d", w.Code)
	}
	if m, _ := e.srv.Store.AllMeta(context.Background()); len(m) != 0 {
		t.Fatalf("orphan meta written: %v", m)
	}
}

func TestNameRuleOnNewNamesOnly(t *testing.T) {
	e := newEnv(t)
	e.login()
	if w := e.do("POST", "/api/records", map[string]any{"type": "A", "name": "Bad_Name", "content": "10.0.0.1"}); w.Code != 422 || !strings.Contains(w.Body.String(), "名稱格式不符") {
		t.Fatalf("create with bad name: %d %s", w.Code, w.Body.String())
	}
	g := e.mock.Find("A", "gitlab")
	if w := e.do("PATCH", "/api/records/"+g.ID, map[string]any{"name": "Git_Lab"}); w.Code != 422 {
		t.Fatalf("rename to bad name: %d", w.Code)
	}
	// An existing name that breaks the rule can still be edited and restored.
	old := e.mock.Find("A", "Old_Box")
	e.mustOK(e.do("PATCH", "/api/records/"+old.ID, map[string]any{"content": "10.0.2.6"}))
	e.mustOK(e.do("DELETE", "/api/records/"+old.ID, nil))
	del := e.audits()[0]["detail"].(map[string]any)
	e.mustOK(e.do("POST", "/api/records/restore", map[string]any{"mode": "recreate", "data": del["before"]}))
}

func TestBulkMetaAuditedPerRecord(t *testing.T) {
	e := newEnv(t)
	e.login()
	a, b := e.mock.Find("A", "host001"), e.mock.Find("A", "host002")
	e.mustOK(e.do("POST", "/api/records/bulk-meta", map[string]any{"ids": []string{a.ID, b.ID}, "set": map[string]any{"owner": "維運組"}}))
	hist := e.mustOK(e.do("GET", "/api/records/"+a.ID+"/history", nil))["entries"].([]any)
	if len(hist) != 1 || hist[0].(map[string]any)["action"] != "bulk_meta" || !strings.Contains(hist[0].(map[string]any)["detail"].(string), `"meta_before"`) {
		t.Fatalf("history of a bulk-edited record: %v", hist)
	}
}

func TestUpdateOnlySentFields(t *testing.T) {
	e := newEnv(t)
	e.login()
	mx := e.mock.Find("MX", mockcf.Zone)
	e.mustOK(e.do("PATCH", "/api/records/"+mx.ID, map[string]any{"priority": 20}))
	got := e.mock.Find("MX", mockcf.Zone)
	if got.Priority == nil || *got.Priority != 20 || got.Content != "mail.example.com" {
		t.Fatalf("priority change lost or other fields changed: %+v", got)
	}
	if a := e.audits()[0]; a["action"] != "record_update" {
		t.Fatalf("priority change not recorded as an update: %v", a)
	}
}
