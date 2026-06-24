// Package scheduler runs the recurring background jobs: the Tehran-midnight
// daily reset, expired-config cleanup and the 2-hourly backup.
package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/backup"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/config"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/hiddify"
)

// Start wires up and launches the cron jobs (all evaluated in Tehran time).
func Start(tb *tele.Bot, store *db.Store, cfg *config.Config) *cron.Cron {
	c := cron.New(cron.WithLocation(db.Tehran))

	// Every 2 hours: backup and ship to admins.
	_, _ = c.AddFunc("0 */2 * * *", func() { runBackup(tb, store, cfg) })

	// Daily at Tehran midnight: reset panel budgets + claim gate, clean up.
	_, _ = c.AddFunc("0 0 * * *", func() {
		if err := store.ResetPanelUsage(); err != nil {
			log.Printf("reset panel usage: %v", err)
		}
		cleanupOldConfigs(store)
		log.Printf("daily reset done (Tehran midnight)")
	})

	c.Start()
	log.Printf("scheduler started (backups every 2h, daily reset at Tehran midnight)")
	return c
}

func runBackup(tb *tele.Bot, store *db.Store, cfg *config.Config) {
	ids, _ := store.AdminIDs()
	if len(ids) == 0 {
		return
	}
	keep := store.GetInt(db.KeyBackupKeep)
	caption := "💾 بکاپ خودکار — " + time.Now().In(db.Tehran).Format("2006-01-02 15:04")
	if err := backup.CreateAndSend(tb, store, cfg.BackupDir(), keep, ids, caption); err != nil {
		log.Printf("scheduled backup failed: %v", err)
	}
}

// cleanupOldConfigs deletes panel users from configs issued before today so the
// Hiddify panel doesn't accumulate stale daily users.
func cleanupOldConfigs(store *db.Store) {
	today := db.TehranDay()
	olds, err := store.ConfigsBefore(today)
	if err != nil {
		log.Printf("list old configs: %v", err)
		return
	}
	if len(olds) == 0 {
		return
	}
	clients := map[int64]*hiddify.Client{}
	for _, o := range olds {
		cl, ok := clients[o.PanelID]
		if !ok {
			p, err := store.GetPanel(o.PanelID)
			if err != nil {
				// Panel was removed; just drop the claim row.
				_ = store.DeleteClaimRow(o.ID)
				continue
			}
			cl = hiddify.New(&hiddify.AdminLinkParts{
				Domain: p.Domain, ProxyPath: p.AdminProxyPath, AdminUUID: p.AdminUUID,
			})
			clients[o.PanelID] = cl
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := cl.DeleteUser(ctx, o.ConfigUUID); err != nil {
			log.Printf("delete panel user %s: %v", o.ConfigUUID, err)
		}
		cancel()
		_ = store.DeleteClaimRow(o.ID)
	}
	log.Printf("cleaned up %d expired configs", len(olds))
}
