package db

// Setting keys and their defaults.
const (
	KeyRandomChannelsPerDay = "random_channels_per_day" // how many quota channels to show/day
	KeyBaseDailyMB          = "base_daily_mb"
	KeyPerReferralBonusMB   = "per_referral_bonus_mb"
	KeyMaxDailyMB           = "max_daily_mb"
	KeyConfigDays           = "config_days"      // package_days for each daily config
	KeyDefaultSubType       = "default_sub_type" // auto|sub|sub64|clash|clashmeta|singbox
	KeyAcceptingNewUsers    = "accepting_new_users"
	KeyMaintenance          = "maintenance"
	KeySupportContact       = "support_contact"
	KeyBackupKeep           = "backup_keep"
)

var defaultSettings = map[string]string{
	KeyRandomChannelsPerDay: "2",
	KeyBaseDailyMB:          "500",
	KeyPerReferralBonusMB:   "500",
	KeyMaxDailyMB:           "3072",
	KeyConfigDays:           "1",
	KeyDefaultSubType:       "auto",
	KeyAcceptingNewUsers:    "1",
	KeyMaintenance:          "0",
	KeySupportContact:       "",
	KeyBackupKeep:           "12",
}

// User is a Telegram user known to the bot.
type User struct {
	TGID             int64
	Username         string
	FirstName        string
	ReferrerID       int64
	ReferralsCount   int
	ManualBonusMB    int
	DailyCapOverride int
	Banned           bool
	LastClaimDate    string
	CreatedAt        string
}

// Panel is a connected Hiddify panel.
type Panel struct {
	ID                 int64
	Name               string
	Domain             string
	AdminProxyPath     string
	AdminUUID          string
	SubDomain          string
	SubProxyPath       string
	SubType            string
	DailyVolumeLimitGB int
	UsedTodayMB        int
	Priority           int
	Enabled            bool
	AddedAt            string
}

// Channel is a forced-join channel / advertising slot.
type Channel struct {
	ID            int64
	ChatID        int64
	Title         string
	Username      string
	InviteLink    string
	Kind          string // permanent | quota
	QuotaTarget   int
	NewJoinsCount int
	Priority      int
	Enabled       bool
	IsJoinRequest bool
	AddedAt       string
}

// Remaining returns how many NEW joins are still needed for a quota channel.
func (c Channel) Remaining() int {
	if c.Kind == "permanent" {
		return -1 // unlimited
	}
	r := c.QuotaTarget - c.NewJoinsCount
	if r < 0 {
		return 0
	}
	return r
}

// Claim records one issued config.
type Claim struct {
	ID         int64
	TGID       int64
	ClaimDate  string
	PanelID    int64
	ConfigUUID string
	SubLink    string
	VolumeMB   int
	CreatedAt  string
}
