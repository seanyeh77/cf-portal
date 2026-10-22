package httpapi

import (
	"net/http"

	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/model"
)

type restoreBody struct {
	Mode     string             `json:"mode"`
	RecordID string             `json:"record_id"`
	Data     *model.RecordInput `json:"data"`
	Meta     *model.Meta        `json:"meta"`
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) {
	var b restoreBody
	if !decode(w, r, &b) {
		return
	}
	switch b.Mode {
	case "recreate":
		s.restoreRecreate(w, r, b)
	case "revert":
		s.restoreRevert(w, r, b)
	case "delete":
		s.restoreDelete(w, r, b)
	case "meta":
		s.restoreMeta(w, r, b)
	default:
		writeErr(w, http.StatusUnprocessableEntity, "mode 須為 recreate、revert、delete 或 meta")
	}
}

func (s *Server) restoreRecreate(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.Data == nil {
		writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
		return
	}
	var meta model.Meta
	if b.Meta != nil {
		meta = *b.Meta
	}
	rec, _, err := s.createRecord(r, *b.Data, meta, false, "record_restore", map[string]any{"from_record_id": b.RecordID})
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
}

func (s *Server) restoreRevert(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.Data == nil || b.RecordID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
		return
	}
	ctx := r.Context()
	cur, err := s.CF.Get(ctx, b.RecordID)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	in := *b.Data
	if err := dnsrec.Validate(&in, s.Cfg.CFZoneName); err != nil {
		writeAnyErr(w, err)
		return
	}
	var change func(model.Meta) model.Meta
	if b.Meta != nil {
		change = setMeta(*b.Meta)
	}
	metaBefore, metaAfter, err := s.nextMeta(ctx, b.RecordID, change)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	rec, _, err := s.updateRecord(r, *cur, in, metaBefore, metaAfter, "record_restore")
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec})
}

func (s *Server) restoreDelete(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.RecordID == "" {
		writeErr(w, http.StatusUnprocessableEntity, "缺少 record_id")
		return
	}
	cur, err := s.CF.Get(r.Context(), b.RecordID)
	if err == nil {
		err = s.deleteRecord(r, *cur, "record_restore_delete")
	}
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) restoreMeta(w http.ResponseWriter, r *http.Request, b restoreBody) {
	if b.RecordID == "" || b.Meta == nil {
		writeErr(w, http.StatusUnprocessableEntity, "缺少還原資料")
		return
	}
	s.replaceMeta(w, r, b.RecordID, setMeta(*b.Meta), map[string]any{"restore": true})
}
