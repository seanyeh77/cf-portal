// Package store holds all SQLite access.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/seanyeh77/cf-portal/internal/model"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database.
type Store struct {
	db  *sql.DB
	Dir string
	// Defaults are the setting values used until a setting is saved.
	Defaults map[string]string
}

const schema = `
CREATE TABLE IF NOT EXISTS listings (
  record_id TEXT PRIMARY KEY,
  hostname TEXT NOT NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  url TEXT,
  sort_order INTEGER NOT NULL DEFAULT 0,
  featured INTEGER NOT NULL DEFAULT 0,
  tags TEXT NOT NULL DEFAULT '',
  icon TEXT NOT NULL DEFAULT 'auto',
  icon_file TEXT,
  created_at REAL, updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS checks (record_id TEXT, ts REAL, ok INTEGER, ms INTEGER, status INTEGER);
DROP INDEX IF EXISTS checks_rec_ts;
CREATE INDEX IF NOT EXISTS checks_rec_cover ON checks(record_id, ts, ok, ms, status);
CREATE INDEX IF NOT EXISTS checks_ts ON checks(ts);
CREATE TABLE IF NOT EXISTS record_meta (
  record_id TEXT PRIMARY KEY,
  owner TEXT NOT NULL DEFAULT '', purpose TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '',
  auto_delete INTEGER NOT NULL DEFAULT 0, no_scan INTEGER NOT NULL DEFAULT 0,
  updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS record_scan (
  record_id TEXT PRIMARY KEY,
  first_ts REAL NOT NULL, last_ts REAL NOT NULL, last_ok_ts REAL,
  reachable INTEGER NOT NULL, open_ports TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '',
  dns_status TEXT NOT NULL DEFAULT '',
  dns_detail TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS issue_mutes (record_id TEXT, kind TEXT, ts REAL, PRIMARY KEY (record_id, kind));
CREATE TABLE IF NOT EXISTS requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT, token TEXT UNIQUE NOT NULL, ts REAL NOT NULL, ip TEXT NOT NULL,
  requester TEXT, contact TEXT, name TEXT, type TEXT, content TEXT, purpose TEXT, expires_at TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  admin_note TEXT NOT NULL DEFAULT '', decided_ts REAL, record_id TEXT
);
CREATE TABLE IF NOT EXISTS audit (id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, ip TEXT, action TEXT, target TEXT, detail TEXT);
`

// columns lists every column, so that databases created by older versions
// gain new columns with ALTER TABLE ADD COLUMN.
var columns = map[string][][2]string{
	"listings": {{"description", "TEXT NOT NULL DEFAULT ''"}, {"category", "TEXT NOT NULL DEFAULT ''"}, {"url", "TEXT"},
		{"sort_order", "INTEGER NOT NULL DEFAULT 0"}, {"featured", "INTEGER NOT NULL DEFAULT 0"}, {"tags", "TEXT NOT NULL DEFAULT ''"},
		{"icon", "TEXT NOT NULL DEFAULT 'auto'"}, {"icon_file", "TEXT"}, {"created_at", "REAL"}},
	"record_meta": {{"owner", "TEXT NOT NULL DEFAULT ''"}, {"purpose", "TEXT NOT NULL DEFAULT ''"}, {"notes", "TEXT NOT NULL DEFAULT ''"},
		{"expires_at", "TEXT NOT NULL DEFAULT ''"}, {"auto_delete", "INTEGER NOT NULL DEFAULT 0"}, {"no_scan", "INTEGER NOT NULL DEFAULT 0"}},
	"record_scan": {{"last_ok_ts", "REAL"}, {"open_ports", "TEXT NOT NULL DEFAULT ''"}, {"detail", "TEXT NOT NULL DEFAULT ''"},
		{"dns_status", "TEXT NOT NULL DEFAULT ''"}, {"dns_detail", "TEXT NOT NULL DEFAULT ''"}},
	"requests": {{"expires_at", "TEXT DEFAULT ''"}, {"admin_note", "TEXT NOT NULL DEFAULT ''"}, {"decided_ts", "REAL"}, {"record_id", "TEXT"}},
	"audit":    {{"record_id", "TEXT"}},
}

