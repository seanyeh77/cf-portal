// Package loop runs periodic background jobs.
package loop

import (
	"context"
	"sync"
	"time"
)

// Every runs fn now and then every d until ctx is done.
func Every(ctx context.Context, d time.Duration, fn func(context.Context)) {
	fn(ctx)
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// ForEach runs fn(0..n-1) with at most limit calls at once and waits.
func ForEach(n, limit int, fn func(i int)) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(i)
		})
	}
	wg.Wait()
}
