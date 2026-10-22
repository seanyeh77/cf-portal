package scanner

import (
	"context"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/cloudflare"
	"github.com/nycu-sdc/sdc-portal/internal/loop"
	"github.com/nycu-sdc/sdc-portal/internal/model"
	"github.com/nycu-sdc/sdc-portal/internal/snapshots"
	"github.com/nycu-sdc/sdc-portal/internal/store"
)

// Maint runs the periodic maintenance jobs.
type Maint struct {
	CF       *cloudflare.Client
	Store    *store.Store
	Snaps    *snapshots.Store
	Loc      *time.Location
	Keep     int
	Interval time.Duration
	// Forget removes local data (listing, icon, meta, scan) of a deleted record.
	Forget func(ctx context.Context, id string)
	// RetryIcons re-fetches missing automatic icons.
	RetryIcons func(ctx context.Context)

	lastIconRetry time.Time
}

// Snapshot saves the current zone and returns the records it saved.
func (m *Maint) Snapshot(ctx context.Context, source string) (snapshots.Info, []model.Record, error) {
	recs, _, err := m.CF.List(ctx, true)
	if err != nil {
		return snapshots.Info{}, nil, err
	}
	meta, err := m.Store.AllMeta(ctx)
	if err != nil {
		return snapshots.Info{}, nil, err
	}
	info, err := m.Snaps.Create(m.CF.ZoneName(), source, snapshots.FromRecords(recs, meta), time.Now())
	if err != nil {
		return info, nil, err
	}
	if source == "auto" {
		m.Snaps.Prune(m.Keep)
	}
	return info, recs, nil
}

// AutoDelete deletes records whose expiry passed and that are marked for
// automatic deletion. It returns the number deleted.
func (m *Maint) AutoDelete(ctx context.Context, now time.Time) (int, error) {
	today := now.In(m.Loc).Format("2006-01-02")
	meta, err := m.Store.AllMeta(ctx)
	if err != nil {
		return 0, err
	}
	due := func(md model.Meta) bool { return md.AutoDelete && md.ExpiresAt != "" && md.ExpiresAt < today }
	if !slices.ContainsFunc(slices.Collect(maps.Values(meta)), due) {
		return 0, nil // nothing can expire: skip listing the zone
	}
	recs, _, err := m.CF.List(ctx, true)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range recs {
		md, ok := meta[r.ID]
		if !ok || !due(md) {
			continue
		}
		if err := m.CF.Delete(ctx, r.ID); err != nil {
			log.Printf("maint: auto delete %s: %v", r.Name, err)
			continue
		}
		detail, _ := json.Marshal(map[string]any{"record_id": r.ID, "before": r.Input(), "meta": md})
		m.Store.AddAudit(ctx, "system", "record_auto_delete", r.Name, string(detail))
		if m.Forget != nil {
			m.Forget(ctx, r.ID)
		}
		n++
	}
	return n, nil
}

// RunOnce performs one maintenance round.
func (m *Maint) RunOnce(ctx context.Context) {
	now := time.Now()
	if !m.Snaps.HasDay(now) {
		if _, _, err := m.Snapshot(ctx, "auto"); err != nil {
			log.Printf("maint: snapshot: %v", err)
		}
	}
	if _, err := m.AutoDelete(ctx, now); err != nil {
		log.Printf("maint: auto delete: %v", err)
	}
	if m.RetryIcons != nil && now.Sub(m.lastIconRetry) >= 6*time.Hour {
		m.lastIconRetry = now
		m.RetryIcons(ctx)
	}
}

// Run repeats RunOnce on the configured interval until ctx is done.
func (m *Maint) Run(ctx context.Context) { loop.Every(ctx, m.Interval, m.RunOnce) }
