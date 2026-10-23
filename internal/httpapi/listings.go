package httpapi

import (
	"context"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seanyeh77/cf-portal/internal/assets"
	"github.com/seanyeh77/cf-portal/internal/dnsrec"
	"github.com/seanyeh77/cf-portal/internal/model"
	"github.com/seanyeh77/cf-portal/internal/store"
)

type listingBody struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	URL         string   `json:"url"`
	SortOrder   int      `json:"sort_order"`
	Featured    bool     `json:"featured"`
	Tags        []string `json:"tags"`
	Icon        string   `json:"icon"`
}

func invalidf(msg string) error { return &dnsrec.ValidationError{Msg: msg} }

func cleanListing(b listingBody) (listingBody, error) {
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Category = strings.TrimSpace(b.Category)
	b.URL = strings.TrimSpace(b.URL)
	switch {
	case b.Title == "":
		return b, invalidf("請填寫顯示名稱")
	case len([]rune(b.Title)) > 40:
		return b, invalidf("顯示名稱最多 40 字")
	case len([]rune(b.Description)) > 200:
		return b, invalidf("說明最多 200 字")
	case len([]rune(b.Category)) > 20:
		return b, invalidf("分類最多 20 字")
	}
	if b.URL != "" {
		u, err := url.Parse(b.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return b, invalidf("網址須以 http:// 或 https:// 開頭")
		}
	}
	tags := splitList(b.Tags...)
	for _, t := range tags {
		if len([]rune(t)) > 20 {
			return b, invalidf("每個標籤最多 20 字")
		}
	}
	if len(tags) > 6 {
		return b, invalidf("標籤最多 6 個")
	}
	b.Tags = tags
	switch {
	case b.Icon == "", b.Icon == "auto", b.Icon == "none", b.Icon == "upload":
		if b.Icon == "" {
			b.Icon = "auto"
		}
	case strings.HasPrefix(b.Icon, "si:"):
		if !assets.SlugRe.MatchString(strings.TrimPrefix(b.Icon, "si:")) {
			return b, invalidf("Simple Icons 名稱僅能使用小寫英文與數字")
		}
	default:
		return b, invalidf("未知的圖示設定")
	}
	return b, nil
}

func (s *Server) apiPutListing(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var b listingBody
	if err := decode(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	rec, err := s.CF.Get(ctx, id)
	if err != nil {
		return err
	}
	prev, err := s.Store.Listing(ctx, id)
	if err != nil && err != store.ErrNotFound {
		return err
	}
	l, iconErr, err := s.saveListing(r, *rec, prev, b)
	if err != nil {
		return err
	}
	s.Health.CheckSoon(l)
	s.writeListing(w, r, id, map[string]any{"icon_error": iconErr})
	return nil
}

// saveListing validates and stores the listing of rec, replacing prev
// (nil for a new one), and audits it. It reports a failed Simple Icons
// lookup separately: the listing is saved without the icon. The caller
// queues the status check.
func (s *Server) saveListing(r *http.Request, rec model.Record, prev *model.Listing, b listingBody) (model.Listing, string, error) {
	b, err := cleanListing(b)
	if err != nil {
		return model.Listing{}, "", err
	}
	ctx := r.Context()
	id := rec.ID
	l := model.Listing{RecordID: id, Hostname: rec.Name, Title: b.Title, Description: b.Description, Category: b.Category,
		SortOrder: b.SortOrder, Featured: b.Featured, Tags: b.Tags, Icon: b.Icon}
	if b.URL != "" {
		l.URL = &b.URL
	}
	var oldFile *string
	if prev != nil {
		oldFile = prev.IconFile
		l.IconFile = prev.IconFile
	}
	iconErr := ""
	fetchAuto := false
	switch {
	case b.Icon == "none":
		l.IconFile = nil
	case b.Icon == "upload":
		if prev == nil || prev.Icon != "upload" || prev.IconFile == nil {
			return l, "", httpErr{http.StatusUnprocessableEntity, "請先上傳圖片"}
		}
	case b.Icon == "auto":
		urlChanged := prev == nil || prev.EffectiveURL() != l.EffectiveURL()
		if prev == nil || prev.Icon != "auto" || prev.IconFile == nil || urlChanged {
			fetchAuto = true
			if prev != nil && prev.Icon != "auto" {
				l.IconFile = nil
			}
		}
	case strings.HasPrefix(b.Icon, "si:"):
		if prev != nil && prev.Icon == b.Icon && prev.IconFile != nil {
			break // already fetched
		}
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		name, err := s.Icons.FetchSimpleIcon(c, strings.TrimPrefix(b.Icon, "si:"))
		cancel()
		if err != nil {
			iconErr = err.Error()
			l.IconFile = nil
		} else {
			l.IconFile = &name
		}
	}
	if err := s.Store.PutListing(ctx, l); err != nil {
		return l, "", err
	}
	if oldFile != nil && (l.IconFile == nil || *l.IconFile != *oldFile) {
		s.removeIconFile(ctx, oldFile)
	}
	s.auditLog(r, "listing_save", rec.Name, map[string]any{"record_id": id, "title": l.Title, "category": l.Category, "icon": l.Icon, "new": prev == nil})
	if fetchAuto {
		go s.fetchAutoIcon(l)
	}
	return l, iconErr, nil
}

// writeListing answers with the listing as stored now, plus extra fields.
func (s *Server) writeListing(w http.ResponseWriter, r *http.Request, id string, extra map[string]any) {
	saved, _ := s.Store.Listing(r.Context(), id)
	out := map[string]any{"listing": toListingOut(saved)}
	maps.Copy(out, extra)
	writeJSON(w, http.StatusOK, out)
}

// fetchAutoIcon fetches an automatic icon and stores it if the listing
// still uses automatic icons.
func (s *Server) fetchAutoIcon(l model.Listing) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	name, err := s.Icons.FetchAuto(ctx, l.EffectiveURL(), "auto-"+l.RecordID)
	if err != nil {
		return err
	}
	cur, err := s.Store.Listing(ctx, l.RecordID)
	if err != nil || cur.Icon != "auto" {
		assets.Remove(s.Icons.Dir, name)
		return err
	}
	if err := s.Store.SetListingIcon(ctx, l.RecordID, "auto", &name); err != nil {
		return err
	}
	if cur.IconFile != nil && *cur.IconFile != name {
		s.removeIconFile(ctx, cur.IconFile)
	}
	return nil
}

