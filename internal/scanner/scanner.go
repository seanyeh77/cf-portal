// Package scanner checks every record's connectivity and runs the
// periodic maintenance jobs.
package scanner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/cloudflare"
	"github.com/nycu-sdc/sdc-portal/internal/dnsrec"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/store"
)

const concurrency = 32

// Resolver resolves host names; *net.Resolver satisfies it.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Takeover describes a hosting service whose unclaimed sites can be taken over.
type Takeover struct {
	Suffixes    []string
	Fingerprint string
	Service     string
}

// Takeovers lists services checked for unclaimed sites.
var Takeovers = []Takeover{
	{[]string{"github.io"}, "There isn't a GitHub Pages site here", "GitHub Pages"},
	{[]string{"herokuapp.com", "herokudns.com"}, "No such app", "Heroku"},
	{[]string{"netlify.app"}, "Not Found - Request ID", "Netlify"},
	{[]string{"vercel.app"}, "DEPLOYMENT_NOT_FOUND", "Vercel"},
	{[]string{"s3.amazonaws.com", "amazonaws.com"}, "NoSuchBucket", "Amazon S3"},
	{[]string{"readthedocs.io"}, "unknown to Read the Docs", "Read the Docs"},
	{[]string{"surge.sh"}, "project not found", "Surge"},
	{[]string{"bitbucket.io"}, "Repository not found", "Bitbucket"},
	{[]string{"azurewebsites.net"}, "404 Web Site not found", "Azure App Service"},
}

// TakeoverFor returns the takeover entry matching target, if any.
func TakeoverFor(target string) *Takeover {
	t := dnsrec.NormalizeTarget(target)
	for i, tk := range Takeovers {
		for _, s := range tk.Suffixes {
			if t == s || strings.HasSuffix(t, "."+s) {
				return &Takeovers[i]
			}
		}
	}
	return nil
}

var downCodes = map[int]bool{521: true, 522: true, 523: true, 525: true, 526: true, 530: true}

// Status is the state of the scanner.
type Status struct {
	Running  bool    `json:"running"`
	LastTS   float64 `json:"last_ts"`
	Count    int     `json:"count"`
	Error    string  `json:"error"`
	DNSOK    bool    `json:"dns_ok"`
	Interval int     `json:"interval"`
}

// Scanner scans records.
type Scanner struct {
	CF       *cloudflare.Client
	Store    *store.Store
	Resolver Resolver
	Timeout  time.Duration
	Canary   string
	Interval time.Duration
	// Ports returns the ports to probe.
	Ports func(ctx context.Context) []int
	// HTTPBase overrides https://<name> for tests.
	HTTPBase func(name, scheme string) string

	http *http.Client
	mu   sync.Mutex
	st   Status
}

// New returns a scanner.
func New(cf *cloudflare.Client, st *store.Store, timeout, interval time.Duration, canary string, ports func(context.Context) []int) *Scanner {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // probing only, never trusting content
	httpTimeout := max(timeout*2, 5*time.Second)
	return &Scanner{CF: cf, Store: st, Resolver: net.DefaultResolver, Timeout: timeout, Canary: canary, Interval: interval, Ports: ports,
		http: &http.Client{Timeout: httpTimeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		st:   Status{DNSOK: true, Interval: int(interval.Seconds())}}
}

// Status returns a copy of the scanner state.
func (s *Scanner) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// Eligible reports whether r is scanned.
func Eligible(r model.Record, m model.Meta) bool {
	return (r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME") && !m.NoScan
}

// ParsePorts parses and normalizes a port list such as "443, 80 22".
func ParsePorts(s string) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '、' }) {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("連接埠格式不符：%s", f)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("請至少填寫一個連接埠")
	}
	if len(out) > 10 {
		return nil, errors.New("最多 10 個連接埠")
	}
	return out, nil
}

// Trigger starts a full scan in the background unless one is running.
func (s *Scanner) Trigger() bool {
	s.mu.Lock()
	if s.st.Running {
		s.mu.Unlock()
		return false
	}
	s.st.Running = true
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.runLocked(ctx)
	}()
	return true
}

// RunOnce scans all eligible records and waits for completion.
func (s *Scanner) RunOnce(ctx context.Context) {
	s.mu.Lock()
	if s.st.Running {
		s.mu.Unlock()
		return
	}
	s.st.Running = true
	s.mu.Unlock()
	s.runLocked(ctx)
}

func (s *Scanner) runLocked(ctx context.Context) {
	count, dnsOK, err := s.scanAll(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Running = false
	s.st.LastTS = float64(time.Now().Unix())
	s.st.Count = count
	s.st.DNSOK = dnsOK
	s.st.Error = ""
	if err != nil {
		s.st.Error = err.Error()
		log.Printf("scanner: %v", err)
	}
}

func (s *Scanner) dnsWorks(ctx context.Context) bool {
	c, cancel := context.WithTimeout(ctx, s.Timeout*2)
	defer cancel()
	_, err := s.Resolver.LookupHost(c, s.Canary)
	return err == nil
}

func (s *Scanner) scanAll(ctx context.Context) (int, bool, error) {
	recs, _, err := s.CF.List(ctx, true)
	if err != nil {
		return 0, s.Status().DNSOK, err
	}
	meta, err := s.Store.AllMeta(ctx)
	if err != nil {
		return 0, true, err
	}
	dnsOK := s.dnsWorks(ctx)
	ports := s.Ports(ctx)
	keep := map[string]bool{}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	n := 0
	for _, r := range recs {
		if !Eligible(r, meta[r.ID]) {
			continue
		}
		keep[r.ID] = true
		n++
		wg.Add(1)
		sem <- struct{}{}
		go func(r model.Record) {
			defer wg.Done()
			defer func() { <-sem }()
			s.scanAndStore(ctx, r, ports, dnsOK)
		}(r)
	}
	wg.Wait()
	if err := s.Store.PruneScans(ctx, keep); err != nil {
		return n, dnsOK, err
	}
	return n, dnsOK, nil
}

// ScanSoon scans one record in the background.
func (s *Scanner) ScanSoon(r model.Record) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m, _ := s.Store.Meta(ctx, r.ID)
		if !Eligible(r, m) {
			return
		}
		s.scanAndStore(ctx, r, s.Ports(ctx), s.dnsWorks(ctx))
	}()
}

