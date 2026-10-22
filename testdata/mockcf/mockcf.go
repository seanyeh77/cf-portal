// Package mockcf is a Cloudflare v4 API stand-in for tests.
package mockcf

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Zone is the mocked zone name.
const (
	Zone   = "sdc.nycu.club"
	ZoneID = "023e105f4ecef8ad9ca31a8372d0c353"
)

// Record mirrors the Cloudflare record object.
type Record struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Content    string  `json:"content"`
	TTL        int     `json:"ttl"`
	Proxied    bool    `json:"proxied"`
	Proxiable  bool    `json:"proxiable"`
	Priority   *int    `json:"priority,omitempty"`
	Comment    *string `json:"comment"`
	CreatedOn  string  `json:"created_on"`
	ModifiedOn string  `json:"modified_on"`
}

// Server is the mock. It is safe for concurrent use.
type Server struct {
	Token      string
	MaxPerPage int

	mu      sync.Mutex
	records []*Record
	// Calls counts mutating calls, keyed by method.
	Calls map[string]int
}

// New returns a mock seeded with the default records.
func New(token string) *Server {
	s := &Server{Token: token, MaxPerPage: 20}
	s.Reset()
	return s
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func ip(n int) *int { return &n }

// Seed returns the default records.
func Seed() []*Record {
	ts := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Format(time.RFC3339)
	mk := func(typ, name, content string, proxied bool) *Record {
		fq := Zone
		if name != "@" {
			fq = name + "." + Zone
		}
		return &Record{ID: newID(), Type: typ, Name: fq, Content: content, TTL: 1, Proxied: proxied, CreatedOn: ts, ModifiedOn: ts}
	}
	recs := []*Record{
		mk("A", "grafana", "10.0.1.20", false),
		mk("A", "gitlab", "10.0.1.21", false),
		mk("A", "git", "10.0.1.21", false),
		mk("CNAME", "docs", "sdc-docs.pages.dev", true),
		mk("A", "jupyter", "10.0.1.30", false),
		mk("A", "wg", "140.113.235.10", false),
		mk("TXT", "_dmarc", "v=DMARC1; p=none", false),
		mk("CNAME", "alias", "localhost", false),
		mk("CNAME", "chain", "alias.sdc.nycu.club", false),
		mk("A", "local", "127.0.0.1", false),
		mk("A", "Old_Box", "10.0.2.5", false),
		mk("MX", "@", "mail.example.com", false),
		mk("SRV", "_minecraft._tcp", "0 25565 mc.sdc.nycu.club", false),
	}
	recs[0].TTL = 300
	recs[11].Priority = ip(10)
	recs[12].Priority = ip(10)
	recs[12].TTL = 3600
	for i := 0; i < 20; i++ {
		recs = append(recs, mk("A", fmt.Sprintf("student%03d", i), fmt.Sprintf("10.0.3.%d", 100+i), false))
	}
	for _, r := range recs {
		r.Proxiable = r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME"
	}
	return recs
}

// Reset restores the default records.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = Seed()
	s.Calls = map[string]int{}
}

// Records returns a copy of the current records.
func (s *Server) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	for i, r := range s.records {
		out[i] = *r
	}
	return out
}

// Find returns the record with the given type and name (without zone).
func (s *Server) Find(typ, name string) *Record {
	for _, r := range s.Records() {
		if r.Type == typ && (r.Name == name || r.Name == name+"."+Zone) {
			rc := r
			return &rc
		}
	}
	return nil
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, map[string]any{"success": false, "errors": []cfError{{code, msg}}, "messages": []any{}, "result": nil})
}

func ok(w http.ResponseWriter, result any, info any) {
	body := map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result}
	if info != nil {
		body["result_info"] = info
	}
	writeJSON(w, http.StatusOK, body)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/svc/"):
		s.serveSvc(w, r)
		return
	case strings.HasPrefix(p, "/si/"):
		s.serveSI(w, r)
		return
	case p == "/mock/reset":
		s.Reset()
		w.Write([]byte("ok"))
		return
	}
	p = strings.TrimPrefix(p, "/client/v4")
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		fail(w, http.StatusForbidden, 9109, "Invalid access token")
		return
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "zones" && r.Method == http.MethodGet:
		name := r.URL.Query().Get("name")
		res := []any{}
		if name == "" || name == Zone {
			res = append(res, map[string]string{"id": ZoneID, "name": Zone})
		}
		ok(w, res, map[string]int{"page": 1, "total_pages": 1, "count": len(res)})
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		if parts[1] != ZoneID {
			fail(w, http.StatusNotFound, 7003, "Could not route to /zones/"+parts[1])
			return
		}
		if len(parts) == 3 {
			switch r.Method {
			case http.MethodGet:
				s.list(w, r)
			case http.MethodPost:
				s.create(w, r)
			default:
				fail(w, http.StatusMethodNotAllowed, 10000, "method not allowed")
			}
			return
		}
		s.single(w, r, parts[3])
	default:
		fail(w, http.StatusNotFound, 7000, "No route for that URI")
	}
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per < 1 {
		per = 100
	}
	if s.MaxPerPage > 0 && per > s.MaxPerPage {
		per = s.MaxPerPage
	}
	s.mu.Lock()
	all := make([]*Record, len(s.records))
	copy(all, s.records)
	s.mu.Unlock()
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	total := len(all)
	pages := (total + per - 1) / per
	if pages == 0 {
		pages = 1
	}
	start, end := (page-1)*per, page*per
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	out := make([]Record, 0, end-start)
	for _, rec := range all[start:end] {
		out = append(out, *rec)
	}
	ok(w, out, map[string]int{"page": page, "per_page": per, "total_pages": pages, "count": len(out), "total_count": total})
}

