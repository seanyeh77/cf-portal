// Package snapshots saves zone snapshots, compares them with the current
// zone and exports BIND zone files.
package snapshots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seanyeh77/cf-portal/internal/dnsrec"
	"github.com/seanyeh77/cf-portal/internal/model"
)

// NameRe matches snapshot file names.
var NameRe = regexp.MustCompile(`^(\d{8}-\d{6})-(auto|manual)\.json$`)

// Record is one record inside a snapshot.
type Record struct {
	ID string `json:"id"`
	model.RecordInput
	Meta model.Meta `json:"meta"`
}

// Snapshot is the file content.
type Snapshot struct {
	Zone    string   `json:"zone"`
	TS      float64  `json:"ts"`
	Source  string   `json:"source"`
	Records []Record `json:"records"`
}

// Info describes a snapshot file.
type Info struct {
	Name   string  `json:"name"`
	TS     float64 `json:"ts"`
	Source string  `json:"source"`
	Count  int     `json:"count"`
	Size   int64   `json:"size"`
}

// Store manages a snapshot directory.
type Store struct {
	Dir string
	Loc *time.Location

	mu     sync.Mutex
	counts map[string]int // record count per file; files never change once written
}

func (s *Store) count(name string) int {
	s.mu.Lock()
	n, ok := s.counts[name]
	s.mu.Unlock()
	if ok {
		return n
	}
	snap, err := s.Load(name)
	if err != nil {
		return 0
	}
	s.setCount(name, len(snap.Records))
	return len(snap.Records)
}

func (s *Store) setCount(name string, n int) {
	s.mu.Lock()
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	s.counts[name] = n
	s.mu.Unlock()
}

func (s *Store) nameTime(name string) (time.Time, string, bool) {
	m := NameRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, "", false
	}
	t, err := time.ParseInLocation("20060102-150405", m[1], s.Loc)
	return t, m[2], err == nil
}

