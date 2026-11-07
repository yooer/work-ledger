package service

import (
	"time"

	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

// Dashboard 工作台统计。
type Dashboard struct {
	*Core
	Records  *Records
	Settings *Settings
}

// PeriodStat 某时间段的汇总。
type PeriodStat struct {
	RecordCount      int64
	TotalWorkCount   int64
	AvgDifficulty    float64
	ParticipantCount int64
}

// DashboardData 工作台数据（统计口径：管理员全公司、经理本人+本部门、员工本人）。
type DashboardData struct {
	Today, Week, Month PeriodStat
	WeekStart, WeekEnd string
	Categories         []Row
	Recent             []Row
	ScopeLabel         string
	SystemCounts       map[string]int64
}

func (s *Dashboard) aggregate(sc Scope, start, end string) (PeriodStat, error) {
	cond, args := sc.SQL("r")
	args = append(args, start+" 00:00:00", end+" 00:00:00")
	row, err := db.One(s.DB, `SELECT COUNT(*) AS record_count, COALESCE(SUM(r.work_count), 0) AS total_work_count,
		COALESCE(ROUND(AVG(r.difficulty), 1), 0) AS avg_difficulty FROM work_records r
		WHERE `+cond+` AND r.work_at >= ? AND r.work_at < ?`, args...)
	if err != nil {
		return PeriodStat{}, err
	}
	pc, err := db.Int(s.DB, `SELECT COUNT(DISTINCT p.user_id) FROM work_records r
		JOIN work_record_participants p ON p.work_record_id = r.id
		WHERE `+cond+` AND r.work_at >= ? AND r.work_at < ?`, args...)
	if err != nil {
		return PeriodStat{}, err
	}
	return PeriodStat{RecordCount: row.Int("record_count"), TotalWorkCount: row.Int("total_work_count"), AvgDifficulty: row.Float("avg_difficulty"), ParticipantCount: pc}, nil
}

// Build 计算工作台数据；本周按系统设置的周报周期对齐。
func (s *Dashboard) Build(user Row) (*DashboardData, error) {
	sc, err := s.ScopeFor(user)
	if err != nil {
		return nil, err
	}
	today := timeutil.Today()
	weekStart := s.Settings.StartOfCycleFor(today)
	n := timeutil.Now()
	monthStart := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, timeutil.Location())
	nextMonth := monthStart.AddDate(0, 1, 0).Format(timeutil.Date)
	d := &DashboardData{WeekStart: weekStart, WeekEnd: WeekEndFor(weekStart)}
	if d.Today, err = s.aggregate(sc, today, timeutil.AddDays(today, 1)); err != nil {
		return nil, err
	}
	if d.Week, err = s.aggregate(sc, weekStart, timeutil.AddDays(weekStart, 7)); err != nil {
		return nil, err
	}
	if d.Month, err = s.aggregate(sc, monthStart.Format(timeutil.Date), nextMonth); err != nil {
		return nil, err
	}
	cond, args := sc.SQL("r")
	args = append(args, monthStart.Format(timeutil.Date)+" 00:00:00", nextMonth+" 00:00:00")
	if d.Categories, err = db.All(s.DB, `SELECT `+categoryLabelSQL+` AS name, COUNT(*) AS record_count, COALESCE(SUM(r.work_count), 0) AS total_count
		FROM work_records r JOIN work_categories wc ON wc.id = r.work_category_id
		WHERE `+cond+` AND r.work_at >= ? AND r.work_at < ?
		GROUP BY wc.id, wc.name ORDER BY total_count DESC, record_count DESC`, args...); err != nil {
		return nil, err
	}
	list, err := s.Records.List(sc, RecordFilter{}, 1, 6)
	if err != nil {
		return nil, err
	}
	d.Recent = list.Items
	switch {
	case sc.All:
		d.ScopeLabel = "全公司"
	case sc.IsTeamScope():
		d.ScopeLabel = "本人及团队"
	default:
		d.ScopeLabel = "本人"
	}
	return d, nil
}

// SystemCounts 系统设置页的数据规模。
func (c *Core) SystemCounts() (map[string]int64, error) {
	out := map[string]int64{}
	for key, table := range map[string]string{"users": "users", "departments": "departments", "records": "work_records",
		"reports": "weekly_reports", "annual_reports": "annual_reports", "period_reports": "period_reports"} {
		n, err := db.Int(c.DB, "SELECT COUNT(*) FROM "+table)
		if err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, nil
}
