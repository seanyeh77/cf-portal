package snapshots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seanyeh77/cf-portal/internal/model"
)

func TestBIND(t *testing.T) {
	recs := []Record{
		{RecordInput: model.RecordInput{Type: "A", Name: "grafana.example.org", Content: "10.0.1.20", TTL: 1}},
		{RecordInput: model.RecordInput{Type: "TXT", Name: "_dmarc.example.org", Content: `v=DMARC1; p="none"`, TTL: 3600}},
		{RecordInput: model.RecordInput{Type: "TXT", Name: "q.example.org", Content: `"already quoted"`, TTL: 1}},
		{RecordInput: model.RecordInput{Type: "CNAME", Name: "docs.example.org", Content: "docs-site.pages.dev", TTL: 1, Proxied: true}},
		{RecordInput: model.RecordInput{Type: "MX", Name: "example.org", Content: "mail.example.com", TTL: 1, Priority: new(10)}},
		{RecordInput: model.RecordInput{Type: "NS", Name: "sub.example.org", Content: "ns1.example.com.", TTL: 86400}},
		{RecordInput: model.RecordInput{Type: "SRV", Name: "_mc._tcp.example.org", Content: "0 25565 mc.example.org", TTL: 1, Priority: new(10)}},
	}
	out := BIND("example.org", recs, time.Unix(0, 0))
	for _, want := range []string{
		"$ORIGIN example.org.\n$TTL 300\n",
		"grafana.example.org.\t300\tIN\tA\t10.0.1.20\n",
		"_dmarc.example.org.\t3600\tIN\tTXT\t\"v=DMARC1; p=\\\"none\\\"\"\n",
		"q.example.org.\t300\tIN\tTXT\t\"already quoted\"\n",
		"docs.example.org.\t300\tIN\tCNAME\tdocs-site.pages.dev. ; cf-proxied\n",
		"example.org.\t300\tIN\tMX\t10 mail.example.com.\n",
		"sub.example.org.\t86400\tIN\tNS\tns1.example.com.\n",
		"_mc._tcp.example.org.\t300\tIN\tSRV\t10 0 25565 mc.example.org.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	long := BIND("z", []Record{{RecordInput: model.RecordInput{Type: "TXT", Name: "a.z", Content: strings.Repeat("x", 300), TTL: 1}}}, time.Unix(0, 0))
	if !strings.Contains(long, `" "`) {
		t.Error("long TXT must be split")
	}
}

func TestCompare(t *testing.T) {
	snap := []Record{
		{ID: "1", RecordInput: model.RecordInput{Type: "A", Name: "a.z", Content: "10.0.0.1", TTL: 1}},
		{ID: "2", RecordInput: model.RecordInput{Type: "A", Name: "b.z", Content: "10.0.0.2", TTL: 1}},
		{ID: "3", RecordInput: model.RecordInput{Type: "CNAME", Name: "c.z", Content: "x.example.com", TTL: 1}},
		{ID: "4", RecordInput: model.RecordInput{Type: "A", Name: "d.z", Content: "10.0.0.4", TTL: 1}},
	}
	cur := []model.Record{
		{ID: "1", RecordInput: model.RecordInput{Type: "A", Name: "a.z", Content: "10.0.0.1", TTL: 1}},
		{ID: "2", RecordInput: model.RecordInput{Type: "A", Name: "b.z", Content: "10.0.0.99", TTL: 300}},
		{ID: "33", RecordInput: model.RecordInput{Type: "CNAME", Name: "c.z", Content: "X.example.com.", TTL: 1}}, // recreated
		{ID: "5", RecordInput: model.RecordInput{Type: "A", Name: "e.z", Content: "10.0.0.5", TTL: 1}},
	}
	d := Compare(snap, cur)
	if len(d.Removed) != 1 || d.Removed[0].ID != "4" {
		t.Errorf("removed %+v", d.Removed)
	}
	if len(d.Added) != 1 || d.Added[0].ID != "5" {
		t.Errorf("added %+v", d.Added)
	}
	if len(d.Changed) != 2 || d.Changed[0].Now.ID != "2" || strings.Join(d.Changed[0].Fields, ",") != "content,ttl" {
		t.Errorf("changed %+v", d.Changed)
	}
	if d.Changed[1].Now.ID != "33" || strings.Join(d.Changed[1].Fields, ",") != "content" {
		t.Errorf("recreated %+v", d.Changed[1])
	}
	exact := []model.Record{{ID: "9", RecordInput: model.RecordInput{Type: "A", Name: "d.z", Content: "10.0.0.4", TTL: 1}}}
	if d := Compare(snap[3:], exact); len(d.Removed)+len(d.Added)+len(d.Changed) != 0 {
		t.Errorf("recreated with same content must be identical: %+v", d)
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	loc, _ := time.LoadLocation("Asia/Taipei")
	s := &Store{Dir: dir, Loc: loc}
	at := time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := s.Create("z", "auto", []Record{{ID: "1"}}, at.AddDate(0, 0, -i)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := s.Create("z", "manual", nil, at)
	if err != nil || info.Name != "20261022-090203-manual.json" {
		t.Fatalf("%+v %v", info, err)
	}
	if again, _ := s.Create("z", "manual", nil, at); again.Name != "20261022-090204-manual.json" {
		t.Fatalf("collision: %s", again.Name)
	}
	if !s.HasDay(at) || s.HasDay(at.AddDate(0, 0, 5)) {
		t.Error("HasDay")
	}
	if err := s.Prune(2); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	auto, manual := 0, 0
	for _, i := range list {
		if i.Source == "auto" {
			auto++
		} else {
			manual++
		}
	}
	if auto != 2 || manual != 2 || list[0].Count != 0 {
		t.Errorf("after prune auto=%d manual=%d", auto, manual)
	}
	if _, err := s.Load("../../etc/passwd"); err == nil {
		t.Error("path traversal")
	}
	os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{}"), 0o600)
	if l, _ := s.List(); len(l) != 4 {
		t.Errorf("junk listed: %d", len(l))
	}
}
