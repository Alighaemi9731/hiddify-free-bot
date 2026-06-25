package db

import "math/rand"

const channelCols = `id,chat_id,title,username,invite_link,kind,quota_target,new_joins_count,priority,enabled,is_join_request,price_per_1k,advertiser,notified_done,added_at`

func scanChannel(scan func(...any) error) (*Channel, error) {
	c := &Channel{}
	err := scan(&c.ID, &c.ChatID, &c.Title, &c.Username, &c.InviteLink, &c.Kind, &c.QuotaTarget,
		&c.NewJoinsCount, &c.Priority, &c.Enabled, &c.IsJoinRequest, &c.PricePer1k, &c.Advertiser,
		&c.NotifiedDone, &c.AddedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// AddChannel inserts (or replaces by chat_id) a channel.
func (s *Store) AddChannel(c *Channel) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO channels
		(chat_id,title,username,invite_link,kind,quota_target,new_joins_count,priority,enabled,is_join_request,price_per_1k,advertiser,added_at)
		VALUES(?,?,?,?,?,?,0,?,1,?,?,?,?)
		ON CONFLICT(chat_id) DO UPDATE SET title=excluded.title, username=excluded.username,
			invite_link=excluded.invite_link, kind=excluded.kind, quota_target=excluded.quota_target,
			priority=excluded.priority, is_join_request=excluded.is_join_request,
			price_per_1k=excluded.price_per_1k, advertiser=excluded.advertiser`,
		c.ChatID, c.Title, c.Username, c.InviteLink, c.Kind, c.QuotaTarget, c.Priority,
		boolToInt(c.IsJoinRequest), c.PricePer1k, c.Advertiser, NowUTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetChannelByChat returns the tracked channel for a Telegram chat id, or nil.
func (s *Store) GetChannelByChat(chatID int64) (*Channel, error) {
	return scanChannel(s.db.QueryRow(`SELECT `+channelCols+` FROM channels WHERE chat_id=?`, chatID).Scan)
}

// SetChannelPrice sets the advertiser price per 1000 NEW joins.
func (s *Store) SetChannelPrice(id int64, price int) error {
	_, err := s.db.Exec(`UPDATE channels SET price_per_1k=? WHERE id=?`, price, id)
	return err
}

// Channels lists all channels ordered by priority then id.
func (s *Store) Channels() ([]*Channel, error) {
	return s.queryChannels(`SELECT ` + channelCols + ` FROM channels ORDER BY priority DESC, id`)
}

// GetChannel returns one channel by id.
func (s *Store) GetChannel(id int64) (*Channel, error) {
	return scanChannel(s.db.QueryRow(`SELECT `+channelCols+` FROM channels WHERE id=?`, id).Scan)
}

// DeleteChannel removes a channel and its membership snapshots.
func (s *Store) DeleteChannel(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM channel_user WHERE channel_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM channels WHERE id=?`, id)
	return err
}

func (s *Store) SetChannelEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE channels SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	return err
}

func (s *Store) SetChannelQuota(id int64, target int) error {
	// Re-arm the completion alert when the target is raised above current joins.
	_, err := s.db.Exec(`UPDATE channels SET quota_target=?,
		notified_done=CASE WHEN new_joins_count < ? THEN 0 ELSE notified_done END WHERE id=?`,
		target, target, id)
	return err
}

func (s *Store) SetChannelPriority(id int64, p int) error {
	_, err := s.db.Exec(`UPDATE channels SET priority=? WHERE id=?`, p, id)
	return err
}