// Latest returns the time of the newest snapshot (zero if none) without
// reading any snapshot file.
func (s *Store) Latest() time.Time {
	ents, _ := os.ReadDir(s.Dir)
	var latest time.Time
	for _, e := range ents {
		if t, _, ok := s.nameTime(e.Name()); ok && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// FromRecords builds snapshot records.
func FromRecords(recs []model.Record, meta map[string]model.Meta) []Record {
	out := make([]Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, Record{ID: r.ID, RecordInput: r.Input(), Meta: meta[r.ID]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Create writes a new snapshot and returns its info.
func (s *Store) Create(zone, source string, recs []Record, at time.Time) (Info, error) {
	if source != "auto" && source != "manual" {
		return Info{}, errors.New("invalid source")
	}
	t := at.In(s.Loc)
	var name string
	for {
		name = t.Format("20060102-150405") + "-" + source + ".json"
		if _, err := os.Stat(filepath.Join(s.Dir, name)); errors.Is(err, os.ErrNotExist) {
			break
		}
		t = t.Add(time.Second)
	}
	snap := Snapshot{Zone: zone, TS: float64(at.UnixNano()) / 1e9, Source: source, Records: recs}
	b, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return Info{}, err
	}
	tmp := filepath.Join(s.Dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return Info{}, err
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, name)); err != nil {
		return Info{}, err
	}
	s.setCount(name, len(recs))
	return Info{Name: name, TS: snap.TS, Source: source, Count: len(recs), Size: int64(len(b))}, nil
}

// List returns snapshots, newest first.
func (s *Store) List() ([]Info, error) {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	out := []Info{}
	for _, e := range ents {
		t, source, ok := s.nameTime(e.Name())
		if !ok {
			continue
		}
		info := Info{Name: e.Name(), TS: float64(t.Unix()), Source: source, Count: s.count(e.Name())}
		if fi, err := e.Info(); err == nil {
			info.Size = fi.Size()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// Load reads one snapshot.
func (s *Store) Load(name string) (*Snapshot, error) {
	if !NameRe.MatchString(name) {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// HasDay reports whether a snapshot was taken on the given local day.
func (s *Store) HasDay(day time.Time) bool {
	y, m, d := day.In(s.Loc).Date()
	ly, lm, ld := s.Latest().Date()
	return y == ly && m == lm && d == ld
}

// Prune keeps the newest keep automatic snapshots.
func (s *Store) Prune(keep int) error {
	list, err := s.List()
	if err != nil {
		return err
	}
	n := 0
	for _, i := range list {
		if i.Source != "auto" {
			continue
		}
		n++
		if n > keep {
			os.Remove(filepath.Join(s.Dir, i.Name))
			s.mu.Lock()
			delete(s.counts, i.Name)
			s.mu.Unlock()
		}
	}
	return nil
}

// Change is a record that differs between a snapshot and now.
type Change struct {
	Fields []string     `json:"fields"`
	Then   Record       `json:"then"`
	Now    model.Record `json:"now"`
}

// Diff is the comparison of a snapshot with the current zone.
type Diff struct {
	Removed []Record       `json:"removed"`
	Changed []Change       `json:"changed"`
	Added   []model.Record `json:"added"`
}

func sameIdentity(a Record, b model.Record) bool {
	return a.Type == b.Type && strings.EqualFold(a.Name, b.Name) && strings.EqualFold(dnsrec.NormalizeTarget(a.Content), dnsrec.NormalizeTarget(b.Content))
}

// Compare diffs a snapshot against current records. Records are matched by
// id; a snapshot record deleted and later recreated with the same
// type/name/content counts as the same record.
func Compare(snap []Record, current []model.Record) Diff {
	d := Diff{Removed: []Record{}, Changed: []Change{}, Added: []model.Record{}}
	byID := map[string]int{}
	for i, r := range current {
		byID[r.ID] = i
	}
	used := make([]bool, len(current))
	var unmatched []Record
	pairs := map[int]Record{}
	for _, s := range snap {
		if i, ok := byID[s.ID]; ok {
			used[i] = true
			pairs[i] = s
		} else {
			unmatched = append(unmatched, s)
		}
	}
	for _, s := range unmatched {
		found := false
		for i, c := range current {
			if !used[i] && sameIdentity(s, c) {
				used[i] = true
				pairs[i] = s
				found = true
				break
			}
		}
		if !found {
			d.Removed = append(d.Removed, s)
		}
	}
	idx := make([]int, 0, len(pairs))
	for i := range pairs {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		if f := model.ChangedFields(pairs[i].RecordInput, current[i].Input()); len(f) > 0 {
			d.Changed = append(d.Changed, Change{Fields: f, Then: pairs[i], Now: current[i]})
		}
	}
	for i, c := range current {
		if !used[i] {
			d.Added = append(d.Added, c)
		}
	}
	return d
}

func quoteTXT(s string) string {
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		return s
	}
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	var parts []string
	r := []byte(s)
	for len(r) > 255 {
		parts = append(parts, `"`+esc.Replace(string(r[:255]))+`"`)
		r = r[255:]
	}
	parts = append(parts, `"`+esc.Replace(string(r))+`"`)
	return strings.Join(parts, " ")
}

func dot(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// BIND renders records as a zone file.
func BIND(zone string, recs []Record, ts time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "; %s exported %s\n", zone, ts.Format(time.RFC3339))
	fmt.Fprintf(&b, "$ORIGIN %s.\n$TTL 300\n", zone)
	for _, r := range recs {
		ttl := r.TTL
		if ttl <= 1 {
			ttl = 300
		}
		content := r.Content
		switch r.Type {
		case "TXT", "SPF":
			content = quoteTXT(content)
		case "CNAME", "NS", "PTR", "DNAME":
			content = dot(content)
		case "MX":
			content = fmt.Sprintf("%d %s", max(model.Priority(r.Priority), 0), dot(content))
		case "SRV":
			f := strings.Fields(content)
			if len(f) == 3 {
				f[2] = dot(f[2])
				content = strings.Join(f, " ")
			}
			if r.Priority != nil {
				content = fmt.Sprintf("%d %s", *r.Priority, content)
			}
		}
		comment := ""
		if r.Proxied {
			comment = " ; cf-proxied"
		}
		fmt.Fprintf(&b, "%s\t%d\tIN\t%s\t%s%s\n", dot(r.Name), ttl, r.Type, content, comment)
	}
	return b.String()
}
