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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

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
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) base() string {
	return fmt.Sprintf("https://%s/%s/api/v2", c.domain, c.proxyPath)
}

// User mirrors the Hiddify user object (only the fields we care about).
type User struct {
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

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Hiddify-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("اتصال به پنل ناموفق بود: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("پنل خطا داد (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("پاسخ پنل قابل خواندن نبود: %w", err)
		}
	}
	return nil
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

// CreateUser creates a user and returns the resulting object (with uuid).
func (c *Client) CreateUser(ctx context.Context, u User) (*User, error) {
	if u.UUID == "" {
		u.UUID = uuid.NewString()
	}
	if u.Mode == "" {
		u.Mode = "no_reset"
	}
	u.Enable = true
	var out User
	if err := c.do(ctx, http.MethodPost, "/admin/user/", u, &out); err != nil {
		return nil, err
	}
	// Some panel versions echo an empty body; fall back to what we sent.
	if out.UUID == "" {
		out = u
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
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		return nil
	}
	return err
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
