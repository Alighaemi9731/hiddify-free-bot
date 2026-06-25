package db

import (
	"database/sql"
	"time"
)

// InsertPendingClaim writes the claim row BEFORE the panel user is created
// (outbox pattern) so a crash mid-create can never orphan a panel user. It does
// not yet bump panel usage or the user's last_claim_date — see ConfirmClaim.
// expire_at = created_at + configDays·24h pins the config's real lifetime.
func (s *Store) InsertPendingClaim(c *Claim, configDays int) (int64, error) {
	if configDays < 1 {
		configDays = 1
	}
	now := time.Now().UTC()
	expire := now.Add(time.Duration(configDays) * 24 * time.Hour).Format(time.RFC3339)
	res, err := s.db.Exec(`INSERT INTO daily_claims
		(tg_id,claim_date,panel_id,config_uuid,sub_link,volume_mb,created_at,status,expire_at)
		VALUES(?,?,?,?,?,?,?, 'pending', ?)`,
		c.TGID, c.ClaimDate, c.PanelID, c.ConfigUUID, c.SubLink, c.VolumeMB, now.Format(time.RFC3339), expire)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ConfirmClaim marks a pending claim active, records the panel numeric id, bumps
// the panel's usage and the user's last_claim_date — all atomically.
func (s *Store) ConfirmClaim(claimID, tgID, panelID int64, panelUserID, volumeMB int, claimDate string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE daily_claims SET status='active', panel_user_id=? WHERE id=?`,
		panelUserID, claimID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE panels SET used_today_mb=used_today_mb+? WHERE id=?`, volumeMB, panelID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET last_claim_date=? WHERE tg_id=?`, claimDate, tgID); err != nil {
		return err
	}
	return tx.Commit()
}

// LastClaim returns the most recent claim for a user (nil if none).
func (s *Store) LastClaim(tgID int64) (*Claim, error) {
	c := &Claim{}
	err := s.db.QueryRow(`SELECT id,tg_id,claim_date,panel_id,config_uuid,sub_link,volume_mb,created_at
		FROM daily_claims WHERE tg_id=? ORDER BY id DESC LIMIT 1`, tgID).Scan(
		&c.ID, &c.TGID, &c.ClaimDate, &c.PanelID, &c.ConfigUUID, &c.SubLink, &c.VolumeMB, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// OldConfig identifies an issued config for cleanup.
type OldConfig struct {
	ID          int64
	PanelID     int64
	ConfigUUID  string
	PanelUserID int
}

func scanOldConfigs(rows *sql.Rows, err error) ([]OldConfig, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OldConfig
	for rows.Next() {
		var o OldConfig
		if err := rows.Scan(&o.ID, &o.PanelID, &o.ConfigUUID, &o.PanelUserID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ExpiredConfigs returns active configs whose lifetime has elapsed. Rows written
// before per-claim expiry existed (no expire_at) fall back to created_at vs the
// caller-supplied fallback cutoff (now - configDays·24h).
func (s *Store) ExpiredConfigs(nowUTC, fallbackUTC string) ([]OldConfig, error) {
	return scanOldConfigs(s.db.Query(`SELECT id,panel_id,config_uuid,panel_user_id FROM daily_claims
		WHERE status='active' AND
		((expire_at!='' AND expire_at<=?) OR (expire_at='' AND created_at<=?))`, nowUTC, fallbackUTC))
}

// PendingClaimsOlderThan returns pending claims created before the cutoff (UTC
// RFC3339) — used to reconcile crashed creates.
func (s *Store) PendingClaimsOlderThan(cutoffUTC string) ([]OldConfig, error) {
	return scanOldConfigs(s.db.Query(`SELECT id,panel_id,config_uuid,panel_user_id FROM daily_claims
		WHERE status='pending' AND created_at<=?`, cutoffUTC))
}

// MarkClaimActive promotes a reconciled pending row (panel user exists).
func (s *Store) MarkClaimActive(id int64, panelUserID int) error {
	_, err := s.db.Exec(`UPDATE daily_claims SET status='active', panel_user_id=? WHERE id=?`, panelUserID, id)
	return err
}

// DeleteClaimRow removes a single claim row.
func (s *Store) DeleteClaimRow(id int64) error {
	_, err := s.db.Exec(`DELETE FROM daily_claims WHERE id=?`, id)
	return err
}

// DeleteClaimRows removes many claim rows after their panel users are cleaned up.
func (s *Store) DeleteClaimRows(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM daily_claims WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Stats is the admin dashboard summary.
type Stats struct {
	TotalUsers   int
	BannedUsers  int
	ClaimsToday  int
	ClaimsTotal  int
	NewUsers24h  int
	TotalPanels  int
	TotalChannel int
}

func (s *Store) Stats(today string) (*Stats, error) {
	st := &Stats{}
	q := func(dst *int, query string, args ...any) error {
		return s.db.QueryRow(query, args...).Scan(dst)
	}
	if err := q(&st.TotalUsers, `SELECT COUNT(*) FROM users`); err != nil {
		return nil, err
	}
	_ = q(&st.BannedUsers, `SELECT COUNT(*) FROM users WHERE banned=1`)
	_ = q(&st.ClaimsToday, `SELECT COUNT(*) FROM daily_claims WHERE claim_date=?`, today)
	_ = q(&st.ClaimsTotal, `SELECT COUNT(*) FROM daily_claims`)
	_ = q(&st.TotalPanels, `SELECT COUNT(*) FROM panels`)
	_ = q(&st.TotalChannel, `SELECT COUNT(*) FROM channels`)
	return st, nil
}

// TopReferrer is one row of the referral leaderboard.
type TopReferrer struct {
	TGID      int64
	Username  string
	Referrals int
}

// TotalRevenue sums earned revenue across all channels (joins/1000 * price).
func (s *Store) TotalRevenue() int {
	var v int
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(new_joins_count * price_per_1k / 1000), 0) FROM channels`).Scan(&v)
	return v
}

// NewUsersToday counts users created at/after thresholdUTC (start of Tehran day).
func (s *Store) NewUsersToday(thresholdUTC string) int {
	var v int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE created_at >= ?`, thresholdUTC).Scan(&v)
	return v
}

func (s *Store) TopReferrers(limit int) ([]TopReferrer, error) {
	rows, err := s.db.Query(`SELECT tg_id,username,referrals_count FROM users
		WHERE referrals_count > 0 ORDER BY referrals_count DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopReferrer
	for rows.Next() {
		var t TopReferrer
		if err := rows.Scan(&t.TGID, &t.Username, &t.Referrals); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
