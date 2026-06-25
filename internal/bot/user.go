package bot

import (
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/i18n"
)

func (b *Bot) onStart(c tele.Context) error {
	u := c.Sender()
	isNew := !b.store.UserExists(u.ID)

	// Block brand-new sign-ups when the admin has paused intake.
	if isNew && !b.isAdmin(u.ID) && !b.store.GetBool(db.KeyAcceptingNewUsers) {
		return c.Send("⛔️ در حال حاضر ثبت‌نام کاربر جدید بسته است. بعداً دوباره امتحان کنید.")
	}

	_ = b.store.UpsertUser(u.ID, u.Username, u.FirstName)

	if payload := strings.TrimSpace(c.Message().Payload); strings.HasPrefix(payload, "ref_") {
		if rid, err := strconv.ParseInt(payload[4:], 10, 64); err == nil {
			_ = b.store.SetReferrer(u.ID, rid)
		}
	}

	if b.isAdmin(u.ID) {
		return b.showAdminMenu(c, "🛠 پنل مدیریت\nبه ربات مدیریت خوش اومدی.")
	}
	return b.showUserMenu(c, i18n.WelcomeUser)
}

// onText dispatches reply-keyboard buttons and FSM text input.
func (b *Bot) onText(c tele.Context) error {
	uid := c.Sender().ID
	txt := strings.TrimSpace(c.Text())

	if txt == i18n.BtnCancel {
		b.clearState(uid)
		if b.isAdmin(uid) {
			return b.showAdminMenu(c, "لغو شد.")
		}
		return b.showUserMenu(c, "لغو شد.")
	}

	// Mid-flow text goes to the active FSM handler.
	if st := b.getState(uid); st != nil {
		return b.handleStateInput(c, st)
	}

	switch txt {
	// user menu
	case i18n.BtnGetConfig:
		return b.handleGetConfig(c)
	case i18n.BtnAccount:
		return b.handleAccount(c)
	case i18n.BtnReferral:
		return b.handleReferral(c)
	case i18n.BtnStatus:
		return b.handleStatus(c)
	case i18n.BtnAdvertise:
		return b.handleAdvertise(c)
	case i18n.BtnHelp:
		return c.Send(i18n.HelpText)
	}

	if b.isAdmin(uid) {
		switch txt {
		case i18n.BtnAdminStats:
			return b.adminStats(c)
		case i18n.BtnAdminChannels:
			return b.adminChannels(c)
		case i18n.BtnAdminPanels:
			return b.adminPanels(c)
		case i18n.BtnAdminSettings:
			return b.adminSettings(c)
		case i18n.BtnAdminUsers:
			return b.adminUsers(c)
		case i18n.BtnAdminBroadcast:
			return b.adminBroadcastStart(c)
		case i18n.BtnAdminBackup:
			return b.adminBackup(c)
		case i18n.BtnAdminAdmins:
			return b.adminAdmins(c)
		case i18n.BtnAdminUserMenu:
			return b.showUserMenu(c, "منوی کاربری. برای بازگشت /admin را بزنید.")
		}
	}

	if b.isAdmin(uid) {
		return b.showAdminMenu(c, "از منوی مدیریت استفاده کنید 👇")
	}
	return b.showUserMenu(c, "از منوی پایین استفاده کن 👇")
}

func (b *Bot) onAdmin(c tele.Context) error {
	if !b.isAdmin(c.Sender().ID) {
		return c.Send("شما ادمین نیستید.")
	}
	b.clearState(c.Sender().ID)
	return b.showAdminMenu(c, "🛠 پنل مدیریت")
}

func (b *Bot) onMyID(c tele.Context) error {
	return c.Send(fmt.Sprintf("آیدی عددی شما: `%d`", c.Sender().ID), tele.ModeMarkdownV2)
}

