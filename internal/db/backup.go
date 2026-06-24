package db

import (
	"database/sql"
	"fmt"
	"io"
	"os"
)

// Backup writes a consistent snapshot of the database to dest using
// "VACUUM INTO", which is safe to run while the bot is live.
func (s *Store) Backup(dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(dest) // VACUUM INTO fails if the file exists
	_, err := s.db.Exec(`VACUUM INTO ?`, dest)
	return err
}

// Restore validates a candidate sqlite file, swaps it in for the live database
// and reopens the connection. Existing data is overwritten.
func (s *Store) Restore(srcPath string) error {
	if err := validateSQLite(srcPath); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}
	// Drop WAL/SHM side files so the restored DB is authoritative.
	_ = os.Remove(s.path + "-wal")
	_ = os.Remove(s.path + "-shm")
	if err := copyFile(srcPath, s.path); err != nil {
		return fmt.Errorf("copy restore: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(0)", s.path)
	nd, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	nd.SetMaxOpenConns(1)
	if err := nd.Ping(); err != nil {
		return err
	}
	s.db = nd
	// Make sure any new columns/tables exist after restoring an older backup.
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return err
	}
	for k, v := range defaultSettings {
		_, _ = s.db.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES(?,?)`, k, v)
	}
	return nil
}

// validateSQLite checks the file opens and contains the expected core tables.
func validateSQLite(path string) error {
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("فایل بکاپ نامعتبر است: %w", err)
	}
	defer d.Close()
	if err := d.Ping(); err != nil {
		return fmt.Errorf("فایل بکاپ یک دیتابیس معتبر نیست")
	}
	var n int
	err = d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN
		('users','panels','channels','settings')`).Scan(&n)
	if err != nil || n < 4 {
		return fmt.Errorf("ساختار فایل بکاپ با این ربات سازگار نیست")
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
