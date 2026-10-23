package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seanyeh77/cf-portal/internal/model"
	"github.com/seanyeh77/cf-portal/testdata/mockcf"
)

func TestClientAgainstMock(t *testing.T) {
	m := mockcf.New("tok")
	srv := httptest.NewServer(m)
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL+"/client/v4", "tok", mockcf.Zone, "", "")
	recs, _, err := c.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(mockcf.Seed()) {
		t.Fatalf("paging lost records: %d", len(recs))
	}
	if id, _ := c.ZoneID(ctx); id != mockcf.ZoneID {
		t.Fatal("zone id lookup")
	}
	rec, err := c.Create(ctx, model.RecordInput{Type: "A", Name: "new.example.org", Content: "10.9.9.9", TTL: 1})
	if err != nil || rec.ID == "" {
		t.Fatal(err)
	}
	_, err = c.Create(ctx, model.RecordInput{Type: "CNAME", Name: "new.example.org", Content: "x.example.com", TTL: 1})
	var ce *Error
	if !errors.As(err, &ce) || ce.HTTPStatus() != http.StatusBadRequest || ce.Message != "Cloudflare: 81053: An A, AAAA, or CNAME record with that host already exists." {
		t.Fatalf("duplicate CNAME error: %v", err)
	}
	if _, err := c.Update(ctx, rec.ID, model.RecordInput{Type: "A", Name: "new.example.org", Content: "10.9.9.8", TTL: 1, Proxied: true}); err == nil {
		t.Fatal("private IP proxied must fail")
	}
	got, err := c.Update(ctx, rec.ID, model.RecordInput{Type: "A", Name: "new.example.org", Content: "10.9.9.8", TTL: 300})
	if err != nil || got.Content != "10.9.9.8" || got.TTL != 300 {
		t.Fatalf("update: %+v %v", got, err)
	}
	if err := c.Delete(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, rec.ID); !errors.As(err, &ce) || ce.HTTPStatus() != 404 {
		t.Fatalf("get deleted: %v", err)
	}
	bad := New(srv.URL+"/client/v4", "wrong", mockcf.Zone, mockcf.ZoneID, "")
	if _, _, err := bad.List(ctx, true); !errors.As(err, &ce) || ce.HTTPStatus() != 403 {
		t.Fatalf("token check: %v", err)
	}
}

func TestTunnels(t *testing.T) {
	m := mockcf.New("tok")
	srv := httptest.NewServer(m)
	defer srv.Close()
	ctx := context.Background()
	// With a preset zone id the account comes from GET /zones/{id}.
	for _, zid := range []string{"", mockcf.ZoneID} {
		c := New(srv.URL+"/client/v4", "tok", mockcf.Zone, zid, "")
		ts, err := c.Tunnels(ctx)
		if err != nil {
			t.Fatal(zid, err)
		}
		if ts[mockcf.TunnelOK].Name != "home-lab" || ts[mockcf.TunnelOK].Status != "healthy" || !ts[mockcf.TunnelDeleted].Deleted || ts[mockcf.TunnelDown].Deleted {
			t.Fatalf("%s: %+v", zid, ts)
		}
		if _, ok := ts[mockcf.TunnelGone]; ok {
			t.Fatal("gone tunnel listed")
		}
	}
	m.SetTunnelsForbidden(true)
	c := New(srv.URL+"/client/v4", "tok", mockcf.Zone, "", "")
	_, err := c.Tunnels(ctx)
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != http.StatusForbidden {
		t.Fatalf("forbidden: %v", err)
	}
	// The failure is cached rather than retried on every call.
	m.SetTunnelsForbidden(false)
	if _, err := c.Tunnels(ctx); err == nil {
		t.Fatal("cached failure expected")
	}
	if _, err := New(srv.URL+"/client/v4", "tok", mockcf.Zone, "", "wrong-account").Tunnels(ctx); err == nil {
		t.Fatal("CF_ACCOUNT_ID must take precedence")
	}
}