func (b *Bot) handleAccount(c tele.Context) error {
	u, err := b.store.GetUser(c.Sender().ID)
	if err != nil {
		return c.Send("خطا در دریافت اطلاعات.")
	}
	cap := b.store.DailyCapMB(u)
	bonus := b.store.GetInt(db.KeyPerReferralBonusMB)
	max := b.store.GetInt(db.KeyMaxDailyMB)
	claimed := "❌ هنوز نگرفتی"
	if u.LastClaimDate == db.TehranDay() {
		claimed = "✅ امروز گرفتی"
	}
	msg := fmt.Sprintf(`👤 حساب کاربری

📦 سقف حجم روزانه: %s
👥 دعوت‌های موفق: %d نفر
🎁 پاداش هر دعوت: %d مگابایت (تا سقف %s)
📅 وضعیت امروز: %s

⏰ ریست بعدی تا %s دیگر`,
		fmtVol(cap), u.ReferralsCount, bonus, fmtVol(max), claimed, untilTehranMidnight())
	return c.Send(msg)
}

func (b *Bot) handleReferral(c tele.Context) error {
	u, _ := b.store.GetUser(c.Sender().ID)
	link := fmt.Sprintf("https://t.me/%s?start=ref_%d", b.me.Username, c.Sender().ID)
	bonus := b.store.GetInt(db.KeyPerReferralBonusMB)
	cnt := 0
	if u != nil {
		cnt = u.ReferralsCount
	}
	msg := fmt.Sprintf(`🔗 لینک دعوت شما:

%s

هر کسی با این لینک وارد شود و کانفیگ بگیرد، به حجم روزانه‌ی شما %d مگابایت اضافه می‌شود.

👥 تا حالا %d دعوت موفق داشته‌اید.`, link, bonus, cnt)
	return c.Send(msg)
}

func (b *Bot) handleStatus(c tele.Context) error {
	cl, err := b.store.LastClaim(c.Sender().ID)
	if err != nil || cl == nil {
		return c.Send("هنوز کانفیگی نگرفته‌اید. دکمه «🎁 دریافت کانفیگ امروز» را بزنید.")
	}
	when := "قدیمی"
	if cl.ClaimDate == db.TehranDay() {
		when = "امروز"
	}
	msg := fmt.Sprintf("📊 آخرین کانفیگ (%s):\n📦 حجم: %s\n\n`%s`", when, fmtVol(cl.VolumeMB), cl.SubLink)
	return c.Send(msg, tele.ModeMarkdown)
}

func (b *Bot) handleAdvertise(c tele.Context) error {
	support := strings.TrimSpace(b.store.Get(db.KeySupportContact))
	url := supportURL(support)

	msg := `📣 تبلیغ کانال شما

اگر می‌خواهید کانالتان در این ربات تبلیغ شود و ممبر واقعی بگیرید، با پشتیبانی در ارتباط باشید.

ما کانال شما را در صف نمایش قرار می‌دهیم و شما فقط بابت ممبرهای جدید و واقعی هزینه می‌کنید.`

	if url != "" {
		m := &tele.ReplyMarkup{}
		m.Inline(m.Row(m.URL("✉️ ارتباط با پشتیبانی", url)))
		return c.Send(msg, m)
	}
	if support == "" {
		support = "به‌زودی"
	}
	return c.Send(msg + "\n\n☎️ پشتیبانی: " + support)
}

// supportURL turns a support-contact setting (a @username, t.me link or full
// URL) into a tappable link. Returns "" if it can't form a sensible link.
func supportURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	h := strings.TrimPrefix(raw, "@")
	h = strings.TrimPrefix(h, "t.me/")
	h = strings.TrimPrefix(h, "https://t.me/")
	h = strings.TrimPrefix(h, "http://t.me/")
	if h == "" || strings.ContainsAny(h, " \t\n@/") {
		return "" // not a clean username — show as plain text instead
	}
	return "https://t.me/" + h
}

// fmtMoney renders an integer amount with thousands separators + "تومان".
func fmtMoney(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, d := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, d)
	}
	res := string(out)
	if neg {
		res = "-" + res
	}
	return res + " تومان"
}

// fmtVol renders MB as MB or GB.
func fmtVol(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d گیگابایت", mb/1024)
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1f گیگابایت", float64(mb)/1024)
	}
	return fmt.Sprintf("%d مگابایت", mb)
}
