package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/backup"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/hiddify"
)

// ============================ DASHBOARD ============================

func (b *Bot) adminStats(c tele.Context) error {
	today := db.TehranDay()
	st, err := b.store.Stats(today)
	if err != nil {
		return c.Send("خطا در دریافت آمار.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 داشبورد\n\n")
	fmt.Fprintf(&sb, "👥 کل کاربران: %d (مسدود: %d)\n", st.TotalUsers, st.BannedUsers)
	fmt.Fprintf(&sb, "🎁 کانفیگ امروز: %d\n📦 کل کانفیگ‌ها: %d\n", st.ClaimsToday, st.ClaimsTotal)
	fmt.Fprintf(&sb, "🖥 پنل‌ها: %d | 📣 کانال‌ها: %d\n", st.TotalPanels, st.TotalChannel)
	if rev := b.store.TotalRevenue(); rev > 0 {
		fmt.Fprintf(&sb, "💰 درآمد کل تبلیغات: %s\n", fmtMoney(rev))
	}
	sb.WriteString("\n")

	panels, _ := b.store.Panels()
	if len(panels) > 0 {
		sb.WriteString("— مصرف پنل‌ها (امروز) —\n")
		for _, p := range panels {
			limit := "نامحدود"
			if p.DailyVolumeLimitGB > 0 {
				limit = fmt.Sprintf("%dGB", p.DailyVolumeLimitGB)
			}
			status := "✅"
			if !p.Enabled {
				status = "⛔️"
			}
			fmt.Fprintf(&sb, "%s %s: %s / %s\n", status, p.Name, fmtVol(p.UsedTodayMB), limit)
		}
		sb.WriteString("\n")
	}

	tops, _ := b.store.TopReferrers(5)
	if len(tops) > 0 {
		sb.WriteString("🏆 برترین دعوت‌کننده‌ها:\n")
		for i, t := range tops {
			name := t.Username
			if name == "" {
				name = strconv.FormatInt(t.TGID, 10)
			}
			fmt.Fprintf(&sb, "%d. %s — %d دعوت\n", i+1, name, t.Referrals)
		}
	}
	return c.Send(sb.String())
}

// ============================ CHANNELS ============================

func (b *Bot) adminChannels(c tele.Context) error {
	chs, err := b.store.Channels()
	if err != nil {
		return c.Send("خطا در دریافت کانال‌ها.")
	}
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("➕ افزودن کانال", cbChAdd, "go")))
	if err := c.Send("📣 مدیریت کانال‌های تبلیغ / جوین اجباری:", m); err != nil {
		return err
	}
	if len(chs) == 0 {
		return c.Send("هنوز کانالی اضافه نشده. دکمه «➕ افزودن کانال» را بزنید.")
	}
	for _, ch := range chs {
		_ = c.Send(channelCard(ch), channelKeyboard(ch))
	}
	return nil
}

func channelCard(ch *db.Channel) string {
	status := "✅ فعال"
	if !ch.Enabled {
		status = "⛔️ غیرفعال"
	}
	if ch.Kind == "permanent" {
		return fmt.Sprintf("📣 %s\nنوع: دائمی (همیشگی)\nاولویت: %d | %s",
			channelDisplay(ch), ch.Priority, status)
	}
	done := ch.NewJoinsCount
	target := ch.QuotaTarget
	pct := 0
	if target > 0 {
		pct = done * 100 / target
	}
	card := fmt.Sprintf("📣 %s\nسفارش جوین: %d/%d (%d%%) | باقی‌مانده: %d\nاولویت: %d | %s",
		channelDisplay(ch), done, target, pct, ch.Remaining(), ch.Priority, status)
	if ch.Advertiser != "" {
		card += "\n👤 سفارش‌دهنده: " + ch.Advertiser
	}
	if ch.PricePer1k > 0 {
		card += fmt.Sprintf("\n💵 قیمت هر ۱۰۰۰ جوین: %s\n💰 درآمد تا الان: %s",
			fmtMoney(ch.PricePer1k), fmtMoney(ch.Revenue()))
	}
	return card
}

func channelKeyboard(ch *db.Channel) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	id := strconv.FormatInt(ch.ID, 10)
	toggle := "⛔️ غیرفعال‌کردن"
	if !ch.Enabled {
		toggle = "✅ فعال‌کردن"
	}
	m.Inline(
		m.Row(m.Data("🎯 تعداد", cbChQuota, id), m.Data("💵 قیمت", cbChPrice, id)),
		m.Row(m.Data("⭐️ اولویت", cbChPrio, id), m.Data("📊 آمار", cbChStats, id)),
		m.Row(m.Data(toggle, cbChToggle, id), m.Data("🗑 حذف", cbChDel, id)),
	)
	return m
}

func (b *Bot) cbChannelAdd(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "add_channel")
	st.Data["step"] = "ref"
	return c.Send("یکی از این‌ها را بفرستید:\n• یک پست از کانال را forward کنید\n• یا @username کانال\n• یا آیدی عددی کانال (مثل -1001234567)\n\nℹ️ ربات باید در آن کانال ادمین باشد.", cancelMenu())
}

