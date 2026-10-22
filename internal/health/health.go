// Package health checks the services listed on the portal.
package health

import (
	"context"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/httpclient"
	"github.com/nycu-sdc/sdc-portal/internal/loop"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/store"
)

const (
	retention   = 25 * time.Hour
	bucketSize  = 30 * time.Minute
	bucketCount = 48
	concurrency = 10
)

// Checker runs HTTP checks against listings and keeps their summaries.
type Checker struct {
	Store    *store.Store
	Interval time.Duration
	client   *http.Client
	// cache holds the summaries computed after the latest checks; public
	// pages read it instead of scanning a day of check rows per request.
	cache     atomic.Pointer[summaries]
	refreshMu sync.Mutex
}

type summaries struct {
	at   time.Time
	byID map[string]Summary
}

// New returns a checker.
func New(st *store.Store, interval, timeout time.Duration, verifyTLS bool) *Checker {
	return &Checker{Store: st, Interval: interval, client: httpclient.New(timeout, verifyTLS, httpclient.LimitRedirects)}
}

// Check performs one check of a listing.
func (c *Checker) Check(ctx context.Context, l model.Listing) store.Check {
	start := time.Now()
	res := store.Check{RecordID: l.RecordID, TS: float64(start.UnixNano()) / 1e9}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.EffectiveURL(), nil)
	if err == nil {
		req.Header.Set("User-Agent", "sdc-portal/1.0 (+status check)")
		var resp *http.Response
		resp, err = c.client.Do(req)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			res.Status = resp.StatusCode
			res.OK = resp.StatusCode < 500
		}
	}
	res.MS = int(time.Since(start).Milliseconds())
	return res
}

// record stores results in one transaction and refreshes the summaries.
func (c *Checker) record(ctx context.Context, checks []store.Check) {
	if err := c.Store.AddChecks(ctx, checks, float64(time.Now().Add(-retention).Unix())); err != nil {
		log.Printf("health: store checks: %v", err)
	}
	if err := c.refresh(ctx, time.Now(), true); err != nil {
		log.Printf("health: summarize: %v", err)
	}
}

// RunOnce checks all listings.
func (c *Checker) RunOnce(ctx context.Context) {
	ls, err := c.Store.Listings(ctx)
	if err != nil {
		log.Printf("health: %v", err)
		return
	}
	results := make([]store.Check, len(ls))
	loop.ForEach(len(ls), concurrency, func(i int) { results[i] = c.Check(ctx, ls[i]) })
	c.record(ctx, results)
}

// CheckSoon checks one listing in the background.
func (c *Checker) CheckSoon(l model.Listing) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.record(ctx, []store.Check{c.Check(ctx, l)})
	}()
}

// Run checks on the configured interval until ctx is done.
func (c *Checker) Run(ctx context.Context) { loop.Every(ctx, c.Interval, c.RunOnce) }

// Summary is the status of one service.
type Summary struct {
	Last        *store.Check `json:"last"`
	Uptime24    *float64     `json:"uptime_24h"`
	Buckets     []*float64   `json:"buckets"`
	BucketStart float64      `json:"bucket_start"`
	BucketSize  int          `json:"bucket_size"`
}

// refresh recomputes the summaries from aggregated checks, one at a time.
// force is for new check results; otherwise a cache already in now's
// bucket (e.g. built while this caller waited for the lock) is kept.
func (c *Checker) refresh(ctx context.Context, now time.Time, force bool) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if cur := c.cache.Load(); cur != nil && !force && bucketStart(cur.at).Equal(bucketStart(now)) {
		return nil
	}
	start := bucketStart(now)
	stats, err := c.Store.CheckStats(ctx, float64(start.Unix()), bucketSize.Seconds(), float64(now.Add(-24*time.Hour).Unix()))
	if err != nil {
		return err
	}
	byID := make(map[string]Summary, len(stats))
	for id, st := range stats {
		byID[id] = summaryOf(st, start)
	}
	c.cache.Store(&summaries{at: now, byID: byID})
	return nil
}

// Summaries returns the cached summaries of the given listings (all
// records with checks if nil). The cache is rebuilt after every check run,
// and when the 30-minute buckets have moved on since.
func (c *Checker) Summaries(ctx context.Context, now time.Time, ls []model.Listing) (map[string]Summary, error) {
	cur := c.cache.Load()
	if cur == nil || !bucketStart(now).Equal(bucketStart(cur.at)) {
		if err := c.refresh(ctx, now, false); err != nil {
			return nil, err
		}
		cur = c.cache.Load()
	}
	if ls == nil {
		return cur.byID, nil
	}
	out := make(map[string]Summary, len(ls))
	for _, l := range ls {
		sum, ok := cur.byID[l.RecordID]
		if !ok {
			sum = summaryOf(nil, bucketStart(cur.at)) // not checked yet
		}
		out[l.RecordID] = sum
	}
	return out, nil
}

// bucketStart is the start of the oldest of the 48 buckets ending at now.
func bucketStart(now time.Time) time.Time {
	return now.Truncate(bucketSize).Add(-(bucketCount - 1) * bucketSize)
}

// summaryOf turns aggregated checks (nil: none) into a summary.
func summaryOf(st *store.CheckStat, start time.Time) Summary {
	s := Summary{Buckets: make([]*float64, bucketCount), BucketStart: float64(start.Unix()), BucketSize: int(bucketSize.Seconds())}
	if st == nil {
		return s
	}
	for i, b := range st.Buckets {
		if i >= 0 && i < bucketCount && b[1] > 0 {
			v := ratio(b[0], b[1])
			s.Buckets[i] = &v
		}
	}
	if st.UpTotal > 0 {
		v := ratio(st.UpOK, st.UpTotal)
		s.Uptime24 = &v
	}
	s.Last = st.Last
	return s
}

// ratio is ok/total rounded to 3 decimals, enough for the status bars and
// much shorter in JSON.
func ratio(ok, total int) float64 {
	return math.Round(float64(ok)/float64(total)*1000) / 1000
}
