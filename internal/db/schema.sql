-- hidybot schema. All timestamps are stored as RFC3339 text (UTC).
-- "Day" values (last_claim_date, shown_date, claim_date) are Tehran calendar
-- dates in the form YYYY-MM-DD so the daily reset lines up with Iran midnight.

CREATE TABLE IF NOT EXISTS users (
    tg_id                INTEGER PRIMARY KEY,
    username             TEXT    DEFAULT '',
    first_name           TEXT    DEFAULT '',
    referrer_id          INTEGER DEFAULT 0,
    referrals_count      INTEGER DEFAULT 0,
    manual_bonus_mb      INTEGER DEFAULT 0,   -- admin-granted extra daily MB
    daily_cap_override_mb INTEGER DEFAULT 0,  -- if >0, replaces the computed cap
    banned               INTEGER DEFAULT 0,
    last_claim_date      TEXT    DEFAULT '',  -- Tehran YYYY-MM-DD of last claim
    created_at           TEXT    DEFAULT ''
);

-- Every config handed out (one new config per day per user).
CREATE TABLE IF NOT EXISTS daily_claims (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tg_id       INTEGER NOT NULL,
    claim_date  TEXT    NOT NULL,            -- Tehran YYYY-MM-DD
    panel_id    INTEGER NOT NULL,
    config_uuid TEXT    NOT NULL,
    sub_link    TEXT    NOT NULL,
    volume_mb   INTEGER NOT NULL,
    created_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_claims_tg ON daily_claims(tg_id);
CREATE INDEX IF NOT EXISTS idx_claims_date ON daily_claims(claim_date);

CREATE TABLE IF NOT EXISTS panels (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT    NOT NULL,
    domain               TEXT    NOT NULL,
    admin_proxy_path     TEXT    NOT NULL,
    admin_uuid           TEXT    NOT NULL,
    sub_domain           TEXT    NOT NULL,
    sub_proxy_path       TEXT    NOT NULL,
    sub_type             TEXT    DEFAULT 'auto',
    daily_volume_limit_gb INTEGER DEFAULT 0,  -- 0 = unlimited
    used_today_mb        INTEGER DEFAULT 0,
    priority             INTEGER DEFAULT 0,
    enabled              INTEGER DEFAULT 1,
    added_at             TEXT    DEFAULT ''
);

-- Forced-join channels = advertising queue.
CREATE TABLE IF NOT EXISTS channels (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id         INTEGER NOT NULL,        -- Telegram chat id (-100...)
    title           TEXT    DEFAULT '',
    username        TEXT    DEFAULT '',       -- without @ (public channels)
    invite_link     TEXT    DEFAULT '',
    kind            TEXT    DEFAULT 'quota',  -- 'permanent' | 'quota'
    quota_target    INTEGER DEFAULT 0,        -- target NEW joins (quota kind)
    new_joins_count INTEGER DEFAULT 0,        -- achieved NEW joins
    priority        INTEGER DEFAULT 0,        -- higher = shown first / finished first
    enabled         INTEGER DEFAULT 1,
    is_join_request INTEGER DEFAULT 0,
    added_at        TEXT    DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_chat ON channels(chat_id);

-- Per (channel,user) snapshot used to count only NEW joins and de-dupe.
CREATE TABLE IF NOT EXISTS channel_user (
    channel_id        INTEGER NOT NULL,
    tg_id             INTEGER NOT NULL,
    shown_date        TEXT    NOT NULL,       -- Tehran YYYY-MM-DD it was assigned
    was_member_before INTEGER DEFAULT 0,
    counted           INTEGER DEFAULT 0,      -- already counted toward quota?
    joined_at         TEXT    DEFAULT '',
    PRIMARY KEY (channel_id, tg_id)
);

CREATE TABLE IF NOT EXISTS referrals (
    referrer_id INTEGER NOT NULL,
    referee_id  INTEGER NOT NULL,
    counted     INTEGER DEFAULT 0,            -- counted after referee's first claim
    created_at  TEXT    DEFAULT '',
    PRIMARY KEY (referee_id)
);

CREATE TABLE IF NOT EXISTS admins (
    tg_id    INTEGER PRIMARY KEY,
    added_at TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id   INTEGER,
    action     TEXT,
    detail     TEXT,
    created_at TEXT
);
