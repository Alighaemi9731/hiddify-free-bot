package bot

import (
	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/i18n"
)

// Callback unique ids for inline buttons.
const (
	cbVerify      = "verify"
	cbChDel       = "chdel"
	cbChToggle    = "chtog"
	cbChQuota     = "chquota"
	cbChPrio      = "chprio"
	cbChPrice     = "chprice"
	cbChStats     = "chstats"
	cbPanelDel    = "pdel"
	cbPanelToggle = "ptog"
	cbPanelLimit  = "plimit"
	cbSetting     = "setk"
	cbBackupNow   = "bnow"
	cbRestore     = "restore"
	cbChAdd       = "chadd"
	cbPanelAdd    = "padd"
	cbAdminAdd    = "aadd"
	cbAdminDel    = "adel"
	cbNoop        = "noop"
)

// userMenu is the reply keyboard for regular users.
func userMenu() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{ResizeKeyboard: true}
	m.Reply(
		m.Row(m.Text(i18n.BtnGetConfig)),
		m.Row(m.Text(i18n.BtnAccount), m.Text(i18n.BtnReferral)),
		m.Row(m.Text(i18n.BtnStatus), m.Text(i18n.BtnHelp)),
		m.Row(m.Text(i18n.BtnAdvertise)),
	)
	return m
}

// adminMenu is the management reply keyboard for admins.
func adminMenu() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{ResizeKeyboard: true}
	m.Reply(
		m.Row(m.Text(i18n.BtnAdminStats)),
		m.Row(m.Text(i18n.BtnAdminChannels), m.Text(i18n.BtnAdminPanels)),
		m.Row(m.Text(i18n.BtnAdminUsers), m.Text(i18n.BtnAdminSettings)),
		m.Row(m.Text(i18n.BtnAdminBroadcast), m.Text(i18n.BtnAdminBackup)),
		m.Row(m.Text(i18n.BtnAdminAdmins), m.Text(i18n.BtnAdminUserMenu)),
	)
	return m
}

// cancelMenu shows a single cancel button while an FSM flow is active.
func cancelMenu() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{ResizeKeyboard: true}
	m.Reply(m.Row(m.Text(i18n.BtnCancel)))
	return m
}

func (b *Bot) showUserMenu(c tele.Context, text string) error {
	return c.Send(text, userMenu())
}

func (b *Bot) showAdminMenu(c tele.Context, text string) error {
	return c.Send(text, adminMenu())
}
