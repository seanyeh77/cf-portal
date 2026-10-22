package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/auth"
	"github.com/nycu-sdc/sdc-portal/internal/cloudflare"
	"github.com/nycu-sdc/sdc-portal/internal/config"
	"github.com/nycu-sdc/sdc-portal/internal/health"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/scanner"
	"github.com/nycu-sdc/sdc-portal/internal/snapshots"
	"github.com/nycu-sdc/sdc-portal/internal/store"
	"github.com/nycu-sdc/sdc-portal/testdata/mockcf"
)

const testPassword = "correct horse battery"

var (
	hashOnce sync.Once
	pwHash   string
)

type env struct {
	t    *testing.T
	mock *mockcf.Server
	srv  *Server
	h    http.Handler
	// cookie is the admin session cookie.
	cookie *http.Cookie
}

type noDNS struct{}

func (noDNS) LookupHost(context.Context, string) ([]string, error) { return nil, &net404{} }

type net404 struct{}

func (*net404) Error() string { return "no dns in tests" }

func newEnv(t *testing.T, mut ...func(*config.Config)) *env {
	t.Helper()
	hashOnce.Do(func() { pwHash, _ = auth.HashPassword(testPassword) })
	m := mockcf.New("tok")
	cfsrv := httptest.NewServer(m)
	t.Cleanup(cfsrv.Close)
	dir := t.TempDir()
	vars := map[string]string{
		"CF_API_TOKEN": "tok", "CF_API_BASE": cfsrv.URL + "/client/v4", "ADMIN_PASSWORD_HASH": pwHash,
		"SECRET_KEY": strings.Repeat("s", 32), "COOKIE_SECURE": "false", "DB_PATH": filepath.Join(dir, "portal.db"),
		"SIMPLE_ICONS_BASE": cfsrv.URL + "/si", "SCAN_TIMEOUT": "0.05",
	}
	cfg, err := config.Load(func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mut {
		f(cfg)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.Defaults = store.DefaultSettings(cfg.PortalTitle)
	cf := cloudflare.New(cfg.CFAPIBase, cfg.CFAPIToken, cfg.CFZoneName, "")
	snaps := &snapshots.Store{Dir: filepath.Join(dir, "snapshots"), Loc: cfg.Timezone}
	icons := assets.NewFetcher(filepath.Join(dir, "icons"), cfg.SimpleIconsBase, 5*time.Second, true)
	hc := health.New(st, time.Minute, 5*time.Second, true)
	sc := scanner.New(cf, st, cfg.ScanTimeout, time.Hour, "canary.invalid", func(context.Context) []int { return []int{9} })
	sc.Resolver = noDNS{}
	mt := &scanner.Maint{CF: cf, Store: st, Snaps: snaps, Loc: cfg.Timezone, Keep: 30, Interval: time.Hour}
	srv := New(cfg, cf, st, sc, mt, hc, icons, snaps)
	srv.Assets = fstest.MapFS{
		"portal.html": {Data: []byte("<html><body>portal<!--INIT--></body></html>")},
		"admin.html":  {Data: []byte("<html><body>admin<!--INIT--></body></html>")},
	}
	return &env{t: t, mock: m, srv: srv, h: srv.Handler()}
}

type reqOpt func(*http.Request)

func from(ip string) reqOpt     { return func(r *http.Request) { r.RemoteAddr = ip + ":40000" } }
func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noCSRF() reqOpt            { return func(r *http.Request) { r.Header.Del(csrfHeader) } }

func (e *env) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		rd = strings.NewReader(b.Encode())
		ctype = "application/x-www-form-urlencoded"
	case *multipart.Writer:
		panic("use upload")
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
		ctype = "application/json"
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "127.0.0.1:40000"
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if strings.HasPrefix(path, "/api/") {
		req.Header.Set(csrfHeader, csrfValue)
	}
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func (e *env) upload(path, filename string, data []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(csrfHeader, csrfValue)
	req.AddCookie(e.cookie)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func (e *env) login() {
	e.t.Helper()
	w := e.do("POST", "/login", url.Values{"username": {"admin"}, "password": {testPassword}})
	if w.Code != http.StatusSeeOther {
		e.t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			e.cookie = c
		}
	}
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return m
}

func (e *env) mustOK(w *httptest.ResponseRecorder, codes ...int) map[string]any {
	e.t.Helper()
	ok := w.Code == 200
	for _, c := range codes {
		ok = ok || w.Code == c
	}
	if !ok {
		e.t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		return decodeBody(e.t, w)
	}
	return nil
}

func (e *env) audits() []map[string]any {
	rows, _ := e.srv.Store.Audit(context.Background(), 500)
	var out []map[string]any
	for _, r := range rows {
		var d map[string]any
		json.Unmarshal([]byte(r.Detail), &d)
		out = append(out, map[string]any{"action": r.Action, "target": r.Target, "detail": d})
	}
	return out
}

func modelRequest(i int) model.Request {
	return model.Request{Token: fmt.Sprintf("tok%d", i), IP: "10.9.9.9", Requester: "x", Contact: "x", Name: fmt.Sprintf("bulk%d.sdc.nycu.club", i), Type: "A", Content: "10.0.0.1", Purpose: "x"}
}
