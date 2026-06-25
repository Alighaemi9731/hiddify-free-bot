package db

import "testing"

func TestUserListSearchDelete(t *testing.T) {
	store := openTemp(t)

	for _, u := range []struct {
		id   int64
		name string
	}{{1, "ali"}, {2, "sara"}, {3, "reza"}} {
		if err := store.UpsertUser(u.id, u.name, u.name); err != nil {
			t.Fatal(err)
		}
	}

	if n := store.CountUsers(); n != 3 {
		t.Fatalf("CountUsers = %d, want 3", n)
	}

	// Paging: page size 2.
	page1, err := store.ListUsers(0, 2)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1 len=%d err=%v", len(page1), err)
	}
	page2, _ := store.ListUsers(2, 2)
	if len(page2) != 1 {
		t.Fatalf("page2 len=%d, want 1", len(page2))
	}

	// Search by username (case-insensitive, '@' tolerated).
	u, err := store.FindUserByUsername("@SARA")
	if err != nil || u == nil || u.TGID != 2 {
		t.Fatalf("FindUserByUsername = %+v err=%v", u, err)
	}

	// Delete cascades.
	_, _ = store.AddChannel(&Channel{ChatID: -9, Kind: "quota", QuotaTarget: 5})
	ch, _ := store.GetChannelByChat(-9)
	_ = store.AssignChannel(ch.ID, 2, "2026-06-25", false)
	_ = store.SetReferrer(2, 1)
	if err := store.DeleteUserFull(2); err != nil {
		t.Fatal(err)
	}
	if store.UserExists(2) {
		t.Error("user 2 still exists after delete")
	}
	if was, _ := store.ChannelUserState(ch.ID, 2); was {
		t.Error("channel_user row not deleted")
	}
	if store.CountUsers() != 2 {
		t.Errorf("CountUsers after delete = %d, want 2", store.CountUsers())
	}
}
