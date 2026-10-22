package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/store"
)

const (
	requestsPerHour = 5
	maxPending      = 100
)

var requestTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "TXT": true}

func (s *Server) requestPageData(r *http.Request, errs []string, values map[string]string) map[string]any {
	set := s.settings(r.Context())
	if values == nil {
		values = map[string]string{"type": "A"}
	}
	return map[string]any{"page": "request", "site": s.publicSettings(set), "note": set["request_note"],
		"name_rule_hint": set["name_rule_hint"], "errors": errs, "values": values, "today": s.today()}
}

// writeClosed answers request pages while requests are disabled.
func (s *Server) writeClosed(w http.ResponseWriter, set map[string]string) {
	s.writePage(w, http.StatusNotFound, "portal", map[string]any{"page": "closed", "site": s.publicSettings(set)})
}

func (s *Server) pageRequest(w http.ResponseWriter, r *http.Request) {
	if set := s.settings(r.Context()); set["requests_enabled"] != "1" {
		s.writeClosed(w, set)
		return
	}
	s.writePage(w, http.StatusOK, "portal", s.requestPageData(r, nil, nil))
}

// validateRequest checks a public request and returns the normalized input.
func (s *Server) validateRequest(v map[string]string, rule string) (model.RecordInput, []string) {
	var errs []string
	if v["requester"] == "" {
		errs = append(errs, "請填寫姓名")
	} else if len([]rune(v["requester"])) > 60 {
		errs = append(errs, "姓名最多 60 字")
	}
	if v["contact"] == "" {
		errs = append(errs, "請填寫聯絡方式")
	} else if len([]rune(v["contact"])) > 120 {
		errs = append(errs, "聯絡方式最多 120 字")
	}
	in := model.RecordInput{Type: strings.ToUpper(v["type"]), Name: v["name"], Content: v["content"], TTL: 1}
	if v["name"] == "" {
		errs = append(errs, "請填寫網址名稱")
	} else {
		in.Name = dnsrec.NormalizeName(v["name"], s.Cfg.CFZoneName)
		if in.Name == s.Cfg.CFZoneName {
			errs = append(errs, "名稱格式不符：不可申請根網域")
		} else if msg := dnsrec.NameRuleError(in.Name, s.Cfg.CFZoneName, rule); msg != "" {
			errs = append(errs, "名稱格式不符："+msg)
		}
	}
	if !requestTypes[in.Type] {
		errs = append(errs, "請選擇指向類型")
	}
	if v["content"] == "" {
		errs = append(errs, "請填寫指向內容")
	}
	if v["purpose"] == "" {
		errs = append(errs, "請填寫用途")
	} else if len([]rune(v["purpose"])) > 300 {
		errs = append(errs, "用途最多 300 字")
	}
	if e := v["expires_at"]; e != "" {
		if !validDate(e) {
			errs = append(errs, "使用期限格式應為 YYYY-MM-DD")
		} else if e < s.today() {
			errs = append(errs, "使用期限不可早於今天")
		}
	}
	if len(errs) == 0 {
		probe := in
		if err := dnsrec.Validate(&probe, s.Cfg.CFZoneName); err != nil {
			errs = append(errs, err.Error())
		} else {
			in = probe
		}
	}
	return in, errs
}

