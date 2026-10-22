// Package loop runs periodic background jobs.
package loop

import (
	"context"
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
