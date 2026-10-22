package httpapi

import (
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/model"
)

var exportHeader = []string{"name", "type", "content", "proxied", "ttl", "comment", "owner", "purpose", "notes", "expires_at",
	"auto_delete", "no_scan", "listed_title", "listed_category", "group", "reachable", "last_ok", "issues"}

// cell neutralizes values a spreadsheet would treat as a formula. A value
// that already starts with "'" is escaped too, so the import side
// (web/src/admin/lib/csv.ts unescapeCell) can always strip exactly one.
func cell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r'", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) apiExportCSV(w http.ResponseWriter, r *http.Request) error {
	z, err := s.loadZone(r.Context(), true)
	if err != nil {
		return err
	}
	recs := z.Records
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+s.Cfg.CFZoneName+"-handover-"+s.Now().In(s.Cfg.Timezone).Format("20060102")+`.csv"`)
	w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cw.Write(exportHeader)
	for _, rec := range recs {
		o := z.out(rec)
		reachable, lastOK := "", ""
		if o.Scan != nil {
			reachable = strconv.FormatBool(o.Scan.Reachable == 1)
			if o.Scan.LastOKTS != nil {
				lastOK = time.Unix(int64(*o.Scan.LastOKTS), 0).In(s.Cfg.Timezone).Format("2006-01-02 15:04")
			}
		}
		var issues []string
		for _, is := range o.Issues {
			issues = append(issues, is.Label)
		}
		title, cat := "", ""
		if o.Listing != nil {
			title, cat = o.Listing.Title, o.Listing.Category
		}
		ttl := strconv.Itoa(rec.TTL)
		content := rec.Content
		if p := model.Priority(rec.Priority); rec.Type == "MX" && p >= 0 {
			content = strconv.Itoa(p) + " " + content
		}
		row := []string{rec.Name, rec.Type, content, strconv.FormatBool(rec.Proxied), ttl, rec.Comment, o.Meta.Owner, o.Meta.Purpose, o.Meta.Notes,
			o.Meta.ExpiresAt, strconv.FormatBool(o.Meta.AutoDelete), strconv.FormatBool(o.Meta.NoScan), title, cat, o.Group, reachable, lastOK, strings.Join(issues, "；")}
		for i := range row {
			row[i] = cell(row[i])
		}
		cw.Write(row)
	}
	cw.Flush()
	return nil
}
