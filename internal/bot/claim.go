package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/hiddify"
)

func (b *Bot) handleGetConfig(c tele.Context) error { return b.tryClaim(c) }

func (b *Bot) cbVerifyMembership(c tele.Context) error {
	_ = c.Respond(&tele.CallbackResponse{Text: "در حال بررسی عضویت..."})
	return b.tryClaim(c)
}

// tryClaim runs the full daily-config flow: gate checks -> assign today's
// channels -> verify membership (crediting new joins) -> issue the config.
func (b *Bot) tryClaim(c tele.Context) error {
	uid := c.Sender().ID
	if !b.store.UserExists(uid) {
		s := c.Sender()
		_ = b.store.UpsertUser(uid, s.Username, s.FirstName)
	}
	u, err := b.store.GetUser(uid)
	if err != nil {
		return c.Send("خطا در دریافت اطلاعات کاربر.")
	}
	if u.Banned {
		return c.Send("⛔️ دسترسی شما به ربات مسدود شده است.")
	}
	if b.store.GetBool(db.KeyMaintenance) && !b.isAdmin(uid) {
		return c.Send("🛠 ربات موقتاً در حال تعمیر است. لطفاً بعداً امتحان کنید.")
	}

	today := db.TehranDay()
	if u.LastClaimDate == today {
		return c.Send(fmt.Sprintf("✅ کانفیگ امروزت رو گرفتی!\n⏰ کانفیگ بعدی تا %s دیگه فعال میشه.", untilTehranMidnight()))
	}

	// Today's channel set (stable within the day).
	chs, err := b.store.TodaysChannels(uid, today)
	if err != nil {
		return c.Send("خطای داخلی. بعداً امتحان کنید.")
	}
	if len(chs) == 0 {
		n := b.store.GetInt(db.KeyRandomChannelsPerDay)
		picked, err := b.store.PickChannelsForUser(uid, n)
		if err != nil {
			return c.Send("خطای داخلی. بعداً امتحان کنید.")
		}
		for _, ch := range picked {
			was, merr := b.isMember(ch.ChatID, uid)
			if merr != nil {
				log.Printf("snapshot membership ch=%d user=%d: %v", ch.ChatID, uid, merr)
			}
			_ = b.store.AssignChannel(ch.ID, uid, today, was)
		}
		chs = picked
	}

	// Verify membership for each assigned channel; credit NEW joins.
	var notJoined []*db.Channel
	for _, ch := range chs {
		member, merr := b.isMember(ch.ChatID, uid)
		if merr != nil {
			// Bot likely isn't admin in that channel — don't block the user,
			// just warn the operator and skip this channel as a requirement.
			log.Printf("membership check ch=%d (%s) user=%d: %v", ch.ChatID, ch.Title, uid, merr)
			continue
		}
		if member {
			was, counted := b.store.ChannelUserState(ch.ID, uid)
			if !counted {
				if !was {
					if done, _ := b.store.CreditNewJoin(ch.ID, uid); done {
						b.notifyOrderComplete(ch)
					}
				} else {
					_ = b.store.MarkCounted(ch.ID, uid)
				}
			}
			continue
		}
		notJoined = append(notJoined, ch)
	}

	if len(notJoined) > 0 {
		return b.sendJoinPrompt(c, notJoined)
	}
	return b.issueConfig(c, u, today)
}

// sendJoinPrompt asks the user to join the channels they're still missing.
func (b *Bot) sendJoinPrompt(c tele.Context, channels []*db.Channel) error {
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, ch := range channels {
		url := channelJoinURL(ch)
		label := "➕ عضویت در " + channelDisplay(ch)
		if url != "" {
			rows = append(rows, m.Row(m.URL(label, url)))
		}
	}
	rows = append(rows, m.Row(m.Data("✅ عضو شدم، بررسی کن", cbVerify, "go")))
	m.Inline(rows...)
	return c.Send("📣 برای دریافت کانفیگ رایگان امروز، اول در کانال‌های زیر عضو شو و بعد دکمه «✅ عضو شدم» رو بزن:", m)
}

