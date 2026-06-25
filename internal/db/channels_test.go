package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestChannelRevenue(t *testing.T) {
	c := Channel{NewJoinsCount: 2500, PricePer1k: 50000}
	if got := c.Revenue(); got != 125000 {
		t.Errorf("Revenue() = %d, want 125000", got)
	}
	if (Channel{NewJoinsCount: 999, PricePer1k: 1000}).Revenue() != 999 {
		t.Errorf("partial-thousand revenue wrong")
	}
}

func TestPriceRoundTrip(t *testing.T) {
	store := openTemp(t)
	id, err := store.AddChannel(&Channel{
		ChatID: -100123, Title: "ad", Kind: "quota", QuotaTarget: 1000,
		PricePer1k: 50000, Advertiser: "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := store.GetChannel(id)
	if err != nil {
		t.Fatal(err)
	}
	if ch.PricePer1k != 50000 || ch.Advertiser != "acme" {
		t.Errorf("got price=%d advertiser=%q", ch.PricePer1k, ch.Advertiser)
	}
	got, _ := store.GetChannelByChat(-100123)
	if got == nil || got.ID != id {
		t.Errorf("GetChannelByChat mismatch")
	}
}

// TestMigrationAddsColumns creates a DB whose channels table predates the
// revenue columns, then confirms Open() adds them without error.
func TestMigrationAddsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors the v1.0.x channels schema (before the revenue columns).
	_, err = raw.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER, title TEXT DEFAULT '',
		username TEXT DEFAULT '', invite_link TEXT DEFAULT '', kind TEXT DEFAULT 'quota',
		quota_target INTEGER DEFAULT 0, new_joins_count INTEGER DEFAULT 0,
		priority INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, is_join_request INTEGER DEFAULT 0,
		added_at TEXT DEFAULT '')`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Exec(`INSERT INTO channels(chat_id,title,kind,quota_target,new_joins_count,enabled)
		VALUES(-1,'old','quota',100,10,1)`)
	raw.Close()

	store, err := Open(path) // should ALTER TABLE to add the new columns
	if err != nil {
		t.Fatalf("Open after old schema: %v", err)
	}
	defer store.Close()
	ch, err := store.GetChannelByChat(-1)
	if err != nil {
		t.Fatalf("read migrated channel: %v", err)
	}
	if ch.Title != "old" || ch.PricePer1k != 0 {
		t.Errorf("migrated row wrong: %+v", ch)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
