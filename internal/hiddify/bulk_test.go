package hiddify

import (
	"context"
	"errors"
	"html"
	"os"
	"testing"
	"time"
)

// TestLiveBulkDelete runs the full create→resolve→bulk-delete path against a real
// panel when HIDDIFY_ADMIN_LINK is set. It creates ONE throwaway user and removes
// it. Never point this at a customer's admin.
func TestLiveBulkDelete(t *testing.T) {
	link := os.Getenv("HIDDIFY_ADMIN_LINK")
	if link == "" {
		t.Skip("set HIDDIFY_ADMIN_LINK to run")
	}
	ap, err := ParseAdminLink(link)
	if err != nil {
		t.Fatal(err)
	}
	c := New(ap)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	created, err := c.CreateUser(ctx, User{Name: "zz-bulktest-delete-me", UsageLimitGB: 0.1, PackageDays: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("created uuid=%s id=%d", created.UUID, created.ID)

	resolved, missing := c.ResolveUserIDs(ctx, []string{created.UUID})
	id, ok := resolved[created.UUID]
	if !ok {
		t.Fatalf("resolve: resolved=%v missing=%v", resolved, missing)
	}
	if err := c.BulkUserAction(ctx, "delete", []int{id}); err != nil {
		t.Fatalf("bulk delete: %v", err)
	}
	// Should now be gone.
	if _, err := c.GetUser(ctx, created.UUID); err == nil {
		t.Errorf("user still present after bulk delete")
	} else {
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Logf("post-delete GET error (expected 404): %v", err)
		}
	}
}

func TestCSRFRegex(t *testing.T) {
	page := `<form><input name="csrf_token" type="hidden" value="20260626##abc&amp;def"></form>`
	m := csrfRe.FindSubmatch([]byte(page))
	if m == nil {
		t.Fatal("csrf token not found")
	}
	if got := html.UnescapeString(string(m[1])); got != "20260626##abc&def" {
		t.Errorf("csrf = %q", got)
	}
}

func TestPanelRootAndBase(t *testing.T) {
	c := New(&AdminLinkParts{Domain: "d.example", ProxyPath: "pp", AdminUUID: "k"})
	if c.panelRoot() != "https://d.example/pp" {
		t.Errorf("panelRoot = %q", c.panelRoot())
	}
	if c.base() != "https://d.example/pp/api/v2" {
		t.Errorf("base = %q", c.base())
	}
	if c.lockKey() != "d.example/pp" {
		t.Errorf("lockKey = %q", c.lockKey())
	}
}
