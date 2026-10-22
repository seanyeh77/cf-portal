package scanner

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/cloudflare"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/snapshots"
	"github.com/nycu-sdc/sdc-portal/internal/store"
	"github.com/nycu-sdc/sdc-portal/testdata/mockcf"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if host == "broken.example" {
		return nil, &net.DNSError{Err: "server misbehaving", Name: host, IsTemporary: true}
	}
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func newScanner(t *testing.T, res fakeResolver, page http.HandlerFunc) *Scanner {
	srv := httptest.NewServer(page)
	t.Cleanup(srv.Close)
	s := New(nil, nil, 500*time.Millisecond, time.Hour, "canary.example", nil)
	s.Resolver = res
	s.HTTPBase = func(name, scheme string) string { return srv.URL + "/" + scheme + "/" + name }
	return s
}

func TestParsePorts(t *testing.T) {
	p, err := ParsePorts("443, 80 22,443")
	if err != nil || len(p) != 3 || p[0] != 443 || p[2] != 22 {
		t.Fatalf("%v %v", p, err)
	}
	for _, bad := range []string{"", "x", "0", "70000"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCNAME(t *testing.T) {
	s := newScanner(t, fakeResolver{"canary.example": {"1.1.1.1"}, "ok.example.com": {"192.0.2.1"}, "gone.github.io": {"185.199.108.153"}, "live.github.io": {"185.199.108.153"}},
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "dead.") {
				w.WriteHeader(404)
				w.Write([]byte("<h1>There isn't a GitHub Pages site here.</h1>"))
				return
			}
			w.Write([]byte("hello"))
		})
	ctx := context.Background()
	r := s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "CNAME", Name: "a.z", Content: "gone.example.com"}}, []int{1}, true)
	if r.DNSStatus != "nxdomain" || r.DNSDetail != "gone.example.com：查無網域" || r.Reachable {
		t.Errorf("nxdomain: %+v", r)
	}
	r = s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "CNAME", Name: "a.z", Content: "gone.example.com"}}, []int{1}, false)
	if r.DNSStatus != "unknown" {
		t.Errorf("DNS unavailable must be unknown: %+v", r)
	}
	r = s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "CNAME", Name: "a.z", Content: "broken.example"}}, []int{1}, true)
	if r.DNSStatus != "unknown" {
		t.Errorf("temporary failure must be unknown: %+v", r)
	}
	r = s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "CNAME", Name: "dead.z", Content: "gone.github.io"}}, []int{1}, true)
	if r.DNSStatus != "unclaimed" || r.DNSDetail != "GitHub Pages：查無對應站台" {
		t.Errorf("unclaimed: %+v", r)
	}
	r = s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "CNAME", Name: "live.z", Content: "live.github.io"}}, []int{1}, true)
	if r.DNSStatus != "ok" || !strings.HasPrefix(r.DNSDetail, "解析為 185.199.108.153") {
		t.Errorf("claimed: %+v", r)
	}
	if TakeoverFor("x.herokudns.com").Service != "Heroku" || TakeoverFor("github.io.evil.com") != nil {
		t.Error("TakeoverFor")
	}
}

