package httpapi

import (
	"net/http"

	"github.com/seanyeh77/cf-portal/internal/dnsrec"
	"github.com/seanyeh77/cf-portal/internal/model"
)

type restoreBody struct {
	Mode     string             `json:"mode"`
	RecordID string             `json:"record_id"`
	Data     *model.RecordInput `json:"data"`
	Meta     *model.Meta        `json:"meta"`
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) error {
	var b restoreBody
	if err := decode(w, r, &b); err != nil {
		return err
	}
	switch b.Mode {
	case "recreate":
		return s.restoreRecreate(w, r, b)
	case "revert":
		return s.restoreRevert(w, r, b)
	case "delete":
		return s.restoreDelete(w, r, b)
	case "meta":
		return s.restoreMeta(w, r, b)
	}
	return httpErr{http.StatusUnprocessableEntity, "mode 須為 recreate、revert、delete 或 meta"}
}

func (s *Server) restoreRecreate(w http.ResponseWriter, r *http.Request, b restoreBody) error {
	if b.Data == nil {
		return httpErr{http.StatusUnprocessableEntity, "缺少還原資料"}
	}
	var meta model.Meta
	if b.Meta != nil {
		meta = *b.Meta
	}
	rec, _, err := s.createRecord(r, *b.Data, meta, false, "record_restore", map[string]any{"from_record_id": b.RecordID})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
	return nil
}

func (s *Server) restoreRevert(w http.ResponseWriter, r *http.Request, b restoreBody) error {
	if b.Data == nil || b.RecordID == "" {
		return httpErr{http.StatusUnprocessableEntity, "缺少還原資料"}
	}
	ctx := r.Context()
	cur, err := s.CF.Get(ctx, b.RecordID)
	if err != nil {
		return err
	}
	in := *b.Data
	if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
		return err
	}
	var change func(model.Meta) model.Meta
	if b.Meta != nil {
		change = setMeta(*b.Meta)
	}
	metaBefore, metaAfter, err := s.nextMeta(ctx, b.RecordID, change)
	if err != nil {
		return err
	}
	rec, _, err := s.updateRecord(r, *cur, in, metaBefore, metaAfter, "record_restore")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
	return nil
}

func (s *Server) restoreDelete(w http.ResponseWriter, r *http.Request, b restoreBody) error {
	if b.RecordID == "" {
		return httpErr{http.StatusUnprocessableEntity, "缺少 record_id"}
	}
	cur, err := s.CF.Get(r.Context(), b.RecordID)
	if err == nil {
		err = s.deleteRecord(r, *cur, "record_restore_delete")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) restoreMeta(w http.ResponseWriter, r *http.Request, b restoreBody) error {
	if b.RecordID == "" || b.Meta == nil {
		return httpErr{http.StatusUnprocessableEntity, "缺少還原資料"}
	}
	return s.replaceMeta(w, r, b.RecordID, setMeta(*b.Meta), map[string]any{"restore": true})
}
