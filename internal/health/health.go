// Package health checks the services listed on the portal.
package health

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/assets"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/store"
)

const (
	retention   = 25 * time.Hour
	bucketSize  = 30 * time.Minute
	bucketCount = 48
	concurrency = 10
)

// Checker runs HTTP checks against listings.
type Checker struct {
	Store    *store.Store
	Interval time.Duration
	client   *http.Client
}

// New returns a checker.
func New(st *store.Store, interval, timeout time.Duration, verifyTLS bool) *Checker {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: !verifyTLS} //nolint:gosec // configurable for self-signed hosts
	return &Checker{Store: st, Interval: interval, client: &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: assets.LimitRedirects}}
}

// Check performs one check and stores the result.
func (c *Checker) Check(ctx context.Context, l model.Listing) store.Check {
	start := time.Now()
	res := store.Check{TS: float64(start.UnixNano()) / 1e9}
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
	if err := c.Store.AddCheck(ctx, l.RecordID, res); err != nil {
		log.Printf("health: store check: %v", err)
	}
	return res
}

// RunOnce checks all listings.
func (c *Checker) RunOnce(ctx context.Context) {
	ls, err := c.Store.Listings(ctx)
	if err != nil {
		log.Printf("health: %v", err)
		return
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, l := range ls {
		wg.Add(1)
		sem <- struct{}{}
		go func(l model.Listing) {
			defer wg.Done()
			defer func() { <-sem }()
			c.Check(ctx, l)
		}(l)
	}
	wg.Wait()
	c.Store.PruneChecks(ctx, float64(time.Now().Add(-retention).Unix()))
}

// CheckSoon checks one listing in the background.
func (c *Checker) CheckSoon(l model.Listing) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.Check(ctx, l)
	}()
}

// Run loops until ctx is done.
func (c *Checker) Run(ctx context.Context) {
	c.RunOnce(ctx)
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.RunOnce(ctx)
		}
	}
}

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

// Summaries summarizes checks for all listings.
func (c *Checker) Summaries(ctx context.Context, now time.Time) (map[string]Summary, error) {
	start := now.Truncate(bucketSize).Add(-(bucketCount - 1) * bucketSize)
	from := now.Add(-24 * time.Hour)
	if start.Before(from) {
		from = start
	}
	all, err := c.Store.ChecksSince(ctx, float64(from.Unix()))
	if err != nil {
		return nil, err
	}
	ls, err := c.Store.Listings(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Summary{}
	for _, l := range ls {
		out[l.RecordID] = Summarize(all[l.RecordID], now)
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
			v := float64(okN[i]) / float64(total[i])
			s.Buckets[i] = &v
		}
	}
	if upTotal > 0 {
		v := float64(upOK) / float64(upTotal)
		s.Uptime24 = &v
	}
	if n := len(checks); n > 0 {
		l := checks[n-1]
		s.Last = &LastCheck{TS: l.TS, OK: l.OK, MS: l.MS, Status: l.Status}
	}
	return s
}
