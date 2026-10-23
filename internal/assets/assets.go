// Package assets validates images and fetches service icons.
package assets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seanyeh77/cf-portal/internal/httpclient"
)

// NameRe matches the file names this system serves.
var NameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+\.(png|jpg|gif|webp|ico|svg)$`)

// ContentTypes maps extensions to media types.
var ContentTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
	"ico": "image/x-icon", "svg": "image/svg+xml",
}

// Sniff returns the extension of an image judged from its first bytes, or "".
// The file name and Content-Type are never consulted.
func Sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "webp"
	case len(b) >= 6 && bytes.Equal(b[0:4], []byte{0, 0, 1, 0}) && b[4]+b[5] > 0:
		return "ico"
	}
	if isSVG(b) {
		return "svg"
	}
	return ""
}

var (
	svgProlog  = regexp.MustCompile(`(?s)<\?xml.*?\?>|<!--.*?-->|<!doctype[^>]*>`)
	unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

func isSVG(b []byte) bool {
	head := b
	if len(head) > 4096 {
		head = head[:4096]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	t := bytes.TrimPrefix(head, []byte("\xEF\xBB\xBF"))
	t = bytes.TrimLeft(t, " \t\r\n")
	if !bytes.HasPrefix(t, []byte("<")) {
		return false
	}
	low := bytes.ToLower(t)
	idx := bytes.Index(low, []byte("<svg"))
	if idx < 0 {
		return false
	}
	// Only a prolog (xml declaration, comments, doctype) may precede <svg.
	pre := low[:idx]
	pre = svgProlog.ReplaceAll(pre, nil)
	return len(bytes.TrimSpace(pre)) == 0
}

// Save validates data and writes it to dir as prefix-<ts>.<ext>. allowed
// restricts the accepted formats (nil allows all).
func Save(dir, prefix string, data []byte, allowed map[string]bool) (string, error) {
	ext := Sniff(data)
	if ext == "" {
		return "", errors.New("不支援的圖片格式（僅接受 PNG、JPEG、GIF、WebP、ICO、SVG）")
	}
	if allowed != nil && !allowed[ext] {
		return "", fmt.Errorf("不接受 %s 格式", strings.ToUpper(ext))
	}
	safe := unsafeName.ReplaceAllString(prefix, "")
	name := fmt.Sprintf("%s-%d.%s", safe, time.Now().UnixMilli(), ext)
	if !NameRe.MatchString(name) {
		return "", errors.New("invalid file name")
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o640); err != nil {
		return "", err
	}
	return name, nil
}

// Remove deletes a file previously written by Save; invalid names are ignored.
func Remove(dir, name string) {
	if NameRe.MatchString(name) {
		os.Remove(filepath.Join(dir, name))
	}
}

// Fetcher downloads icons.
type Fetcher struct {
	Client          *http.Client
	SimpleIconsBase string
	Dir             string
}

// NewFetcher returns a fetcher with the given timeout and TLS policy.
func NewFetcher(dir, siBase string, timeout time.Duration, verifyTLS bool) *Fetcher {
	return &Fetcher{Dir: dir, SimpleIconsBase: siBase, Client: httpclient.New(timeout, verifyTLS, httpclient.LimitRedirects)}
}

const maxIcon = 512 << 10

func (f *Fetcher) get(ctx context.Context, u string, limit int64) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "cf-portal/1.0 (+icon fetch)")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > limit {
		return nil, nil, errors.New("too large")
	}
	return b, resp.Request.URL, nil
}

var (
	linkRe = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrRe = regexp.MustCompile(`(?is)([a-z-]+)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
)

type candidate struct {
	href string
	size int
}

// IconCandidates extracts icon links from HTML, best first.
func IconCandidates(html string, base *url.URL) []string {
	var cands []candidate
	for _, tag := range linkRe.FindAllString(html, -1) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = m[3] + m[4] + m[5]
		}
		rel := strings.ToLower(attrs["rel"])
		if !strings.Contains(rel, "icon") || strings.Contains(rel, "mask-icon") || attrs["href"] == "" {
			continue
		}
		ref, err := url.Parse(strings.TrimSpace(attrs["href"]))
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		// Only the page's own host: third-party HTML must not steer
		// requests to other (possibly internal) hosts.
		if (abs.Scheme != "http" && abs.Scheme != "https") || !strings.EqualFold(abs.Hostname(), base.Hostname()) {
			continue
		}
		size := 32
		sizes := strings.ToLower(attrs["sizes"])
		switch {
		case strings.Contains(rel, "apple-touch-icon"):
			size = 180
		case sizes == "any" || strings.HasSuffix(strings.ToLower(abs.Path), ".svg"):
			size = 512
		case strings.Contains(sizes, "x"):
			if w, err := strconv.Atoi(strings.SplitN(strings.Fields(sizes)[0], "x", 2)[0]); err == nil {
				size = w
			}
		}
		cands = append(cands, candidate{abs.String(), size})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].size > cands[j].size })
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		if !seen[c.href] {
			seen[c.href] = true
			out = append(out, c.href)
		}
	}
	for _, p := range []string{"/apple-touch-icon.png", "/favicon.ico"} {
		u := base.ResolveReference(&url.URL{Path: p}).String()
		if !seen[u] {
			out = append(out, u)
		}
	}
	return out
}

// FetchAuto finds the best icon for pageURL and saves it. It returns the
// saved file name.
func (f *Fetcher) FetchAuto(ctx context.Context, pageURL, prefix string) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", err
	}
	var cands []string
	if body, final, err := f.get(ctx, pageURL, 2<<20); err == nil {
		cands = IconCandidates(string(body), final)
	} else {
		cands = IconCandidates("", base)
	}
	for _, c := range cands {
		data, _, err := f.get(ctx, c, maxIcon)
		if err != nil || Sniff(data) == "" {
			continue
		}
		return Save(f.Dir, prefix, data, nil)
	}
	return "", errors.New("找不到可用的圖示")
}

// SlugRe validates Simple Icons slugs.
var SlugRe = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// FetchSimpleIcon downloads a Simple Icons SVG once and returns its file name.
func (f *Fetcher) FetchSimpleIcon(ctx context.Context, slug string) (string, error) {
	if !SlugRe.MatchString(slug) {
		return "", fmt.Errorf("Simple Icons 名稱格式不符：%s", slug)
	}
	name := "si-" + slug + ".svg"
	path := filepath.Join(f.Dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, nil
	}
	data, _, err := f.get(ctx, f.SimpleIconsBase+"/"+slug+".svg", maxIcon)
	if err != nil || Sniff(data) != "svg" {
		return "", fmt.Errorf("Simple Icons 查無 %s，暫以文字圖示顯示", slug)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return "", err
	}
	return name, nil
}