func (s *Server) submitRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set := s.settings(ctx)
	if set["requests_enabled"] != "1" {
		s.writeClosed(w, set)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	v := map[string]string{}
	for _, k := range []string{"requester", "contact", "name", "type", "content", "purpose", "expires_at"} {
		v[k] = strings.TrimSpace(r.PostFormValue(k))
	}
	in, errs := s.validateRequest(v, set["name_rule"])
	ip := clientIP(r)
	if len(errs) == 0 {
		pending, fromIP, err := s.Store.CountRequests(ctx, ip, float64(s.Now().Add(-time.Hour).Unix()))
		switch {
		case err != nil:
			errs = append(errs, "暫時無法送出，請稍後再試。")
		case fromIP >= requestsPerHour:
			errs = append(errs, "每小時最多申請 5 筆，請稍後再試。")
		case pending >= maxPending:
			errs = append(errs, "待審申請已滿，請聯絡管理者。")
		}
	}
	if len(errs) > 0 {
		s.writePage(w, http.StatusUnprocessableEntity, "portal", s.requestPageData(r, errs, v))
		return
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	req := model.Request{Token: hex.EncodeToString(tok), IP: ip, Requester: v["requester"], Contact: v["contact"],
		Name: in.Name, Type: in.Type, Content: in.Content, Purpose: v["purpose"], ExpiresAt: v["expires_at"]}
	if _, err := s.Store.AddRequest(ctx, req); err != nil {
		s.writePage(w, http.StatusInternalServerError, "portal", s.requestPageData(r, []string{"暫時無法送出，請稍後再試。"}, v))
		return
	}
	http.Redirect(w, r, "/request/"+req.Token, http.StatusSeeOther)
}

func (s *Server) pageRequestStatus(w http.ResponseWriter, r *http.Request) {
	req, err := s.Store.RequestByToken(r.Context(), r.PathValue("token"))
	set := s.settings(r.Context())
	if err != nil {
		s.writePage(w, http.StatusNotFound, "portal", map[string]any{"page": "request_status", "site": s.publicSettings(set), "request": nil})
		return
	}
	s.writePage(w, http.StatusOK, "portal", map[string]any{"page": "request_status", "site": s.publicSettings(set), "request": map[string]any{
		"name": req.Name, "type": req.Type, "content": req.Content, "purpose": req.Purpose, "expires_at": req.ExpiresAt,
		"requester": req.Requester, "status": req.Status, "admin_note": req.AdminNote, "ts": req.TS, "decided_ts": req.DecidedTS,
	}})
}

// ---- admin ----

type requestOut struct {
	model.Request
	Conflict  string `json:"conflict"`
	NameOK    bool   `json:"name_ok"`
	NameError string `json:"name_error"`
}

func (s *Server) apiRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqs, err := s.Store.Requests(ctx)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	var recs []model.Record
	set := s.settings(ctx)
	rule := set["name_rule"]
	out := make([]requestOut, 0, len(reqs))
	for _, q := range reqs {
		o := requestOut{Request: q, NameOK: true}
		if q.Status == "pending" {
			if recs == nil {
				if recs, _, err = s.CF.List(ctx, false); err != nil {
					writeAnyErr(w, err)
					return
				}
			}
			in := model.RecordInput{Type: q.Type, Name: q.Name, Content: q.Content, TTL: 1}
			if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
				o.Conflict = err.Error()
			} else if err := dnsrec.Conflict(in, recs, ""); err != nil {
				o.Conflict = err.Error()
			}
			if msg := dnsrec.NameRuleError(q.Name, s.Cfg.CFZoneName, rule); msg != "" {
				o.NameOK, o.NameError = false, msg
			}
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out, "enabled": set["requests_enabled"] == "1"})
}

func (s *Server) loadRequest(w http.ResponseWriter, r *http.Request) *model.Request {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "找不到申請")
		return nil
	}
	q, err := s.Store.Request(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "找不到申請")
		return nil
	}
	if q.Status != "pending" {
		writeErr(w, http.StatusConflict, "此申請已處理")
		return nil
	}
	return q
}

func (s *Server) apiApprove(w http.ResponseWriter, r *http.Request) {
	q := s.loadRequest(w, r)
	if q == nil {
		return
	}
	var b struct {
		Record *model.RecordInput `json:"record"`
		Meta   *model.Meta        `json:"meta"`
		Note   string             `json:"note"`
	}
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	in := model.RecordInput{Type: q.Type, Name: q.Name, Content: q.Content, TTL: 1}
	if b.Record != nil {
		in = *b.Record
	}
	meta := model.Meta{Owner: q.Requester + "（" + q.Contact + "）", Purpose: q.Purpose, ExpiresAt: q.ExpiresAt}
	if b.Meta != nil {
		meta = *b.Meta
	}
	note := trimLen(b.Note, 500)
	rec, _, err := s.createRecord(r, in, meta, "request_approve", map[string]any{"request_id": q.ID, "note": note})
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	if err := s.Store.DecideRequest(r.Context(), q.ID, "approved", note, &rec.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
}

func (s *Server) apiReject(w http.ResponseWriter, r *http.Request) {
	q := s.loadRequest(w, r)
	if q == nil {
		return
	}
	var b struct {
		Note string `json:"note"`
	}
	if err := decode(w, r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	note := trimLen(b.Note, 500)
	if note == "" {
		writeErr(w, http.StatusUnprocessableEntity, "請填寫駁回原因")
		return
	}
	if err := s.Store.DecideRequest(r.Context(), q.ID, "rejected", note, nil); err != nil {
		writeAnyErr(w, err)
		return
	}
	s.auditLog(r, "request_reject", q.Name, map[string]any{"request_id": q.ID, "note": note})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
