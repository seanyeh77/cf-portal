// Package dnsrec holds name normalization, content validation, conflict
// detection and naming rules for DNS records.
package dnsrec

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"

	"github.com/seanyeh77/cf-portal/internal/model"
)

// Editable lists the types this system can create and modify.
var Editable = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true}

// Proxiable lists the types Cloudflare can proxy.
var Proxiable = map[string]bool{"A": true, "AAAA": true, "CNAME": true}

// ValidationError is a user-facing validation failure (HTTP 422).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ConflictError is a conflict with existing records (HTTP 409).
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// NormalizeName turns user input into a fully qualified lowercase name.
func NormalizeName(in, zone string) string {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in)), ".")
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	if n == "" || n == "@" || n == zone {
		return zone
	}
	if strings.HasSuffix(n, "."+zone) {
		return n
	}
	return n + "." + zone
}

// NormalizeTarget lowercases a hostname target and strips the trailing dot.
func NormalizeTarget(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

var tunnelRe = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.cfargotunnel\.com$`)

// TunnelID returns the tunnel id of a CNAME target such as
// <uuid>.cfargotunnel.com, or "" for any other target. Such targets resolve
// only inside Cloudflare, so they never appear in public DNS.
func TunnelID(target string) string {
	if m := tunnelRe.FindStringSubmatch(NormalizeTarget(target)); m != nil {
		return m[1]
	}
	return ""
}

// Tunnel returns the tunnel id a record routes to, or "" when it is not a
// CNAME to a Cloudflare Tunnel.
func Tunnel(r model.Record) string {
	if r.Type != "CNAME" {
		return ""
	}
	return TunnelID(r.Content)
}

// Relative returns the part of name in front of the zone ("" for the apex).
func Relative(name, zone string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == zone {
		return ""
	}
	return strings.TrimSuffix(name, "."+zone)
}

// IsLocalAddr reports loopback or unspecified addresses.
func IsLocalAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsUnspecified()
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPrivateAddr reports addresses that are not reachable from the internet
// (private, link-local, CGNAT), excluding loopback and unspecified.
func IsPrivateAddr(a netip.Addr) bool {
	a = a.Unmap()
	if IsLocalAddr(a) {
		return false
	}
	return a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// BadCNAMETarget returns why target is not a usable CNAME target, or "".
func BadCNAMETarget(target, name string) string {
	t := NormalizeTarget(target)
	switch {
	case t == "localhost" || strings.HasSuffix(t, ".localhost"):
		return "CNAME 目標為 localhost"
	case isIP(t):
		return "CNAME 目標為 IP 位址，請改用 A 或 AAAA record"
	case !strings.Contains(t, "."):
		return "CNAME 目標不是完整網域"
	case name != "" && t == NormalizeTarget(name):
		return "CNAME 不可指向自己"
	}
	return ""
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(strings.Trim(s, "[]"))
	return err == nil
}

var hostRe = regexp.MustCompile(`^(\*\.)?([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)*[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// Validate normalizes in (name, TTL, proxy) and checks the content.
func Validate(in *model.RecordInput, zone string) error {
	in.Type = strings.ToUpper(strings.TrimSpace(in.Type))
	in.Name = NormalizeName(in.Name, zone)
	in.Content = strings.TrimSpace(in.Content)
	in.Comment = strings.TrimSpace(in.Comment)
	if !Editable[in.Type] {
		return invalid("%s 請於 Cloudflare 編輯", in.Type)
	}
	if len(in.Name) > 253 || !hostRe.MatchString(in.Name) {
		return invalid("名稱格式不正確：%s", in.Name)
	}
	if in.Content == "" {
		return invalid("請填寫內容")
	}
	if !Proxiable[in.Type] {
		in.Proxied = false
	}
	if in.Proxied {
		in.TTL = 1
	}
	if in.TTL == 0 {
		in.TTL = 1
	}
	switch in.Type {
	case "A", "AAAA":
		a, err := netip.ParseAddr(in.Content)
		if err != nil {
			return invalid("%v", err)
		}
		if in.Type == "A" && !a.Is4() {
			return invalid("A record 須為 IPv4 位址")
		}
		if in.Type == "AAAA" && !a.Is6() {
			return invalid("AAAA record 須為 IPv6 位址")
		}
		if IsLocalAddr(a) {
			return invalid("不可指向本機位址（127.x、::1、0.0.0.0）")
		}
		in.Content = a.String()
	case "CNAME":
		in.Content = NormalizeTarget(in.Content)
		if msg := BadCNAMETarget(in.Content, in.Name); msg != "" {
			return invalid("%s", msg)
		}
	case "MX":
		in.Content = NormalizeTarget(in.Content)
		if in.Priority == nil {
			return invalid("MX records need a priority")
		}
		if *in.Priority < 0 || *in.Priority > 65535 {
			return invalid("priority must be between 0 and 65535")
		}
	case "TXT":
		if len(in.Content) > 2048 {
			return invalid("TXT 內容過長")
		}
	}
	if in.Type != "MX" {
		in.Priority = nil
	}
	if in.TTL != 1 && (in.TTL < 60 || in.TTL > 86400) {
		return invalid("ttl must be 1 (auto) or 60 to 86400 seconds")
	}
	if len([]rune(in.Comment)) > 100 {
		return invalid("Cloudflare 備註最多 100 字")
	}
	return nil
}

func sameContent(typ, a, b string) bool {
	switch typ {
	case "CNAME", "MX", "NS":
		return NormalizeTarget(a) == NormalizeTarget(b)
	case "A", "AAAA":
		x, e1 := netip.ParseAddr(a)
		y, e2 := netip.ParseAddr(b)
		if e1 == nil && e2 == nil {
			return x == y
		}
	}
	return a == b
}

var cnameExclusive = map[string]bool{"A": true, "AAAA": true, "CNAME": true}

// Conflict checks in against existing records, ignoring excludeID.
func Conflict(in model.RecordInput, existing []model.Record, excludeID string) error {
	name := NormalizeTarget(in.Name)
	for _, r := range existing {
		if r.ID == excludeID && excludeID != "" {
			continue
		}
		if NormalizeTarget(r.Name) != name {
			continue
		}
		if r.Type == in.Type && sameContent(r.Type, r.Content, in.Content) {
			return &ConflictError{Msg: fmt.Sprintf("已有相同的 %s record", in.Type)}
		}
		if (in.Type == "CNAME" && cnameExclusive[r.Type]) || (r.Type == "CNAME" && cnameExclusive[in.Type]) {
			return &ConflictError{Msg: fmt.Sprintf("%s 已有 %s record，CNAME 不可與其他 record 同名", name, r.Type)}
		}
	}
	return nil
}

var ruleCache sync.Map // rule string → compiled rule (or error)

type compiled struct {
	re  *regexp.Regexp
	err error
}

// compileRule compiles a custom naming rule anchored to the whole name,
// once per distinct rule.
func compileRule(rule string) (*regexp.Regexp, error) {
	if c, ok := ruleCache.Load(rule); ok {
		return c.(compiled).re, c.(compiled).err
	}
	re, err := regexp.Compile("^(?:" + rule + ")$")
	ruleCache.Store(rule, compiled{re, err})
	return re, err
}

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// NameRuleError returns why name breaks the naming rule, or "".
// rule is the optional custom regular expression; an invalid one passes.
func NameRuleError(name, zone, rule string) string {
	rel := Relative(name, zone)
	if rel == "" {
		return ""
	}
	hasUnderscore := false
	for _, seg := range strings.Split(rel, ".") {
		if strings.HasPrefix(seg, "_") {
			hasUnderscore = true
			continue
		}
		if seg == "*" {
			continue
		}
		if !labelRe.MatchString(seg) {
			return fmt.Sprintf("「%s」僅能使用小寫英文、數字與連字號，且不可以連字號開頭或結尾", seg)
		}
	}
	if rule != "" && !hasUnderscore {
		re, err := compileRule(rule)
		if err == nil && !re.MatchString(rel) {
			return fmt.Sprintf("「%s」不符合自訂命名規則", rel)
		}
	}
	return ""
}

var trailingDigits = regexp.MustCompile(`[0-9]+$`)

// Group returns the group key of a record name ("" for the apex).
func Group(name, zone string) string {
	rel := Relative(name, zone)
	if rel == "" {
		return ""
	}
	first := strings.SplitN(rel, ".", 2)[0]
	if strings.HasPrefix(first, "_") {
		return "_系統"
	}
	first = strings.SplitN(first, "-", 2)[0]
	if g := trailingDigits.ReplaceAllString(first, ""); g != "" {
		return g
	}
	return first
}
