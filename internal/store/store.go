// Package store holds all SQLite access.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nycu-sdc/sdc-portal/internal/model"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database.
type Store struct {
	db  *sql.DB
	Dir string
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
CREATE INDEX IF NOT EXISTS checks_rec_ts ON checks(record_id, ts);
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
			}
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

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

// Settings returns all settings merged over defaults.
func (s *Store) Settings(ctx context.Context, defaults map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range defaults {
		out[k] = v
	}
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
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
	rows, err := s.db.QueryContext(ctx, "SELECT record_id, owner, purpose, notes, expires_at, auto_delete, no_scan FROM record_meta")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.Meta{}
	for rows.Next() {
		var id string
		var m model.Meta
		if err := rows.Scan(&id, &m.Owner, &m.Purpose, &m.Notes, &m.ExpiresAt, &m.AutoDelete, &m.NoScan); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
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
		id, m.Owner, m.Purpose, m.Notes, m.ExpiresAt, b2i(m.AutoDelete), b2i(m.NoScan), now())
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
	rows, err := s.db.QueryContext(ctx, "SELECT record_id, first_ts, last_ts, last_ok_ts, reachable, open_ports, detail, dns_status, dns_detail FROM record_scan")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.Scan{}
	for rows.Next() {
		var id string
		var sc model.Scan
		var lastOK sql.NullFloat64
		if err := rows.Scan(&id, &sc.FirstTS, &sc.LastTS, &lastOK, &sc.Reachable, &sc.OpenPorts, &sc.Detail, &sc.DNSStatus, &sc.DNSDetail); err != nil {
			return nil, err
		}
		if lastOK.Valid {
			v := lastOK.Float64
			sc.LastOKTS = &v
		}
		out[id] = sc
	}
	return out, rows.Err()
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
		id, ts, ts, lastOK, b2i(reachable), ports, detail, dnsStatus, dnsDetail)
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
	rows, err := s.db.QueryContext(ctx, "SELECT record_id, kind FROM issue_mutes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][k] = true
	}
	return out, rows.Err()
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

func scanListing(sc interface{ Scan(...any) error }) (model.Listing, error) {
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
	rows, err := s.db.QueryContext(ctx, "SELECT "+listingCols+" FROM listings ORDER BY sort_order, title COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Listing
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
		l.RecordID, l.Hostname, l.Title, l.Description, l.Category, l.URL, l.SortOrder, b2i(l.Featured), l.Tags, l.Icon, l.IconFile, t, t)
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
	TS     float64
	OK     bool
	MS     int
	Status int
}

// AddCheck stores a check result.
func (s *Store) AddCheck(ctx context.Context, id string, c Check) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO checks(record_id, ts, ok, ms, status) VALUES(?,?,?,?,?)", id, c.TS, b2i(c.OK), c.MS, c.Status)
	return err
}

// ChecksSince returns checks since ts, grouped by record id, oldest first.
func (s *Store) ChecksSince(ctx context.Context, ts float64) (map[string][]Check, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT record_id, ts, ok, ms, status FROM checks WHERE ts>=? ORDER BY ts", ts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Check{}
	for rows.Next() {
		var id string
		var c Check
		var st sql.NullInt64
		if err := rows.Scan(&id, &c.TS, &c.OK, &c.MS, &st); err != nil {
			return nil, err
		}
		c.Status = int(st.Int64)
		out[id] = append(out[id], c)
	}
	return out, rows.Err()
}

// PruneChecks deletes checks older than ts.
func (s *Store) PruneChecks(ctx context.Context, ts float64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM checks WHERE ts<?", ts)
	return err
}

// ---- requests ----

const requestCols = "id, token, ts, ip, COALESCE(requester,''), COALESCE(contact,''), COALESCE(name,''), COALESCE(type,''), COALESCE(content,''), COALESCE(purpose,''), COALESCE(expires_at,''), status, admin_note, decided_ts, record_id"

func scanRequest(sc interface{ Scan(...any) error }) (model.Request, error) {
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
	rows, err := s.db.QueryContext(ctx, "SELECT "+requestCols+" FROM requests ORDER BY status<>'pending', COALESCE(decided_ts, ts) DESC LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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

// AddAudit writes an audit row.
func (s *Store) AddAudit(ctx context.Context, ip, action, target, detail string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO audit(ts, ip, action, target, detail) VALUES(?,?,?,?,?)", now(), ip, action, target, detail)
	return err
}

func (s *Store) audit(ctx context.Context, where string, args ...any) ([]model.AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, ts, COALESCE(ip,''), COALESCE(action,''), COALESCE(target,''), COALESCE(detail,'') FROM audit "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		var a model.AuditEntry
		if err := rows.Scan(&a.ID, &a.TS, &a.IP, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Audit returns the latest n audit rows.
func (s *Store) Audit(ctx context.Context, n int) ([]model.AuditEntry, error) {
	return s.audit(ctx, "ORDER BY id DESC LIMIT ?", n)
}

// AuditForRecord returns audit rows about one record id or name.
func (s *Store) AuditForRecord(ctx context.Context, id, name string) ([]model.AuditEntry, error) {
	return s.audit(ctx, `WHERE (json_valid(detail) AND json_extract(detail,'$.record_id')=?) OR (target<>'' AND lower(target)=lower(?))
		ORDER BY id DESC LIMIT 100`, id, strings.TrimSuffix(name, "."))
}
