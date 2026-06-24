// Package bot wires the Telegram bot together: menus, the daily-config claim
// flow, the admin panel and the background jobs.
package bot

import (
	"fmt"
	"log"
	"sync"
	"time"

	tele "gopkg.in/telebot.v4"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/config"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/hiddify"
)

// Bot holds the running state.
type Bot struct {
	tb      *tele.Bot
	store   *db.Store
	cfg     *config.Config
	version string
	me      *tele.User

	stMu  sync.Mutex
	state map[int64]*UserState
}

// UserState is a tiny per-user finite-state machine for multi-step admin input.
type UserState struct {
	Action string
	Data   map[string]string
}

// New constructs the bot.
func New(cfg *config.Config, store *db.Store, version string) (*Bot, error) {
	pref := tele.Settings{
		Token:  cfg.BotToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Printf("handler error: %v", err)
		},
	}
	tb, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}
	b := &Bot{
		tb:      tb,
		store:   store,
		cfg:     cfg,
		version: version,
		me:      tb.Me,
		state:   map[int64]*UserState{},
	}
	// The owner from the installer is always an admin.
	_ = store.AddAdmin(cfg.AdminID)
	b.registerHandlers()
	return b, nil
}

// TB exposes the underlying telebot instance (used by the scheduler/backup).
func (b *Bot) TB() *tele.Bot { return b.tb }

// Start begins long polling (blocking).
func (b *Bot) Start() {
	log.Printf("bot @%s started", b.me.Username)
	b.tb.Start()
}

// Stop stops the bot.
func (b *Bot) Stop() { b.tb.Stop() }

func (b *Bot) registerHandlers() {
	b.tb.Handle("/start", b.onStart)
	b.tb.Handle("/admin", b.onAdmin)
	b.tb.Handle("/myid", b.onMyID)
	b.tb.Handle(tele.OnText, b.onText)
	b.tb.Handle(tele.OnDocument, b.onDocument)
	b.tb.Handle(tele.OnPhoto, b.onMaybeBroadcastMedia)

	// Inline callbacks (dynamic ones carry an id in the data).
	b.tb.Handle(&tele.Btn{Unique: cbVerify}, b.cbVerifyMembership)
	b.tb.Handle(&tele.Btn{Unique: cbChDel}, b.cbChannelDelete)
	b.tb.Handle(&tele.Btn{Unique: cbChToggle}, b.cbChannelToggle)
	b.tb.Handle(&tele.Btn{Unique: cbChQuota}, b.cbChannelSetQuota)
	b.tb.Handle(&tele.Btn{Unique: cbChPrio}, b.cbChannelSetPriority)
	b.tb.Handle(&tele.Btn{Unique: cbChStats}, b.cbChannelStats)
	b.tb.Handle(&tele.Btn{Unique: cbPanelDel}, b.cbPanelDelete)
	b.tb.Handle(&tele.Btn{Unique: cbPanelToggle}, b.cbPanelToggle)
	b.tb.Handle(&tele.Btn{Unique: cbPanelLimit}, b.cbPanelSetLimit)
	b.tb.Handle(&tele.Btn{Unique: cbSetting}, b.cbSetting)
	b.tb.Handle(&tele.Btn{Unique: cbBackupNow}, b.cbBackupNow)
	b.tb.Handle(&tele.Btn{Unique: cbRestore}, b.cbRestore)
	b.tb.Handle(&tele.Btn{Unique: cbChAdd}, b.cbChannelAdd)
	b.tb.Handle(&tele.Btn{Unique: cbPanelAdd}, b.cbPanelAdd)
	b.tb.Handle(&tele.Btn{Unique: cbAdminAdd}, b.cbAdminAdd)
	b.tb.Handle(&tele.Btn{Unique: cbAdminDel}, b.cbAdminDelete)
	b.tb.Handle(&tele.Btn{Unique: cbNoop}, func(c tele.Context) error { return c.Respond() })
}

// ---- admin / state helpers ----

func (b *Bot) isAdmin(id int64) bool {
	return id == b.cfg.AdminID || b.store.IsAdmin(id)
}

func (b *Bot) setState(id int64, action string) *UserState {
	b.stMu.Lock()
	defer b.stMu.Unlock()
	st := &UserState{Action: action, Data: map[string]string{}}
	b.state[id] = st
	return st
}

func (b *Bot) getState(id int64) *UserState {
	b.stMu.Lock()
	defer b.stMu.Unlock()
	return b.state[id]
}

func (b *Bot) clearState(id int64) {
	b.stMu.Lock()
	defer b.stMu.Unlock()
	delete(b.state, id)
}

// ---- membership ----

// isMember reports whether a Telegram user is currently in a chat. Requires the
// bot to be an administrator of that chat.
func (b *Bot) isMember(chatID, userID int64) (bool, error) {
	cm, err := b.tb.ChatMemberOf(tele.ChatID(chatID), &tele.User{ID: userID})
	if err != nil {
		return false, err
	}
	switch cm.Role {
	case tele.Creator, tele.Administrator, tele.Member:
		return true, nil
	case tele.Restricted:
		return cm.Member, nil
	default:
		return false, nil
	}
}

// clientFor builds a Hiddify API client for a panel row.
func clientFor(p *db.Panel) *hiddify.Client {
	return hiddify.New(&hiddify.AdminLinkParts{
		Domain:    p.Domain,
		ProxyPath: p.AdminProxyPath,
		AdminUUID: p.AdminUUID,
	})
}

// notifyAdmins sends a text message to every admin.
func (b *Bot) notifyAdmins(text string) {
	ids, _ := b.store.AdminIDs()
	for _, id := range ids {
		_, _ = b.tb.Send(tele.ChatID(id), text)
	}
}

// untilTehranMidnight returns a human string for time left until the daily reset.
func untilTehranMidnight() string {
	now := time.Now().In(db.Tehran)
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, db.Tehran).Add(24 * time.Hour)
	d := next.Sub(now)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%d ساعت و %d دقیقه", h, m)
}
