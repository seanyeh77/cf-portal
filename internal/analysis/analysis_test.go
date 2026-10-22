package analysis

import (
	"testing"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/model"
)

const zone = "sdc.nycu.club"

var (
	taipei, _ = time.LoadLocation("Asia/Taipei")
	now       = time.Date(2026, 10, 22, 12, 0, 0, 0, taipei)
)

func rec(id, typ, name, content string) model.Record {
	return model.Record{ID: id, Type: typ, Name: name + "." + zone, Content: content, TTL: 1}
}

type fixture struct {
	recs  []model.Record
	meta  map[string]model.Meta
	scans map[string]model.Scan
	mutes map[string]map[string]bool
	set   Settings
}

func newFixture(recs ...model.Record) *fixture {
	fx := &fixture{recs: recs, meta: map[string]model.Meta{}, scans: map[string]model.Scan{}, mutes: map[string]map[string]bool{},
		set: Settings{ZombieDays: 7, ExpiryWarnDays: 14}}
	for _, r := range recs {
		fx.meta[r.ID] = model.Meta{Owner: "someone"}
	}
	return fx
}

func (fx *fixture) run() *Result {
	return Analyze(Input{Zone: zone, Records: fx.recs, Meta: fx.meta, Scans: fx.scans, Mutes: fx.mutes, Settings: fx.set, Now: now, Loc: taipei})
}

func kinds(r *Result, id string) []string {
	var out []string
	for _, i := range r.Records[id].Issues {
		out = append(out, i.Kind)
	}
	return out
}

func has(r *Result, id, kind string) bool {
	for _, k := range kinds(r, id) {
		if k == kind {
			return true
		}
	}
	return false
}

func issue(r *Result, id, kind string) Issue {
	for _, i := range r.Records[id].Issues {
		if i.Kind == kind {
			return i
		}
	}
	return Issue{}
}

func TestBadTarget(t *testing.T) {
	fx := newFixture(rec("1", "CNAME", "a", "localhost"), rec("2", "CNAME", "b", "10.0.0.1"), rec("3", "CNAME", "c", "gitlab"), rec("4", "CNAME", "d", "x.github.io"))
	r := fx.run()
	for _, id := range []string{"1", "2", "3"} {
		if !has(r, id, "bad_target") {
			t.Errorf("%s should be bad_target: %v", id, kinds(r, id))
		}
	}
	if has(r, "4", "bad_target") {
		t.Error("valid target flagged")
	}
}

func TestLoopback(t *testing.T) {
	fx := newFixture(rec("1", "A", "a", "127.0.0.1"), rec("2", "AAAA", "b", "::1"), rec("3", "A", "c", "0.0.0.0"), rec("4", "A", "d", "10.0.0.1"))
	r := fx.run()
	for _, id := range []string{"1", "2", "3"} {
		if !has(r, id, "loopback") {
			t.Errorf("%s should be loopback", id)
		}
	}
	if has(r, "4", "loopback") || has(r, "1", "private_ip") {
		t.Error("wrong loopback/private classification")
	}
}

func TestDangling(t *testing.T) {
	fx := newFixture(
		rec("1", "CNAME", "a", "gone.example.com"),
		rec("2", "CNAME", "b", "c.sdc.nycu.club"),
		rec("3", "A", "c", "10.0.0.1"),
		rec("4", "CNAME", "d", "localhost"),
		rec("5", "CNAME", "e", "fine.example.com"),
	)
	fx.scans["1"] = model.Scan{LastTS: 1, DNSStatus: "nxdomain", DNSDetail: "gone.example.com：查無網域"}
	fx.scans["2"] = model.Scan{LastTS: 1, DNSStatus: "nxdomain"}
	fx.scans["4"] = model.Scan{LastTS: 1, DNSStatus: "nxdomain"}
	fx.scans["5"] = model.Scan{LastTS: 1, DNSStatus: "unknown"}
	r := fx.run()
	if !has(r, "1", "dangling") || issue(r, "1", "dangling").Detail != "gone.example.com：查無網域" {
		t.Errorf("1 should be dangling: %v", r.Records["1"].Issues)
	}
	if has(r, "2", "dangling") {
		t.Error("target in zone must not be dangling")
	}
	if has(r, "4", "dangling") {
		t.Error("bad_target must not also be dangling")
	}
	if has(r, "5", "dangling") {
		t.Error("unknown must not be dangling")
	}
}

