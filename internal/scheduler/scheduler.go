// Package scheduler runs the recurring background jobs: the Tehran-midnight
// daily reset + report, expired-config cleanup, hourly panel health checks and
// the 2-hourly backup.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/backup"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/config"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/hiddify"
)

// Scheduler owns the cron jobs and the small amount of state they need.
type Scheduler struct {
	tb    *tele.Bot
	store *db.Store
	cfg   *config.Config

	mu      sync.Mutex
	healthy map[int64]bool // panel id -> last known health (default healthy)
	warned  map[int64]bool // panel id -> near-capacity warning sent today
}

// Start wires up and launches the cron jobs (all evaluated in Tehran time).
func Start(tb *tele.Bot, store *db.Store, cfg *config.Config) *cron.Cron {
	s := &Scheduler{
		tb: tb, store: store, cfg: cfg,
		healthy: map[int64]bool{},
		warned:  map[int64]bool{},
	}
	c := cron.New(cron.WithLocation(db.Tehran))

	// Every 2 hours: backup and ship to admins.
	_, _ = c.AddFunc("0 */2 * * *", s.runBackup)

	// Hourly: panel health + near-capacity warnings.
	_, _ = c.AddFunc("0 * * * *", s.healthCheck)

	// Daily at Tehran midnight: send report, then reset budgets + clean up.
	_, _ = c.AddFunc("0 0 * * *", func() {
		s.sendDailyReport()
		if err := store.ResetPanelUsage(); err != nil {
			log.Printf("reset panel usage: %v", err)
		}
		s.mu.Lock()
		s.warned = map[int64]bool{}
		s.mu.Unlock()
		s.cleanupOldConfigs()
		log.Printf("daily reset done (Tehran midnight)")
	})

	c.Start()
	log.Printf("scheduler started (backup/2h, health/1h, daily report+reset at Tehran midnight)")
	return c
}

func (s *Scheduler) runBackup() {
	ids, _ := s.store.AdminIDs()
	if len(ids) == 0 {
		return
	}
	keep := s.store.GetInt(db.KeyBackupKeep)
	caption := "💾 بکاپ خودکار — " + time.Now().In(db.Tehran).Format("2006-01-02 15:04")
	if err := backup.CreateAndSend(s.tb, s.store, s.cfg.BackupDir(), keep, ids, caption); err != nil {
		log.Printf("scheduled backup failed: %v", err)
	}
}

// healthCheck pings each enabled panel and alerts admins on state changes and
// near-capacity conditions.
func (s *Scheduler) healthCheck() {
	panels, err := s.store.Panels()
	if err != nil {
		return
	}
	for _, p := range panels {
		if !p.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, perr := clientFor(p).Ping(ctx)
		cancel()

		s.mu.Lock()
		prev, seen := s.healthy[p.ID]
		if !seen {
			prev = true
		}
		now := perr == nil
		s.healthy[p.ID] = now
		warned := s.warned[p.ID]
		s.mu.Unlock()

		switch {
		case prev && !now:
			s.notifyAdmins(fmt.Sprintf("🚨 پنل «%s» در دسترس نیست!\n%v", p.Name, perr))
		case !prev && now:
			s.notifyAdmins(fmt.Sprintf("✅ پنل «%s» دوباره در دسترس است.", p.Name))
		}

		// Near-capacity warning (once per day).
		if now && p.DailyVolumeLimitGB > 0 {
			limitMB := p.DailyVolumeLimitGB * 1024
			if p.UsedTodayMB >= limitMB*90/100 && !warned {
				s.mu.Lock()
				s.warned[p.ID] = true
				s.mu.Unlock()
				s.notifyAdmins(fmt.Sprintf("⚠️ ظرفیت پنل «%s» رو به اتمام است: %d%% مصرف شده.",
					p.Name, p.UsedTodayMB*100/limitMB))
			}
		}
	}
}

// sendDailyReport summarises the day for admins (called just before the reset).
func (s *Scheduler) sendDailyReport() {
	ids, _ := s.store.AdminIDs()
	if len(ids) == 0 {
		return
	}
	today := db.TehranDay()
	now := time.Now().In(db.Tehran)
	startUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, db.Tehran).UTC().Format(time.RFC3339)

	st, _ := s.store.Stats(today)
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 گزارش روزانه (%s)\n\n", today)
	if st != nil {
		fmt.Fprintf(&sb, "👥 کل کاربران: %d\n🆕 کاربر جدید امروز: %d\n", st.TotalUsers, s.store.NewUsersToday(startUTC))
		fmt.Fprintf(&sb, "🎁 کانفیگ امروز: %d | کل: %d\n", st.ClaimsToday, st.ClaimsTotal)
	}
	if rev := s.store.TotalRevenue(); rev > 0 {
		fmt.Fprintf(&sb, "💰 درآمد کل تبلیغات: %s\n", fmtMoney(rev))
	}

	if panels, _ := s.store.Panels(); len(panels) > 0 {
		sb.WriteString("\n— مصرف پنل‌ها —\n")
		for _, p := range panels {
			limit := "نامحدود"
			if p.DailyVolumeLimitGB > 0 {
				limit = fmt.Sprintf("%dGB", p.DailyVolumeLimitGB)
			}
			fmt.Fprintf(&sb, "• %s: %dMB / %s\n", p.Name, p.UsedTodayMB, limit)
		}
	}

	if chs, _ := s.store.Channels(); len(chs) > 0 {
		var active []string
		for _, ch := range chs {
			if ch.Enabled && ch.Kind == "quota" && ch.Remaining() > 0 {
				name := ch.Title
				if name == "" {
					name = "@" + ch.Username
				}
				active = append(active, fmt.Sprintf("• %s: %d/%d (باقی %d)",
					name, ch.NewJoinsCount, ch.QuotaTarget, ch.Remaining()))
			}
		}
		if len(active) > 0 {
			sb.WriteString("\n— سفارش‌های فعال —\n" + strings.Join(active, "\n") + "\n")
		}
	}

	for _, id := range ids {
		_, _ = s.tb.Send(tele.ChatID(id), sb.String())
	}
}

func (s *Scheduler) notifyAdmins(text string) {
	ids, _ := s.store.AdminIDs()
	for _, id := range ids {
		_, _ = s.tb.Send(tele.ChatID(id), text)
	}
}

// cleanupOldConfigs deletes panel users from configs issued before today so the
// Hiddify panel doesn't accumulate stale daily users.
func (s *Scheduler) cleanupOldConfigs() {
	today := db.TehranDay()
	olds, err := s.store.ConfigsBefore(today)
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
			p, err := s.store.GetPanel(o.PanelID)
			if err != nil {
				_ = s.store.DeleteClaimRow(o.ID)
				continue
			}
			cl = clientFor(p)
			clients[o.PanelID] = cl
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := cl.DeleteUser(ctx, o.ConfigUUID); err != nil {
			log.Printf("delete panel user %s: %v", o.ConfigUUID, err)
		}
		cancel()
		_ = s.store.DeleteClaimRow(o.ID)
	}
	log.Printf("cleaned up %d expired configs", len(olds))
}

func clientFor(p *db.Panel) *hiddify.Client {
	return hiddify.New(&hiddify.AdminLinkParts{
		Domain: p.Domain, ProxyPath: p.AdminProxyPath, AdminUUID: p.AdminUUID,
	})
}

// fmtMoney renders an integer amount with thousands separators + "تومان".
func fmtMoney(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out) + " تومان"
}