func TestProxied(t *testing.T) {
	s := newScanner(t, fakeResolver{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "down") {
			w.WriteHeader(522)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	ctx := context.Background()
	r := s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "A", Name: "down.z", Content: "1.2.3.4", Proxied: true}}, []int{1}, true)
	if r.Reachable || r.Detail != "來源主機無法連線（HTTP 522）" {
		t.Errorf("522: %+v", r)
	}
	r = s.Scan(ctx, model.Record{RecordInput: model.RecordInput{Type: "A", Name: "up.z", Content: "1.2.3.4", Proxied: true}}, []int{1}, true)
	if !r.Reachable || r.Detail != "經 Cloudflare：HTTP 302" {
		t.Errorf("redirect must not be followed: %+v", r)
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	open := ln.Addr().(*net.TCPAddr).Port
	closed := open + 1
	s := newScanner(t, fakeResolver{}, nil)
	r := s.Scan(context.Background(), model.Record{RecordInput: model.RecordInput{Type: "A", Name: "x.z", Content: "127.0.0.1"}}, []int{open, closed}, true)
	want := "127.0.0.1：埠 " + strconv.Itoa(open) + " 有回應"
	if !r.Reachable || r.Detail != want || r.OpenPorts != strconv.Itoa(open) {
		t.Errorf("open: %+v", r)
	}
	r = s.Scan(context.Background(), model.Record{RecordInput: model.RecordInput{Type: "A", Name: "x.z", Content: "127.0.0.1"}}, []int{closed}, true)
	if r.Reachable || r.Detail != "127.0.0.1：埠 "+strconv.Itoa(closed)+" 無回應" {
		t.Errorf("closed: %+v", r)
	}
}

func TestRunAndMaint(t *testing.T) {
	m := mockcf.New("tok")
	srv := httptest.NewServer(m)
	defer srv.Close()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	cf := cloudflare.New(srv.URL+"/client/v4", "tok", mockcf.Zone, "")
	ctx := context.Background()
	stud := m.Find("A", "student001")
	st.PutMeta(ctx, stud.ID, model.Meta{NoScan: true})
	st.PutScan(ctx, stud.ID, 1, false, "", "x", "", "")
	st.PutScan(ctx, "gone", 1, false, "", "x", "", "")
	s := New(cf, st, 50*time.Millisecond, time.Hour, "canary", func(context.Context) []int { return []int{9} })
	s.Resolver = fakeResolver{}
	s.RunOnce(ctx)
	scans, _ := st.AllScans(ctx)
	if _, ok := scans["gone"]; ok {
		t.Error("scan of deleted record kept")
	}
	if _, ok := scans[stud.ID]; ok {
		t.Error("scan of no_scan record kept")
	}
	if _, ok := scans[m.Find("A", "grafana").ID]; !ok {
		t.Error("grafana not scanned")
	}
	if _, ok := scans[m.Find("TXT", "_dmarc").ID]; ok {
		t.Error("TXT scanned")
	}
	if st := s.Status(); st.Running || st.DNSOK || st.Count == 0 {
		t.Errorf("status %+v", st)
	}

	loc, _ := time.LoadLocation("Asia/Taipei")
	mt := &Maint{CF: cf, Store: st, Snaps: &snapshots.Store{Dir: filepath.Join(dir, "snapshots"), Loc: loc}, Loc: loc, Keep: 30, Interval: time.Hour}
	var forgotten []string
	mt.Forget = func(_ context.Context, id string) { forgotten = append(forgotten, id); st.ForgetRecord(ctx, id) }
	exp := m.Find("A", "student002")
	keep := m.Find("A", "student003")
	st.PutMeta(ctx, exp.ID, model.Meta{Owner: "x", ExpiresAt: "2026-10-21", AutoDelete: true})
	st.PutMeta(ctx, keep.ID, model.Meta{Owner: "x", ExpiresAt: "2026-10-21"})
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, loc)
	n, err := mt.AutoDelete(ctx, now)
	if err != nil || n != 1 || m.Find("A", "student002") != nil || m.Find("A", "student003") == nil || len(forgotten) != 1 {
		t.Fatalf("auto delete n=%d err=%v", n, err)
	}
	audit, _ := st.Audit(ctx, 10)
	if len(audit) != 1 || audit[0].Action != "record_auto_delete" || !strings.Contains(audit[0].Detail, exp.ID) || !strings.Contains(audit[0].Detail, `"before"`) {
		t.Fatalf("audit %+v", audit)
	}
	mt.RunOnce(ctx)
	mt.RunOnce(ctx)
	if l, _ := mt.Snaps.List(); len(l) != 1 || l[0].Source != "auto" {
		t.Fatalf("daily snapshot: %+v", l)
	}
}
