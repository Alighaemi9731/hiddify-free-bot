// Package db is the SQLite persistence layer (pure-Go driver).
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Tehran is the timezone used for all daily resets.
var Tehran = mustLoadTehran()

func mustLoadTehran() *time.Location {
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		// Fallback: fixed +03:30 (Iran abolished DST in 2022).
		return time.FixedZone("IRST", 3*3600+30*60)
	}
	return loc
}

// TehranDay returns today's Tehran calendar date as YYYY-MM-DD.
func TehranDay() string { return time.Now().In(Tehran).Format("2006-01-02") }

// NowUTC returns the current time formatted as RFC3339 (UTC).
func NowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// Store wraps the database connection.
type Store struct {
	db   *sql.DB
	path string
	mu   sync.Mutex // guards backup/restore swaps
}

// Open opens (creating if needed) the sqlite database, enables WAL and applies
// the schema + default settings.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(0)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // sqlite: single writer keeps things simple & safe
	if err := d.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: d, path: path}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	for k, v := range defaultSettings {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES(?,?)`, k, v); err != nil {
			return err
		}
	}
	return nil
}

// DB exposes the raw connection (used by backup/restore).
func (s *Store) DB() *sql.DB { return s.db }

// Lock/Unlock guard backup file swaps.
func (s *Store) Lock()   { s.mu.Lock() }
func (s *Store) Unlock() { s.mu.Unlock() }

// Close closes the connection.
func (s *Store) Close() error { return s.db.Close() }

// ---- settings helpers ----

func (s *Store) Get(key string) string {
	var v string
	_ = s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) GetInt(key string) int {
	n, _ := strconv.Atoi(s.Get(key))
	return n
}

func (s *Store) GetBool(key string) bool { return s.Get(key) == "1" }

func (s *Store) Set(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) SetInt(key string, v int) error { return s.Set(key, strconv.Itoa(v)) }
func (s *Store) SetBool(key string, v bool) error {
	if v {
		return s.Set(key, "1")
	}
	return s.Set(key, "0")
}

// Audit records an admin action.
func (s *Store) Audit(actor int64, action, detail string) {
	_, _ = s.db.Exec(`INSERT INTO audit_log(actor_id,action,detail,created_at) VALUES(?,?,?,?)`,
		actor, action, detail, NowUTC())
}
