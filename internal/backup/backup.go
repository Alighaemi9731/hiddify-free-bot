// Package backup snapshots the database, compresses it and ships it to the
// bot's admins over Telegram.
package backup

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
)

// CreateAndSend makes a compressed snapshot, sends it to every admin and prunes
// old local snapshots (keeping the newest `keep`).
func CreateAndSend(tb *tele.Bot, store *db.Store, dir string, keep int, adminIDs []int64, caption string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ts := time.Now().In(db.Tehran).Format("2006-01-02_15-04")
	raw := filepath.Join(dir, "hidybot-"+ts+".db")
	gz := raw + ".gz"

	if err := store.Backup(raw); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	defer os.Remove(raw)
	if err := gzipFile(raw, gz); err != nil {
		return fmt.Errorf("gzip: %w", err)
	}

	doc := &tele.Document{File: tele.FromDisk(gz), FileName: filepath.Base(gz), Caption: caption}
	for _, id := range adminIDs {
		if _, err := tb.Send(tele.ChatID(id), doc); err != nil {
			// keep trying other admins
			continue
		}
	}
	prune(dir, keep)
	return nil
}

func gzipFile(src, dst string) error {
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
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Sync()
}

func prune(dir string, keep int) {
	if keep <= 0 {
		keep = 12
	}
	entries, err := filepath.Glob(filepath.Join(dir, "hidybot-*.db.gz"))
	if err != nil {
		return
	}
	if len(entries) <= keep {
		return
	}
	sort.Strings(entries) // timestamped names sort chronologically
	for _, old := range entries[:len(entries)-keep] {
		_ = os.Remove(old)
	}
}