func (s *Scanner) scanAndStore(ctx context.Context, r model.Record, ports []int, dnsOK bool) {
	res := s.Scan(ctx, r, ports, dnsOK)
	if err := s.Store.PutScan(ctx, r.ID, float64(time.Now().Unix()), res.Reachable, res.OpenPorts, res.Detail, res.DNSStatus, res.DNSDetail); err != nil {
		log.Printf("scanner: store %s: %v", r.Name, err)
	}
}

// Result is the outcome of scanning one record.
type Result struct {
	Reachable bool
	OpenPorts string
	Detail    string
	DNSStatus string
	DNSDetail string
}

// Scan probes one record.
func (s *Scanner) Scan(ctx context.Context, r model.Record, ports []int, dnsOK bool) Result {
	var res Result
	var addrs []string
	switch r.Type {
	case "CNAME":
		target := dnsrec.NormalizeTarget(r.Content)
		c, cancel := context.WithTimeout(ctx, s.Timeout*2)
		got, err := s.Resolver.LookupHost(c, target)
		cancel()
		var de *net.DNSError
		switch {
		case err == nil:
			addrs = got
			res.DNSStatus = "ok"
			shown := got
			if len(shown) > 3 {
				shown = shown[:3]
			}
			res.DNSDetail = "解析為 " + strings.Join(shown, "、")
		case errors.As(err, &de) && de.IsNotFound && dnsOK:
			res.DNSStatus = "nxdomain"
			res.DNSDetail = target + "：查無網域"
		default:
			res.DNSStatus = "unknown"
			res.DNSDetail = target + "：無法解析"
		}
		if res.DNSStatus == "ok" {
			if tk := TakeoverFor(target); tk != nil && s.unclaimed(ctx, r.Name, tk) {
				res.DNSStatus = "unclaimed"
				res.DNSDetail = tk.Service + "：查無對應站台"
			}
		}
	case "A", "AAAA":
		addrs = []string{r.Content}
	}
	if r.Proxied {
		s.probeHTTP(ctx, r.Name, &res)
		return res
	}
	if len(addrs) == 0 {
		res.Detail = res.DNSDetail
		return res
	}
	s.probeTCP(ctx, addrs[0], ports, &res)
	return res
}

func (s *Scanner) url(name, scheme string) string {
	if s.HTTPBase != nil {
		return s.HTTPBase(name, scheme)
	}
	return scheme + "://" + name + "/"
}

func (s *Scanner) fetch(ctx context.Context, u string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "sdc-portal/1.0 (+record scan)")
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return resp.StatusCode, string(b), nil
}

func (s *Scanner) unclaimed(ctx context.Context, name string, tk *Takeover) bool {
	for _, scheme := range []string{"https", "http"} {
		_, body, err := s.fetch(ctx, s.url(name, scheme))
		if err != nil {
			continue
		}
		return strings.Contains(body, tk.Fingerprint)
	}
	return false
}

func (s *Scanner) probeHTTP(ctx context.Context, name string, res *Result) {
	code, _, err := s.fetch(ctx, s.url(name, "https"))
	switch {
	case err != nil:
		res.Detail = "經 Cloudflare：無法連線"
	case downCodes[code]:
		res.Detail = fmt.Sprintf("來源主機無法連線（HTTP %d）", code)
	default:
		res.Reachable = true
		res.Detail = fmt.Sprintf("經 Cloudflare：HTTP %d", code)
	}
}

func joinPorts(ps []int, sep string) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, sep)
}

func (s *Scanner) probeTCP(ctx context.Context, addr string, ports []int, res *Result) {
	if a, err := netip.ParseAddr(addr); err == nil {
		addr = a.Unmap().String()
	}
	open := make([]bool, len(ports))
	var wg sync.WaitGroup
	for i, p := range ports {
		wg.Add(1)
		go func(i, p int) {
			defer wg.Done()
			d := net.Dialer{Timeout: s.Timeout}
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(p)))
			if err == nil {
				c.Close()
				open[i] = true
			}
		}(i, p)
	}
	wg.Wait()
	var openPorts []int
	for i, ok := range open {
		if ok {
			openPorts = append(openPorts, ports[i])
		}
	}
	if len(openPorts) > 0 {
		res.Reachable = true
		res.OpenPorts = joinPorts(openPorts, ",")
		res.Detail = fmt.Sprintf("%s：埠 %s 有回應", addr, joinPorts(openPorts, "、"))
	} else {
		res.Detail = fmt.Sprintf("%s：埠 %s 無回應", addr, joinPorts(ports, "、"))
	}
}

// Run scans on the configured interval until ctx is done.
func (s *Scanner) Run(ctx context.Context) {
	s.RunOnce(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunOnce(ctx)
		}
	}
}
