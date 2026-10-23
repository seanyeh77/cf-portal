// Package model holds the plain data types shared across packages.
package model

import "strings"

// Record is a Cloudflare DNS record.
type Record struct {
	ID string `json:"id"`
	RecordInput
	CreatedOn  string `json:"created_on,omitempty"`
	ModifiedOn string `json:"modified_on,omitempty"`
}

// RecordInput is the editable part of a record.
type RecordInput struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
	Comment  string `json:"comment"`
}

// Input returns the editable fields of r.
func (r Record) Input() RecordInput { return r.RecordInput }

// Meta is management information kept only in this system.
type Meta struct {
	Owner      string `json:"owner"`
	Purpose    string `json:"purpose"`
	Notes      string `json:"notes"`
	ExpiresAt  string `json:"expires_at"`
	AutoDelete bool   `json:"auto_delete"`
	NoScan     bool   `json:"no_scan"`
}

// MetaPatch carries optional meta fields; nil means unchanged.
type MetaPatch struct {
	Owner      *string `json:"owner,omitempty"`
	Purpose    *string `json:"purpose,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	ExpiresAt  *string `json:"expires_at,omitempty"`
	AutoDelete *bool   `json:"auto_delete,omitempty"`
	NoScan     *bool   `json:"no_scan,omitempty"`
}

// Apply returns m with the set fields of p applied.
func (p MetaPatch) Apply(m Meta) Meta {
	if p.Owner != nil {
		m.Owner = *p.Owner
	}
	if p.Purpose != nil {
		m.Purpose = *p.Purpose
	}
	if p.Notes != nil {
		m.Notes = *p.Notes
	}
	if p.ExpiresAt != nil {
		m.ExpiresAt = *p.ExpiresAt
	}
	if p.AutoDelete != nil {
		m.AutoDelete = *p.AutoDelete
	}
	if p.NoScan != nil {
		m.NoScan = *p.NoScan
	}
	return m
}

// IsZero reports whether m carries no information.
func (m Meta) IsZero() bool {
	return m.Owner == "" && m.Purpose == "" && m.Notes == "" && m.ExpiresAt == "" && !m.AutoDelete && !m.NoScan
}

// Scan is the result of the record connectivity scan.
type Scan struct {
	FirstTS   float64  `json:"first_ts"`
	LastTS    float64  `json:"last_ts"`
	LastOKTS  *float64 `json:"last_ok_ts"`
	Reachable int      `json:"reachable"`
	OpenPorts string   `json:"open_ports"`
	Detail    string   `json:"detail"`
	DNSStatus string   `json:"dns_status"`
	DNSDetail string   `json:"dns_detail"`
}

// Tunnel is a Cloudflare Tunnel of the zone's account.
type Tunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Status is Cloudflare's: healthy, degraded, down or inactive.
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
}

// Listing is a record published on the service portal.
type Listing struct {
	RecordID    string   `json:"record_id"`
	Hostname    string   `json:"hostname"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	URL         *string  `json:"url"`
	SortOrder   int      `json:"sort_order"`
	Featured    bool     `json:"featured"`
	Tags        []string `json:"tags"`
	Icon        string   `json:"icon"`
	IconFile    *string  `json:"icon_file"`
	CreatedAt   float64  `json:"created_at"`
	UpdatedAt   float64  `json:"updated_at"`
}

// EffectiveURL returns the URL the listing links to.
func (l Listing) EffectiveURL() string {
	if l.URL != nil && *l.URL != "" {
		return *l.URL
	}
	return "https://" + l.Hostname
}

// Request is a public subdomain request.
type Request struct {
	ID        int64    `json:"id"`
	Token     string   `json:"-"`
	TS        float64  `json:"ts"`
	IP        string   `json:"ip"`
	Requester string   `json:"requester"`
	Contact   string   `json:"contact"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Content   string   `json:"content"`
	Purpose   string   `json:"purpose"`
	ExpiresAt string   `json:"expires_at"`
	Status    string   `json:"status"`
	AdminNote string   `json:"admin_note"`
	DecidedTS *float64 `json:"decided_ts"`
	RecordID  *string  `json:"record_id"`
}

// Input is the record a request asks for.
func (q Request) Input() RecordInput {
	return RecordInput{Type: q.Type, Name: q.Name, Content: q.Content, TTL: 1}
}

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID     int64   `json:"id"`
	TS     float64 `json:"ts"`
	IP     string  `json:"ip"`
	Action string  `json:"action"`
	Target string  `json:"target"`
	Detail string  `json:"detail"`
}

// Priority dereferences a record priority; -1 means none.
func Priority(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// ChangedFields lists the fields that differ between a and b, by JSON name.
// Names compare case-insensitively, as DNS does.
func ChangedFields(a, b RecordInput) []string {
	var f []string
	if a.Type != b.Type {
		f = append(f, "type")
	}
	if !strings.EqualFold(a.Name, b.Name) {
		f = append(f, "name")
	}
	if a.Content != b.Content {
		f = append(f, "content")
	}
	if a.TTL != b.TTL {
		f = append(f, "ttl")
	}
	if a.Proxied != b.Proxied {
		f = append(f, "proxied")
	}
	if Priority(a.Priority) != Priority(b.Priority) {
		f = append(f, "priority")
	}
	if a.Comment != b.Comment {
		f = append(f, "comment")
	}
	return f
}
