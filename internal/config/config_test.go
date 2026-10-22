package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoad(t *testing.T) {
	base := map[string]string{"CF_API_TOKEN": "t", "ADMIN_PASSWORD_HASH": "argon2id:1:1:1:a:b", "SECRET_KEY": strings.Repeat("x", 32)}
	c, err := Load(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.CFZoneName != "sdc.nycu.club" || c.SessionHours != 12 || !c.CookieSecure || len(c.AllowedCIDRs) != 5 || c.ScanTimeout.Seconds() != 2.5 {
		t.Fatalf("defaults %+v", c)
	}
	for _, p := range c.AllowedCIDRs {
		if p.String() == "172.16.0.0/12" {
			t.Fatal("docker bridge range must not be allowed by default")
		}
	}
	if _, err := Load(env(map[string]string{})); err == nil || !strings.Contains(err.Error(), "CF_API_TOKEN is required") {
		t.Fatalf("required: %v", err)
	}
	bad := map[string]string{}
	for k, v := range base {
		bad[k] = v
	}
	bad["ALLOWED_CIDRS"] = "10.0.0.0/8,10.0.0.300/32"
	bad["SECRET_KEY"] = "short"
	bad["TIMEZONE"] = "Mars/Base"
	_, err = Load(env(bad))
	for _, want := range []string{"ALLOWED_CIDRS", "SECRET_KEY must be at least 32", "TIMEZONE"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