func (s *Server) retryIcons(ctx context.Context) {
	ls, err := s.Store.Listings(ctx)
	if err != nil {
		return
	}
	for _, l := range ls {
		if l.Icon == "auto" && l.IconFile == nil {
			if err := s.fetchAutoIcon(l); err != nil {
				log.Printf("icon retry %s: %v", l.Hostname, err)
			}
		}
	}
}

func (s *Server) apiRefreshIcon(w http.ResponseWriter, r *http.Request) error {
	l, err := s.Store.Listing(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := s.fetchAutoIcon(*l); err != nil {
		return httpErr{http.StatusUnprocessableEntity, "找不到可用的圖示：" + err.Error()}
	}
	s.writeListing(w, r, l.RecordID, nil)
	return nil
}

// fileTooLarge reports an upload over its limit (HTTP 413).
type fileTooLarge struct{ limit int64 }

func (e fileTooLarge) Error() string { return "檔案超過 " + humanSize(e.limit) }

// readUpload reads the "file" part of a multipart form, at most limit bytes.
func readUpload(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit+64<<10)
	f, _, err := r.FormFile("file")
	if errors.As(err, new(*http.MaxBytesError)) {
		return nil, fileTooLarge{limit}
	}
	if err != nil {
		return nil, invalidf("請選擇檔案（上限 " + humanSize(limit) + "）")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fileTooLarge{limit}
	}
	return b, nil
}

func humanSize(n int64) string {
	if n >= 1<<20 {
		return strconv.FormatInt(n>>20, 10) + " MB"
	}
	return strconv.FormatInt(n>>10, 10) + " KB"
}

func (s *Server) apiUploadIcon(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	l, err := s.Store.Listing(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	data, err := readUpload(w, r, 1<<20)
	if err != nil {
		return err
	}
	name, err := assets.Save(s.Icons.Dir, "up-"+l.RecordID, data, nil)
	if err != nil {
		return httpErr{http.StatusUnprocessableEntity, err.Error()}
	}
	old := l.IconFile
	if err := s.Store.SetListingIcon(ctx, l.RecordID, "upload", &name); err != nil {
		return err
	}
	s.removeIconFile(ctx, old)
	s.auditLog(r, "icon_upload", l.Hostname, map[string]any{"record_id": l.RecordID, "file": name})
	s.writeListing(w, r, l.RecordID, nil)
	return nil
}

func (s *Server) apiDeleteListing(w http.ResponseWriter, r *http.Request) error {
	if err := s.removeListing(r, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// removeListing takes a record off the portal and audits it.
func (s *Server) removeListing(r *http.Request, id string) error {
	ctx := r.Context()
	l, err := s.Store.Listing(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteListing(ctx, l.RecordID); err != nil {
		return err
	}
	s.removeIconFile(ctx, l.IconFile)
	s.auditLog(r, "listing_remove", l.Hostname, map[string]any{"record_id": l.RecordID, "title": l.Title})
	return nil
}
