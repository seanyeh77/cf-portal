package httpapi

import (
	"net/http"
	"strconv"

	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/loop"
	"github.com/nycu-sdc/sdc-portal/internal/model"
)

// cfWorkers bounds concurrent Cloudflare writes in bulk operations, well
// within the API rate limit.
const cfWorkers = 4

type batchResult struct {
	Index    int    `json:"index"`
	OK       bool   `json:"ok"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	Error    string `json:"error,omitempty"`
	Warning  string `json:"warning,omitempty"`
	RecordID string `json:"record_id,omitempty"`
}

const maxBatch = 500

func (s *Server) apiBatch(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Records []recordBody `json:"records"`
		DryRun  bool         `json:"dry_run"`
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if len(b.Records) == 0 {
		return httpErr{http.StatusUnprocessableEntity, "沒有可建立的資料"}
	}
	if len(b.Records) > maxBatch {
		return httpErr{http.StatusUnprocessableEntity, "一次最多建立 500 筆"}
	}
	ctx := r.Context()
	existing, _, err := s.CF.List(ctx, true)
	if err != nil {
		return err
	}
	rule := s.settings(ctx)["name_rule"]
	results := make([]batchResult, len(b.Records))
	inputs := make([]model.RecordInput, len(b.Records))
	metas := make([]model.Meta, len(b.Records))
	// Earlier rows count as existing, so duplicates within the batch are caught.
	pseudo := append([]model.Record(nil), existing...)
	ok := 0
	for i, row := range b.Records {
		in, meta, err := s.prepareCreate(row.RecordInput, row.Meta)
		if err == nil {
			err = dnsrec.Conflict(in, pseudo, "")
		}
		res := batchResult{Index: i, Type: in.Type, Name: in.Name, Content: in.Content}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			ok++
			pseudo = append(pseudo, model.Record{ID: "batch-" + strconv.Itoa(i), RecordInput: model.RecordInput{Type: in.Type, Name: in.Name, Content: in.Content}})
		}
		metas[i] = meta
		if msg := dnsrec.NameRuleError(in.Name, s.Cfg.CFZoneName, rule); msg != "" {
			res.Warning = "名稱不符規則：" + msg
		}
		inputs[i] = in
		results[i] = res
	}
	var created []model.Record
	if !b.DryRun {
		recs := make([]*model.Record, len(results))
		loop.ForEach(len(results), cfWorkers, func(i int) {
			if !results[i].OK {
				return
			}
			rec, err := s.commitCreate(r, inputs[i], metas[i], "record_create", map[string]any{"batch": true})
			if err != nil {
				results[i].OK = false
				results[i].Error = err.Error()
				return
			}
			results[i].RecordID = rec.ID
			recs[i] = rec
		})
		for _, rec := range recs {
			if rec != nil {
				created = append(created, *rec)
			}
		}
		ok = len(created)
		s.Scanner.ScanSoon(created...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "ok": ok, "skipped": len(results) - ok, "created": len(created), "dry_run": b.DryRun})
	return nil
}