// postMigrate creates indexes on migrated columns; it runs on every start.
const postMigrate = `
CREATE INDEX IF NOT EXISTS audit_record ON audit(record_id);
CREATE INDEX IF NOT EXISTS audit_target ON audit(lower(target));
`

// backfills run once, right after their column has been added.
var backfills = map[string]string{
	"audit.record_id": `UPDATE audit SET record_id=json_extract(detail,'$.record_id') WHERE json_valid(detail)`,
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	for _, sub := range []string{"", "icons", "media", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	s := &Store{db: db, Dir: dir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	for table, cols := range columns {
		rows, err := s.db.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var n string
			rows.Scan(&n)
			have[n] = true
		}
		rows.Close()
		for _, c := range cols {
			if !have[c[0]] {
				if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c[0], c[1])); err != nil {
					return fmt.Errorf("migrate %s.%s: %w", table, c[0], err)
				}
				if q, ok := backfills[table+"."+c[0]]; ok {
					if _, err := s.db.Exec(q); err != nil {
						return fmt.Errorf("backfill %s.%s: %w", table, c[0], err)
					}
				}
			}
		}
	}
	if _, err := s.db.Exec(postMigrate); err != nil {
		return fmt.Errorf("migrate indexes: %w", err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// rowScanner is *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(...any) error }

// each runs q and calls fn for every row; it owns Close and Err.
func (s *Store) each(ctx context.Context, fn func(*sql.Rows) error, q string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// list returns one scanned value per row (never nil).
func list[T any](ctx context.Context, s *Store, scan func(rowScanner) (T, error), q string, args ...any) ([]T, error) {
	out := []T{}
	err := s.each(ctx, func(r *sql.Rows) error {
		v, err := scan(r)
		out = append(out, v)
		return err
	}, q, args...)
	return out, err
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// ---- settings ----

// DefaultSettings returns the default value of every setting.
func DefaultSettings(title string) map[string]string {
	return map[string]string{
		"title": title, "subtitle": "", "accent": "#2dd4bf", "logo": "", "hero": "",
		"announce_text": "", "announce_level": "info", "name_rule": "",
		"name_rule_hint": "限小寫英文、數字與連字號，例：hpc-grafana",
		"zombie_days":    "7", "expiry_warn_days": "14", "scan_ports": "443,80,22",
		"requests_enabled": "1", "request_note": "",
	}
}

// Settings returns all settings merged over s.Defaults.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	out := maps.Clone(s.Defaults)
	if out == nil {
		out = DefaultSettings("Service Portal")
	}
	err := s.each(ctx, func(r *sql.Rows) error {
		var k, v string
		err := r.Scan(&k, &v)
		out[k] = v
		return err
	}, "SELECT key, value FROM settings")
	return out, err
}

// SettingInt returns an integer setting, falling back to its default.
func SettingInt(set map[string]string, key string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(set[key])); err == nil {
		return n
	}
	n, _ := strconv.Atoi(DefaultSettings("")[key])
	return n
}

// SetSettings stores the given keys.
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- meta ----

// AllMeta returns meta keyed by record id.
func (s *Store) AllMeta(ctx context.Context) (map[string]model.Meta, error) {
	out := map[string]model.Meta{}
	err := s.each(ctx, func(r *sql.Rows) error {
		var id string
		var m model.Meta
		err := r.Scan(&id, &m.Owner, &m.Purpose, &m.Notes, &m.ExpiresAt, &m.AutoDelete, &m.NoScan)
		out[id] = m
		return err
	}, "SELECT record_id, owner, purpose, notes, expires_at, auto_delete, no_scan FROM record_meta")
	return out, err
}

// querier is *sql.DB or *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readMeta(ctx context.Context, q querier, id string) (model.Meta, error) {
	var m model.Meta
	err := q.QueryRowContext(ctx, "SELECT owner, purpose, notes, expires_at, auto_delete, no_scan FROM record_meta WHERE record_id=?", id).
		Scan(&m.Owner, &m.Purpose, &m.Notes, &m.ExpiresAt, &m.AutoDelete, &m.NoScan)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Meta{}, nil
	}
	return m, err
}

