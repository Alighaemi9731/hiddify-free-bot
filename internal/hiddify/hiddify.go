// Package hiddify is a small client for the Hiddify Manager REST API v2.
//
// Verified against a live Hiddify Manager v12.0.0 panel:
//
//	Base URL : https://<domain>/<admin_proxy_path>/api/v2
//	Auth     : header "Hiddify-API-Key: <admin_uuid>"
//	Endpoints: GET  /panel/info/                 -> {"version": "..."}
//	           GET  /admin/me/                   -> admin info
//	           GET  /admin/user/                 -> [user, ...]
//	           POST /admin/user/                 -> create user (returns user)
//	           GET  /admin/user/{uuid}/          -> single user
//	           PATCH/DELETE /admin/user/{uuid}/  -> update / delete
//
// The subscription proxy path is NOT exposed by the API, so it is parsed from a
// sample subscription link supplied by the admin when a panel is added.
package hiddify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrPanelFull is returned by CreateUser when the panel/admin user limit is hit,
// so the caller can fall through to another panel.
var ErrPanelFull = errors.New("panel user limit reached")

// APIError carries the HTTP status + body of a non-2xx panel response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("پنل خطا داد (HTTP %d): %s", e.Status, e.Body)
}

var panelFullRe = regexp.MustCompile(`(?i)max|limit|quota|ظرفیت|حداکثر`)

// sharedTransport is reused across all panel clients (the default pool is small
// for multi-panel bursts).
var sharedTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	MaxConnsPerHost:     10,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   true,
}

// panelLocks serializes writes per panel (each write triggers a server-side
// quick_apply_users); parallelism across panels is fine.
var panelLocks sync.Map

func panelMu(key string) *sync.Mutex {
	mu, _ := panelLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AdminLinkParts holds everything extractable from a Hiddify admin link.
type AdminLinkParts struct {
	Domain    string // e.g. mainsub.iranfilm-dl.online
	ProxyPath string // admin proxy path, e.g. knrvQP52yPDxcsCaK9dtX2SH55BPL
	AdminUUID string // admin secret = API key
}

// SubLinkParts holds everything extractable from a sample subscription link.
type SubLinkParts struct {
	Domain    string // subscription domain
	ProxyPath string // client/subscription proxy path
}

// ParseAdminLink extracts the domain, admin proxy path and admin uuid from a
// Hiddify admin URL such as:
//
//	https://domain/<proxy_path>/<admin_uuid>/admin/adminuser/
func ParseAdminLink(raw string) (*AdminLinkParts, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("لینک ادمین نامعتبر است")
	}
	segs := splitPath(u.Path)
	// Expected: [proxy_path, admin_uuid, "admin", ...]
	idx := indexOfUUID(segs)
	if idx < 1 {
		return nil, fmt.Errorf("UUID ادمین در لینک پیدا نشد")
	}
	return &AdminLinkParts{
		Domain:    u.Host,
		ProxyPath: strings.Join(segs[:idx], "/"),
		AdminUUID: strings.ToLower(segs[idx]),
	}, nil
}

// ParseSubLink extracts the subscription domain and proxy path from a sample
// subscription URL such as:
//
//	https://domain/<sub_proxy_path>/<user_uuid>/#Name
func ParseSubLink(raw string) (*SubLinkParts, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("لینک ساب نامعتبر است")
	}
	segs := splitPath(u.Path)
	idx := indexOfUUID(segs)
	if idx < 1 {
		return nil, fmt.Errorf("UUID کاربر در لینک ساب پیدا نشد")
	}
	return &SubLinkParts{
		Domain:    u.Host,
		ProxyPath: strings.Join(segs[:idx], "/"),
	}, nil
}