func (b *Bot) cbChannelDelete(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	if err := b.store.DeleteChannel(id); err != nil {
		return c.RespondText("خطا در حذف")
	}
	b.store.Audit(c.Sender().ID, "channel_delete", c.Data())
	_ = c.Respond(&tele.CallbackResponse{Text: "حذف شد ✅"})
	return c.Edit("🗑 این کانال حذف شد.")
}

func (b *Bot) cbChannelToggle(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	ch, err := b.store.GetChannel(id)
	if err != nil {
		return c.RespondText("یافت نشد")
	}
	_ = b.store.SetChannelEnabled(id, !ch.Enabled)
	ch.Enabled = !ch.Enabled
	_ = c.Respond(&tele.CallbackResponse{Text: "تغییر کرد ✅"})
	return c.Edit(channelCard(ch), channelKeyboard(ch))
}

func (b *Bot) cbChannelStats(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	ch, err := b.store.GetChannel(id)
	if err != nil {
		return c.RespondText("یافت نشد")
	}
	return c.RespondAlert(strings.ReplaceAll(channelCard(ch), "\n", "  •  "))
}

func (b *Bot) cbChannelSetQuota(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "set_quota")
	st.Data["ch"] = c.Data()
	return c.Send("تعداد جوین هدف جدید را بفرستید (عدد). ۰ یعنی کانال دائمی/همیشگی.", cancelMenu())
}

func (b *Bot) cbChannelSetPriority(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "set_prio")
	st.Data["ch"] = c.Data()
	return c.Send("عدد اولویت را بفرستید (بزرگ‌تر = مهم‌تر، اول نمایش داده می‌شود).", cancelMenu())
}

func (b *Bot) cbChannelSetPrice(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "set_price")
	st.Data["ch"] = c.Data()
	return c.Send("قیمت هر ۱۰۰۰ جوین را به تومان بفرستید (عدد). ۰ = رایگان/بدون قیمت.", cancelMenu())
}

// ============================ PANELS ============================

func (b *Bot) adminPanels(c tele.Context) error {
	panels, err := b.store.Panels()
	if err != nil {
		return c.Send("خطا در دریافت پنل‌ها.")
	}
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("➕ افزودن پنل", cbPanelAdd, "go")))
	if err := c.Send("🖥 مدیریت پنل‌های هیدیفای:", m); err != nil {
		return err
	}
	if len(panels) == 0 {
		return c.Send("هنوز پنلی اضافه نشده. دکمه «➕ افزودن پنل» را بزنید.")
	}
	for _, p := range panels {
		_ = c.Send(panelCard(p), panelKeyboard(p))
	}
	return nil
}

func panelCard(p *db.Panel) string {
	status := "✅ فعال"
	if !p.Enabled {
		status = "⛔️ غیرفعال"
	}
	limit := "نامحدود"
	if p.DailyVolumeLimitGB > 0 {
		limit = fmt.Sprintf("%d گیگ/روز", p.DailyVolumeLimitGB)
	}
	return fmt.Sprintf("🖥 %s\nدامنه: %s\nنوع ساب: %s\nسقف روزانه: %s | مصرف امروز: %s\nاولویت: %d | %s",
		p.Name, p.Domain, p.SubType, limit, fmtVol(p.UsedTodayMB), p.Priority, status)
}

func panelKeyboard(p *db.Panel) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	id := strconv.FormatInt(p.ID, 10)
	toggle := "⛔️ غیرفعال"
	if !p.Enabled {
		toggle = "✅ فعال"
	}
	m.Inline(
		m.Row(m.Data("📦 سقف حجم", cbPanelLimit, id), m.Data(toggle, cbPanelToggle, id)),
		m.Row(m.Data("🗑 حذف", cbPanelDel, id)),
	)
	return m
}

func (b *Bot) cbPanelAdd(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "add_panel")
	st.Data["step"] = "admin"
	return c.Send("لینک کامل ادمین پنل هیدیفای را بفرستید:\nمثال: https://domain/proxypath/uuid/admin/", cancelMenu())
}

func (b *Bot) cbPanelDelete(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	if err := b.store.DeletePanel(id); err != nil {
		return c.RespondText("خطا")
	}
	b.store.Audit(c.Sender().ID, "panel_delete", c.Data())
	_ = c.Respond(&tele.CallbackResponse{Text: "حذف شد ✅"})
	return c.Edit("🗑 این پنل حذف شد.")
}

func (b *Bot) cbPanelToggle(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	p, err := b.store.GetPanel(id)
	if err != nil {
		return c.RespondText("یافت نشد")
	}
	_ = b.store.SetPanelEnabled(id, !p.Enabled)
	p.Enabled = !p.Enabled
	_ = c.Respond(&tele.CallbackResponse{Text: "تغییر کرد ✅"})
	return c.Edit(panelCard(p), panelKeyboard(p))
}