func writeMeta(ctx context.Context, q querier, id string, m model.Meta) error {
	if m.IsZero() {
		_, err := q.ExecContext(ctx, "DELETE FROM record_meta WHERE record_id=?", id)
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO record_meta(record_id, owner, purpose, notes, expires_at, auto_delete, no_scan, updated_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(record_id) DO UPDATE SET owner=excluded.owner, purpose=excluded.purpose, notes=excluded.notes,
		expires_at=excluded.expires_at, auto_delete=excluded.auto_delete, no_scan=excluded.no_scan, updated_at=excluded.updated_at`,
		id, m.Owner, m.Purpose, m.Notes, m.ExpiresAt, m.AutoDelete, m.NoScan, now())
	return err
}

// Meta returns one record's meta (zero value if absent).
func (s *Store) Meta(ctx context.Context, id string) (model.Meta, error) {
	return readMeta(ctx, s.db, id)
}

// PutMeta replaces one record's meta. Empty meta deletes the row.
func (s *Store) PutMeta(ctx context.Context, id string, m model.Meta) error {
	return writeMeta(ctx, s.db, id, m)
}

// RestoreMeta sets a record's meta to restore only if it still equals
// expect, in one transaction. It reports whether the meta was restored.
func (s *Store) RestoreMeta(ctx context.Context, id string, expect, restore model.Meta) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	cur, err := readMeta(ctx, tx, id)
	if err != nil || cur != expect {
		return false, err
	}
	if err := writeMeta(ctx, tx, id, restore); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ForgetRecord deletes all local data about a deleted record and returns
// the listing that was removed, if any.
func (s *Store) ForgetRecord(ctx context.Context, id string) (*model.Listing, error) {
	l, err := s.Listing(ctx, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, table := range []string{"listings", "record_meta", "record_scan", "issue_mutes", "checks"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE record_id=?", id); err != nil {
			return nil, err
		}
	}
	return l, tx.Commit()
}

// ---- scans ----

// AllScans returns scan results keyed by record id.
func (s *Store) AllScans(ctx context.Context) (map[string]model.Scan, error) {
	out := map[string]model.Scan{}
	err := s.each(ctx, func(r *sql.Rows) error {
		var id string
		var sc model.Scan
		var lastOK sql.NullFloat64
		if err := r.Scan(&id, &sc.FirstTS, &sc.LastTS, &lastOK, &sc.Reachable, &sc.OpenPorts, &sc.Detail, &sc.DNSStatus, &sc.DNSDetail); err != nil {
			return err
		}
		if lastOK.Valid {
			sc.LastOKTS = &lastOK.Float64
		}
		out[id] = sc
		return nil
	}, "SELECT record_id, first_ts, last_ts, last_ok_ts, reachable, open_ports, detail, dns_status, dns_detail FROM record_scan")
	return out, err
}

// PutScan records a scan result. first_ts is kept from the first write and
// last_ok_ts only moves when the record is reachable.
func (s *Store) PutScan(ctx context.Context, id string, ts float64, reachable bool, ports, detail, dnsStatus, dnsDetail string) error {
	var lastOK any
	if reachable {
		lastOK = ts
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO record_scan(record_id, first_ts, last_ts, last_ok_ts, reachable, open_ports, detail, dns_status, dns_detail)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(record_id) DO UPDATE SET last_ts=excluded.last_ts,
		last_ok_ts=COALESCE(excluded.last_ok_ts, record_scan.last_ok_ts), reachable=excluded.reachable, open_ports=excluded.open_ports,
		detail=excluded.detail, dns_status=excluded.dns_status, dns_detail=excluded.dns_detail`,
		id, ts, ts, lastOK, reachable, ports, detail, dnsStatus, dnsDetail)
	return err
}

// PruneScans deletes scan rows written before ts whose record id is not in
// keep. Rows written later (records created or re-enabled while a full scan
// ran) are kept.
func (s *Store) PruneScans(ctx context.Context, keep map[string]bool, ts float64) error {
	ids := make([]any, 0, len(keep)+1)
	ids = append(ids, ts)
	marks := make([]string, 0, len(keep))
	for id := range keep {
		ids = append(ids, id)
		marks = append(marks, "?")
	}
	q := "DELETE FROM record_scan WHERE last_ts < ?"
	if len(marks) > 0 {
		q += " AND record_id NOT IN (" + strings.Join(marks, ",") + ")"
	}
	_, err := s.db.ExecContext(ctx, q, ids...)
	return err
}

// ---- mutes ----

// Mutes returns muted kinds keyed by record id.
func (s *Store) Mutes(ctx context.Context) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	err := s.each(ctx, func(r *sql.Rows) error {
		var id, k string
		if err := r.Scan(&id, &k); err != nil {
			return err
		}
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][k] = true
		return nil
	}, "SELECT record_id, kind FROM issue_mutes")
	return out, err
}