func TestUnclaimed(t *testing.T) {
	fx := newFixture(rec("1", "CNAME", "a", "me.github.io"), rec("2", "CNAME", "b", "you.github.io"))
	fx.scans["1"] = model.Scan{LastTS: 1, DNSStatus: "unclaimed", DNSDetail: "GitHub Pages：查無對應站台"}
	fx.scans["2"] = model.Scan{LastTS: 1, DNSStatus: "ok"}
	r := fx.run()
	if !has(r, "1", "unclaimed") || issue(r, "1", "unclaimed").Detail != "GitHub Pages：查無對應站台" {
		t.Error("1 should be unclaimed")
	}
	if has(r, "2", "unclaimed") {
		t.Error("2 should not be unclaimed")
	}
}

func TestExpiry(t *testing.T) {
	fx := newFixture(rec("1", "A", "a", "1.1.1.1"), rec("2", "A", "b", "1.1.1.2"), rec("3", "A", "c", "1.1.1.3"), rec("4", "A", "d", "1.1.1.4"), rec("5", "A", "e", "1.1.1.5"))
	fx.meta["1"] = model.Meta{Owner: "x", ExpiresAt: "2026-10-21", AutoDelete: true}
	fx.meta["2"] = model.Meta{Owner: "x", ExpiresAt: "2026-10-22"}
	fx.meta["3"] = model.Meta{Owner: "x", ExpiresAt: "2026-11-05"}
	fx.meta["4"] = model.Meta{Owner: "x", ExpiresAt: "2026-11-06"}
	r := fx.run()
	if !has(r, "1", "expired") || issue(r, "1", "expired").Detail != "到期日 2026-10-21（自動刪除）" {
		t.Errorf("1 expired: %v", r.Records["1"].Issues)
	}
	if has(r, "2", "expired") || !has(r, "2", "expiring") {
		t.Error("expiring today is not expired")
	}
	if !has(r, "3", "expiring") || issue(r, "3", "expiring").Detail != "到期日 2026-11-05" {
		t.Error("3 within 14 days")
	}
	if has(r, "4", "expiring") || has(r, "5", "expiring") || has(r, "5", "expired") {
		t.Error("4 and 5 not expiring")
	}
}

func TestUpstreamChain(t *testing.T) {
	fx := newFixture(
		rec("alias", "CNAME", "alias", "localhost"),
		rec("chain", "CNAME", "chain", "alias.sdc.nycu.club"),
		rec("chain2", "CNAME", "chain2", "chain.sdc.nycu.club."),
		rec("chain3", "CNAME", "chain3", "chain2.sdc.nycu.club"),
		rec("ok", "A", "ok", "10.0.0.1"),
		rec("okc", "CNAME", "okc", "ok.sdc.nycu.club"),
		rec("loopA", "A", "loop", "127.0.0.1"),
		rec("toloop", "CNAME", "toloop", "loop.sdc.nycu.club"),
		rec("cyc1", "CNAME", "cyc1", "cyc2.sdc.nycu.club"),
		rec("cyc2", "CNAME", "cyc2", "cyc1.sdc.nycu.club"),
	)
	r := fx.run()
	if got := issue(r, "chain", "upstream_broken"); got.Detail != "上游 alias.sdc.nycu.club：CNAME 目標無效" || got.Upstream != "alias" {
		t.Errorf("chain: %+v", got)
	}
	if got := issue(r, "chain2", "upstream_broken"); got.Detail != "上游 chain.sdc.nycu.club：受上游影響" || got.Upstream != "chain" {
		t.Errorf("chain2: %+v", got)
	}
	if !has(r, "chain3", "upstream_broken") {
		t.Error("chain3 must inherit through several levels")
	}
	if has(r, "okc", "upstream_broken") || has(r, "alias", "upstream_broken") {
		t.Error("false upstream_broken")
	}
	if !has(r, "toloop", "upstream_broken") {
		t.Error("loopback upstream")
	}
	if has(r, "cyc1", "upstream_broken") {
		t.Error("cycle must not be broken")
	}
	if !has(r, "chain2", "cname_chain") {
		t.Error("cname_chain")
	}
	if has(r, "okc", "cname_chain") {
		t.Error("CNAME to A is not a chain")
	}
}

