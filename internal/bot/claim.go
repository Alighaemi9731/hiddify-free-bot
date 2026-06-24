package bot

import (
	"context"
	"fmt"
	"log"
	"time"

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
					_ = b.store.CreditNewJoin(ch.ID, uid)
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

// issueConfig creates a Hiddify user and sends the subscription link.
func (b *Bot) issueConfig(c tele.Context, u *db.User, today string) error {
	capMB := b.store.DailyCapMB(u)

	panel, err := b.store.PickPanelForVolume(capMB)
	if err != nil {
		return c.Send("خطای داخلی هنگام انتخاب پنل.")
	}
	if panel == nil {
		return c.Send("😔 فعلاً ظرفیت کانفیگ رایگان تموم شده. لطفاً بعداً دوباره امتحان کن.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := clientFor(panel)
	tgID := u.TGID
	name := fmt.Sprintf("free-%d-%s", u.TGID, today)
	created, err := client.CreateUser(ctx, hiddify.User{
		Name:         name,
		UsageLimitGB: float64(capMB) / 1024.0,
		PackageDays:  b.store.GetInt(db.KeyConfigDays),
		Mode:         "no_reset",
		TelegramID:   &tgID,
		Comment:      "free-daily-bot",
		Lang:         "fa",
	})
	if err != nil {
		log.Printf("create user on panel %d: %v", panel.ID, err)
		return c.Send("⚠️ خطا در ساخت کانفیگ. لطفاً چند دقیقه دیگه دوباره امتحان کن.")
	}

	sub := hiddify.SubLink(panel.SubDomain, panel.SubProxyPath, created.UUID, panel.SubType, name)
	if err := b.store.RecordClaim(&db.Claim{
		TGID: u.TGID, ClaimDate: today, PanelID: panel.ID,
		ConfigUUID: created.UUID, SubLink: sub, VolumeMB: capMB,
	}); err != nil {
		log.Printf("record claim: %v", err)
	}

	// Credit the inviter the first time this user actually claims.
	if refID, _ := b.store.CountReferralIfPending(u.TGID); refID != 0 {
		_, _ = b.tb.Send(tele.ChatID(refID),
			"🎉 یک نفر با لینک دعوت شما کانفیگ گرفت!\nحجم روزانه‌ی شما افزایش پیدا کرد. 🚀")
	}

	msg := fmt.Sprintf(`✅ کانفیگ رایگان امروز شما آماده‌ست!

📦 حجم: %s
⏳ این لینک را کپی کن و در برنامه‌ات Import کن:

`+"`%s`"+`

برای آموزش اتصال دکمه «❓ راهنما و آموزش اتصال» رو بزن.`, fmtVol(capMB), sub)
	return c.Send(msg, tele.ModeMarkdown)
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