// SetMute mutes or unmutes an issue.
func (s *Store) SetMute(ctx context.Context, id, kind string, muted bool) error {
	var err error
	if muted {
		_, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO issue_mutes(record_id, kind, ts) VALUES(?,?,?)", id, kind, now())
	} else {
		_, err = s.db.ExecContext(ctx, "DELETE FROM issue_mutes WHERE record_id=? AND kind=?", id, kind)
	}
	return err
}

// ---- listings ----

const listingCols = "record_id, hostname, title, description, category, url, sort_order, featured, tags, icon, icon_file, COALESCE(created_at, updated_at), updated_at"

func scanListing(sc rowScanner) (model.Listing, error) {
	var l model.Listing
	var url, icon sql.NullString
	err := sc.Scan(&l.RecordID, &l.Hostname, &l.Title, &l.Description, &l.Category, &url, &l.SortOrder, &l.Featured, &l.Tags, &l.Icon, &icon, &l.CreatedAt, &l.UpdatedAt)
	if url.Valid {
		l.URL = &url.String
	}
	if icon.Valid && icon.String != "" {
		l.IconFile = &icon.String
	}
	return l, err
}

// Listings returns all listings ordered for display.
func (s *Store) Listings(ctx context.Context) ([]model.Listing, error) {
	return list(ctx, s, scanListing, "SELECT "+listingCols+" FROM listings ORDER BY sort_order, title COLLATE NOCASE")
}

