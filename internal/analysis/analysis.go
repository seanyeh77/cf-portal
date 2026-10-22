// Package analysis finds problems in the zone's records.
package analysis

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/model"
)

// Levels in order of severity.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
	Info   = "info"
)

// LevelRank orders levels; lower is more severe.
var LevelRank = map[string]int{High: 0, Medium: 1, Low: 2, Info: 3}

// Kind describes one kind of issue.
type Kind struct {
	Key   string `json:"key"`
	Level string `json:"level"`
	Label string `json:"label"`
	Help  string `json:"help"`
}

// Kinds lists all issue kinds in display order.
var Kinds = []Kind{
	{"bad_target", High, "CNAME 目標無效", "CNAME 須指向完整網域，不可為 localhost、IP 位址或不含網域的名稱。請改為正確的目標或刪除此 record。"},
	{"loopback", High, "指向本機位址", "127.0.0.1、::1、0.0.0.0 只代表使用者自己的電腦，無法連到伺服器。請改為正確的 IP 或刪除此 record。"},
	{"dangling", High, "CNAME 目標不存在", "目標網域已不存在，第三方可註冊該網域並接管此子網域。若服務已停用，請刪除此 record。"},
	{"unclaimed", High, "CNAME 目標未使用", "託管服務回報此網址無對應站台，第三方可能認領並接管此子網域。請刪除此 record 或重建站台。"},
	{"expired", High, "已過期", "已超過到期日。請確認後刪除，或延後到期日。"},
	{"upstream_broken", Medium, "受上游影響", "此 CNAME 指向本網域另一筆無法使用的 record。請先修正上游 record，修正後此項會自動解除。"},
	{"zombie", Medium, "長期無回應", "連續多日無任何回應，服務可能已停用。請與負責人確認；非常態開機的主機可停用連線檢查。"},
	{"proxied_private", Medium, "內網位址啟用 Proxy", "Cloudflare 無法連線至內網位址。請停用 Proxy。"},
	{"expiring", Medium, "即將到期", "即將到達到期日。如需繼續使用，請延後到期日。"},
	{"no_owner", Low, "未指派負責人", "請填寫負責人與用途，以利交接。"},
	{"name_rule", Low, "名稱不符規則", "名稱不符合命名規則。既有 record 可忽略。"},
	{"unproxied_web", Low, "網頁未經 Proxy", "主機直接對外提供網頁，來源 IP 公開。建議啟用 Proxy。"},
	{"private_ip", Info, "指向內網位址", "公開 DNS 可查得此內網位址。內網專用服務通常可接受。"},
	{"cname_chain", Info, "CNAME 串接", "目標為本網域的另一筆 CNAME，可改為直接指向最終目標。"},
	{"shared_target", Info, "目標重複", "與其他 record 指向相同目標，可能為同一主機的多項服務或未清除的舊名稱。若為同一服務，可改用 CNAME 指向主要名稱，日後只需維護一筆。"},
}

// KindByKey indexes Kinds.
var KindByKey = func() map[string]Kind {
	m := map[string]Kind{}
	for _, k := range Kinds {
		m[k.Key] = k
	}
	return m
}()

var kindOrder = func() map[string]int {
	m := map[string]int{}
	for i, k := range Kinds {
		m[k.Key] = i
	}
	return m
}()

// brokenKinds make a record unusable for CNAMEs pointing at it.
var brokenKinds = map[string]bool{"bad_target": true, "loopback": true, "dangling": true, "unclaimed": true}

// Issue is one problem found on a record.
type Issue struct {
	Kind     string `json:"kind"`
	Level    string `json:"level"`
	Label    string `json:"label"`
	Help     string `json:"help"`
	Detail   string `json:"detail"`
	Upstream string `json:"upstream,omitempty"`
}

// Settings are the rule settings used by the analysis.
type Settings struct {
	ZombieDays     int
	ExpiryWarnDays int
	NameRule       string
}

// Input is everything the analysis looks at.
type Input struct {
	Zone     string
	Records  []model.Record
	Meta     map[string]model.Meta
	Scans    map[string]model.Scan
	Mutes    map[string]map[string]bool
	Settings Settings
	Now      time.Time
	Loc      *time.Location
}

// RecordResult is the analysis of one record.
type RecordResult struct {
	Issues []Issue `json:"issues"`
	Muted  []Issue `json:"muted"`
	Group  string  `json:"group"`
}

