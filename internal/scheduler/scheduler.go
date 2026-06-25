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

	// Every 30 min: bulk-delete truly-expired configs + reconcile crashed creates.
	_, _ = c.AddFunc("*/30 * * * *", s.cleanupExpiredConfigs)

	// Daily at Tehran midnight: send report, then reset budgets.
	_, _ = c.AddFunc("0 0 * * *", func() {
		s.sendDailyReport()
		if err := store.ResetPanelUsage(); err != nil {
			log.Printf("reset panel usage: %v", err)
		}
		s.mu.Lock()
		s.warned = map[int64]bool{}
		s.mu.Unlock()
		log.Printf("daily reset done (Tehran midnight)")
	})

	c.Start()
	log.Printf("scheduler started (backup/2h, health/1h, cleanup/30m, report+reset at Tehran midnight)")
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

// cleanupExpiredConfigs removes panel users whose configs have truly expired
// (≥ their lifetime), grouped per panel and deleted with ONE bulk Flask-Admin
// action per chunk → a single server-side apply instead of one per user. It
// also reconciles crashed creates (pending rows).
func (s *Scheduler) cleanupExpiredConfigs() {
	s.reconcilePending()

	now := time.Now().UTC()
	configDays := s.store.GetInt(db.KeyConfigDays)
	if configDays < 1 {
		configDays = 1
	}
	fallback := now.Add(-time.Duration(configDays) * 24 * time.Hour).Format(time.RFC3339)
	olds, err := s.store.ExpiredConfigs(now.Format(time.RFC3339), fallback)
	if err != nil {
		log.Printf("list expired configs: %v", err)
		return
	}
	if len(olds) == 0 {
		return
	}

	chunkSize := s.store.GetInt(db.KeyCleanupChunk)
	if chunkSize < 1 {
		chunkSize = 400
	}

	// Group expired configs by panel.
	byPanel := map[int64][]db.OldConfig{}
	for _, o := range olds {
		byPanel[o.PanelID] = append(byPanel[o.PanelID], o)
	}

	total := 0
	for panelID, rows := range byPanel {
		p, err := s.store.GetPanel(panelID)
		if err != nil { // panel gone → just drop the local rows
			_ = s.store.DeleteClaimRows(idsOf(rows))
			continue
		}
		cl := clientFor(p)

		// Resolve numeric ids: prefer the cached panel_user_id, else look it up.
		var ids []int             // panel rowids to bulk-delete
		var handledClaims []int64 // claim rows covered by this delete (drop on success)
		var doneClaims []int64    // already-gone rows (drop unconditionally)
		var needResolve []string  // uuids missing a cached id
		uuidToClaim := map[string]int64{}
		for _, o := range rows {
			uuidToClaim[o.ConfigUUID] = o.ID
			if o.PanelUserID > 0 {
				ids = append(ids, o.PanelUserID)
				handledClaims = append(handledClaims, o.ID)
			} else {
				needResolve = append(needResolve, o.ConfigUUID)
			}
		}
		if len(needResolve) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			resolved, missing := cl.ResolveUserIDs(ctx, needResolve)
			cancel()
			for u, id := range resolved {
				ids = append(ids, id)
				handledClaims = append(handledClaims, uuidToClaim[u])
			}
			for _, u := range missing { // 404 → already gone, drop the row
				doneClaims = append(doneClaims, uuidToClaim[u])
			}
			// uuids that failed transiently are in neither set → kept for next run.
		}

		// Chunked bulk delete = one apply per chunk. Only drop the handled claim
		// rows if every chunk succeeded (else keep them to retry).
		ok := true
		for start := 0; start < len(ids); start += chunkSize {
			end := start + chunkSize
			if end > len(ids) {
				end = len(ids)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			if err := cl.BulkUserAction(ctx, "delete", ids[start:end]); err != nil {
				log.Printf("bulk delete panel %d: %v", panelID, err)
				ok = false
				cancel()
				break // keep rows → retry next run
			}
			cancel()
		}
		if ok {
			doneClaims = append(doneClaims, handledClaims...)
		}
		if err := s.store.DeleteClaimRows(dedupeIDs(doneClaims)); err != nil {
			log.Printf("delete claim rows panel %d: %v", panelID, err)
		}
		total += len(doneClaims)
	}
	if total > 0 {
		log.Printf("cleanup: removed %d expired configs (bulk)", total)
	}
}

// reconcilePending fixes claims whose create may have crashed: pending rows
// older than 10 min are checked against the panel — present → activate, 404 → drop.
func (s *Scheduler) reconcilePending() {
	cutoff := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	pend, err := s.store.PendingClaimsOlderThan(cutoff)
	if err != nil || len(pend) == 0 {
		return
	}
	for _, o := range pend {
		p, err := s.store.GetPanel(o.PanelID)
		if err != nil {
			_ = s.store.DeleteClaimRow(o.ID)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		u, err := clientFor(p).GetUser(ctx, o.ConfigUUID)
		cancel()
		if err != nil {
			_ = s.store.DeleteClaimRow(o.ID) // 404 / unreachable → free the slot
			continue
		}
		_ = s.store.MarkClaimActive(o.ID, u.ID)
	}
}

func idsOf(rows []db.OldConfig) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func dedupeIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
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
