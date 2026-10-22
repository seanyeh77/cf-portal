package httpapi

import (
	"context"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie  = "cfp_session"
	csrfHeader     = "X-Requested-With"
	csrfValue      = "cf-portal"
	forbiddenText  = "此頁面只開放內部網路或 VPN 存取。"
	maxLoginFails  = 5
	loginLockout   = 15 * time.Minute
	httpLoginError = "目前為 HTTP 連線，無法保存登入狀態。請改用 HTTPS，或設定 COOKIE_SECURE=false。"
)

func contains(list []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// resolveClient returns the client address and whether the original
// request used HTTPS. Forwarded headers are honored only from trusted
// proxies; the client is the right-most X-Forwarded-For entry that is not
// itself a trusted proxy.
func resolveClient(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, r.TLS != nil
	}
	peer = peer.Unmap()
	https := r.TLS != nil
	if !contains(trusted, peer) {
		return peer, https
	}
	client := peer
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(strings.Join(xff, ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				break
			}
			a = a.Unmap()
			client = a
			if !contains(trusted, a) {
				break
			}
		}
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		last := strings.TrimSpace(p[strings.LastIndexByte(p, ',')+1:])
		https = strings.EqualFold(last, "https")
	}
	return client, https
}

// isAdminPath reports paths restricted by ADMIN_ALLOWED_CIDRS.
func isAdminPath(p string) bool {
	return p == "/admin" || strings.HasPrefix(p, "/admin/") || strings.HasPrefix(p, "/api/") || p == "/api" ||
		p == "/login" || p == "/logout"
}

var forbiddenPage = template.Must(template.New("403").Parse(`<!doctype html><html lang="zh-Hant"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>403</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f6f7f8;color:#20262e;font:16px/1.6 system-ui,"Noto Sans TC","PingFang TC","Microsoft JhengHei",sans-serif}
main{background:#fff;border-top:3px solid #b42318;padding:32px 40px;max-width:420px}h1{font-size:18px;margin:0 0 8px}p{margin:0;color:#66707c;font-size:14px}
@media (prefers-color-scheme:dark){body{background:#14181d;color:#e6e9ec}main{background:#1c2229}p{color:#98a2ad}}</style></head>
<body><main><h1>{{.Msg}}</h1><p>來源位址：{{.IP}}</p></main></body></html>`))

func (s *Server) allowlist(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, https := resolveClient(r, s.Cfg.TrustedProxies)
		ctx := context.WithValue(context.WithValue(r.Context(), keyIP, ip), keyHTTPS, https)
		r = r.WithContext(ctx)
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		list := s.Cfg.AllowedCIDRs
		if isAdminPath(r.URL.Path) {
			list = s.Cfg.AdminAllowedCIDRs
		}
		if !ip.IsValid() || !contains(list, ip) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasSuffix(r.URL.Path, ".json") {
				writeErr(w, http.StatusForbidden, forbiddenText)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			forbiddenPage.Execute(w, map[string]string{"Msg": forbiddenText, "IP": ip.String()})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggedIn reports whether the request carries a valid session.
func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	user, ok := s.signer.Verify(c.Value, s.Now())
	return ok && user == s.Cfg.AdminUsername
}

// sameOrigin checks that an Origin header, if present, matches Host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) requireAdmin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			writeErr(w, http.StatusUnauthorized, "登入已逾時，請重新登入。")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) != csrfValue || !sameOrigin(r) {
				writeErr(w, http.StatusForbidden, "請求來源驗證失敗，請重新整理頁面。")
				return
			}
		}
		h(w, r)
	})
}

type throttleEntry struct {
	fails int
	until time.Time
}

type throttle struct {
	mu sync.Mutex
	m  map[string]*throttleEntry
}

func newThrottle() *throttle { return &throttle{m: map[string]*throttleEntry{}} }

func (t *throttle) locked(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[ip]
	return e != nil && now.Before(e.until)
}

func (t *throttle) fail(ip string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[ip]
	if e == nil || (!e.until.IsZero() && !now.Before(e.until)) {
		e = &throttleEntry{}
		t.m[ip] = e
	}
	e.fails++
	if e.fails >= maxLoginFails {
		e.until = now.Add(loginLockout)
	}
	if len(t.m) > 10000 {
		for k, v := range t.m {
			if now.After(v.until) {
				delete(t.m, k)
			}
		}
	}
}

func (t *throttle) reset(ip string) {
	t.mu.Lock()
	delete(t.m, ip)
	t.mu.Unlock()
}
