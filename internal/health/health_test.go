package health

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/store"
	"github.com/nycu-sdc/sdc-portal/testdata/mockcf"
)

func TestSummaries(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 14, 10, 0, 0, time.UTC)
	ts := func(d time.Duration) float64 { return float64(now.Add(d).Unix()) }
	st.AddChecks(ctx, []store.Check{
		{RecordID: "a", TS: ts(-30 * time.Hour), OK: false},
		{RecordID: "a", TS: ts(-35 * time.Minute), OK: true, MS: 10},
		{RecordID: "a", TS: ts(-40 * time.Minute), OK: false},
		{RecordID: "a", TS: ts(-5 * time.Minute), OK: true, MS: 42, Status: 200},
	}, 0)
	c := New(st, time.Minute, time.Second, true)
	sums, err := c.Summaries(ctx, now, []model.Listing{{RecordID: "a"}, {RecordID: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	s := sums["a"]
	if s.Last == nil || s.Last.MS != 42 || !s.Last.OK || s.Last.Status != 200 {
		t.Fatalf("last %+v", s.Last)
	}
	if s.Uptime24 == nil || *s.Uptime24 != 0.667 {
		t.Fatalf("uptime %v", *s.Uptime24)
	}
	if len(s.Buckets) != 48 || s.Buckets[47] == nil || *s.Buckets[47] != 1 || s.Buckets[46] == nil || *s.Buckets[46] != 0.5 || s.Buckets[0] != nil {
		t.Fatalf("buckets %v", s.Buckets[45:])
	}
	if time.Unix(int64(s.BucketStart), 0).UTC().Format("15:04") != "14:30" {
		t.Fatalf("start %v", s.BucketStart)
	}
	// A listing without checks gets a well-formed empty summary.
	if n := sums["new"]; n.Last != nil || len(n.Buckets) != 48 || n.BucketSize != 1800 || n.BucketStart != s.BucketStart {
		t.Fatalf("unchecked listing %+v", n)
	}
}

func TestCheck(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mockcf.New("x"))
	defer srv.Close()
	c := New(st, time.Minute, 5*time.Second, true)
	up, down := srv.URL+"/svc/gitlab/", srv.URL+"/svc/down/"
	ctx := context.Background()
	st.PutListing(ctx, model.Listing{RecordID: "a", Hostname: "a", Title: "A", URL: &up, Icon: "none"})
	st.PutListing(ctx, model.Listing{RecordID: "b", Hostname: "b", Title: "B", URL: &down, Icon: "none"})
	c.RunOnce(ctx)
	sum, err := c.Summaries(ctx, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sum["a"].Last.OK || sum["a"].Last.Status != 200 || sum["b"].Last.OK || sum["b"].Last.Status != 503 {
		t.Fatalf("%+v %+v", sum["a"].Last, sum["b"].Last)
	}
}
