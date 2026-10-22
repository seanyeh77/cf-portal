package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nycu-sdc/sdc-portal/internal/config"
)

func TestResolveClient(t *testing.T) {
	trusted, _ := config.ParseCIDRs("127.0.0.1,10.9.0.0/16")
	cases := []struct {
		remote, xff, proto string
		want               string
		https              bool
	}{
		{"8.8.8.8:1", "10.0.0.5", "https", "8.8.8.8", false},            // untrusted peer: headers ignored
		{"127.0.0.1:1", "10.0.0.5", "https", "10.0.0.5", true},          // trusted proxy
		{"127.0.0.1:1", "1.2.3.4, 10.0.0.5", "", "10.0.0.5", false},     // right-most untrusted
		{"127.0.0.1:1", "10.0.0.5, 10.9.1.1", "", "10.0.0.5", false},    // skip trusted hops
		{"127.0.0.1:1", "", "", "127.0.0.1", false},                     // no header
		{"[::ffff:10.1.2.3]:1", "", "", "10.1.2.3", false},              // mapped peer
		{"127.0.0.1:1", "::ffff:192.168.1.9", "", "192.168.1.9", false}, // mapped in header
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		got, https := resolveClient(r, trusted)
		if got != netip.MustParseAddr(c.want) || https != c.https {
			t.Errorf("%+v: got %s https=%v", c, got, https)
		}
	}
}

func TestAllowlist(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.AdminAllowedCIDRs, _ = config.ParseCIDRs("10.0.5.0/24,127.0.0.1")
	})
	cases := []struct {
		ip, path string
		want     int
	}{
		{"10.1.2.3", "/", 200},
		{"::ffff:10.1.2.3", "/", 200},
		{"172.17.0.1", "/", 403},
		{"8.8.8.8", "/portal.json", 403},
		{"8.8.8.8", "/healthz", 200},
		{"10.1.2.3", "/login", 403},
		{"10.0.5.7", "/login", 200},
		{"::ffff:10.0.5.7", "/login", 200},
		{"10.1.2.3", "/api/records", 403},
		{"10.1.2.3", "/admin", 403},
	}
	for _, c := range cases {
		w := e.do("GET", c.path, nil, from(c.ip))
		if c.ip[0] == ':' {
			w = e.do("GET", c.path, nil, func(r *http.Request) { r.RemoteAddr = "[" + c.ip + "]:1" })
		}
		if w.Code != c.want {
			t.Errorf("%s %s: %d, want %d", c.ip, c.path, w.Code, c.want)
		}
		if c.want == 403 && !strings.Contains(w.Body.String(), forbiddenText) {
			t.Errorf("%s %s: missing message", c.ip, c.path)
		}
	}
	// X-Forwarded-For from an untrusted peer cannot bypass the list.
	if w := e.do("GET", "/", nil, from("8.8.8.8"), header("X-Forwarded-For", "10.0.0.1")); w.Code != 403 {
		t.Errorf("spoofed XFF accepted: %d", w.Code)
	}
	// From the trusted proxy it is honored.
	if w := e.do("GET", "/", nil, header("X-Forwarded-For", "8.8.8.8")); w.Code != 403 {
		t.Errorf("proxied external client allowed: %d", w.Code)
	}
}

func TestCSRFAndAuth(t *testing.T) {
	e := newEnv(t)
	if w := e.do("GET", "/api/records", nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := e.do("GET", "/admin", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("admin redirect: %d", w.Code)
	}
	e.login()
	body := map[string]any{"type": "A", "name": "csrf", "content": "10.0.0.1"}
	if w := e.do("POST", "/api/records", body, noCSRF()); w.Code != 403 {
		t.Errorf("missing header: %d", w.Code)
	}
	if w := e.do("POST", "/api/records", body, header(csrfHeader, "XMLHttpRequest")); w.Code != 403 {
		t.Errorf("wrong header: %d", w.Code)
	}
	if w := e.do("POST", "/api/records", body, header("Origin", "https://evil.example")); w.Code != 403 {
		t.Errorf("cross origin: %d", w.Code)
	}
	if w := e.do("POST", "/api/records", body, header("Origin", "http://example.com")); w.Code != 201 {
		t.Errorf("same origin: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("GET", "/api/records", nil, noCSRF()); w.Code != 200 {
		t.Errorf("GET needs no header: %d", w.Code)
	}
	bad := *e.cookie
	bad.Value += "x"
	e.cookie = &bad
	if w := e.do("GET", "/api/records", nil); w.Code != 401 {
		t.Errorf("tampered cookie: %d", w.Code)
	}
}

func TestLoginThrottleAndHTTPS(t *testing.T) {
	e := newEnv(t)
	wrong := url.Values{"username": {"admin"}, "password": {"nope"}}
	for i := 0; i < 4; i++ {
		if w := e.do("POST", "/login", wrong, from("10.0.0.9")); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := e.do("POST", "/login", wrong, from("10.0.0.9")); w.Code != 429 {
		t.Fatalf("5th failure should lock: %d", w.Code)
	}
	right := url.Values{"username": {"admin"}, "password": {testPassword}}
	if w := e.do("POST", "/login", right, from("10.0.0.9")); w.Code != 429 {
		t.Fatalf("locked IP must stay locked: %d", w.Code)
	}
	if w := e.do("POST", "/login", right, from("10.0.0.10")); w.Code != 303 {
		t.Fatalf("other IP: %d", w.Code)
	}
	w := e.do("POST", "/login", right, from("10.0.0.10"))
	c := w.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure {
		t.Errorf("cookie flags %+v", c)
	}
	n := 0
	for _, a := range e.audits() {
		if a["action"] == "login_failed" {
			n++
		}
	}
	if n != 5 {
		t.Errorf("login_failed audits: %d", n)
	}

	s := newEnv(t, func(c *config.Config) { c.CookieSecure = true })
	w = s.do("POST", "/login", right)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "目前為 HTTP 連線，無法保存登入狀態") {
		t.Fatalf("HTTP login must be refused: %d", w.Code)
	}
	w = s.do("POST", "/login", right, header("X-Forwarded-Proto", "https"))
	if w.Code != 303 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("HTTPS via trusted proxy: %d", w.Code)
	}
	if w := s.do("POST", "/login", right, from("10.0.0.3"), header("X-Forwarded-Proto", "https")); w.Code != 400 {
		t.Fatalf("X-Forwarded-Proto from untrusted peer must be ignored: %d", w.Code)
	}
}

func TestServeFiles(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.srv.Store.Dir, "icons")
	os.WriteFile(filepath.Join(dir, "x.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o600)
	os.WriteFile(filepath.Join(e.srv.Store.Dir, "secret.png"), []byte("secret"), 0o600)
	w := e.do("GET", "/icons/x.svg", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		w.Header().Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; sandbox" {
		t.Fatalf("headers %v", w.Header())
	}
	for _, p := range []string{"/icons/..%2fsecret.png", "/icons/../secret.png", "/media/x.html", "/icons/.htaccess", "/icons/a%00.png"} {
		if w := e.do("GET", p, nil); w.Code == 200 {
			t.Errorf("%s served", p)
		}
	}
}

func TestOversizedBody(t *testing.T) {
	e := newEnv(t)
	e.login()
	big := map[string]string{"notes": strings.Repeat("x", maxJSONBody)}
	w := e.do("PUT", "/api/records/x/meta", big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d %s", w.Code, w.Body.String())
	}
}
