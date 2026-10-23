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

	"github.com/seanyeh77/cf-portal/internal/model"
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
	zoneMu   sync.Mutex // serializes the zone lookup on a cache miss
	zoneID   string
	account  string
	cache    []model.Record
	cachedAt time.Time
	// gen increases on every write, so a List that started before a
	// write never stores its (stale) result.
	gen uint64

	tunnelMu sync.Mutex
	tunnels  tunnelCache
}

// New returns a client. zoneID and accountID may be empty; they are then
// looked up from the zone name.
func New(base, token, zoneName, zoneID, accountID string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, zoneName: zoneName, zoneID: zoneID, account: accountID,
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
	c.mu.Lock()
	id := c.zoneID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	id, _, err := c.zone(ctx)
	return id, err
}

// AccountID returns the account that owns the zone.
func (c *Client) AccountID(ctx context.Context) (string, error) {
	_, acc, err := c.zone(ctx)
	return acc, err
}

// zone resolves and caches the zone id and its account id.
func (c *Client) zone(ctx context.Context) (string, string, error) {
	cached := func() (string, string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.zoneID, c.account
	}
	if id, acc := cached(); id != "" && acc != "" {
		return id, acc, nil
	}
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()
	id, acc := cached()
	if id != "" && acc != "" {
		return id, acc, nil
	}
	type zoneObj struct {
		ID      string `json:"id"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	var z zoneObj
	if id != "" {
		env, err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(id), nil)
		if err != nil {
			return "", "", err
		}
		if err := json.Unmarshal(env.Result, &z); err != nil {
			return "", "", &Error{Status: http.StatusBadGateway, Message: "Cloudflare: " + err.Error()}
		}
	} else {
		env, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(c.zoneName), nil)
		if err != nil {
			return "", "", err
		}
		var zones []zoneObj
		if err := json.Unmarshal(env.Result, &zones); err != nil || len(zones) == 0 {
			return "", "", &Error{Status: http.StatusBadGateway, Message: "Cloudflare: zone " + c.zoneName + " not found"}
		}
		z = zones[0]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.zoneID == "" {
		c.zoneID = z.ID
	}
	if c.account == "" {
		c.account = z.Account.ID
	}
	return c.zoneID, c.account, nil
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

// Get returns one record.
func (c *Client) Get(ctx context.Context, id string) (*model.Record, error) {
	return c.one(ctx, http.MethodGet, "/"+url.PathEscape(id), nil)
}

// Create adds a record.
func (c *Client) Create(ctx context.Context, in model.RecordInput) (*model.Record, error) {
	return c.one(ctx, http.MethodPost, "", in)
}

// Update patches a record with all editable fields.
func (c *Client) Update(ctx context.Context, id string, in model.RecordInput) (*model.Record, error) {
	return c.one(ctx, http.MethodPatch, "/"+url.PathEscape(id), in)
}

// Delete removes a record.
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.one(ctx, http.MethodDelete, "/"+url.PathEscape(id), nil)
	return err
}

// tunnelTTL bounds how stale the tunnel list may be. A failed lookup is
// cached as long, so a token without the Tunnel permission costs one
// request per period.
const tunnelTTL = time.Minute

type tunnelCache struct {
	byID map[string]model.Tunnel
	err  error
	at   time.Time
}

// Tunnels returns the account's Cloudflare Tunnels by id, deleted ones
// included. It needs the Account > Cloudflare Tunnel > Read permission.
func (c *Client) Tunnels(ctx context.Context) (map[string]model.Tunnel, error) {
	c.tunnelMu.Lock()
	defer c.tunnelMu.Unlock()
	if !c.tunnels.at.IsZero() && time.Since(c.tunnels.at) < tunnelTTL {
		return c.tunnels.byID, c.tunnels.err
	}
	byID, err := c.listTunnels(ctx)
	if ctx.Err() != nil {
		return nil, err // a cancelled request says nothing about Cloudflare
	}
	c.tunnels = tunnelCache{byID: byID, err: err, at: time.Now()}
	return byID, err
}

func (c *Client) listTunnels(ctx context.Context) (map[string]model.Tunnel, error) {
	acc, err := c.AccountID(ctx)
	if err != nil {
		return nil, err
	}
	if acc == "" {
		return nil, &Error{Status: http.StatusBadGateway, Message: "Cloudflare: account id unknown"}
	}
	out := map[string]model.Tunnel{}
	for page := 1; ; page++ {
		env, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/accounts/%s/cfd_tunnel?page=%d&per_page=100", url.PathEscape(acc), page), nil)
		if err != nil {
			return nil, err
		}
		var ts []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Status    string  `json:"status"`
			DeletedAt *string `json:"deleted_at"`
		}
		if err := json.Unmarshal(env.Result, &ts); err != nil {
			return nil, &Error{Status: http.StatusBadGateway, Message: "Cloudflare: " + err.Error()}
		}
		for _, t := range ts {
			out[strings.ToLower(t.ID)] = model.Tunnel{ID: t.ID, Name: t.Name, Status: t.Status, Deleted: t.DeletedAt != nil && *t.DeletedAt != ""}
		}
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(ts) == 0 {
			break
		}
	}
	return out, nil
}