func TestUpstreamDangling(t *testing.T) {
	fx := newFixture(rec("1", "CNAME", "pages", "gone.github.io"), rec("2", "CNAME", "www", "pages.sdc.nycu.club"))
	fx.scans["1"] = model.Scan{LastTS: 1, DNSStatus: "unclaimed"}
	r := fx.run()
	if !has(r, "2", "upstream_broken") {
		t.Error("unclaimed upstream propagates")
	}
}

func TestZombie(t *testing.T) {
	fx := newFixture(rec("1", "A", "a", "10.0.0.1"), rec("2", "A", "b", "10.0.0.2"), rec("3", "A", "c", "10.0.0.3"), rec("4", "A", "d", "10.0.0.4"), rec("5", "A", "e", "10.0.0.5"))
	day := 86400.0
	n := float64(now.Unix())
	fx.scans["1"] = model.Scan{FirstTS: n - 30*day, LastTS: n, LastOKTS: new(n - 5*day - 100), Reachable: 0}
	fx.scans["2"] = model.Scan{FirstTS: n - 8*day, LastTS: n, Reachable: 0}
	fx.scans["3"] = model.Scan{FirstTS: n - 30*day, LastTS: n, LastOKTS: new(n - 8*day), Reachable: 0}
	fx.scans["4"] = model.Scan{FirstTS: n - 30*day, LastTS: n, Reachable: 0}
	fx.meta["4"] = model.Meta{Owner: "x", NoScan: true}
	fx.scans["5"] = model.Scan{FirstTS: n - 30*day, LastTS: n, LastOKTS: new(n - 30*day), Reachable: 1}
	r := fx.run()
	if has(r, "1", "zombie") {
		t.Error("5 days is below threshold")
	}
	if got := issue(r, "2", "zombie").Detail; got != "8 日無回應，從未回應" {
		t.Errorf("2: %q", got)
	}
	if got := issue(r, "3", "zombie").Detail; got != "8 日無回應，最後回應 2026-10-14" {
		t.Errorf("3: %q", got)
	}
	if has(r, "4", "zombie") || has(r, "5", "zombie") {
		t.Error("no_scan or reachable is not zombie")
	}
	fx.set.ZombieDays = 5
	if r := fx.run(); !has(r, "1", "zombie") || issue(r, "1", "zombie").Detail != "5 日無回應，最後回應 2026-10-17" {
		t.Errorf("custom zombie days: %v", r.Records["1"].Issues)
	}
}

func TestProxyRules(t *testing.T) {
	a := rec("1", "A", "a", "10.0.0.1")
	a.Proxied = true
	b := rec("2", "A", "b", "8.8.8.8")
	c := rec("3", "A", "c", "8.8.4.4")
	d := rec("4", "A", "d", "1.0.0.1")
	d.Proxied = true
	e := rec("5", "A", "e", "10.0.0.9")
	fx := newFixture(a, b, c, d, e)
	fx.scans["2"] = model.Scan{LastTS: 1, Reachable: 1, OpenPorts: "443,22"}
	fx.scans["3"] = model.Scan{LastTS: 1, Reachable: 1, OpenPorts: "22"}
	fx.scans["4"] = model.Scan{LastTS: 1, Reachable: 1, OpenPorts: "443"}
	fx.scans["5"] = model.Scan{LastTS: 1, Reachable: 1, OpenPorts: "80"}
	r := fx.run()
	if !has(r, "1", "proxied_private") || !has(r, "1", "private_ip") {
		t.Error("1 proxied_private + private_ip")
	}
	if !has(r, "2", "unproxied_web") || has(r, "3", "unproxied_web") || has(r, "4", "unproxied_web") || has(r, "5", "unproxied_web") {
		t.Error("unproxied_web")
	}
	if has(r, "2", "private_ip") || has(r, "4", "proxied_private") {
		t.Error("public addresses")
	}
}