func (s *Store) queryChannels(q string, args ...any) ([]*Channel, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		c, err := scanChannel(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TodaysChannels returns the channels already assigned to a user for `day`
// (stable selection within the day).
func (s *Store) TodaysChannels(tgID int64, day string) ([]*Channel, error) {
	return s.queryChannels(`SELECT `+prefixCols("c", channelCols)+`
		FROM channels c JOIN channel_user cu ON cu.channel_id=c.id
		WHERE cu.tg_id=? AND cu.shown_date=? AND c.enabled=1
		ORDER BY c.priority DESC, c.id`, tgID, day)
}

// PickChannelsForUser chooses the channel set for a fresh daily assignment:
// every enabled permanent channel plus up to n quota channels (with remaining
// quota, not yet completed by this user) chosen by priority-weighted random.
func (s *Store) PickChannelsForUser(tgID int64, n int) ([]*Channel, error) {
	perm, err := s.queryChannels(`SELECT ` + channelCols + ` FROM channels
		WHERE enabled=1 AND kind='permanent' ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	pool, err := s.queryChannels(`SELECT `+prefixCols("c", channelCols)+`
		FROM channels c
		WHERE c.enabled=1 AND c.kind='quota' AND c.new_joins_count < c.quota_target
		  AND NOT EXISTS (SELECT 1 FROM channel_user cu
		                  WHERE cu.channel_id=c.id AND cu.tg_id=? AND cu.counted=1)
		ORDER BY c.priority DESC, c.id`, tgID)
	if err != nil {
		return nil, err
	}
	picked := weightedSample(pool, n)
	return append(perm, picked...), nil
}

// AssignChannel persists a daily assignment, capturing was_member_before only
// when the (channel,user) pair is first seen.
func (s *Store) AssignChannel(channelID, tgID int64, day string, wasMemberBefore bool) error {
	_, err := s.db.Exec(`INSERT INTO channel_user(channel_id,tg_id,shown_date,was_member_before,counted)
		VALUES(?,?,?,?,0)
		ON CONFLICT(channel_id,tg_id) DO UPDATE SET shown_date=excluded.shown_date`,
		channelID, tgID, day, boolToInt(wasMemberBefore))
	return err
}

// ChannelUserState reports the (was_member_before, counted) snapshot.
func (s *Store) ChannelUserState(channelID, tgID int64) (wasMember, counted bool) {
	var wm, c int
	_ = s.db.QueryRow(`SELECT was_member_before,counted FROM channel_user WHERE channel_id=? AND tg_id=?`,
		channelID, tgID).Scan(&wm, &c)
	return wm == 1, c == 1
}

// CreditNewJoin marks the pair counted and increments the channel's NEW-join
// counter — used only when the user was NOT a member before. It returns
// justCompleted=true the first time a quota channel reaches its target.
func (s *Store) CreditNewJoin(channelID, tgID int64) (justCompleted bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE channel_user SET counted=1, joined_at=? WHERE channel_id=? AND tg_id=?`,
		NowUTC(), channelID, tgID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE channels SET new_joins_count=new_joins_count+1 WHERE id=?`, channelID); err != nil {
		return false, err
	}
	// Detect first-time completion of a quota order.
	var kind string
	var count, target, notified int
	if err = tx.QueryRow(`SELECT kind,new_joins_count,quota_target,notified_done FROM channels WHERE id=?`,
		channelID).Scan(&kind, &count, &target, &notified); err != nil {
		return false, err
	}
	if kind == "quota" && target > 0 && count >= target && notified == 0 {
		if _, err = tx.Exec(`UPDATE channels SET notified_done=1 WHERE id=?`, channelID); err != nil {
			return false, err
		}
		justCompleted = true
	}
	return justCompleted, tx.Commit()
}

// MarkCounted flags the pair as counted without crediting a new join (the user
// was already a member before we asked).
func (s *Store) MarkCounted(channelID, tgID int64) error {
	_, err := s.db.Exec(`UPDATE channel_user SET counted=1 WHERE channel_id=? AND tg_id=?`, channelID, tgID)
	return err
}

func weightedSample(pool []*Channel, n int) []*Channel {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	if n >= len(pool) {
		out := make([]*Channel, len(pool))
		copy(out, pool)
		return out
	}
	items := make([]*Channel, len(pool))
	copy(items, pool)
	var out []*Channel
	for len(out) < n && len(items) > 0 {
		total := 0
		for _, c := range items {
			total += c.Priority + 1
		}
		r := rand.Intn(total)
		idx := 0
		for i, c := range items {
			r -= c.Priority + 1
			if r < 0 {
				idx = i
				break
			}
		}
		out = append(out, items[idx])
		items = append(items[:idx], items[idx+1:]...)
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// prefixCols rewrites "a,b,c" into "t.a,t.b,t.c" for JOIN disambiguation.
func prefixCols(prefix, cols string) string {
	out := ""
	col := ""
	flush := func() {
		if col != "" {
			if out != "" {
				out += ","
			}
			out += prefix + "." + col
			col = ""
		}
	}
	for _, r := range cols {
		switch r {
		case ',':
			flush()
		case ' ', '\n', '\t':
			// skip whitespace inside the column list
		default:
			col += string(r)
		}
	}
	flush()
	return out
}
