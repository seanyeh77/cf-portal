package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/testdata/mockcf"
)

func TestClientAgainstMock(t *testing.T) {
	m := mockcf.New("tok")
	srv := httptest.NewServer(m)
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL+"/client/v4", "tok", mockcf.Zone, "")
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
	rec, err := c.Create(ctx, model.RecordInput{Type: "A", Name: "new.sdc.nycu.club", Content: "10.9.9.9", TTL: 1})
	if err != nil || rec.ID == "" {
		t.Fatal(err)
	}
	_, err = c.Create(ctx, model.RecordInput{Type: "CNAME", Name: "new.sdc.nycu.club", Content: "x.example.com", TTL: 1})
	var ce *Error
	if !errors.As(err, &ce) || ce.HTTPStatus() != http.StatusBadRequest || ce.Message != "Cloudflare: 81053: An A, AAAA, or CNAME record with that host already exists." {
		t.Fatalf("duplicate CNAME error: %v", err)
	}
	if _, err := c.Update(ctx, rec.ID, model.RecordInput{Type: "A", Name: "new.sdc.nycu.club", Content: "10.9.9.8", TTL: 1, Proxied: true}); err == nil {
		t.Fatal("private IP proxied must fail")
	}
	got, err := c.Update(ctx, rec.ID, model.RecordInput{Type: "A", Name: "new.sdc.nycu.club", Content: "10.9.9.8", TTL: 300})
	if err != nil || got.Content != "10.9.9.8" || got.TTL != 300 {
		t.Fatalf("update: %+v %v", got, err)
	}
	if err := c.Delete(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, rec.ID); !errors.As(err, &ce) || ce.HTTPStatus() != 404 {
		t.Fatalf("get deleted: %v", err)
	}
	bad := New(srv.URL+"/client/v4", "wrong", mockcf.Zone, mockcf.ZoneID)
	if _, _, err := bad.List(ctx, true); !errors.As(err, &ce) || ce.HTTPStatus() != 403 {
		t.Fatalf("token check: %v", err)
	}
}