func (b *Bot) cbPanelSetLimit(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "set_limit")
	st.Data["panel"] = c.Data()
	return c.Send("سقف حجم روزانه‌ی این پنل را به گیگابایت بفرستید (۰ = نامحدود).", cancelMenu())
}

// ============================ SETTINGS ============================

var settingLabels = []struct {
	Key, Label string
	Bool       bool
}{
	{db.KeyRandomChannelsPerDay, "🎲 تعداد کانال نمایشی روزانه", false},
	{db.KeyBaseDailyMB, "📦 حجم پایه روزانه (MB)", false},
	{db.KeyPerReferralBonusMB, "🎁 پاداش هر دعوت (MB)", false},
	{db.KeyMaxDailyMB, "⬆️ سقف حجم روزانه (MB)", false},
	{db.KeyConfigDays, "📅 روز اعتبار هر کانفیگ", false},
	{db.KeyDefaultSubType, "🔗 نوع لینک ساب", false},
	{db.KeyDeliveryMode, "📤 نحوه تحویل به کاربر", false},
	{db.KeySupportContact, "☎️ آیدی پشتیبانی", false},
	{db.KeyBackupKeep, "💾 تعداد بکاپ نگه‌داری", false},
	{db.KeyAcceptingNewUsers, "🚪 پذیرش کاربر جدید", true},
	{db.KeyMaintenance, "🛠 حالت تعمیر", true},
}

func (b *Bot) adminSettings(c tele.Context) error {
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, s := range settingLabels {
		val := b.store.Get(s.Key)
		if s.Bool {
			if val == "1" {
				val = "روشن ✅"
			} else {
				val = "خاموش ⛔️"
			}
		} else if s.Key == db.KeyDeliveryMode {
			if val == "configs" {
				val = "کانفیگ مستقیم"
			} else {
				val = "لینک ساب"
			}
		} else if val == "" {
			val = "—"
		}
		rows = append(rows, m.Row(m.Data(fmt.Sprintf("%s: %s", s.Label, val), cbSetting, s.Key)))
	}
	m.Inline(rows...)
	return c.Send("⚙️ تنظیمات ربات — روی هر مورد بزنید تا تغییر کند:", m)
}

func (b *Bot) cbSetting(c tele.Context) error {
	key := c.Data()
	// Boolean settings toggle immediately.
	for _, s := range settingLabels {
		if s.Key == key && s.Bool {
			cur := b.store.GetBool(key)
			_ = b.store.SetBool(key, !cur)
			_ = c.Respond(&tele.CallbackResponse{Text: "تغییر کرد ✅"})
			return b.adminSettings(c)
		}
	}
	// Delivery mode cycles between the two values on tap.
	if key == db.KeyDeliveryMode {
		if b.store.Get(key) == "configs" {
			_ = b.store.Set(key, "link")
		} else {
			_ = b.store.Set(key, "configs")
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "تغییر کرد ✅"})
		return b.adminSettings(c)
	}
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "set_setting")
	st.Data["key"] = key
	hint := "مقدار جدید را بفرستید."
	if key == db.KeyDefaultSubType {
		hint = "نوع ساب را بفرستید: auto یا sub یا sub64 یا clash یا clashmeta یا singbox"
	} else if key == db.KeySupportContact {
		hint = "آیدی یا لینک پشتیبانی را بفرستید (مثل @support)."
	} else {
		hint = "یک عدد بفرستید."
	}
	return c.Send(hint, cancelMenu())
}

// ============================ USERS ============================

const usersPageSize = 8

func (b *Bot) adminUsers(c tele.Context) error {
	return b.renderUserList(c, 0, false)
}

