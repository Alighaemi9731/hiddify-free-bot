package hiddify

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveCreateDelete exercises the real panel end-to-end. It is skipped unless
// HIDDIFY_LIVE=1 and HIDDIFY_ADMIN_LINK / HIDDIFY_SUB_LINK are set, so it never
// runs in CI.
//
//	HIDDIFY_LIVE=1 \
//	HIDDIFY_ADMIN_LINK='https://.../proxy/uuid/admin/' \
//	HIDDIFY_SUB_LINK='https://.../subproxy/uuid/#x' \
//	go test ./internal/hiddify/ -run TestLiveCreateDelete -v
func TestLiveCreateDelete(t *testing.T) {
	if os.Getenv("HIDDIFY_LIVE") != "1" {
		t.Skip("set HIDDIFY_LIVE=1 to run the live panel test")
	}
	ap, err := ParseAdminLink(os.Getenv("HIDDIFY_ADMIN_LINK"))
	if err != nil {
		t.Fatalf("parse admin link: %v", err)
	}
	sp, err := ParseSubLink(os.Getenv("HIDDIFY_SUB_LINK"))
	if err != nil {
		t.Fatalf("parse sub link: %v", err)
	}
	t.Logf("admin domain=%s proxy=%s sub domain=%s proxy=%s", ap.Domain, ap.ProxyPath, sp.Domain, sp.ProxyPath)

	cl := New(ap)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ver, err := cl.Ping(ctx)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Logf("panel version: %s", ver)

	created, err := cl.CreateUser(ctx, User{
		Name:         "bot-selftest",
		UsageLimitGB: 0.5,
		PackageDays:  1,
		Mode:         "no_reset",
		Comment:      "automated self-test, safe to delete",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Logf("created uuid: %s", created.UUID)

	// Always clean up.
	defer func() {
		if err := cl.DeleteUser(context.Background(), created.UUID); err != nil {
			t.Errorf("delete user: %v", err)
		} else {
			t.Logf("deleted test user ok")
		}
	}()

	got, err := cl.GetUser(ctx, created.UUID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.UUID != created.UUID {
		t.Fatalf("uuid mismatch: %s != %s", got.UUID, created.UUID)
	}

	sub := SubLink(sp.Domain, sp.ProxyPath, created.UUID, "auto", "bot-selftest")
	t.Logf("sub link: %s", sub)

	// Fetch the sub link and confirm it returns real config content.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, sub, nil)
	req.Header.Set("User-Agent", "v2rayNG/1.8.0")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("fetch sub: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	t.Logf("sub HTTP %d, %d bytes", resp.StatusCode, len(body))
	if resp.StatusCode != 200 {
		t.Fatalf("sub link returned HTTP %d", resp.StatusCode)
	}
	s := string(body)
	if !(strings.Contains(s, "vless://") || strings.Contains(s, "vmess://") ||
		strings.Contains(s, "trojan://") || strings.Contains(s, "ss://") || len(body) > 100) {
		t.Fatalf("sub link body doesn't look like a config: %.120q", s)
	}
}
