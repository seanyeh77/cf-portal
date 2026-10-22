package httpapi

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/auth"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/webui"
)

func (s *Server) writePage(w http.ResponseWriter, status int, entry string, data any) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", pageCSP)
	w.WriteHeader(status)
	w.Write(s.page(entry, data))
}

func (s *Server) page(entry string, data any) []byte {
	return webui.Render(s.Assets, entry, data)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if strings.HasSuffix(p, ".html") || p == "" {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.Assets, p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, p, time.Time{}, strings.NewReader(string(b)))
}

// serveFile serves uploaded images with a restrictive policy.
func (s *Server) serveFile(sub string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !assets.NameRe.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(s.Store.Dir, sub, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		h := w.Header()
		h.Set("Content-Type", assets.ContentTypes[name[strings.LastIndexByte(name, '.')+1:]])
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		h.Set("Cache-Control", "public, max-age=86400")
		fi, _ := f.Stat()
		var mod time.Time
		if fi != nil {
			mod = fi.ModTime()
		}
		http.ServeContent(w, r, "", mod, f)
	}
}

func mediaURL(name string) string {
	if name == "" {
		return ""
	}
	return "/media/" + name
}

func iconURL(l model.Listing) string {
	if l.IconFile == nil || *l.IconFile == "" {
		return ""
	}
	return "/icons/" + *l.IconFile
}

// publicSettings are the settings shown on public pages.
func (s *Server) publicSettings(set map[string]string) map[string]any {
	return map[string]any{
		"title": set["title"], "subtitle": set["subtitle"], "accent": set["accent"],
		"logo": mediaURL(set["logo"]), "hero": mediaURL(set["hero"]),
		"announce_text": set["announce_text"], "announce_level": set["announce_level"],
		"requests_enabled": set["requests_enabled"] == "1", "zone": s.Cfg.CFZoneName,
		"check_interval": int(s.Cfg.CheckInterval.Seconds()),
	}
}

type publicService struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	URL         string   `json:"url"`
	Hostname    string   `json:"hostname"`
	Tags        []string `json:"tags"`
	Icon        string   `json:"icon"`
	IconURL     string   `json:"icon_url"`
	Featured    bool     `json:"featured"`
	SortOrder   int      `json:"sort_order"`
	CreatedAt   float64  `json:"created_at"`
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) portalData(r *http.Request) (map[string]any, error) {
	ctx := r.Context()
	set := s.settings(ctx)
	ls, err := s.Store.Listings(ctx)
	if err != nil {
		return nil, err
	}
	status, err := s.Health.Summaries(ctx, s.Now())
	if err != nil {
		return nil, err
	}
	services := []publicService{}
	featured := []string{}
	catCount := map[string]int{}
	for _, l := range ls {
		services = append(services, publicService{ID: l.RecordID, Title: l.Title, Description: l.Description, Category: l.Category,
			URL: l.EffectiveURL(), Hostname: l.Hostname, Tags: splitTags(l.Tags), Icon: l.Icon, IconURL: iconURL(l),
			Featured: l.Featured, SortOrder: l.SortOrder, CreatedAt: l.CreatedAt})
		if l.Featured {
			featured = append(featured, l.RecordID)
		}
		c := l.Category
		if c == "" {
			c = "其他"
		}
		catCount[c]++
	}
	cats := []map[string]any{}
	for c, n := range catCount {
		cats = append(cats, map[string]any{"name": c, "count": n})
	}
	sort.Slice(cats, func(i, j int) bool {
		a, b := cats[i]["name"].(string), cats[j]["name"].(string)
		if (a == "其他") != (b == "其他") {
			return b == "其他"
		}
		return a < b
	})
	return map[string]any{
		"site": s.publicSettings(set), "categories": cats, "featured": featured, "services": services,
		"status": status, "now": float64(s.Now().Unix()),
	}, nil
}

func (s *Server) pagePortal(w http.ResponseWriter, r *http.Request) {
	data, err := s.portalData(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.writePage(w, http.StatusOK, "portal", map[string]any{"page": "portal", "portal": data, "admin": s.loggedIn(r)})
}

func (s *Server) portalJSON(w http.ResponseWriter, r *http.Request) {
	data, err := s.portalData(r)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) statusJSON(w http.ResponseWriter, r *http.Request) {
	st, err := s.Health.Summaries(r.Context(), s.Now())
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "interval": int(s.Cfg.CheckInterval.Seconds()), "now": float64(s.Now().Unix())})
}

// ---- login ----

func (s *Server) loginData(r *http.Request, errMsg, user string) map[string]any {
	set := s.settings(r.Context())
	return map[string]any{"page": "login", "site": s.publicSettings(set), "error": errMsg, "username": user}
}

func (s *Server) pageLogin(w http.ResponseWriter, r *http.Request) {
	if s.loggedIn(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	msg := ""
	if s.Cfg.CookieSecure && !isHTTPS(r) {
		msg = httpLoginError
	}
	s.writePage(w, http.StatusOK, "portal", s.loginData(r, msg, ""))
}

func (s *Server) doLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	user := strings.TrimSpace(r.PostFormValue("username"))
	pw := r.PostFormValue("password")
	ip := clientIP(r)
	now := s.Now()
	fail := func(status int, msg string) { s.writePage(w, status, "portal", s.loginData(r, msg, user)) }
	if s.Cfg.CookieSecure && !isHTTPS(r) {
		fail(http.StatusBadRequest, httpLoginError)
		return
	}
	if s.throttle.locked(ip, now) {
		fail(http.StatusTooManyRequests, "登入失敗次數過多，請 15 分鐘後再試。")
		return
	}
	okUser := auth.EqualString(user, s.Cfg.AdminUsername)
	okPw := auth.VerifyPassword(s.Cfg.AdminPasswordHash, pw)
	if !okUser || !okPw {
		s.throttle.fail(ip, now)
		s.auditLog(r, "login_failed", user, map[string]any{"username": user})
		if s.throttle.locked(ip, now) {
			fail(http.StatusTooManyRequests, "登入失敗次數過多，請 15 分鐘後再試。")
			return
		}
		fail(http.StatusUnauthorized, "帳號或密碼錯誤。")
		return
	}
	s.throttle.reset(ip)
	exp := now.Add(time.Duration(s.Cfg.SessionHours) * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.signer.Issue(user, exp), Path: "/", Expires: exp,
		MaxAge: s.Cfg.SessionHours * 3600, HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.auditLog(r, "login", user, map[string]any{})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) doLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) pageAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	set := s.settings(r.Context())
	s.writePage(w, http.StatusOK, "admin", map[string]any{"page": "admin", "zone": s.Cfg.CFZoneName, "title": set["title"]})
}