// renderUserList shows one page of users (newest first) with a button per user.
func (b *Bot) renderUserList(c tele.Context, page int, edit bool) error {
	total := b.store.CountUsers()
	pages := (total + usersPageSize - 1) / usersPageSize
	if pages == 0 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	users, err := b.store.ListUsers(page*usersPageSize, usersPageSize)
	if err != nil {
		return c.Send("خطا در دریافت کاربران.")
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, u := range users {
		rows = append(rows, m.Row(m.Data(userRowLabel(u), cbUserOpen, strconv.FormatInt(u.TGID, 10))))
	}
	rows = append(rows, m.Row(
		m.Data("◀️ قبلی", cbUsersList, strconv.Itoa(page-1)),
		m.Data(fmt.Sprintf("صفحه %d/%d", page+1, pages), cbNoop, "x"),
		m.Data("بعدی ▶️", cbUsersList, strconv.Itoa(page+1)),
	))
	rows = append(rows, m.Row(m.Data("🔍 جستجو با آیدی یا یوزرنیم", cbUserSearch, "go")))
	m.Inline(rows...)

	text := fmt.Sprintf("👥 مدیریت کاربران — مجموع: %d نفر\nروی هر کاربر بزنید تا مدیریتش کنید:", total)
	if edit {
		return c.Edit(text, m)
	}
	return c.Send(text, m)
}

func userRowLabel(u *db.User) string {
	name := u.FirstName
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	if name == "" {
		name = "کاربر"
	}
	label := fmt.Sprintf("👤 %s · %d", name, u.TGID)
	if u.Banned {
		label = "🚫 " + label
	}
	return label
}

func userCardText(b *Bot, u *db.User) string {
	cap := b.store.DailyCapMB(u)
	banned := "خیر"
	if u.Banned {
		banned = "بله ⛔️"
	}
	ov := "—"
	if u.DailyCapOverride > 0 {
		ov = fmtVol(u.DailyCapOverride)
	}
	uname := "—"
	if u.Username != "" {
		uname = "@" + u.Username
	}
	return fmt.Sprintf(`👤 کاربر %d
نام: %s (%s)
سقف روزانه فعلی: %s
دعوت موفق: %d | پاداش دستی: %s
override سقف: %s
مسدود: %s`,
		u.TGID, u.FirstName, uname, fmtVol(cap), u.ReferralsCount, fmtVol(u.ManualBonusMB), ov, banned)
}

func userCardKeyboard(u *db.User) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	id := strconv.FormatInt(u.TGID, 10)
	ban := "🚫 مسدود کردن"
	if u.Banned {
		ban = "✅ رفع مسدودی"
	}
	m.Inline(
		m.Row(m.Data(ban, cbUserBan, id)),
		m.Row(m.Data("🎁 پاداش حجم", cbUserBonus, id), m.Data("📦 سقف ثابت", cbUserCap, id)),
		m.Row(m.Data("✉️ پیام به کاربر", cbUserMsg, id), m.Data("🗑 حذف کاربر", cbUserDel, id)),
		m.Row(m.Data("🔙 بازگشت به لیست", cbUsersList, "0")),
	)
	return m
}

// showUserCard renders a user's card (edit in place when invoked from a callback).
func (b *Bot) showUserCard(c tele.Context, u *db.User, edit bool) error {
	if edit {
		return c.Edit(userCardText(b, u), userCardKeyboard(u))
	}
	return c.Send(userCardText(b, u), userCardKeyboard(u))
}

func (b *Bot) cbUsersList(c tele.Context) error {
	_ = c.Respond()
	page, _ := strconv.Atoi(c.Data())
	return b.renderUserList(c, page, true)
}

func (b *Bot) cbUserSearch(c tele.Context) error {
	_ = c.Respond()
	b.setState(c.Sender().ID, "find_user")
	return c.Send("آیدی عددی یا یوزرنیم (مثل @user) کاربر را بفرستید.", cancelMenu())
}

// openUserByID loads a user and shows their card; used by callbacks.
func (b *Bot) openUserByID(c tele.Context, id int64, edit bool) error {
	u, err := b.store.GetUser(id)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "کاربر یافت نشد"})
		return nil
	}
	return b.showUserCard(c, u, edit)
}

func (b *Bot) cbUserOpen(c tele.Context) error {
	_ = c.Respond()
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	return b.openUserByID(c, id, true)
}

func (b *Bot) cbUserBan(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	u, err := b.store.GetUser(id)
	if err != nil {
		return c.RespondText("یافت نشد")
	}
	_ = b.store.SetBan(id, !u.Banned)
	_ = c.Respond(&tele.CallbackResponse{Text: "انجام شد ✅"})
	return b.openUserByID(c, id, true)
}

func (b *Bot) cbUserBonus(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "user_bonus")
	st.Data["uid"] = c.Data()
	return c.Send("مقدار پاداش حجم را به مگابایت بفرستید (عدد). ۰ = حذف پاداش.", cancelMenu())
}

func (b *Bot) cbUserCap(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "user_cap")
	st.Data["uid"] = c.Data()
	return c.Send("سقف ثابت روزانه را به مگابایت بفرستید (عدد). ۰ = حذف سقف ثابت.", cancelMenu())
}

func (b *Bot) cbUserMsg(c tele.Context) error {
	_ = c.Respond()
	st := b.setState(c.Sender().ID, "user_msg")
	st.Data["uid"] = c.Data()
	return c.Send("پیامی که می‌خواهید برای این کاربر ارسال شود را بفرستید (متن/عکس/فایل).", cancelMenu())
}

func (b *Bot) cbUserDelete(c tele.Context) error {
	_ = c.Respond()
	id := c.Data()
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(
		m.Data("✅ بله، حذف کن", cbUserDelYes, id),
		m.Data("✖️ انصراف", cbUserOpen, id),
	))
	return c.Edit("⚠️ از حذف کامل این کاربر مطمئنی؟ همه‌ی اطلاعاتش پاک می‌شود.", m)
}

