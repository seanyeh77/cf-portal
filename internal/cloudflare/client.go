// Package cloudflare is a small client for the Cloudflare v4 DNS API.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nycu-sdc/sdc-portal/internal/model"
)

// Error is an error reported by the Cloudflare API.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// HTTPStatus is the status the portal should answer with.
func (e *Error) HTTPStatus() int {
	if e.Status >= 400 && e.Status < 500 {
		return e.Status
	}
	return http.StatusBadGateway
}

// Client talks to one zone.
type Client struct {
	base     string
	token    string
	zoneName string
	http     *http.Client

	mu       sync.Mutex
	zoneMu   sync.Mutex // serializes the zone id lookup
	zoneID   string
	cache    []model.Record
	cachedAt time.Time
	// gen increases on every write, so a List that started before a
	// write never stores its (stale) result.
	gen uint64
}

// New returns a client. zoneID may be empty.
func New(base, token, zoneName, zoneID string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, zoneName: zoneName, zoneID: zoneID,
		http: &http.Client{Timeout: 30 * time.Second}}
}

// ZoneName returns the managed zone.
func (c *Client) ZoneName() string { return c.zoneName }

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*envelope, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Message: "Cloudflare: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Message: fmt.Sprintf("Cloudflare: unexpected response (HTTP %d)", resp.StatusCode)}
	}
	if !env.Success || resp.StatusCode >= 400 {
		var parts []string
		for _, e := range env.Errors {
			parts = append(parts, fmt.Sprintf("%d: %s", e.Code, e.Message))
		}
		if len(parts) == 0 {
			parts = append(parts, fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		st := resp.StatusCode
		if st < 400 {
			st = http.StatusBadGateway
		}
		return nil, &Error{Status: st, Message: "Cloudflare: " + strings.Join(parts, "; ")}
	}
	return &env, nil
}

// ZoneID resolves and caches the zone id.
func (c *Client) ZoneID(ctx context.Context) (string, error) {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()
	c.mu.Lock()
	id := c.zoneID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	env, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(c.zoneName), nil)
	if err != nil {
		return "", err
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &zones); err != nil || len(zones) == 0 {
		return "", &Error{Status: http.StatusBadGateway, Message: "Cloudflare: zone " + c.zoneName + " not found"}
	}
	c.mu.Lock()
	c.zoneID = zones[0].ID
	c.mu.Unlock()
	return zones[0].ID, nil
}

// cacheTTL bounds how stale List may be when fresh is false.
const cacheTTL = 15 * time.Second

// Invalidate drops the cached record list.
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.cache = nil
	c.gen++
	c.mu.Unlock()
}

// List returns all records of the zone. With fresh=false a list younger
// than cacheTTL may be reused.
func (c *Client) List(ctx context.Context, fresh bool) ([]model.Record, time.Time, error) {
	c.mu.Lock()
	if !fresh && c.cache != nil && time.Since(c.cachedAt) < cacheTTL {
		out := append([]model.Record(nil), c.cache...)
		at := c.cachedAt
		c.mu.Unlock()
		return out, at, nil
	}
	gen := c.gen
	c.mu.Unlock()
	zid, err := c.ZoneID(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	var all []model.Record
	for page := 1; ; page++ {
		env, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?page=%d&per_page=100&order=name", zid, page), nil)
		if err != nil {
			return nil, time.Time{}, err
		}
		var recs []model.Record
		if err := json.Unmarshal(env.Result, &recs); err != nil {
			return nil, time.Time{}, &Error{Status: http.StatusBadGateway, Message: "Cloudflare: " + err.Error()}
		}
		all = append(all, recs...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(recs) == 0 {
			break
		}
	}
	now := time.Now()
	c.mu.Lock()
	if c.gen == gen {
		c.cache = append([]model.Record(nil), all...)
		c.cachedAt = now
	}
	c.mu.Unlock()
	return all, now, nil
}

func (c *Client) one(ctx context.Context, method, path string, body any) (*model.Record, error) {
	zid, err := c.ZoneID(ctx)
	if err != nil {
		return nil, err
	}
	env, err := c.do(ctx, method, "/zones/"+zid+"/dns_records"+path, body)
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		c.Invalidate()
	}
	var rec model.Record
	if len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, &rec); err != nil {
			return nil, &Error{Status: http.StatusBadGateway, Message: "Cloudflare: " + err.Error()}
		}
	}
	return &rec, nil
}

type payload struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
	Comment  string `json:"comment"`
}

func toPayload(in model.RecordInput) payload {
	return payload{Type: in.Type, Name: in.Name, Content: in.Content, TTL: in.TTL, Proxied: in.Proxied, Priority: in.Priority, Comment: in.Comment}
}

// Get returns one record.
func (c *Client) Get(ctx context.Context, id string) (*model.Record, error) {
	return c.one(ctx, http.MethodGet, "/"+url.PathEscape(id), nil)
}

// Create adds a record.
func (c *Client) Create(ctx context.Context, in model.RecordInput) (*model.Record, error) {
	return c.one(ctx, http.MethodPost, "", toPayload(in))
}

// Update patches a record with all editable fields.
func (c *Client) Update(ctx context.Context, id string, in model.RecordInput) (*model.Record, error) {
	return c.one(ctx, http.MethodPatch, "/"+url.PathEscape(id), toPayload(in))
}

// Delete removes a record.
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.one(ctx, http.MethodDelete, "/"+url.PathEscape(id), nil)
	return err
}
