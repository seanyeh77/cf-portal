package dnsrec

import (
	"errors"
	"strings"
	"testing"

	"github.com/nycu-sdc/sdc-portal/internal/model"
)

const zone = "sdc.nycu.club"

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"grafana":                "grafana.sdc.nycu.club",
		"@":                      "sdc.nycu.club",
		"":                       "sdc.nycu.club",
		"  ":                     "sdc.nycu.club",
		"Grafana.SDC.nycu.club.": "grafana.sdc.nycu.club",
		"a.b":                    "a.b.sdc.nycu.club",
		"sdc.nycu.club":          "sdc.nycu.club",
	}
	for in, want := range cases {
		if got := NormalizeName(in, zone); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		in   model.RecordInput
		want string // substring of error, "" for ok
	}{
		{"a ok", model.RecordInput{Type: "A", Name: "x", Content: "10.0.1.20"}, ""},
		{"a parse", model.RecordInput{Type: "A", Name: "x", Content: "10.0.1"}, "ParseAddr"},
		{"a v6", model.RecordInput{Type: "A", Name: "x", Content: "2001:db8::1"}, "IPv4"},
		{"a loopback", model.RecordInput{Type: "A", Name: "x", Content: "127.0.0.5"}, "不可指向本機位址"},
		{"a unspecified", model.RecordInput{Type: "A", Name: "x", Content: "0.0.0.0"}, "不可指向本機位址"},
		{"aaaa loopback", model.RecordInput{Type: "AAAA", Name: "x", Content: "::1"}, "不可指向本機位址"},
		{"aaaa unspecified", model.RecordInput{Type: "AAAA", Name: "x", Content: "::"}, "不可指向本機位址"},
		{"aaaa ok", model.RecordInput{Type: "AAAA", Name: "x", Content: "2001:db8::1"}, ""},
		{"cname localhost", model.RecordInput{Type: "CNAME", Name: "x", Content: "localhost"}, "CNAME 目標為 localhost"},
		{"cname sub localhost", model.RecordInput{Type: "CNAME", Name: "x", Content: "app.localhost."}, "CNAME 目標為 localhost"},
		{"cname ip", model.RecordInput{Type: "CNAME", Name: "x", Content: "10.0.0.1"}, "CNAME 目標為 IP 位址，請改用 A 或 AAAA record"},
		{"cname nodot", model.RecordInput{Type: "CNAME", Name: "x", Content: "gitlab"}, "CNAME 目標不是完整網域"},
		{"cname self", model.RecordInput{Type: "CNAME", Name: "x", Content: "x.sdc.nycu.club."}, "CNAME 不可指向自己"},
		{"cname ok", model.RecordInput{Type: "CNAME", Name: "x", Content: "Me.GitHub.io."}, ""},
		{"mx no prio", model.RecordInput{Type: "MX", Name: "@", Content: "mx.example.com"}, "MX records need a priority"},
		{"mx ok", model.RecordInput{Type: "MX", Name: "@", Content: "mx.example.com", Priority: new(10)}, ""},
		{"ttl bad", model.RecordInput{Type: "A", Name: "x", Content: "10.0.0.1", TTL: 30}, "ttl must be 1 (auto) or 60 to 86400 seconds"},
		{"ttl too big", model.RecordInput{Type: "A", Name: "x", Content: "10.0.0.1", TTL: 90000}, "ttl must be 1"},
		{"ttl proxied forced", model.RecordInput{Type: "A", Name: "x", Content: "1.2.3.4", TTL: 30, Proxied: true}, ""},
		{"srv not editable", model.RecordInput{Type: "SRV", Name: "x", Content: "1 1 1 a.b"}, "SRV 請於 Cloudflare 編輯"},
		{"empty content", model.RecordInput{Type: "TXT", Name: "x", Content: " "}, "請填寫內容"},
	}
	for _, c := range cases {
		in := c.in
		err := Validate(&in, zone)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		var ve *ValidationError
		if err == nil || !errors.As(err, &ve) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	in := model.RecordInput{Type: "a", Name: "Grafana", Content: "1.2.3.4", TTL: 300, Proxied: true}
	if err := Validate(&in, zone); err != nil || in.TTL != 1 || in.Type != "A" || in.Name != "grafana.sdc.nycu.club" {
		t.Errorf("normalization failed: %+v %v", in, err)
	}
	in = model.RecordInput{Type: "TXT", Name: "x", Content: "v", Proxied: true}
	if err := Validate(&in, zone); err != nil || in.Proxied {
		t.Errorf("TXT must not be proxied: %+v %v", in, err)
	}
}

