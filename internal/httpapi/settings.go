package httpapi

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/scanner"
	"github.com/nycu-sdc/sdc-portal/internal/snapshots"
)

func strconvF(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// settingLimits are the editable settings and their maximum lengths.
var settingLimits = map[string]int{
	"title": 40, "subtitle": 120, "accent": 7, "announce_text": 300, "announce_level": 4, "name_rule": 300,
	"name_rule_hint": 120, "zombie_days": 3, "expiry_warn_days": 3, "scan_ports": 80, "requests_enabled": 1, "request_note": 1000,
}

func (s *Server) settingsOut(set map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range set {
		out[k] = v
	}
	out["logo_url"] = mediaURL(set["logo"])
	out["hero_url"] = mediaURL(set["hero"])
	out["zone"] = s.Cfg.CFZoneName
	return out
}

func (s *Server) apiGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsOut(s.settings(r.Context())))
}

func normalizeSetting(k, v string) (string, error) {
	v = strings.TrimSpace(v)
	if k == "announce_text" || k == "request_note" {
		v = strings.ReplaceAll(v, "\r\n", "\n")
	}
	if len([]rune(v)) > settingLimits[k] && k != "scan_ports" {
		return "", invalidf(k + " 過長")
	}
	switch k {
	case "title":
		if v == "" {
			return "", invalidf("請填寫網站名稱")
		}
	case "accent":
		if !hexColor.MatchString(v) {
			return "", invalidf("主色須為 #RRGGBB 格式")
		}
		v = strings.ToLower(v)
	case "announce_level":
		if v != "info" && v != "warn" {
			return "", invalidf("公告樣式須為 info 或 warn")
		}
	case "name_rule":
		if v != "" {
			if _, err := regexp.Compile("^(?:" + v + ")$"); err != nil {
				return "", invalidf("命名規則不是有效的正規表示式：" + err.Error())
			}
		}
	case "zombie_days", "expiry_warn_days":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			return "", invalidf("天數須為 1 到 365")
		}
		v = strconv.Itoa(n)
	case "scan_ports":
		ports, err := scanner.ParsePorts(v)
		if err != nil {
			return "", invalidf(err.Error())
		}
		v = joinInts(ports)
	case "requests_enabled":
		if v == "true" {
			v = "1"
		}
		if v != "1" {
			v = "0"
		}
	}
	return v, nil
}

func joinInts(a []int) string {
	s := make([]string, len(a))
	for i, n := range a {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

func (s *Server) apiPutSettings(w http.ResponseWriter, r *http.Request) {
	var b map[string]any
	if err := decode(r, &b); err != nil {
		writeAnyErr(w, err)
		return
	}
	ctx := r.Context()
	cur := s.settings(ctx)
	save := map[string]string{}
	changes := map[string][2]string{}
	for k, raw := range b {
		if _, ok := settingLimits[k]; !ok {
			continue
		}
		var v string
		switch t := raw.(type) {
		case string:
			v = t
		case bool:
			v = map[bool]string{true: "1", false: "0"}[t]
		case float64:
			v = strconvF(t)
		default:
			continue
		}
		nv, err := normalizeSetting(k, v)
		if err != nil {
			writeAnyErr(w, err)
			return
		}
		save[k] = nv
		if cur[k] != nv {
			changes[k] = [2]string{cur[k], nv}
		}
	}
	if err := s.Store.SetSettings(ctx, save); err != nil {
		writeAnyErr(w, err)
		return
	}
	if len(changes) > 0 {
		s.auditLog(r, "settings_save", "網站設定", map[string]any{"changes": changes})
	}
	writeJSON(w, http.StatusOK, s.settingsOut(s.settings(ctx)))
}

func (s *Server) mediaDir() string { return s.Store.Dir + "/media" }

func (s *Server) apiUploadMedia(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	var limit int64
	var allowed map[string]bool
	switch kind {
	case "logo":
		limit = 1 << 20
	case "hero":
		limit = 5 << 20
		allowed = map[string]bool{"png": true, "jpg": true, "gif": true, "webp": true}
	default:
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	data, err := readUpload(w, r, limit)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	name, err := assets.Save(s.mediaDir(), kind, data, allowed)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx := r.Context()
	old := s.settings(ctx)[kind]
	if err := s.Store.SetSettings(ctx, map[string]string{kind: name}); err != nil {
		writeAnyErr(w, err)
		return
	}
	if old != "" && old != name {
		assets.Remove(s.mediaDir(), old)
	}
	s.auditLog(r, "media_upload", kind, map[string]any{"file": name})
	writeJSON(w, http.StatusOK, s.settingsOut(s.settings(ctx)))
}

func (s *Server) apiDeleteMedia(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "logo" && kind != "hero" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ctx := r.Context()
	old := s.settings(ctx)[kind]
	if err := s.Store.SetSettings(ctx, map[string]string{kind: ""}); err != nil {
		writeAnyErr(w, err)
		return
	}
	if old != "" {
		assets.Remove(s.mediaDir(), old)
		s.auditLog(r, "media_remove", kind, map[string]any{"file": old})
	}
	writeJSON(w, http.StatusOK, s.settingsOut(s.settings(ctx)))
}

// ---- snapshots ----

func (s *Server) apiSnapshots(w http.ResponseWriter, r *http.Request) {
	list, err := s.Snaps.List()
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list, "keep": s.Cfg.SnapshotKeep})
}

func (s *Server) apiCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	info, err := s.Maint.Snapshot(r.Context(), "manual")
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	s.auditLog(r, "snapshot_create", info.Name, map[string]any{"count": info.Count})
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) loadSnapshot(w http.ResponseWriter, r *http.Request) *snapshots.Snapshot {
	snap, err := s.Snaps.Load(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "找不到快照")
		return nil
	}
	return snap
}

func (s *Server) apiDownloadSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap := s.loadSnapshot(w, r)
	if snap == nil {
		return
	}
	base := strings.TrimSuffix(name, ".json")
	if r.URL.Query().Get("format") == "zone" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+s.Cfg.CFZoneName+"-"+base+`.zone"`)
		w.Write([]byte(snapshots.BIND(snap.Zone, snap.Records, time.Unix(int64(snap.TS), 0).In(s.Cfg.Timezone))))
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+s.Cfg.CFZoneName+"-"+base+`.json"`)
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) apiSnapshotDiff(w http.ResponseWriter, r *http.Request) {
	snap := s.loadSnapshot(w, r)
	if snap == nil {
		return
	}
	cur, _, err := s.CF.List(r.Context(), true)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("name"), "ts": snap.TS, "source": snap.Source, "diff": snapshots.Compare(snap.Records, cur)})
}

func (s *Server) apiAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Audit(r.Context(), 200)
	if err != nil {
		writeAnyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": rows})
}
