package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/nycu-sdc/sdc-portal/internal/model"
)

func TestMigrationAddsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// An older schema without the scan detail columns.
	if _, err := db.Exec(`CREATE TABLE record_scan (record_id TEXT PRIMARY KEY, first_ts REAL NOT NULL, last_ts REAL NOT NULL, reachable INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.PutScan(ctx, "r", 10, false, "", "d", "nxdomain", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScan(ctx, "r", 20, true, "443", "ok", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScan(ctx, "r", 30, false, "", "down", "", ""); err != nil {
		t.Fatal(err)
	}
	sc := (func() map[string]model.Scan { m, _ := s.AllScans(ctx); return m })()["r"]
	if sc.FirstTS != 10 || sc.LastTS != 30 || sc.LastOKTS == nil || *sc.LastOKTS != 20 || sc.Detail != "down" {
		t.Fatalf("scan %+v", sc)
	}
}

func TestMetaAndForget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	s.PutMeta(ctx, "a", model.Meta{Owner: "x"})
	s.PutListing(ctx, model.Listing{RecordID: "a", Hostname: "a", Title: "A", Icon: "none"})
	s.SetMute(ctx, "a", "no_owner", true)
	l, err := s.ForgetRecord(ctx, "a")
	if err != nil || l == nil || l.Title != "A" {
		t.Fatalf("forget %v %v", l, err)
	}
	m, _ := s.AllMeta(ctx)
	mu, _ := s.Mutes(ctx)
	if len(m) != 0 || len(mu) != 0 {
		t.Fatal("left over data")
	}
	set, _ := s.Settings(ctx, DefaultSettings("T"))
	if set["title"] != "T" || set["zombie_days"] != "7" {
		t.Fatal("defaults")
	}
}