func TestConflict(t *testing.T) {
	existing := []model.Record{
		{ID: "1", Type: "A", Name: "gitlab.sdc.nycu.club", Content: "10.0.1.21"},
		{ID: "2", Type: "CNAME", Name: "docs.sdc.nycu.club", Content: "sdc-docs.pages.dev"},
		{ID: "3", Type: "TXT", Name: "gitlab.sdc.nycu.club", Content: "hello"},
	}
	cases := []struct {
		in      model.RecordInput
		exclude string
		want    string
	}{
		{model.RecordInput{Type: "A", Name: "gitlab.sdc.nycu.club", Content: "10.0.1.21"}, "", "已有相同的 A record"},
		{model.RecordInput{Type: "A", Name: "gitlab.sdc.nycu.club", Content: "10.0.1.22"}, "", ""},
		{model.RecordInput{Type: "A", Name: "gitlab.sdc.nycu.club", Content: "10.0.1.21"}, "1", ""},
		{model.RecordInput{Type: "CNAME", Name: "gitlab.sdc.nycu.club", Content: "x.example.com"}, "", "gitlab.sdc.nycu.club 已有 A record，CNAME 不可與其他 record 同名"},
		{model.RecordInput{Type: "A", Name: "docs.sdc.nycu.club", Content: "1.2.3.4"}, "", "docs.sdc.nycu.club 已有 CNAME record，CNAME 不可與其他 record 同名"},
		{model.RecordInput{Type: "CNAME", Name: "docs.sdc.nycu.club", Content: "SDC-docs.pages.dev."}, "", "已有相同的 CNAME record"},
		{model.RecordInput{Type: "CNAME", Name: "docs.sdc.nycu.club", Content: "other.pages.dev"}, "2", ""},
		{model.RecordInput{Type: "TXT", Name: "docs.sdc.nycu.club", Content: "x"}, "", ""},
	}
	for i, c := range cases {
		err := Conflict(c.in, existing, c.exclude)
		if c.want == "" {
			if err != nil {
				t.Errorf("case %d: unexpected %v", i, err)
			}
			continue
		}
		var ce *ConflictError
		if err == nil || !errors.As(err, &ce) || err.Error() != c.want {
			t.Errorf("case %d: got %v, want %q", i, err, c.want)
		}
	}
}

func TestNameRule(t *testing.T) {
	ok := []string{"grafana.sdc.nycu.club", "hpc-grafana.sdc.nycu.club", "_dmarc.sdc.nycu.club", "*.lab.sdc.nycu.club", "sdc.nycu.club", "_acme-challenge.Old.sdc.nycu.club"}
	for _, n := range ok {
		if e := NameRuleError(n, zone, ""); e != "" && !strings.Contains(n, "Old") {
			t.Errorf("%s: unexpected %s", n, e)
		}
	}
	bad := []string{"Old_Box.sdc.nycu.club", "-x.sdc.nycu.club", "x-.sdc.nycu.club", "a_b.sdc.nycu.club"}
	for _, n := range bad {
		if NameRuleError(n, zone, "") == "" {
			t.Errorf("%s should break the rule", n)
		}
	}
	if NameRuleError("grafana.sdc.nycu.club", zone, `hpc-.*`) == "" {
		t.Error("custom rule should apply")
	}
	if NameRuleError("hpc-grafana.sdc.nycu.club", zone, `hpc-.*`) != "" {
		t.Error("custom rule should match")
	}
	if NameRuleError("_dmarc.sdc.nycu.club", zone, `hpc-.*`) != "" {
		t.Error("custom rule skips names with _ labels")
	}
	if NameRuleError("grafana.sdc.nycu.club", zone, `([`) != "" {
		t.Error("invalid custom rule must pass")
	}
}

func TestGroup(t *testing.T) {
	cases := map[string]string{
		"student001.sdc.nycu.club": "student",
		"hpc-alice.sdc.nycu.club":  "hpc",
		"_dmarc.sdc.nycu.club":     "_系統",
		"grafana.sdc.nycu.club":    "grafana",
		"a.b.sdc.nycu.club":        "a",
		"sdc.nycu.club":            "",
		"123.sdc.nycu.club":        "123",
	}
	for in, want := range cases {
		if got := Group(in, zone); got != want {
			t.Errorf("Group(%q) = %q, want %q", in, got, want)
		}
	}
}