func (b *Bot) cbUserDeleteConfirm(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	if err := b.store.DeleteUserFull(id); err != nil {
		return c.RespondText("خطا در حذف")
	}
	b.store.Audit(c.Sender().ID, "user_delete", c.Data())
	_ = c.Respond(&tele.CallbackResponse{Text: "حذف شد ✅"})
	return b.renderUserList(c, 0, true)
}

// deliverAdminDM forwards the admin's message to a target user.
func (b *Bot) deliverAdminDM(c tele.Context, st *UserState) error {
	id, _ := strconv.ParseInt(st.Data["uid"], 10, 64)
	b.clearState(c.Sender().ID)
	if _, err := b.tb.Copy(tele.ChatID(id), c.Message()); err != nil {
		return b.showAdminMenu(c, "❌ ارسال نشد (شاید کاربر ربات را بلاک کرده).")
	}
	return b.showAdminMenu(c, "✅ پیام برای کاربر ارسال شد.")
}

// ============================ BROADCAST ============================

func (b *Bot) adminBroadcastStart(c tele.Context) error {
	b.setState(c.Sender().ID, "broadcast")
	return c.Send("پیامی که می‌خواهید برای همه ارسال شود را بفرستید (متن، عکس، فایل، ...).", cancelMenu())
}

func (b *Bot) doBroadcast(c tele.Context) error {
	uid := c.Sender().ID
	b.clearState(uid)
	ids, _ := b.store.AllUserIDs()
	msg := c.Message()
	_ = c.Send(fmt.Sprintf("⏳ در حال ارسال به %d کاربر...", len(ids)), adminMenu())
	go func() {
		sent, fail := 0, 0
		for _, id := range ids {
			if _, err := b.tb.Copy(tele.ChatID(id), msg); err != nil {
				fail++
			} else {
				sent++
			}
			time.Sleep(40 * time.Millisecond) // ~25 msg/s
		}
		_, _ = b.tb.Send(tele.ChatID(uid),
			fmt.Sprintf("📢 پیام همگانی تمام شد.\n✅ موفق: %d\n❌ ناموفق: %d", sent, fail))
	}()
	return nil
}

// ============================ BACKUP / RESTORE ============================

func (b *Bot) adminBackup(c tele.Context) error {
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(m.Data("💾 بکاپ‌گیری فوری", cbBackupNow, "go")),
		m.Row(m.Data("♻️ ریستور از فایل", cbRestore, "go")),
	)
	return c.Send("💾 بکاپ و ریستور\n\nهر ۲ ساعت یک بکاپ خودکار برای ادمین‌ها ارسال می‌شود.", m)
}

func (b *Bot) cbBackupNow(c tele.Context) error {
	_ = c.Respond(&tele.CallbackResponse{Text: "در حال تهیه بکاپ..."})
	ids, _ := b.store.AdminIDs()
	keep := b.store.GetInt(db.KeyBackupKeep)
	err := backup.CreateAndSend(b.tb, b.store, b.cfg.BackupDir(), keep, ids,
		"💾 بکاپ دستی — "+time.Now().In(db.Tehran).Format("2006-01-02 15:04"))
	if err != nil {
		return c.Send("خطا در بکاپ: " + err.Error())
	}
	return nil
}

func (b *Bot) cbRestore(c tele.Context) error {
	_ = c.Respond()
	b.setState(c.Sender().ID, "restore")
	return c.Send("فایل بکاپ (.db یا .db.gz) را همینجا ارسال کنید تا اطلاعات بازگردانی شود.\n⚠️ اطلاعات فعلی جایگزین می‌شود.", cancelMenu())
}

// ============================ ADMINS ============================

func (b *Bot) adminAdmins(c tele.Context) error {
	ids, _ := b.store.AdminIDs()
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	rows = append(rows, m.Row(m.Data("➕ افزودن ادمین", cbAdminAdd, "go")))
	for _, id := range ids {
		label := fmt.Sprintf("🗑 حذف %d", id)
		if id == b.cfg.AdminID {
			label = fmt.Sprintf("👑 %d (مالک)", id)
			rows = append(rows, m.Row(m.Data(label, cbNoop, "x")))
			continue
		}
		rows = append(rows, m.Row(m.Data(label, cbAdminDel, strconv.FormatInt(id, 10))))
	}
	m.Inline(rows...)
	return c.Send("👮 مدیریت ادمین‌ها:", m)
}

func (b *Bot) cbAdminAdd(c tele.Context) error {
	_ = c.Respond()
	b.setState(c.Sender().ID, "add_admin")
	return c.Send("آیدی عددی ادمین جدید را بفرستید.", cancelMenu())
}

func (b *Bot) cbAdminDelete(c tele.Context) error {
	id, _ := strconv.ParseInt(c.Data(), 10, 64)
	if id == b.cfg.AdminID {
		return c.RespondText("مالک قابل حذف نیست")
	}
	_ = b.store.RemoveAdmin(id)
	_ = c.Respond(&tele.CallbackResponse{Text: "حذف شد ✅"})
	return c.Edit("🗑 ادمین حذف شد.")
}

// ============================ FSM INPUT ROUTER ============================