// Level is the row level: the first non-info issue level, or "".
func (r RecordResult) Level() string {
	for _, i := range r.Issues {
		if i.Level != Info {
			return i.Level
		}
	}
	return ""
}

// Result is the output of Analyze.
type Result struct {
	Records map[string]*RecordResult
	Summary map[string]int
	Groups  [][2]any
}

func newIssue(kind, detail string) Issue {
	k := KindByKey[kind]
	return Issue{Kind: kind, Level: k.Level, Label: k.Label, Help: k.Help, Detail: detail}
}

func addrOf(r model.Record) (netip.Addr, bool) {
	if r.Type != "A" && r.Type != "AAAA" {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(strings.TrimSpace(r.Content))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// TargetKey is the key used to compare record targets.
func TargetKey(r model.Record) string {
	switch r.Type {
	case "A", "AAAA":
		if a, ok := addrOf(r); ok {
			return a.String()
		}
		return strings.TrimSpace(r.Content)
	case "CNAME":
		return dnsrec.NormalizeTarget(r.Content)
	}
	return ""
}

func hasPort(ports string, want ...string) bool {
	for _, p := range strings.Split(ports, ",") {
		for _, w := range want {
			if strings.TrimSpace(p) == w {
				return true
			}
		}
	}
	return false
}

// AddDays adds n days to a YYYY-MM-DD date.
func AddDays(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// Analyze runs all rules.
func Analyze(in Input) *Result {
	loc := in.Loc
	if loc == nil {
		loc = time.UTC
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := now.In(loc).Format("2006-01-02")
	zone := in.Zone

	byName := map[string][]model.Record{}
	for _, r := range in.Records {
		n := dnsrec.NormalizeTarget(r.Name)
		byName[n] = append(byName[n], r)
	}
	targets := map[string][]string{}
	for _, r := range in.Records {
		if k := TargetKey(r); k != "" {
			targets[k] = append(targets[k], r.ID)
		}
	}

	raw := map[string][]Issue{}
	add := func(id, kind, detail string) { raw[id] = append(raw[id], newIssue(kind, detail)) }

	for _, r := range in.Records {
		meta := in.Meta[r.ID]
		scan, scanned := in.Scans[r.ID]
		addr, isAddr := addrOf(r)
		target := dnsrec.NormalizeTarget(r.Content)
		badTarget := false

		if r.Type == "CNAME" {
			if msg := dnsrec.BadCNAMETarget(r.Content, r.Name); msg != "" {
				badTarget = true
				add(r.ID, "bad_target", msg)
			}
		}
		if isAddr && dnsrec.IsLocalAddr(addr) {
			add(r.ID, "loopback", addr.String())
		}
		if r.Type == "CNAME" && scanned && !badTarget {
			if scan.DNSStatus == "nxdomain" && len(byName[target]) == 0 {
				d := scan.DNSDetail
				if d == "" {
					d = target + "：查無網域"
				}
				add(r.ID, "dangling", d)
			}
			if scan.DNSStatus == "unclaimed" {
				d := scan.DNSDetail
				if d == "" {
					d = scan.Detail
				}
				add(r.ID, "unclaimed", d)
			}
		}
		if meta.ExpiresAt != "" {
			suffix := ""
			if meta.AutoDelete {
				suffix = "（自動刪除）"
			}
			detail := "到期日 " + meta.ExpiresAt + suffix
			if meta.ExpiresAt < today {
				add(r.ID, "expired", detail)
			} else if meta.ExpiresAt <= AddDays(today, in.Settings.ExpiryWarnDays) {
				add(r.ID, "expiring", detail)
			}
		}
		if scanned && !meta.NoScan && scan.Reachable == 0 && in.Settings.ZombieDays > 0 && scan.LastTS > 0 {
			ref := scan.FirstTS
			if scan.LastOKTS != nil {
				ref = *scan.LastOKTS
			}
			days := int(now.Sub(time.Unix(int64(ref), 0)).Hours() / 24)
			if days >= in.Settings.ZombieDays {
				var d string
				if scan.LastOKTS != nil {
					d = fmt.Sprintf("%d 日無回應，最後回應 %s", days, time.Unix(int64(*scan.LastOKTS), 0).In(loc).Format("2006-01-02"))
				} else {
					d = fmt.Sprintf("%d 日無回應，從未回應", days)
				}
				add(r.ID, "zombie", d)
			}
		}
		if isAddr && r.Proxied && dnsrec.IsPrivateAddr(addr) {
			add(r.ID, "proxied_private", addr.String())
		}
		if strings.TrimSpace(meta.Owner) == "" {
			add(r.ID, "no_owner", "")
		}
		if msg := dnsrec.NameRuleError(r.Name, zone, in.Settings.NameRule); msg != "" {
			add(r.ID, "name_rule", msg)
		}
		if isAddr && !r.Proxied && !dnsrec.IsPrivateAddr(addr) && !dnsrec.IsLocalAddr(addr) && scanned && hasPort(scan.OpenPorts, "80", "443") {
			add(r.ID, "unproxied_web", fmt.Sprintf("%s：埠 %s 有回應", addr, strings.ReplaceAll(scan.OpenPorts, ",", "、")))
		}
		if isAddr && dnsrec.IsPrivateAddr(addr) {
			add(r.ID, "private_ip", addr.String())
		}
		if r.Type == "CNAME" && !badTarget {
			for _, t := range byName[target] {
				if t.Type == "CNAME" {
					add(r.ID, "cname_chain", "→ "+target+" → "+dnsrec.NormalizeTarget(t.Content))
					break
				}
			}
		}
		// TargetKey is "" for other types, and "" is never collected.
		if k := TargetKey(r); len(targets[k]) >= 2 {
			add(r.ID, "shared_target", fmt.Sprintf("%d 筆指向 %s", len(targets[k]), k))
		}
	}

	// upstream_broken propagates down CNAME chains.
	type state struct {
		done   bool
		broken *Issue // issue describing the upstream problem, nil if fine
	}
	memo := map[string]*state{}
	var brokenOf func(id string, depth int) *Issue
	recByID := map[string]model.Record{}
	for _, r := range in.Records {
		recByID[r.ID] = r
	}
	var upstreamOf func(r model.Record, depth int) *Issue
	brokenOf = func(id string, depth int) *Issue {
		// returns the first broken (or upstream_broken) issue of id
		if s, ok := memo[id]; ok {
			if !s.done {
				return nil // cycle
			}
			return s.broken
		}
		s := &state{}
		memo[id] = s
		var res *Issue
		for _, is := range raw[id] {
			if brokenKinds[is.Kind] {
				c := is
				res = &c
				break
			}
		}
		if res == nil && depth < 32 {
			res = upstreamOf(recByID[id], depth+1)
		}
		s.done, s.broken = true, res
		return res
	}
	upstreamIssue := map[string]Issue{}
	upstreamOf = func(r model.Record, depth int) *Issue {
		if r.Type != "CNAME" {
			return nil
		}
		target := dnsrec.NormalizeTarget(r.Content)
		for _, t := range byName[target] {
			if t.ID == r.ID {
				continue
			}
			if b := brokenOf(t.ID, depth); b != nil {
				is := newIssue("upstream_broken", fmt.Sprintf("上游 %s：%s", target, b.Label))
				is.Upstream = t.ID
				upstreamIssue[r.ID] = is
				return &is
			}
		}
		return nil
	}
	for _, r := range in.Records {
		brokenOf(r.ID, 0)
	}
	for id, is := range upstreamIssue {
		raw[id] = append(raw[id], is)
	}

	res := &Result{Records: map[string]*RecordResult{}, Summary: map[string]int{}}
	groupCount := map[string]int{}
	for _, r := range in.Records {
		issues := raw[r.ID]
		sort.SliceStable(issues, func(i, j int) bool {
			li, lj := LevelRank[issues[i].Level], LevelRank[issues[j].Level]
			if li != lj {
				return li < lj
			}
			return kindOrder[issues[i].Kind] < kindOrder[issues[j].Kind]
		})
		rr := &RecordResult{Issues: []Issue{}, Muted: []Issue{}, Group: dnsrec.Group(r.Name, zone)}
		for _, is := range issues {
			if in.Mutes[r.ID][is.Kind] {
				rr.Muted = append(rr.Muted, is)
			} else {
				rr.Issues = append(rr.Issues, is)
				res.Summary[is.Kind]++
			}
		}
		res.Records[r.ID] = rr
		if rr.Group != "" {
			groupCount[rr.Group]++
		}
	}
	for g, n := range groupCount {
		if n >= 2 {
			res.Groups = append(res.Groups, [2]any{g, n})
		}
	}
	sort.Slice(res.Groups, func(i, j int) bool { return res.Groups[i][0].(string) < res.Groups[j][0].(string) })
	if res.Groups == nil {
		res.Groups = [][2]any{}
	}
	return res
}
