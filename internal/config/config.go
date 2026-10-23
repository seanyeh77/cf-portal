// Package config reads and validates environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultListen is the default LISTEN_ADDR.
const DefaultListen = ":8080"

const defaultCIDRs = "127.0.0.0/8,::1/128,10.0.0.0/8,192.168.0.0/16,fc00::/7"

// Config is the runtime configuration.
type Config struct {
	CFAPIToken        string
	CFZoneName        string
	CFZoneID          string
	CFAPIBase         string
	AdminUsername     string
	AdminPasswordHash string
	SecretKey         string
	SessionHours      int
	CookieSecure      bool
	AllowedCIDRs      []netip.Prefix
	AdminAllowedCIDRs []netip.Prefix
	TrustedProxies    []netip.Prefix
	DBPath            string
	PortalTitle       string
	CheckInterval     time.Duration
	CheckTimeout      time.Duration
	CheckVerifyTLS    bool
	SimpleIconsBase   string
	RecordScanEvery   time.Duration
	ScanTimeout       time.Duration
	ScanDNSCanary     string
	MaintInterval     time.Duration
	SnapshotKeep      int
	Timezone          *time.Location
	Listen            string
}

type reader struct {
	get  func(string) string
	errs []string
}

func (r *reader) str(key, def string) string {
	if v := strings.TrimSpace(r.get(key)); v != "" {
		return v
	}
	return def
}

func (r *reader) required(key string) string {
	v := strings.TrimSpace(r.get(key))
	if v == "" {
		r.errs = append(r.errs, key+" is required")
	}
	return v
}

func (r *reader) integer(key string, def, min int) int {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		r.errs = append(r.errs, fmt.Sprintf("%s must be an integer >= %d (got %q)", key, min, v))
		return def
	}
	return n
}

func (r *reader) seconds(key string, def float64) time.Duration {
	v := r.str(key, "")
	f := def
	if v != "" {
		var err error
		f, err = strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			r.errs = append(r.errs, fmt.Sprintf("%s must be a positive number of seconds (got %q)", key, v))
			f = def
		}
	}
	return time.Duration(f * float64(time.Second))
}

func (r *reader) boolean(key string, def bool) bool {
	v := strings.ToLower(r.str(key, ""))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	r.errs = append(r.errs, fmt.Sprintf("%s must be true or false (got %q)", key, v))
	return def
}

func (r *reader) cidrs(key, def string) []netip.Prefix {
	out, err := ParseCIDRs(r.str(key, def))
	if err != nil {
		r.errs = append(r.errs, fmt.Sprintf("%s: %v", key, err))
	}
	return out
}

// ParseCIDRs parses a comma separated list of CIDRs or bare IPs.
func ParseCIDRs(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", part)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// Load reads the configuration using getenv.
func Load(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	r := &reader{get: getenv}
	c := &Config{
		CFAPIToken:        r.required("CF_API_TOKEN"),
		CFZoneName:        strings.TrimSuffix(strings.ToLower(r.required("CF_ZONE_NAME")), "."),
		CFZoneID:          r.str("CF_ZONE_ID", ""),
		CFAPIBase:         strings.TrimSuffix(r.str("CF_API_BASE", "https://api.cloudflare.com/client/v4"), "/"),
		AdminUsername:     r.str("ADMIN_USERNAME", "admin"),
		AdminPasswordHash: r.required("ADMIN_PASSWORD_HASH"),
		SecretKey:         r.required("SECRET_KEY"),
		SessionHours:      r.integer("SESSION_HOURS", 12, 1),
		CookieSecure:      r.boolean("COOKIE_SECURE", true),
		AllowedCIDRs:      r.cidrs("ALLOWED_CIDRS", defaultCIDRs),
		AdminAllowedCIDRs: r.cidrs("ADMIN_ALLOWED_CIDRS", r.str("ALLOWED_CIDRS", defaultCIDRs)),
		TrustedProxies:    r.cidrs("TRUSTED_PROXIES", "127.0.0.1"),
		DBPath:            r.str("DB_PATH", "/data/portal.db"),
		PortalTitle:       r.str("PORTAL_TITLE", "Service Portal"),
		CheckInterval:     r.seconds("CHECK_INTERVAL", 60),
		CheckTimeout:      r.seconds("CHECK_TIMEOUT", 8),
		CheckVerifyTLS:    r.boolean("CHECK_VERIFY_TLS", true),
		SimpleIconsBase:   strings.TrimSuffix(r.str("SIMPLE_ICONS_BASE", "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons"), "/"),
		RecordScanEvery:   r.seconds("RECORD_SCAN_INTERVAL", 21600),
		ScanTimeout:       r.seconds("SCAN_TIMEOUT", 2.5),
		ScanDNSCanary:     r.str("SCAN_DNS_CANARY", "cloudflare.com"),
		MaintInterval:     r.seconds("MAINT_INTERVAL", 3600),
		SnapshotKeep:      r.integer("SNAPSHOT_KEEP", 30, 1),
		Listen:            r.str("LISTEN_ADDR", DefaultListen),
	}
	if c.SecretKey != "" && len(c.SecretKey) < 32 {
		r.errs = append(r.errs, "SECRET_KEY must be at least 32 characters")
	}
	if c.AdminPasswordHash != "" && !strings.HasPrefix(c.AdminPasswordHash, "argon2id:") {
		r.errs = append(r.errs, "ADMIN_PASSWORD_HASH must be generated by `portal hashpw` (argon2id:...)")
	}
	if c.CheckInterval < time.Second {
		r.errs = append(r.errs, "CHECK_INTERVAL must be at least 1 second")
	}
	tz := r.str("TIMEZONE", "Asia/Taipei")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		r.errs = append(r.errs, fmt.Sprintf("TIMEZONE: unknown time zone %q", tz))
		loc = time.UTC
	}
	c.Timezone = loc
	if len(r.errs) > 0 {
		return nil, errors.New("configuration error:\n  - " + strings.Join(r.errs, "\n  - "))
	}
	return c, nil
}
