// Package cloudflare is a small client for the Cloudflare v4 DNS API.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/seanyeh77/cf-portal/internal/loop"
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

// call does one API call and decodes its result into T.
func call[T any](ctx context.Context, c *Client, method, path string, body any) (T, *envelope, error) {
	var out T
	env, err := c.do(ctx, method, path, body)
	if err != nil {
		return out, nil, err
	}
	if len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, &out); err != nil {
			return out, env, &Error{Status: http.StatusBadGateway, Message: "Cloudflare: " + err.Error()}
		}
	}
	return out, env, nil
}

// allPages fetches every page of a list, whose page n is at pathFor(n). The
// first page tells how many there are; the rest are fetched together.
func allPages[T any](ctx context.Context, c *Client, pathFor func(n int) string) ([]T, error) {
	page := func(n int) ([]T, int, error) {
		items, env, err := call[[]T](ctx, c, http.MethodGet, pathFor(n), nil)
		if err != nil {
			return nil, 0, err
		}
		total := 1
		if env.ResultInfo != nil && len(items) > 0 {
			total = env.ResultInfo.TotalPages
		}
		return items, total, nil
	}
	first, total, err := page(1)
	if err != nil {
		return nil, err
	}
	rest := make([][]T, max(total-1, 0))
	errs := make([]error, len(rest))
	loop.ForEach(len(rest), pageWorkers, func(i int) { rest[i], _, errs[i] = page(i + 2) })
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return slices.Concat(append([][]T{first}, rest...)...), nil
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
		var err error
		if z, _, err = call[zoneObj](ctx, c, http.MethodGet, "/zones/"+url.PathEscape(id), nil); err != nil {
			return "", "", err
		}
	} else {
		zones, _, err := call[[]zoneObj](ctx, c, http.MethodGet, "/zones?name="+url.QueryEscape(c.zoneName), nil)
		if err != nil {
			return "", "", err
		}
		if len(zones) == 0 {
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

// pageWorkers bounds the record list pages fetched at once.
const pageWorkers = 4

// cacheTTL bounds how stale List may be when fresh is false.
const cacheTTL = 15 * time.Second

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
	all, err := allPages[model.Record](ctx, c, func(n int) string {
		return fmt.Sprintf("/zones/%s/dns_records?page=%d&per_page=100&order=name", zid, n)
	})
	if err != nil {
		return nil, time.Time{}, err
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
	rec, _, err := call[model.Record](ctx, c, method, "/zones/"+zid+"/dns_records"+path, body)
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		c.writeThrough(method == http.MethodDelete, rec)
	}
	return &rec, nil
}

// writeThrough applies a successful write to the cached list, so the list
// read right after it needs no new fetch. A List already in flight started
// before the write, so gen++ keeps it from storing its stale result.
func (c *Client) writeThrough(deleted bool, rec model.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if c.cache == nil {
		return
	}
	if rec.ID == "" {
		c.cache = nil // cannot tell which record changed
		return
	}
	switch i := slices.IndexFunc(c.cache, func(r model.Record) bool { return r.ID == rec.ID }); {
	case deleted:
		if i >= 0 {
			c.cache = slices.Delete(c.cache, i, i+1)
		}
	case i >= 0:
		c.cache[i] = rec
	default:
		c.cache = append(c.cache, rec)
	}
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
// It is not a multiple of the admin page's one-minute poll, so idle polls
// do not all miss it.
const tunnelTTL = 90 * time.Second

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
	ts, err := allPages[struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		Status    string  `json:"status"`
		DeletedAt *string `json:"deleted_at"`
	}](ctx, c, func(n int) string {
		return fmt.Sprintf("/accounts/%s/cfd_tunnel?page=%d&per_page=100", url.PathEscape(acc), n)
	})
	if err != nil {
		return nil, err
	}
	out := map[string]model.Tunnel{}
	for _, t := range ts {
		out[strings.ToLower(t.ID)] = model.Tunnel{ID: t.ID, Name: t.Name, Status: t.Status, Deleted: t.DeletedAt != nil && *t.DeletedAt != ""}
	}
	return out, nil
}