type input struct {
	Type     *string `json:"type"`
	Name     *string `json:"name"`
	Content  *string `json:"content"`
	TTL      *int    `json:"ttl"`
	Proxied  *bool   `json:"proxied"`
	Priority *int    `json:"priority"`
	Comment  *string `json:"comment"`
}

func (in input) apply(rec *Record) {
	if in.Type != nil {
		rec.Type = strings.ToUpper(*in.Type)
	}
	if in.Name != nil {
		n := strings.TrimSuffix(*in.Name, ".")
		if n == "@" {
			n = Zone
		} else if !strings.HasSuffix(n, Zone) {
			n += "." + Zone
		}
		rec.Name = n
	}
	if in.Content != nil {
		rec.Content = *in.Content
	}
	if in.TTL != nil {
		rec.TTL = *in.TTL
	}
	if in.Proxied != nil {
		rec.Proxied = *in.Proxied
	}
	if in.Priority != nil {
		rec.Priority = in.Priority
	}
	if in.Comment != nil {
		c := *in.Comment
		rec.Comment = &c
	}
	rec.Proxiable = rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME"
}

// check validates rec against the other records; caller holds mu.
func (s *Server) check(rec *Record) (int, string) {
	if rec.Type == "" || rec.Content == "" {
		return 9000, "DNS record type and content are required"
	}
	if rec.Proxied {
		if !rec.Proxiable {
			return 9004, "This record type cannot be proxied."
		}
		if a, err := netip.ParseAddr(rec.Content); err == nil && (a.IsPrivate() || a.IsLoopback()) {
			return 9003, "Invalid 'proxied' value: a private or reserved IP cannot be proxied"
		}
		rec.TTL = 1
	}
	for _, o := range s.records {
		if o.ID == rec.ID || !strings.EqualFold(o.Name, rec.Name) {
			continue
		}
		if o.Type == rec.Type && strings.EqualFold(o.Content, rec.Content) {
			return 81058, "An identical record already exists."
		}
		if o.Type == "CNAME" || rec.Type == "CNAME" {
			return 81053, "An A, AAAA, or CNAME record with that host already exists."
		}
	}
	return 0, ""
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var in input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, 9207, "Request body is invalid.")
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	rec := &Record{ID: newID(), TTL: 1, CreatedOn: ts, ModifiedOn: ts}
	in.apply(rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	if code, msg := s.check(rec); code != 0 {
		fail(w, http.StatusBadRequest, code, msg)
		return
	}
	s.records = append(s.records, rec)
	s.Calls["POST"]++
	ok(w, *rec, nil)
}

func (s *Server) single(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, rec := range s.records {
		if rec.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		fail(w, http.StatusNotFound, 81044, "Record does not exist.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		ok(w, *s.records[idx], nil)
	case http.MethodPatch, http.MethodPut:
		var in input
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, 9207, "Request body is invalid.")
			return
		}
		rec := *s.records[idx]
		in.apply(&rec)
		if code, msg := s.check(&rec); code != 0 {
			fail(w, http.StatusBadRequest, code, msg)
			return
		}
		rec.ModifiedOn = time.Now().UTC().Format(time.RFC3339Nano)
		s.records[idx] = &rec
		s.Calls["PATCH"]++
		ok(w, rec, nil)
	case http.MethodDelete:
		s.records = append(s.records[:idx], s.records[idx+1:]...)
		s.Calls["DELETE"]++
		ok(w, map[string]string{"id": id}, nil)
	default:
		fail(w, http.StatusMethodNotAllowed, 10000, "method not allowed")
	}
}

// A 1x1 PNG and a 2x2 PNG used as icons.
var (
	PNG1 = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xcf\xc0\xf0\x1f\x00\x05\x00\x01\xff\x89\x99=\x1d\x00\x00\x00\x00IEND\xaeB`\x82")
	ICO  = append([]byte{0, 0, 1, 0, 1, 0, 1, 1, 0, 0, 1, 0, 32, 0}, make([]byte, 40)...)
)

// iconPNG draws a 32x32 two-colour icon whose colour depends on name.
func iconPNG(name string) []byte {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	fg := color.RGBA{uint8(60 + h%150), uint8(90 + (h/7)%120), uint8(140 + (h/13)%100), 255}
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			c := fg
			if (x-16)*(x-16)+(y-16)*(y-16) < 64 {
				c = color.RGBA{255, 255, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func (s *Server) serveSvc(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/svc/")
	name, file, _ := strings.Cut(rest, "/")
	if name == "down" {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	switch file {
	case "":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title>
<link rel="mask-icon" href="mask.svg" color="#000">
<link rel="icon" href="favicon-32.png" sizes="32x32">
<link rel="apple-touch-icon" href="/svc/%s/apple.png">
</head><body>%s</body></html>`, name, name, name)
	case "apple.png", "favicon-32.png":
		w.Header().Set("Content-Type", "image/png")
		w.Write(iconPNG(name))
	case "mask.svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveSI(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/si/"), ".svg")
	if slug == "missing" || slug == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	fmt.Fprintf(w, `<svg role="img" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><title>%s</title><path d="M0 0h24v24H0z"/></svg>`, slug)
}
