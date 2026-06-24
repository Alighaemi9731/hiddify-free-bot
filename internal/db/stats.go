package db

// RecordClaim stores an issued config and bumps the panel usage counter.
func (s *Store) RecordClaim(c *Claim) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO daily_claims(tg_id,claim_date,panel_id,config_uuid,sub_link,volume_mb,created_at)
		VALUES(?,?,?,?,?,?,?)`, c.TGID, c.ClaimDate, c.PanelID, c.ConfigUUID, c.SubLink, c.VolumeMB, NowUTC()); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE panels SET used_today_mb=used_today_mb+? WHERE id=?`, c.VolumeMB, c.PanelID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET last_claim_date=? WHERE tg_id=?`, c.ClaimDate, c.TGID); err != nil {
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

// YesterdayConfigs returns config uuids+panel ids claimed before `day` that may
// be cleaned up from their panels.
type OldConfig struct {
	ID         int64
	PanelID    int64
	ConfigUUID string
}

func (s *Store) ConfigsBefore(day string) ([]OldConfig, error) {
	rows, err := s.db.Query(`SELECT id,panel_id,config_uuid FROM daily_claims WHERE claim_date < ?`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OldConfig
	for rows.Next() {
		var o OldConfig
		if err := rows.Scan(&o.ID, &o.PanelID, &o.ConfigUUID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteClaimRow removes a claim row after its panel user is cleaned up.
func (s *Store) DeleteClaimRow(id int64) error {
	_, err := s.db.Exec(`DELETE FROM daily_claims WHERE id=?`, id)
	return err
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