func (b *Bot) handleStateInput(c tele.Context, st *UserState) error {
	if !b.isAdmin(c.Sender().ID) {
		b.clearState(c.Sender().ID)
		return b.showUserMenu(c, "")
	}
	switch st.Action {
	case "add_channel":
		return b.fsmAddChannel(c, st)
	case "set_quota":
		return b.fsmSetChannelNumber(c, st, true)
	case "set_prio":
		return b.fsmSetChannelNumber(c, st, false)
	case "set_price":
		return b.fsmSetPrice(c, st)
	case "add_panel":
		return b.fsmAddPanel(c, st)
	case "set_limit":
		return b.fsmSetPanelLimit(c, st)
	case "set_setting":
		return b.fsmSetSetting(c, st)
	case "find_user":
		return b.fsmFindUser(c, st)
	case "user_bonus":
		return b.fsmUserNumber(c, st, true)
	case "user_cap":
		return b.fsmUserNumber(c, st, false)
	case "user_msg":
		return b.deliverAdminDM(c, st)
	case "add_admin":
		return b.fsmAddAdmin(c, st)
	case "broadcast":
		return b.doBroadcast(c)
	default:
		b.clearState(c.Sender().ID)
		return b.showAdminMenu(c, "نامشخص بود، دوباره از منو انتخاب کنید.")
	}
}

func (b *Bot) fsmAddChannel(c tele.Context, st *UserState) error {
	switch st.Data["step"] {
	case "ref":
		chat, err := b.resolveChannel(c)
		if err != nil {
			return c.Send("❌ نشد. مطمئن شو ربات در کانال ادمین است و دوباره @username یا آیدی یا forward بفرست.")
		}
		st.Data["chat_id"] = strconv.FormatInt(chat.ID, 10)
		st.Data["title"] = chat.Title
		st.Data["username"] = chat.Username
		st.Data["invite"] = chat.InviteLink
		st.Data["step"] = "quota"
		return c.Send(fmt.Sprintf("کانال «%s» شناسایی شد.\nتعداد جوین هدف را بفرست (عدد). ۰ = کانال دائمی/همیشگی.", chat.Title), cancelMenu())
	case "quota":
		n, err := strconv.Atoi(strings.TrimSpace(c.Text()))
		if err != nil || n < 0 {
			return c.Send("یک عدد معتبر بفرست (۰ یا بیشتر).")
		}
		if n == 0 {
			// Permanent channel — no quota/price needed.
			return b.finishAddChannel(c, st, "permanent", 0, 0, "")
		}
		st.Data["quota"] = strconv.Itoa(n)
		st.Data["step"] = "price"
		return c.Send("قیمت هر ۱۰۰۰ جوین را به تومان بفرست (عدد). اگر می‌خواهی نام سفارش‌دهنده هم ثبت شود، بعد از یک فاصله بنویس.\nمثال: 50000 کانال‌فلان\n(۰ یعنی بدون قیمت)", cancelMenu())
	case "price":
		fields := strings.Fields(strings.TrimSpace(c.Text()))
		if len(fields) == 0 {
			return c.Send("یک عدد بفرست (۰ یا بیشتر).")
		}
		price, err := strconv.Atoi(fields[0])
		if err != nil || price < 0 {
			return c.Send("قیمت باید عدد باشد.")
		}
		advertiser := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Text()), fields[0]))
		quota, _ := strconv.Atoi(st.Data["quota"])
		return b.finishAddChannel(c, st, "quota", quota, price, advertiser)
	}
	return nil
}

// finishAddChannel persists a new channel and returns to the admin menu.
func (b *Bot) finishAddChannel(c tele.Context, st *UserState, kind string, quota, price int, advertiser string) error {
	chatID, _ := strconv.ParseInt(st.Data["chat_id"], 10, 64)
	isReq := st.Data["username"] == "" // private channels usually use join requests
	_, err := b.store.AddChannel(&db.Channel{
		ChatID: chatID, Title: st.Data["title"], Username: st.Data["username"],
		InviteLink: st.Data["invite"], Kind: kind, QuotaTarget: quota,
		PricePer1k: price, Advertiser: advertiser, IsJoinRequest: isReq,
	})
	b.clearState(c.Sender().ID)
	if err != nil {
		return b.showAdminMenu(c, "خطا در ذخیره کانال: "+err.Error())
	}
	b.store.Audit(c.Sender().ID, "channel_add", st.Data["title"])
	return b.showAdminMenu(c, "✅ کانال اضافه شد.")
}

func (b *Bot) fsmSetPrice(c tele.Context, st *UserState) error {
	price, err := strconv.Atoi(strings.TrimSpace(c.Text()))
	if err != nil || price < 0 {
		return c.Send("یک عدد معتبر بفرست.")
	}
	id, _ := strconv.ParseInt(st.Data["ch"], 10, 64)
	_ = b.store.SetChannelPrice(id, price)
	b.clearState(c.Sender().ID)
	return b.showAdminMenu(c, "✅ قیمت ثبت شد.")
}