func splitPath(p string) []string {
	out := []string{}
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func indexOfUUID(segs []string) int {
	for i, s := range segs {
		if uuidRe.MatchString(s) {
			return i
		}
	}
	return -1
}

// Client talks to one Hiddify panel.
type Client struct {
	domain    string
	proxyPath string
	apiKey    string
	http      *http.Client
}

// New builds a client from the admin link parts.
func New(p *AdminLinkParts) *Client {
	return &Client{
		domain:    p.Domain,
		proxyPath: p.ProxyPath,
		apiKey:    p.AdminUUID,
		http:      &http.Client{Timeout: 30 * time.Second, Transport: sharedTransport},
	}
}

func (c *Client) base() string {
	return fmt.Sprintf("https://%s/%s/api/v2", c.domain, c.proxyPath)
}

// panelRoot is the Flask-Admin web UI root (NOT under /api/v2) used for bulk actions.
func (c *Client) panelRoot() string {
	return fmt.Sprintf("https://%s/%s", c.domain, c.proxyPath)
}

func (c *Client) lockKey() string { return c.domain + "/" + c.proxyPath }

// User mirrors the Hiddify user object (only the fields we care about).
type User struct {
	ID             int     `json:"id,omitempty"` // Hiddify internal rowid (needed for bulk action)
	UUID           string  `json:"uuid,omitempty"`
	Name           string  `json:"name,omitempty"`
	UsageLimitGB   float64 `json:"usage_limit_GB"`
	PackageDays    int     `json:"package_days"`
	Mode           string  `json:"mode,omitempty"`
	Comment        string  `json:"comment,omitempty"`
	TelegramID     *int64  `json:"telegram_id,omitempty"`
	Lang           string  `json:"lang,omitempty"`
	Enable         bool    `json:"enable"`
	IsActive       bool    `json:"is_active,omitempty"`
	CurrentUsageGB float64 `json:"current_usage_GB,omitempty"`
	LastOnline     string  `json:"last_online,omitempty"`
}

// do performs a JSON request with retry/backoff. Idempotent ops (GET/DELETE)
// retry on 429/5xx; every method retries on a transient network error (create
// uses our own uuid, so a retried POST is effectively idempotent).
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	idempotent := method == http.MethodGet || method == http.MethodDelete
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(200<<(attempt-1))*time.Millisecond +
				time.Duration(rand.Intn(120))*time.Millisecond
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		status, data, err := c.doOnce(ctx, method, c.base()+path, payload, body != nil)
		if err != nil {
			lastErr = fmt.Errorf("اتصال به پنل ناموفق بود: %w", err)
			continue // network error → retry
		}
		if status >= 200 && status < 300 {
			if out != nil && len(data) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("پاسخ پنل قابل خواندن نبود: %w", err)
				}
			}
			return nil
		}
		apiErr := &APIError{Status: status, Body: strings.TrimSpace(string(data))}
		if idempotent && (status == 429 || status >= 500) {
			lastErr = apiErr
			continue
		}
		return apiErr
	}
	return lastErr
}

// doOnce performs a single HTTP attempt and returns status + body.
func (c *Client) doOnce(ctx context.Context, method, fullURL string, payload []byte, hasBody bool) (int, []byte, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Hiddify-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, data, nil
}

// PanelInfo is the response of /panel/info/.
type PanelInfo struct {
	Version string `json:"version"`
}

// Ping verifies connectivity + credentials and returns the panel version.
func (c *Client) Ping(ctx context.Context) (string, error) {
	var info PanelInfo
	if err := c.do(ctx, http.MethodGet, "/panel/info/", nil, &info); err != nil {
		return "", err
	}
	return info.Version, nil
}

// AdminInfo is a subset of /admin/me/.
type AdminInfo struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	UUID       string `json:"uuid"`
	TelegramID int64  `json:"telegram_id"`
}

// Me returns the authenticated admin's info.
func (c *Client) Me(ctx context.Context) (*AdminInfo, error) {
	var a AdminInfo
	if err := c.do(ctx, http.MethodGet, "/admin/me/", nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateUser creates a user and returns the resulting object (with uuid + id).
// Writes are serialized per panel (each triggers a server-side apply) with a
// small pace. A panel/admin user-limit response maps to ErrPanelFull.
func (c *Client) CreateUser(ctx context.Context, u User) (*User, error) {
	if u.UUID == "" {
		u.UUID = uuid.NewString()
	}
	if u.Mode == "" {
		u.Mode = "no_reset"
	}
	u.Enable = true

	mu := panelMu(c.lockKey())
	mu.Lock()
	defer mu.Unlock()

	var out User
	err := c.do(ctx, http.MethodPost, "/admin/user/", u, &out)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 403) && panelFullRe.MatchString(ae.Body) {
			return nil, ErrPanelFull
		}
		return nil, err
	}
	// Some panel versions echo an empty body; fall back to what we sent.
	if out.UUID == "" {
		out = u
	}
	// Pace consecutive writes to the same panel so applies don't pile up.
	select {
	case <-time.After(250 * time.Millisecond):
	case <-ctx.Done():
	}
	return &out, nil
}

// GetUser fetches a single user by uuid.
func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/admin/user/"+id+"/", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUser removes a user by uuid. A 404 is treated as success (already gone).
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/admin/user/"+id+"/", nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 404 {
		return nil
	}
	return err
}

var csrfRe = regexp.MustCompile(`name=["']csrf_token["'][^>]*value=["']([^"']+)["']`)

