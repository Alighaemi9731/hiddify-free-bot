package db

import "sort"

// AddPanel inserts a panel and returns its id.
func (s *Store) AddPanel(p *Panel) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO panels
		(name,domain,admin_proxy_path,admin_uuid,sub_domain,sub_proxy_path,sub_type,
		 daily_volume_limit_gb,used_today_mb,priority,enabled,added_at)
		VALUES(?,?,?,?,?,?,?,?,0,?,1,?)`,
		p.Name, p.Domain, p.AdminProxyPath, p.AdminUUID, p.SubDomain, p.SubProxyPath, p.SubType,
		p.DailyVolumeLimitGB, p.Priority, NowUTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanPanel(scan func(...any) error) (*Panel, error) {
	p := &Panel{}
	err := scan(&p.ID, &p.Name, &p.Domain, &p.AdminProxyPath, &p.AdminUUID, &p.SubDomain,
		&p.SubProxyPath, &p.SubType, &p.DailyVolumeLimitGB, &p.UsedTodayMB, &p.Priority,
		&p.Enabled, &p.AddedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

const panelCols = `id,name,domain,admin_proxy_path,admin_uuid,sub_domain,sub_proxy_path,sub_type,
	daily_volume_limit_gb,used_today_mb,priority,enabled,added_at`

// Panels lists all panels ordered by priority then id.
func (s *Store) Panels() ([]*Panel, error) {
	rows, err := s.db.Query(`SELECT ` + panelCols + ` FROM panels ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Panel
	for rows.Next() {
		p, err := scanPanel(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPanel returns one panel by id.
func (s *Store) GetPanel(id int64) (*Panel, error) {
	return scanPanel(s.db.QueryRow(`SELECT `+panelCols+` FROM panels WHERE id=?`, id).Scan)
}

// DeletePanel removes a panel.
func (s *Store) DeletePanel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM panels WHERE id=?`, id)
	return err
}

// SetPanelEnabled toggles a panel.
func (s *Store) SetPanelEnabled(id int64, enabled bool) error {
	e := 0
	if enabled {
		e = 1
	}
	_, err := s.db.Exec(`UPDATE panels SET enabled=? WHERE id=?`, e, id)
	return err
}

// SetPanelLimit sets the daily volume limit (GB; 0 = unlimited).
func (s *Store) SetPanelLimit(id int64, gb int) error {
	_, err := s.db.Exec(`UPDATE panels SET daily_volume_limit_gb=? WHERE id=?`, gb, id)
	return err
}

// SetPanelPriority sets the panel priority.
func (s *Store) SetPanelPriority(id int64, p int) error {
	_, err := s.db.Exec(`UPDATE panels SET priority=? WHERE id=?`, p, id)
	return err
}

// PickPanelForVolume returns the best enabled panel that can still absorb
// needMB today (least-loaded first), or nil if none has capacity.
func (s *Store) PickPanelForVolume(needMB int) (*Panel, error) {
	cands, err := s.PanelsForVolume(needMB)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	return cands[0], nil
}

// PanelsForVolume returns all enabled panels with budget for needMB, least-used
// first — so the claim flow can fall through to the next one on ErrPanelFull.
func (s *Store) PanelsForVolume(needMB int) ([]*Panel, error) {
	panels, err := s.Panels()
	if err != nil {
		return nil, err
	}
	var out []*Panel
	for _, p := range panels {
		if !p.Enabled {
			continue
		}
		if p.DailyVolumeLimitGB > 0 && p.UsedTodayMB+needMB > p.DailyVolumeLimitGB*1024 {
			continue // would exceed today's budget
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UsedTodayMB < out[j].UsedTodayMB })
	return out, nil
}

// AddPanelUsage adds consumed MB to a panel's daily counter.
func (s *Store) AddPanelUsage(id int64, mb int) error {
	_, err := s.db.Exec(`UPDATE panels SET used_today_mb=used_today_mb+? WHERE id=?`, mb, id)
	return err
}

// ResetPanelUsage zeroes all panels' daily counters (Tehran midnight cron).
func (s *Store) ResetPanelUsage() error {
	_, err := s.db.Exec(`UPDATE panels SET used_today_mb=0`)
	return err
}