func (b *Bot) fsmSetChannelNumber(c tele.Context, st *UserState, quota bool) error {
	n, err := strconv.Atoi(strings.TrimSpace(c.Text()))
	if err != nil || n < 0 {
		return c.Send("یک عدد معتبر بفرست.")
	}
	id, _ := strconv.ParseInt(st.Data["ch"], 10, 64)
	if quota {
		_ = b.store.SetChannelQuota(id, n)
	} else {
		_ = b.store.SetChannelPriority(id, n)
	}
	b.clearState(c.Sender().ID)
	return b.showAdminMenu(c, "✅ ثبت شد.")
}

func (b *Bot) fsmAddPanel(c tele.Context, st *UserState) error {
	txt := strings.TrimSpace(c.Text())
	switch st.Data["step"] {
	case "admin":
		ap, err := hiddify.ParseAdminLink(txt)
		if err != nil {
			return c.Send("❌ " + err.Error() + "\nدوباره لینک ادمین کامل را بفرست.")
		}
		// Verify credentials against the live panel.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ver, err := hiddify.New(ap).Ping(ctx)
		if err != nil {
			return c.Send("❌ اتصال به پنل ناموفق بود:\n" + err.Error() + "\nلینک ادمین را بررسی کن.")
		}
		st.Data["domain"] = ap.Domain
		st.Data["apath"] = ap.ProxyPath
		st.Data["auuid"] = ap.AdminUUID
		st.Data["step"] = "sub"
		return c.Send(fmt.Sprintf("✅ اتصال موفق (هیدیفای %s).\n\nحالا یک نمونه لینک ساب یک کاربر از این پنل را بفرست تا دامنه و مسیر ساب استخراج شود.", ver), cancelMenu())
	case "sub":
		sp, err := hiddify.ParseSubLink(txt)
		if err != nil {
			return c.Send("❌ " + err.Error() + "\nیک نمونه لینک ساب معتبر بفرست.")
		}
		st.Data["sdomain"] = sp.Domain
		st.Data["spath"] = sp.ProxyPath
		st.Data["step"] = "vol"
		return c.Send("سقف حجم روزانه‌ی این پنل را به گیگابایت بفرست (۰ = نامحدود).", cancelMenu())
	case "vol":
		gb, err := strconv.Atoi(txt)
		if err != nil || gb < 0 {
			return c.Send("یک عدد معتبر بفرست (۰ یا بیشتر).")
		}
		p := &db.Panel{
			Name:               st.Data["domain"],
			Domain:             st.Data["domain"],
			AdminProxyPath:     st.Data["apath"],
			AdminUUID:          st.Data["auuid"],
			SubDomain:          st.Data["sdomain"],
			SubProxyPath:       st.Data["spath"],
			SubType:            b.store.Get(db.KeyDefaultSubType),
			DailyVolumeLimitGB: gb,
		}
		_, err = b.store.AddPanel(p)
		b.clearState(c.Sender().ID)
		if err != nil {
			return b.showAdminMenu(c, "خطا در ذخیره پنل: "+err.Error())
		}
		b.store.Audit(c.Sender().ID, "panel_add", p.Domain)
		return b.showAdminMenu(c, "✅ پنل اضافه شد.")
	}
	return nil
}

func (b *Bot) fsmSetPanelLimit(c tele.Context, st *UserState) error {
	gb, err := strconv.Atoi(strings.TrimSpace(c.Text()))
	if err != nil || gb < 0 {
		return c.Send("یک عدد معتبر بفرست.")
	}
	id, _ := strconv.ParseInt(st.Data["panel"], 10, 64)
	_ = b.store.SetPanelLimit(id, gb)
	b.clearState(c.Sender().ID)
	return b.showAdminMenu(c, "✅ سقف حجم ثبت شد.")
}

func (b *Bot) fsmSetSetting(c tele.Context, st *UserState) error {
	key := st.Data["key"]
	val := strings.TrimSpace(c.Text())
	switch key {
	case db.KeyDefaultSubType:
		val = strings.ToLower(val)
		ok := map[string]bool{"auto": true, "sub": true, "sub64": true, "clash": true, "clashmeta": true, "singbox": true}
		if !ok[val] {
			return c.Send("نوع نامعتبر. یکی از: auto, sub, sub64, clash, clashmeta, singbox")
		}
	case db.KeySupportContact:
		// free text
	default:
		if _, err := strconv.Atoi(val); err != nil {
			return c.Send("یک عدد معتبر بفرست.")
		}
	}
	_ = b.store.Set(key, val)
	b.clearState(c.Sender().ID)
	return b.showAdminMenu(c, "✅ تنظیم ذخیره شد.")
}