func TestNoOwnerNameRuleShared(t *testing.T) {
	fx := newFixture(rec("1", "A", "Old_Box", "10.0.1.21"), rec("2", "A", "gitlab", "10.0.1.21"), rec("3", "CNAME", "x", "a.example.com"), rec("4", "CNAME", "y", "A.example.com."), rec("5", "TXT", "_dmarc", "v=DMARC1"))
	fx.meta["2"] = model.Meta{}
	r := fx.run()
	if !has(r, "1", "name_rule") || has(r, "2", "name_rule") || has(r, "5", "name_rule") {
		t.Error("name_rule")
	}
	if !has(r, "2", "no_owner") || has(r, "1", "no_owner") {
		t.Error("no_owner")
	}
	if got := issue(r, "1", "shared_target").Detail; got != "2 筆指向 10.0.1.21" {
		t.Errorf("shared_target: %q", got)
	}
	if !has(r, "4", "shared_target") {
		t.Error("CNAME targets compare case-insensitively")
	}
	fx.set.NameRule = `[a-z]+`
	fx.recs = append(fx.recs, rec("6", "A", "hpc-x", "1.2.3.4"))
	if r := fx.run(); !has(r, "6", "name_rule") || has(r, "2", "name_rule") {
		t.Error("custom name rule")
	}
}

func TestMutesAndLevel(t *testing.T) {
	fx := newFixture(rec("1", "A", "a", "10.0.0.1"), rec("2", "A", "b", "10.0.0.1"))
	fx.meta["1"] = model.Meta{}
	r := fx.run()
	if r.Records["1"].Level() != Low {
		t.Errorf("level %q", r.Records["1"].Level())
	}
	if r.Records["2"].Level() != "" {
		t.Error("info only rows have no level")
	}
	if kinds(r, "1")[0] != "no_owner" {
		t.Errorf("sorted by level: %v", kinds(r, "1"))
	}
	fx.mutes["1"] = map[string]bool{"no_owner": true, "private_ip": true}
	r = fx.run()
	if has(r, "1", "no_owner") || r.Records["1"].Level() != "" || len(r.Records["1"].Muted) != 2 {
		t.Errorf("muted: %+v", r.Records["1"])
	}
	if r.Summary["no_owner"] != 0 || r.Summary["private_ip"] != 1 {
		t.Errorf("summary %v", r.Summary)
	}
}

func TestGroups(t *testing.T) {
	var recs []model.Record
	for _, n := range []string{"student001", "student002", "student003", "hpc-alice", "hpc-bob", "grafana", "_dmarc", "_acme-challenge"} {
		recs = append(recs, rec(n, "TXT", n, "x"))
	}
	r := newFixture(recs...).run()
	got := map[string]int{}
	for _, g := range r.Groups {
		got[g[0].(string)] = g[1].(int)
	}
	if got["student"] != 3 || got["hpc"] != 2 || got["_系統"] != 2 || got["grafana"] != 0 {
		t.Errorf("groups %v", got)
	}
	if r.Records["student002"].Group != "student" {
		t.Error("record group")
	}
}

func TestKindsOrderedBySeverity(t *testing.T) {
	rank := map[string]int{High: 0, Medium: 1, Low: 2, Info: 3}
	for i := 1; i < len(Kinds); i++ {
		if rank[Kinds[i].Level] < rank[Kinds[i-1].Level] {
			t.Fatalf("%s (%s) listed after %s (%s)", Kinds[i].Key, Kinds[i].Level, Kinds[i-1].Key, Kinds[i-1].Level)
		}
	}
}