// issueConfig creates a Hiddify user and sends the subscription link, using an
// outbox (pending→active) so a crash mid-create can never orphan a panel user,
// and falling through to the next panel if one reports it's full.
func (b *Bot) issueConfig(c tele.Context, u *db.User, today string) error {
	capMB := b.store.DailyCapMB(u)

	panels, err := b.store.PanelsForVolume(capMB)
	if err != nil {
		return c.Send("خطای داخلی هنگام انتخاب پنل.")
	}
	if len(panels) == 0 {
		return c.Send("😔 فعلاً ظرفیت کانفیگ رایگان تموم شده. لطفاً بعداً دوباره امتحان کن.")
	}

	tgID := u.TGID
	name := fmt.Sprintf("free-%d-%s", u.TGID, today)
	cfgUUID := uuid.NewString()
	configDays := b.store.GetInt(db.KeyConfigDays)

	var panel *db.Panel
	var sub string
	allFull := true
	for _, p := range panels {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		subLink := hiddify.SubLink(p.SubDomain, p.SubProxyPath, cfgUUID, p.SubType, name)

		// Outbox: write the DB row (status=pending) BEFORE creating the panel user.
		claimID, ierr := b.store.InsertPendingClaim(&db.Claim{
			TGID: u.TGID, ClaimDate: today, PanelID: p.ID,
			ConfigUUID: cfgUUID, SubLink: subLink, VolumeMB: capMB,
		}, configDays)
		if ierr != nil {
			cancel()
			log.Printf("insert pending claim: %v", ierr)
			return c.Send("خطای داخلی. بعداً امتحان کنید.")
		}

		created, cerr := clientFor(p).CreateUser(ctx, hiddify.User{
			UUID:         cfgUUID,
			Name:         name,
			UsageLimitGB: float64(capMB) / 1024.0,
			PackageDays:  configDays,
			Mode:         "no_reset",
			TelegramID:   &tgID,
			Comment:      "free-daily-bot",
			Lang:         "fa",
		})
		cancel()

		if cerr != nil {
			_ = b.store.DeleteClaimRow(claimID) // roll back the pending row
			if errors.Is(cerr, hiddify.ErrPanelFull) {
				log.Printf("panel %d full, trying next", p.ID)
				continue // try the next panel
			}
			allFull = false
			log.Printf("create user on panel %d: %v", p.ID, cerr)
			continue
		}

		// Success → confirm the claim (status=active, usage, last_claim_date).
		if err := b.store.ConfirmClaim(claimID, u.TGID, p.ID, created.ID, capMB, today); err != nil {
			log.Printf("confirm claim: %v", err)
		}
		panel, sub = p, subLink
		break
	}

	if panel == nil {
		if allFull {
			return c.Send("😔 فعلاً ظرفیت کانفیگ رایگان تموم شده. لطفاً بعداً دوباره امتحان کن.")
		}
		return c.Send("⚠️ خطا در ساخت کانفیگ. لطفاً چند دقیقه دیگه دوباره امتحان کن.")
	}

	// Credit the inviter the first time this user actually claims.
	if refID, _ := b.store.CountReferralIfPending(u.TGID); refID != 0 {
		_, _ = b.tb.Send(tele.ChatID(refID),
			"🎉 یک نفر با لینک دعوت شما کانفیگ گرفت!\nحجم روزانه‌ی شما افزایش پیدا کرد. 🚀")
	}

	// Deliver raw configs instead of the sub link when configured to do so.
	if b.store.Get(db.KeyDeliveryMode) == "configs" {
		if err := b.deliverConfigs(c, sub, capMB); err == nil {
			return nil
		} else {
			log.Printf("deliver configs (falling back to link): %v", err)
		}
	}

	msg := fmt.Sprintf(`✅ کانفیگ رایگان امروز شما آماده‌ست!

📦 حجم: %s
⏳ این لینک را کپی کن و در برنامه‌ات Import کن:

`+"`%s`"+`

برای آموزش اتصال دکمه «❓ راهنما و آموزش اتصال» رو بزن.`, fmtVol(capMB), sub)
	return c.Send(msg, tele.ModeMarkdown)
}

// deliverConfigs fetches the actual configs from the sub link and sends them to
// the user (as copyable lines, or a .txt file if too long).
func (b *Bot) deliverConfigs(c tele.Context, sub string, capMB int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cfgs, err := hiddify.FetchConfigs(ctx, sub)
	if err != nil {
		return err
	}

	header := fmt.Sprintf("✅ کانفیگ رایگان امروز شما (حجم: %s):\n\nکانفیگ زیر را کپی و در برنامه‌ات Import کن 👇", fmtVol(capMB))
	joined := strings.Join(cfgs, "\n")

	// One copyable code block keeps each config on its own line; fall back to a
	// .txt attachment when the message would exceed Telegram's limit.
	body := "```\n" + joined + "\n```"
	if len(header)+len(body) <= 3800 {
		return c.Send(header+"\n\n"+body, tele.ModeMarkdown)
	}
	if err := c.Send(header); err != nil {
		return err
	}
	doc := &tele.Document{File: tele.FromReader(strings.NewReader(joined)), FileName: "configs.txt"}
	return c.Send(doc)
}

// notifyOrderComplete alerts admins when a quota channel reaches its target.
func (b *Bot) notifyOrderComplete(ch *db.Channel) {
	revenue := ch.QuotaTarget * ch.PricePer1k / 1000
	msg := fmt.Sprintf("✅ سفارش تبلیغ تکمیل شد!\n\n📣 کانال: %s\n🎯 جوین هدف: %d (تکمیل شد)",
		channelDisplay(ch), ch.QuotaTarget)
	if ch.Advertiser != "" {
		msg += "\n👤 سفارش‌دهنده: " + ch.Advertiser
	}
	if revenue > 0 {
		msg += "\n💰 درآمد این سفارش: " + fmtMoney(revenue)
	}
	b.notifyAdmins(msg)
}

func channelDisplay(ch *db.Channel) string {
	if ch.Title != "" {
		return ch.Title
	}
	if ch.Username != "" {
		return "@" + ch.Username
	}
	return "کانال"
}

func channelJoinURL(ch *db.Channel) string {
	if ch.Username != "" {
		return "https://t.me/" + ch.Username
	}
	return ch.InviteLink
}
