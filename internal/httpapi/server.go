// Package httpapi serves the portal, the admin API and the public pages.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/auth"
	"github.com/nycu-sdc/sdc-portal/internal/cloudflare"
	"github.com/nycu-sdc/sdc-portal/internal/config"
	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/health"
	"github.com/nycu-sdc/sdc-portal/internal/scanner"
	"github.com/nycu-sdc/sdc-portal/internal/snapshots"
	"github.com/nycu-sdc/sdc-portal/internal/store"
	"github.com/nycu-sdc/sdc-portal/internal/webui"
)

// Server holds the dependencies of all handlers.
type Server struct {
	Cfg     *config.Config
	CF      *cloudflare.Client
	Store   *store.Store
	Scanner *scanner.Scanner
	Maint   *scanner.Maint
	Health  *health.Checker
	Icons   *assets.Fetcher
	Snaps   *snapshots.Store
	// Assets is the built front end; defaults to webui.Assets.
	Assets fs.FS
	Now    func() time.Time

	signer   auth.Signer
	throttle *throttle
	mux      *http.ServeMux
}

// New wires a server.
func New(cfg *config.Config, cf *cloudflare.Client, st *store.Store, sc *scanner.Scanner, mt *scanner.Maint, hc *health.Checker, icons *assets.Fetcher, snaps *snapshots.Store) *Server {
	s := &Server{Cfg: cfg, CF: cf, Store: st, Scanner: sc, Maint: mt, Health: hc, Icons: icons, Snaps: snaps,
		Assets: webui.Assets, Now: time.Now, signer: auth.Signer{Key: []byte(cfg.SecretKey)}, throttle: newThrottle()}
	mt.Forget = s.forget
	mt.RetryIcons = s.retryIcons
	s.routes()
	return s
}

func (s *Server) routes() {
	m := http.NewServeMux()
	s.mux = m
	// public
	m.HandleFunc("GET /{$}", s.pagePortal)
	m.HandleFunc("GET /portal.json", s.portalJSON)
	m.HandleFunc("GET /status.json", s.statusJSON)
	m.HandleFunc("GET /icons/{name}", s.serveFile("icons"))
	m.HandleFunc("GET /media/{name}", s.serveFile("media"))
	m.HandleFunc("GET /request", s.pageRequest)
	m.HandleFunc("POST /request", s.submitRequest)
	m.HandleFunc("GET /request/{token}", s.pageRequestStatus)
	m.HandleFunc("GET /healthz", s.healthz)
	m.HandleFunc("GET /assets/{path...}", s.serveAsset)
	m.HandleFunc("GET /theme-init.js", s.serveAsset)
	m.HandleFunc("GET /favicon.svg", s.serveAsset)
	// login
	m.HandleFunc("GET /login", s.pageLogin)
	m.HandleFunc("POST /login", s.doLogin)
	m.HandleFunc("POST /logout", s.doLogout)
	m.HandleFunc("GET /admin", s.pageAdmin)
	m.HandleFunc("GET /admin/", s.pageAdmin)
	// api
	api := func(pattern string, h http.HandlerFunc) { m.Handle(pattern, s.requireAdmin(h)) }
	api("GET /api/records", s.apiRecords)
	api("POST /api/records", s.apiCreateRecord)
	api("PATCH /api/records/{id}", s.apiUpdateRecord)
	api("DELETE /api/records/{id}", s.apiDeleteRecord)
	api("PUT /api/records/{id}/meta", s.apiPutMeta)
	api("GET /api/records/{id}/history", s.apiHistory)
	api("POST /api/records/bulk-meta", s.apiBulkMeta)
	api("POST /api/records/bulk-delete", s.apiBulkDelete)
	api("POST /api/records/batch", s.apiBatch)
	api("POST /api/records/restore", s.apiRestore)
	api("POST /api/issues/mute", s.apiMute)
	api("POST /api/scan", s.apiScan)
	api("POST /api/records/{id}/scan", s.apiScanOne)
	api("GET /api/export.csv", s.apiExportCSV)
	api("PUT /api/listings/{id}", s.apiPutListing)
	api("POST /api/listings/{id}/icon", s.apiUploadIcon)
	api("POST /api/listings/{id}/icon/refresh", s.apiRefreshIcon)
	api("DELETE /api/listings/{id}", s.apiDeleteListing)
	api("GET /api/settings", s.apiGetSettings)
	api("PUT /api/settings", s.apiPutSettings)
	api("POST /api/settings/media/{kind}", s.apiUploadMedia)
	api("DELETE /api/settings/media/{kind}", s.apiDeleteMedia)
	api("GET /api/snapshots", s.apiSnapshots)
	api("POST /api/snapshots", s.apiCreateSnapshot)
	api("GET /api/snapshots/{name}/download", s.apiDownloadSnapshot)
	api("GET /api/snapshots/{name}/diff", s.apiSnapshotDiff)
	api("GET /api/requests", s.apiRequests)
	api("POST /api/requests/{id}/approve", s.apiApprove)
	api("POST /api/requests/{id}/reject", s.apiReject)
	api("GET /api/audit", s.apiAudit)
	m.Handle("/api/", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	}))
}