// BulkUserAction runs Hiddify's native Flask-Admin action (delete|enable|disable)
// on numeric rowids in ONE request → a single server-side apply for the whole
// batch (instead of one apply per user). Writes are serialized per panel.
func (c *Client) BulkUserAction(ctx context.Context, action string, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	mu := panelMu(c.lockKey())
	mu.Lock()
	defer mu.Unlock()

	jar, _ := cookiejar.New(nil) // the CSRF token is bound to the Flask session cookie
	httpc := &http.Client{
		Timeout:       5 * time.Minute, // a big batch apply is slow
		Transport:     sharedTransport,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // 302 = ok
	}
	listURL := c.panelRoot() + "/admin/user/"
	actURL := c.panelRoot() + "/admin/user/action/"

	// 1) GET the list page → CSRF token (+ session cookie into the jar).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Hiddify-API-Key", c.apiKey)
	page, err := httpc.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(page.Body, 8<<20))
	page.Body.Close()
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		return fmt.Errorf("توکن CSRF در صفحه‌ی بالک پیدا نشد (HTTP %d)", page.StatusCode)
	}
	csrf := html.UnescapeString(string(m[1]))

	// 2) POST the action (form-encoded, repeated rowid).
	form := url.Values{}
	form.Set("csrf_token", csrf)
	form.Set("url", listURL)
	form.Set("action", action)
	for _, id := range ids {
		form.Add("rowid", strconv.Itoa(id))
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, actURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req2.Header.Set("Hiddify-API-Key", c.apiKey)
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req2)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 { // 200/302 = success
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("بالک %s ناموفق بود (HTTP %d): %s", action, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ResolveUserIDs resolves uuids to Hiddify numeric ids (needed for bulk action),
// with bounded concurrency. resolved maps uuid→id for users that exist; missing
// lists uuids that returned 404 (already gone). uuids that fail transiently are
// in neither (so the caller keeps them for the next run).
func (c *Client) ResolveUserIDs(ctx context.Context, uuids []string) (resolved map[string]int, missing []string) {
	type res struct {
		id      int
		uuid    string
		missing bool
	}
	sem := make(chan struct{}, 8)
	out := make(chan res, len(uuids))
	for _, u := range uuids {
		go func(u string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			user, err := c.GetUser(ctx, u)
			if err != nil {
				var ae *APIError
				if errors.As(err, &ae) && ae.Status == 404 {
					out <- res{uuid: u, missing: true}
					return
				}
				out <- res{uuid: u} // transient error → skip this round (retry next run)
				return
			}
			out <- res{id: user.ID, uuid: u}
		}(u)
	}
	resolved = map[string]int{}
	for range uuids {
		r := <-out
		switch {
		case r.missing:
			missing = append(missing, r.uuid)
		case r.id > 0:
			resolved[r.uuid] = r.id
		}
	}
	return resolved, missing
}

var schemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// FetchConfigs downloads a subscription link and returns the real config URIs
// (vless://, vmess://, trojan://, ss://, …) it contains. Hiddify serves the
// subscription base64-encoded for known clients, and injects a fake info config
// (sni=fake_ip_for_sub_link) which is filtered out.
func FetchConfigs(ctx context.Context, subURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "v2rayNG/1.9.5")
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("دریافت کانفیگ ناموفق بود: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("لینک ساب خطا داد (HTTP %d)", resp.StatusCode)
	}
	cfgs := ParseSubBody(body)
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("کانفیگی در لینک ساب پیدا نشد")
	}
	return cfgs, nil
}

// ParseSubBody extracts config URIs from a subscription body (base64 or plain),
// dropping Hiddify's fake info line.
func ParseSubBody(body []byte) []string {
	text := string(body)
	if !strings.Contains(text, "://") {
		if dec, ok := tryBase64(text); ok {
			text = dec
		}
	}
	var out []string
	for _, ln := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		ln = strings.TrimSpace(ln)
		if ln == "" || !schemeRe.MatchString(ln) {
			continue
		}
		if strings.Contains(ln, "fake_ip_for_sub_link") {
			continue // Hiddify usage/expiry placeholder, not a real config
		}
		out = append(out, ln)
	}
	return out
}

// tryBase64 strips whitespace and attempts std/raw base64 decode; ok is true
// only if the decoded text actually contains a config URI.
func tryBase64(s string) (string, bool) {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if dec, err := enc.DecodeString(cleaned); err == nil && strings.Contains(string(dec), "://") {
			return string(dec), true
		}
	}
	return "", false
}

// SubLink builds a working subscription URL for a user uuid given the panel's
// subscription domain + proxy path and the desired client format.
//
//	subType: "" or "auto" -> base link, "sub", "sub64", "clash", "clashmeta",
//	         "singbox", "all.txt"
func SubLink(subDomain, subProxyPath, userUUID, subType, name string) string {
	b := fmt.Sprintf("https://%s/%s/%s/", subDomain, subProxyPath, userUUID)
	switch subType {
	case "", "auto":
		// base link auto-detects the client
	default:
		b += subType + "/"
	}
	if name != "" {
		b += "#" + url.PathEscape(name)
	}
	return b
}
