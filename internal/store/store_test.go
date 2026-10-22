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
	s.Defaults = DefaultSettings("T")
	set, _ := s.Settings(ctx)
	if set["title"] != "T" || set["zombie_days"] != "7" {
		t.Fatal("defaults")
	}
}

func TestPruneScansKeepsNewerRows(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	s.PutScan(ctx, "kept", 100, true, "", "", "", "")
	s.PutScan(ctx, "stale", 100, true, "", "", "", "")
	s.PutScan(ctx, "new", 200, true, "", "", "", "") // written while the scan ran
	if err := s.PruneScans(ctx, map[string]bool{"kept": true}, 150); err != nil {
		t.Fatal(err)
	}
	scans, _ := s.AllScans(ctx)
	if _, ok := scans["stale"]; ok || len(scans) != 2 {
		t.Fatalf("after prune: %v", scans)
	}
}

func TestRestoreMetaOnlyIfUnchanged(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	old, written := model.Meta{Owner: "old"}, model.Meta{Owner: "new"}
	s.PutMeta(ctx, "a", written)
	if ok, err := s.RestoreMeta(ctx, "a", written, old); !ok || err != nil {
		t.Fatalf("restore of unchanged meta: %v %v", ok, err)
	}
	if m, _ := s.Meta(ctx, "a"); m != old {
		t.Fatalf("not restored: %+v", m)
	}
	s.PutMeta(ctx, "a", model.Meta{Owner: "someone else"})
	if ok, _ := s.RestoreMeta(ctx, "a", written, old); ok {
		t.Fatal("restored over a concurrent change")
	}
	if m, _ := s.Meta(ctx, "a"); m.Owner != "someone else" {
		t.Fatalf("concurrent change overwritten: %+v", m)
	}
	// Restoring to empty meta deletes the row.
	s.PutMeta(ctx, "b", written)
	if ok, _ := s.RestoreMeta(ctx, "b", written, model.Meta{}); !ok {
		t.Fatal("restore to empty")
	}
	if all, _ := s.AllMeta(ctx); len(all) != 1 {
		t.Fatalf("row for b kept: %v", all)
	}
}

func TestAuditIndexesRecordID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	s.AddAudit(ctx, "10.0.0.1", "record_update", "old.sdc.nycu.club", `{"record_id":"r1"}`)
	s.AddAudit(ctx, "10.0.0.1", "login", "admin", `{}`)
	// Found by id even though the record has been renamed since.
	rows, err := s.AuditForRecord(ctx, "r1", "new.sdc.nycu.club")
	if err != nil || len(rows) != 1 || rows[0].Target != "old.sdc.nycu.club" {
		t.Fatalf("history by record id: %v %v", rows, err)
	}
}