// Handler returns the root handler with all middleware.
func (s *Server) Handler() http.Handler {
	return s.recoverer(s.securityHeaders(s.allowlist(s.mux)))
}

type ctxKey int

const (
	keyIP ctxKey = iota
	keyHTTPS
)

func clientIP(r *http.Request) string {
	if a, ok := r.Context().Value(keyIP).(netip.Addr); ok {
		return a.String()
	}
	return ""
}

func isHTTPS(r *http.Request) bool {
	v, _ := r.Context().Value(keyHTTPS).(bool)
	return v
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Printf("panic %s %s: %v", r.Method, r.URL.Path, v)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const pageCSP = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeAnyErr maps known error types to status codes.
func writeAnyErr(w http.ResponseWriter, err error) {
	var ve *dnsrec.ValidationError
	var ce *dnsrec.ConflictError
	var cfe *cloudflare.Error
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("請求內容超過 %d MB", tooBig.Limit>>20))
	case errors.As(err, &ve):
		writeErr(w, http.StatusUnprocessableEntity, ve.Msg)
	case errors.As(err, &ce):
		writeErr(w, http.StatusConflict, ce.Msg)
	case errors.As(err, &cfe):
		writeErr(w, cfe.HTTPStatus(), cfe.Message)
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "找不到指定的項目")
	default:
		log.Printf("error: %v", err)
		writeErr(w, http.StatusInternalServerError, "內部錯誤："+err.Error())
	}
}

// maxJSONBody limits JSON request bodies; uploads set their own limits.
const maxJSONBody = 4 << 20

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(v)
	var tooBig *http.MaxBytesError
	if err != nil && !errors.As(err, &tooBig) {
		return &dnsrec.ValidationError{Msg: "請求格式錯誤：" + err.Error()}
	}
	return err
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) auditLog(r *http.Request, action, target string, detail any) {
	b, err := json.Marshal(detail)
	if err != nil {
		b = []byte("{}")
	}
	if err := s.Store.AddAudit(context.WithoutCancel(r.Context()), clientIP(r), action, target, string(b)); err != nil {
		log.Printf("audit: %v", err)
	}
}

func (s *Server) settings(ctx context.Context) map[string]string {
	set, err := s.Store.Settings(ctx, store.DefaultSettings(s.Cfg.PortalTitle))
	if err != nil {
		log.Printf("settings: %v", err)
		return store.DefaultSettings(s.Cfg.PortalTitle)
	}
	return set
}

func (s *Server) today() string {
	return s.Now().In(s.Cfg.Timezone).Format("2006-01-02")
}

func trimLen(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
