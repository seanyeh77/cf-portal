package assets

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nycu-sdc/sdc-portal/testdata/mockcf"
)

func TestSniff(t *testing.T) {
	cases := map[string][]byte{
		"png":  mockcf.PNG1,
		"jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 0, 0},
		"gif":  []byte("GIF89a\x01\x00"),
		"webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"ico":  mockcf.ICO,
		"svg":  []byte(`<?xml version="1.0"?><!-- x --><svg xmlns="http://www.w3.org/2000/svg"/>`),
		"":     []byte("<html><body><svg></svg></body></html>"),
	}
	for want, b := range cases {
		if got := Sniff(b); got != want {
			t.Errorf("Sniff(%q) = %q, want %q", b[:min(len(b), 12)], got, want)
		}
	}
	if Sniff([]byte("hello")) != "" || Sniff([]byte("<svg\x00")) != "" {
		t.Error("non-images must not pass")
	}
	if Sniff([]byte("\xEF\xBB\xBF  <svg viewBox='0 0 1 1'></svg>")) != "svg" {
		t.Error("svg with BOM")
	}
}

func TestNameRe(t *testing.T) {
	good := []string{"logo-123.png", "si-github.svg", "a_b.jpg"}
	bad := []string{"../etc/passwd", "a/b.png", "x.html", "x.png.html", ".png", "a b.png", "..%2fx.png", "x.PNG"}
	for _, n := range good {
		if !NameRe.MatchString(n) {
			t.Errorf("%s should be allowed", n)
		}
	}
	for _, n := range bad {
		if NameRe.MatchString(n) {
			t.Errorf("%s should be rejected", n)
		}
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "x", []byte("<script>alert(1)</script>"), nil); err == nil {
		t.Fatal("html accepted")
	}
	if _, err := Save(dir, "hero", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), map[string]bool{"png": true}); err == nil {
		t.Fatal("svg accepted where not allowed")
	}
	name, err := Save(dir, "../../evil", mockcf.PNG1, nil)
	if err != nil || strings.Contains(name, "/") || !NameRe.MatchString(name) {
		t.Fatalf("save: %q %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func TestIconCandidates(t *testing.T) {
	base, _ := url.Parse("https://svc.example.com/app/")
	html := `<link rel="icon" href="/f16.png" sizes="16x16"><link rel="mask-icon" href="/m.svg">
	<link rel="shortcut icon" href="favicon.ico"><LINK REL="apple-touch-icon" HREF="/apple.png">
	<link rel="icon" type="image/svg+xml" href="/logo.svg"><link rel="icon" href="/f64.png" sizes="64x64">
	<link rel="icon" href="http://10.0.0.1/admin.png" sizes="512x512"><link rel="icon" href="javascript:alert(1)">`
	got := IconCandidates(html, base)
	want := []string{"https://svc.example.com/logo.svg", "https://svc.example.com/apple.png", "https://svc.example.com/f64.png",
		"https://svc.example.com/app/favicon.ico", "https://svc.example.com/f16.png", "https://svc.example.com/apple-touch-icon.png", "https://svc.example.com/favicon.ico"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(mockcf.New("x"))
	defer srv.Close()
	dir := t.TempDir()
	f := NewFetcher(dir, srv.URL+"/si", 5*time.Second, true)
	name, err := f.FetchAuto(context.Background(), srv.URL+"/svc/gitlab/", "auto-x")
	if err != nil || !strings.HasSuffix(name, ".png") {
		t.Fatalf("auto: %q %v", name, err)
	}
	if n, err := f.FetchSimpleIcon(context.Background(), "github"); err != nil || n != "si-github.svg" {
		t.Fatalf("si: %q %v", n, err)
	}
	if _, err := f.FetchSimpleIcon(context.Background(), "missing"); err == nil || err.Error() != "Simple Icons 查無 missing，暫以文字圖示顯示" {
		t.Fatalf("si missing: %v", err)
	}
	if _, err := f.FetchSimpleIcon(context.Background(), "../x"); err == nil {
		t.Fatal("bad slug accepted")
	}
}