func (b *Bot) fsmFindUser(c tele.Context, st *UserState) error {
	txt := strings.TrimSpace(c.Text())
	var u *db.User
	var err error
	if id, e := strconv.ParseInt(txt, 10, 64); e == nil {
		u, err = b.store.GetUser(id)
	} else {
		u, err = b.store.FindUserByUsername(txt)
	}
	b.clearState(c.Sender().ID)
	if err != nil || u == nil {
		return b.showAdminMenu(c, "کاربری با این مشخصات پیدا نشد.")
	}
	return b.showUserCard(c, u, false)
}

// fsmUserNumber handles the bonus/cap number prompts opened from a user card.
func (b *Bot) fsmUserNumber(c tele.Context, st *UserState, bonus bool) error {
	mb, err := strconv.Atoi(strings.TrimSpace(c.Text()))
	if err != nil || mb < 0 {
		return c.Send("یک عدد معتبر بفرست (۰ یا بیشتر).")
	}
	id, _ := strconv.ParseInt(st.Data["uid"], 10, 64)
	if bonus {
		_ = b.store.SetManualBonus(id, mb)
	} else {
		_ = b.store.SetCapOverride(id, mb)
	}
	b.clearState(c.Sender().ID)
	if u, err := b.store.GetUser(id); err == nil {
		return b.showUserCard(c, u, false)
	}
	return b.showAdminMenu(c, "✅ ثبت شد.")
}

func (b *Bot) fsmAddAdmin(c tele.Context, st *UserState) error {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Text()), 10, 64)
	if err != nil {
		return c.Send("یک آیدی عددی معتبر بفرست.")
	}
	_ = b.store.AddAdmin(id)
	b.clearState(c.Sender().ID)
	return b.showAdminMenu(c, "✅ ادمین جدید اضافه شد.")
}

// ============================ DOCUMENT / MEDIA ============================

func (b *Bot) onDocument(c tele.Context) error {
	uid := c.Sender().ID
	st := b.getState(uid)
	if st == nil || !b.isAdmin(uid) {
		return nil
	}
	switch st.Action {
	case "broadcast":
		return b.doBroadcast(c)
	case "user_msg":
		return b.deliverAdminDM(c, st)
	case "restore":
		return b.doRestore(c)
	}
	return nil
}

func (b *Bot) onMaybeBroadcastMedia(c tele.Context) error {
	uid := c.Sender().ID
	st := b.getState(uid)
	if st == nil || !b.isAdmin(uid) {
		return nil
	}
	switch st.Action {
	case "broadcast":
		return b.doBroadcast(c)
	case "user_msg":
		return b.deliverAdminDM(c, st)
	}
	return nil
}

func (b *Bot) doRestore(c tele.Context) error {
	b.clearState(c.Sender().ID)
	doc := c.Message().Document
	if doc == nil {
		return b.showAdminMenu(c, "فایل نامعتبر.")
	}
	tmpDir := filepath.Join(b.cfg.DataDir, "restore-tmp")
	_ = os.MkdirAll(tmpDir, 0o700)
	dl := filepath.Join(tmpDir, doc.FileName)
	if dl == tmpDir || doc.FileName == "" {
		dl = filepath.Join(tmpDir, "incoming.bin")
	}
	if err := b.tb.Download(&doc.File, dl); err != nil {
		return b.showAdminMenu(c, "خطا در دانلود فایل: "+err.Error())
	}
	defer os.RemoveAll(tmpDir)

	dbFile := dl
	if strings.HasSuffix(strings.ToLower(doc.FileName), ".gz") {
		out := strings.TrimSuffix(dl, filepath.Ext(dl))
		if err := gunzip(dl, out); err != nil {
			return b.showAdminMenu(c, "خطا در باز کردن فایل gz: "+err.Error())
		}
		dbFile = out
	}
	if err := b.store.Restore(dbFile); err != nil {
		return b.showAdminMenu(c, "❌ ریستور ناموفق: "+err.Error())
	}
	_ = b.store.AddAdmin(b.cfg.AdminID)
	log.Printf("database restored by %d", c.Sender().ID)
	return b.showAdminMenu(c, "✅ اطلاعات با موفقیت بازگردانی شد. همه کاربران، پنل‌ها و کانال‌ها برگشتند.")
}

// resolveChannel turns a forwarded post / @username / chat id into a Chat.
func (b *Bot) resolveChannel(c tele.Context) (*tele.Chat, error) {
	m := c.Message()
	if m.OriginalChat != nil {
		if ch, err := b.tb.ChatByID(m.OriginalChat.ID); err == nil {
			return ch, nil
		}
		return m.OriginalChat, nil
	}
	txt := strings.TrimSpace(c.Text())
	if txt == "" {
		return nil, fmt.Errorf("empty")
	}
	txt = strings.TrimPrefix(txt, "https://t.me/")
	txt = strings.TrimPrefix(txt, "t.me/")
	txt = strings.TrimPrefix(txt, "@")
	if id, err := strconv.ParseInt(txt, 10, 64); err == nil {
		return b.tb.ChatByID(id)
	}
	return b.tb.ChatByUsername("@" + txt)
}