// Listing returns one listing.
func (s *Store) Listing(ctx context.Context, id string) (*model.Listing, error) {
	l, err := scanListing(s.db.QueryRowContext(ctx, "SELECT "+listingCols+" FROM listings WHERE record_id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// PutListing inserts or updates a listing.
func (s *Store) PutListing(ctx context.Context, l model.Listing) error {
	t := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO listings(record_id, hostname, title, description, category, url, sort_order, featured, tags, icon, icon_file, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(record_id) DO UPDATE SET hostname=excluded.hostname, title=excluded.title,
		description=excluded.description, category=excluded.category, url=excluded.url, sort_order=excluded.sort_order,
		featured=excluded.featured, tags=excluded.tags, icon=excluded.icon, icon_file=excluded.icon_file, updated_at=excluded.updated_at`,
		l.RecordID, l.Hostname, l.Title, l.Description, l.Category, l.URL, l.SortOrder, l.Featured, l.Tags, l.Icon, l.IconFile, t, t)
	return err
}

// SetListingIcon updates only the icon file.
func (s *Store) SetListingIcon(ctx context.Context, id string, mode string, file *string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE listings SET icon=?, icon_file=?, updated_at=? WHERE record_id=?", mode, file, now(), id)
	return err
}

// SetListingHostname updates the hostname after a rename at Cloudflare.
func (s *Store) SetListingHostname(ctx context.Context, id, host string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE listings SET hostname=? WHERE record_id=?", host, id)
	return err
}

// DeleteListing removes a listing and its checks.
func (s *Store) DeleteListing(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM listings WHERE record_id=?", id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM checks WHERE record_id=?", id)
	return err
}

// IconFileInUse reports whether another listing uses file.
func (s *Store) IconFileInUse(ctx context.Context, file string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM listings WHERE icon_file=?", file).Scan(&n)
	return n > 0, err
}

// ---- checks ----

// Check is one service status check.
type Check struct {
	RecordID string  `json:"-"`
	TS       float64 `json:"ts"`
	OK       bool    `json:"ok"`
	MS       int     `json:"ms"`
	Status   int     `json:"status"`
}

// AddChecks stores check results and deletes checks older than pruneBefore,
// in one transaction.
func (s *Store) AddChecks(ctx context.Context, checks []Check, pruneBefore float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range checks {
		if _, err := tx.ExecContext(ctx, "INSERT INTO checks(record_id, ts, ok, ms, status) VALUES(?,?,?,?,?)", c.RecordID, c.TS, c.OK, c.MS, c.Status); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM checks WHERE ts<?", pruneBefore); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckStat aggregates one record's recent checks.
type CheckStat struct {
	// Buckets maps a bucket index (0 = the bucket starting at bucketStart)
	// to {ok, total}.
	Buckets       map[int][2]int
	UpOK, UpTotal int // checks since day0
	Last          *Check
}

// CheckStats aggregates checks per record in SQL: per-bucket counts from
// bucketStart in buckets of bucketSec, totals since day0 (<= bucketStart),
// and the latest check. Both queries read the covering index
// checks_rec_cover; only aggregates leave the database.
func (s *Store) CheckStats(ctx context.Context, bucketStart, bucketSec, day0 float64) (map[string]*CheckStat, error) {
	out := map[string]*CheckStat{}
	get := func(id string) *CheckStat {
		if out[id] == nil {
			out[id] = &CheckStat{Buckets: map[int][2]int{}}
		}
		return out[id]
	}
	// One pass since day0: checks before bucketStart land in bucket -1 and
	// only count toward the 24-hour totals.
	err := s.each(ctx, func(r *sql.Rows) error {
		var id string
		var b, ok, n int
		if err := r.Scan(&id, &b, &ok, &n); err != nil {
			return err
		}
		st := get(id)
		st.UpOK += ok
		st.UpTotal += n
		if b >= 0 {
			st.Buckets[b] = [2]int{ok, n}
		}
		return nil
	}, `SELECT record_id,
		CASE WHEN ts>=?1 THEN CAST((ts-?1)/?2 AS INTEGER) ELSE -1 END AS b, SUM(ok), COUNT(*)
		FROM checks WHERE ts>=?3 GROUP BY record_id, b`, bucketStart, bucketSec, day0)
	if err != nil {
		return nil, err
	}
	// The latest check per record: one index seek each.
	err = s.each(ctx, func(r *sql.Rows) error {
		c := Check{}
		var st sql.NullInt64
		if err := r.Scan(&c.RecordID, &c.TS, &c.OK, &c.MS, &st); err != nil {
			return err
		}
		c.Status = int(st.Int64)
		get(c.RecordID).Last = &c
		return nil
	}, `SELECT c.record_id, c.ts, c.ok, c.ms, c.status
		FROM (SELECT DISTINCT record_id FROM checks) r
		JOIN checks c ON c.rowid = (SELECT rowid FROM checks WHERE record_id = r.record_id ORDER BY ts DESC LIMIT 1)`)
	return out, err
}

// ---- requests ----

const requestCols = "id, token, ts, ip, COALESCE(requester,''), COALESCE(contact,''), COALESCE(name,''), COALESCE(type,''), COALESCE(content,''), COALESCE(purpose,''), COALESCE(expires_at,''), status, admin_note, decided_ts, record_id"

func scanRequest(sc rowScanner) (model.Request, error) {
	var r model.Request
	var dec sql.NullFloat64
	var rid sql.NullString
	err := sc.Scan(&r.ID, &r.Token, &r.TS, &r.IP, &r.Requester, &r.Contact, &r.Name, &r.Type, &r.Content, &r.Purpose, &r.ExpiresAt, &r.Status, &r.AdminNote, &dec, &rid)
	if dec.Valid {
		r.DecidedTS = &dec.Float64
	}
	if rid.Valid {
		r.RecordID = &rid.String
	}
	return r, err
}

// AddRequest stores a new request and returns its id.
func (s *Store) AddRequest(ctx context.Context, r model.Request) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO requests(token, ts, ip, requester, contact, name, type, content, purpose, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, r.Token, now(), r.IP, r.Requester, r.Contact, r.Name, r.Type, r.Content, r.Purpose, r.ExpiresAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Requests returns all requests, pending first, newest first.
func (s *Store) Requests(ctx context.Context) ([]model.Request, error) {
	return list(ctx, s, scanRequest, "SELECT "+requestCols+" FROM requests ORDER BY status<>'pending', COALESCE(decided_ts, ts) DESC LIMIT 500")
}

// Request returns one request by id.
func (s *Store) Request(ctx context.Context, id int64) (*model.Request, error) {
	r, err := scanRequest(s.db.QueryRowContext(ctx, "SELECT "+requestCols+" FROM requests WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// RequestByToken returns one request by token.
func (s *Store) RequestByToken(ctx context.Context, token string) (*model.Request, error) {
	r, err := scanRequest(s.db.QueryRowContext(ctx, "SELECT "+requestCols+" FROM requests WHERE token=?", token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// DecideRequest marks a pending request approved or rejected.
func (s *Store) DecideRequest(ctx context.Context, id int64, status, note string, recordID *string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE requests SET status=?, admin_note=?, decided_ts=?, record_id=? WHERE id=? AND status='pending'", status, note, now(), recordID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PendingRequests counts requests awaiting a decision.
func (s *Store) PendingRequests(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests WHERE status='pending'").Scan(&n)
	return n, err
}

// CountRequests counts pending requests and requests from ip since ts.
func (s *Store) CountRequests(ctx context.Context, ip string, since float64) (pending, fromIP int, err error) {
	if pending, err = s.PendingRequests(ctx); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests WHERE ip=? AND ts>=?", ip, since).Scan(&fromIP)
	return
}

// ---- audit ----

// AddAudit writes an audit row; detail's record_id (if any) is indexed.
func (s *Store) AddAudit(ctx context.Context, ip, action, target, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit(ts, ip, action, target, detail, record_id)
		VALUES(?1, ?2, ?3, ?4, ?5, CASE WHEN json_valid(?5) THEN json_extract(?5, '$.record_id') END)`, now(), ip, action, target, detail)
	return err
}

func (s *Store) audit(ctx context.Context, where string, args ...any) ([]model.AuditEntry, error) {
	return list(ctx, s, func(r rowScanner) (a model.AuditEntry, err error) {
		err = r.Scan(&a.ID, &a.TS, &a.IP, &a.Action, &a.Target, &a.Detail)
		return
	}, "SELECT id, ts, COALESCE(ip,''), COALESCE(action,''), COALESCE(target,''), COALESCE(detail,'') FROM audit "+where, args...)
}

// Audit returns the latest n audit rows.
func (s *Store) Audit(ctx context.Context, n int) ([]model.AuditEntry, error) {
	return s.audit(ctx, "ORDER BY id DESC LIMIT ?", n)
}

// AuditForRecord returns audit rows about one record id or name.
func (s *Store) AuditForRecord(ctx context.Context, id, name string) ([]model.AuditEntry, error) {
	// Two indexed lookups instead of an OR that scans and parses every row.
	return s.audit(ctx, `WHERE id IN (SELECT id FROM audit WHERE record_id=? UNION SELECT id FROM audit WHERE target<>'' AND lower(target)=lower(?))
		ORDER BY id DESC LIMIT 100`, id, strings.TrimSuffix(name, "."))
}
