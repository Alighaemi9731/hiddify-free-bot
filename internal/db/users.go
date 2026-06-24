package db

// UpsertUser ensures a user row exists and refreshes the profile fields.
func (s *Store) UpsertUser(tgID int64, username, firstName string) error {
	_, err := s.db.Exec(`
		INSERT INTO users(tg_id, username, first_name, created_at)
		VALUES(?,?,?,?)
		ON CONFLICT(tg_id) DO UPDATE SET username=excluded.username, first_name=excluded.first_name`,
		tgID, username, firstName, NowUTC())
	return err
}

// GetUser returns a user or nil if absent.
func (s *Store) GetUser(tgID int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT tg_id,username,first_name,referrer_id,referrals_count,
		manual_bonus_mb,daily_cap_override_mb,banned,last_claim_date,created_at
		FROM users WHERE tg_id=?`, tgID).Scan(&u.TGID, &u.Username, &u.FirstName, &u.ReferrerID,
		&u.ReferralsCount, &u.ManualBonusMB, &u.DailyCapOverride, &u.Banned, &u.LastClaimDate, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UserExists reports whether the user is already known.
func (s *Store) UserExists(tgID int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT 1 FROM users WHERE tg_id=?`, tgID).Scan(&n)
	return n == 1
}

// DailyCapMB computes a user's daily volume allowance in MB.
func (s *Store) DailyCapMB(u *User) int {
	if u.DailyCapOverride > 0 {
		return u.DailyCapOverride
	}
	base := s.GetInt(KeyBaseDailyMB)
	bonus := s.GetInt(KeyPerReferralBonusMB)
	max := s.GetInt(KeyMaxDailyMB)
	cap := base + u.ReferralsCount*bonus
	if cap > max {
		cap = max
	}
	return cap + u.ManualBonusMB
}

// SetBan toggles a user's ban flag.
func (s *Store) SetBan(tgID int64, banned bool) error {
	b := 0
	if banned {
		b = 1
	}
	_, err := s.db.Exec(`UPDATE users SET banned=? WHERE tg_id=?`, b, tgID)
	return err
}

// SetManualBonus sets the admin-granted extra MB.
func (s *Store) SetManualBonus(tgID int64, mb int) error {
	_, err := s.db.Exec(`UPDATE users SET manual_bonus_mb=? WHERE tg_id=?`, mb, tgID)
	return err
}

// SetCapOverride sets a hard daily cap override (0 clears it).
func (s *Store) SetCapOverride(tgID int64, mb int) error {
	_, err := s.db.Exec(`UPDATE users SET daily_cap_override_mb=? WHERE tg_id=?`, mb, tgID)
	return err
}

// MarkClaimed records that the user claimed today (Tehran date).
func (s *Store) MarkClaimed(tgID int64, day string) error {
	_, err := s.db.Exec(`UPDATE users SET last_claim_date=? WHERE tg_id=?`, day, tgID)
	return err
}

// ---- referrals ----

// SetReferrer records the inviter for a brand-new user (idempotent, no self).
func (s *Store) SetReferrer(referee, referrer int64) error {
	if referee == referrer || referrer == 0 {
		return nil
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO referrals(referrer_id,referee_id,created_at)
		VALUES(?,?,?)`, referrer, referee, NowUTC())
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE users SET referrer_id=? WHERE tg_id=? AND referrer_id=0`, referrer, referee)
	return err
}

// CountReferralIfPending credits the referrer the first time the referee claims.
// Returns the referrer id (0 if none/already counted) so the caller can notify.
func (s *Store) CountReferralIfPending(referee int64) (int64, error) {
	var referrer int64
	var counted int
	err := s.db.QueryRow(`SELECT referrer_id,counted FROM referrals WHERE referee_id=?`, referee).
		Scan(&referrer, &counted)
	if err != nil || counted == 1 || referrer == 0 {
		return 0, nil
	}
	if _, err := s.db.Exec(`UPDATE referrals SET counted=1 WHERE referee_id=?`, referee); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`UPDATE users SET referrals_count=referrals_count+1 WHERE tg_id=?`, referrer); err != nil {
		return 0, err
	}
	return referrer, nil
}

// ---- admins ----

// AddAdmin grants admin rights.
func (s *Store) AddAdmin(tgID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO admins(tg_id,added_at) VALUES(?,?)`, tgID, NowUTC())
	return err
}

// RemoveAdmin revokes admin rights.
func (s *Store) RemoveAdmin(tgID int64) error {
	_, err := s.db.Exec(`DELETE FROM admins WHERE tg_id=?`, tgID)
	return err
}

// IsAdmin reports whether tgID is an admin.
func (s *Store) IsAdmin(tgID int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT 1 FROM admins WHERE tg_id=?`, tgID).Scan(&n)
	return n == 1
}

// AdminIDs lists all admin ids.
func (s *Store) AdminIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT tg_id FROM admins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AllUserIDs returns every known user id (for broadcasts).
func (s *Store) AllUserIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT tg_id FROM users WHERE banned=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
