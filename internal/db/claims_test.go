package db

import (
	"testing"
	"time"
)

func TestOutboxClaimLifecycle(t *testing.T) {
	s := openTemp(t)
	if err := s.UpsertUser(42, "u", "U"); err != nil {
		t.Fatal(err)
	}
	day := "2026-06-26"
	id, err := s.InsertPendingClaim(&Claim{
		TGID: 42, ClaimDate: day, PanelID: 1, ConfigUUID: "uuid-x", SubLink: "sub", VolumeMB: 512,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	past := now.Add(-time.Hour).Format(time.RFC3339)
	future := now.Add(48 * time.Hour).Format(time.RFC3339)

	// Pending rows are never returned by ExpiredConfigs.
	if got, _ := s.ExpiredConfigs(future, past); len(got) != 0 {
		t.Fatalf("pending claim should not be expired-eligible, got %d", len(got))
	}

	// Confirm → active, sets last_claim_date and panel_user_id.
	if err := s.ConfirmClaim(id, 42, 1, 777, 512, day); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUser(42); u == nil || u.LastClaimDate != day {
		t.Fatalf("last_claim_date not set after confirm")
	}

	// Not expired yet (expire_at ≈ now+24h): now-based query excludes it.
	if got, _ := s.ExpiredConfigs(now.Format(time.RFC3339), past); len(got) != 0 {
		t.Fatalf("fresh active claim must not be expired, got %d", len(got))
	}
	// Far-future "now" → expired; carries the cached panel_user_id.
	got, _ := s.ExpiredConfigs(future, past)
	if len(got) != 1 || got[0].PanelUserID != 777 || got[0].ConfigUUID != "uuid-x" {
		t.Fatalf("expected 1 expired row with id 777, got %+v", got)
	}

	if err := s.DeleteClaimRows([]int64{got[0].ID}); err != nil {
		t.Fatal(err)
	}
	if g2, _ := s.ExpiredConfigs(future, past); len(g2) != 0 {
		t.Fatalf("row should be gone after DeleteClaimRows")
	}
}
