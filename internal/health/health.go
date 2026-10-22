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
	cache atomic.Pointer[summaries]
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
	if err := c.refresh(ctx, time.Now()); err != nil {
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
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, l := range ls {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			results[i] = c.Check(ctx, l)
		})
	}
	wg.Wait()
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
	Last        *LastCheck `json:"last"`
	Uptime24    *float64   `json:"uptime_24h"`
	Buckets     []*float64 `json:"buckets"`
	BucketStart float64    `json:"bucket_start"`
	BucketSize  int        `json:"bucket_size"`
}

// LastCheck is the latest check.
type LastCheck struct {
	TS     float64 `json:"ts"`
	OK     bool    `json:"ok"`
	MS     int     `json:"ms"`
	Status int     `json:"status"`
}

// refresh recomputes the summaries of all listings from the stored checks.
func (c *Checker) refresh(ctx context.Context, now time.Time) error {
	start := now.Truncate(bucketSize).Add(-(bucketCount - 1) * bucketSize)
	from := now.Add(-24 * time.Hour)
	if start.Before(from) {
		from = start
	}
	all, err := c.Store.ChecksSince(ctx, float64(from.Unix()))
	if err != nil {
		return err
	}
	ls, err := c.Store.Listings(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]Summary, len(ls))
	for _, l := range ls {
		byID[l.RecordID] = Summarize(all[l.RecordID], now)
	}
	c.cache.Store(&summaries{at: now, byID: byID})
	return nil
}

// Summaries returns the cached summaries of the given listings (all if
// nil). The cache is rebuilt after every check run, and when the
// 30-minute buckets have moved on since.
func (c *Checker) Summaries(ctx context.Context, now time.Time, ls []model.Listing) (map[string]Summary, error) {
	cur := c.cache.Load()
	if cur == nil || !now.Truncate(bucketSize).Equal(cur.at.Truncate(bucketSize)) {
		if err := c.refresh(ctx, now); err != nil {
			return nil, err
		}
		cur = c.cache.Load()
	}
	if ls == nil {
		return cur.byID, nil
	}
	out := make(map[string]Summary, len(ls))
	for _, l := range ls {
		out[l.RecordID] = cur.byID[l.RecordID]
	}
	return out, nil
}

// Summarize builds a summary from checks (oldest first).
func Summarize(checks []store.Check, now time.Time) Summary {
	start := now.Truncate(bucketSize).Add(-(bucketCount - 1) * bucketSize)
	s := Summary{Buckets: make([]*float64, bucketCount), BucketStart: float64(start.Unix()), BucketSize: int(bucketSize.Seconds())}
	var okN, total [bucketCount]int
	day0 := float64(now.Add(-24 * time.Hour).Unix())
	upOK, upTotal := 0, 0
	for _, c := range checks {
		if c.TS >= day0 {
			upTotal++
			if c.OK {
				upOK++
			}
		}
		i := int((c.TS - s.BucketStart) / bucketSize.Seconds())
		if i >= 0 && i < bucketCount {
			total[i]++
			if c.OK {
				okN[i]++
			}
		}
	}
	for i := range s.Buckets {
		if total[i] > 0 {
			v := ratio(okN[i], total[i])
			s.Buckets[i] = &v
		}
	}
	if upTotal > 0 {
		v := ratio(upOK, upTotal)
		s.Uptime24 = &v
	}
	if n := len(checks); n > 0 {
		l := checks[n-1]
		s.Last = &LastCheck{TS: l.TS, OK: l.OK, MS: l.MS, Status: l.Status}
	}
	return s
}

// ratio is ok/total rounded to 3 decimals, enough for the status bars and
// much shorter in JSON.
func ratio(ok, total int) float64 {
	return math.Round(float64(ok)/float64(total)*1000) / 1000
}
