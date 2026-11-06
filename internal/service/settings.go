package service

import (
	"strconv"
	"sync"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

// 系统设置键名（app_settings 表，键值形式，与 PHP 版兼容）。
const (
	SettingWeekStart       = "week_start_weekday"
	SettingWeekEnd         = "week_end_weekday"
	SettingAutoWeekly      = "auto_weekly_enabled"
	SettingAutoWeeklyDelay = "auto_weekly_delay_days" // 周期结束后第几天生成（1=次日）
	SettingAutoWeeklyTime  = "auto_weekly_time"       // HH:MM
	SettingAutoWeeklyStyle = "auto_weekly_style"
	SettingAutoYearly      = "auto_yearly_enabled"
	SettingAutoYearlyDay   = "auto_yearly_day" // 1 月几号
	SettingAutoYearlyTime  = "auto_yearly_time"
	SettingAutoYearlyStyle = "auto_yearly_style"
	SettingSchedulerLast   = "scheduler_last_result"
	SettingLeaderCanEdit   = "leader_can_edit_records"   // 允许主管领导/部门经理修改下属台账
	SettingLeaderCanDelete = "leader_can_delete_records" // 允许主管领导/部门经理删除下属台账（默认关闭）
)

// DefaultSettings 安装时写入的默认值（已存在则不覆盖）。自动生成默认关闭。
var DefaultSettings = map[string]string{
	SettingWeekStart:       "1",
	SettingWeekEnd:         "7",
	SettingAutoWeekly:      "0",
	SettingAutoWeeklyDelay: "1",
	SettingAutoWeeklyTime:  "08:00",
	SettingAutoWeeklyStyle: "normal",
	SettingAutoYearly:      "0",
	SettingAutoYearlyDay:   "2",
	SettingAutoYearlyTime:  "08:00",
	SettingAutoYearlyStyle: "normal",
	SettingLeaderCanEdit:   "1",
	SettingLeaderCanDelete: "0",
}

type Settings struct {
	*Core
	mu    sync.Mutex
	cache map[string]string
}

func (s *Settings) All() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		return s.cache, nil
	}
	rows, err := db.All(s.DB, "SELECT name, value FROM app_settings")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, r := range rows {
		m[r.Str("name")] = r.Str("value")
	}
	s.cache = m
	return m, nil
}

func (s *Settings) Get(name, def string) string {
	all, err := s.All()
	if err != nil {
		return def
	}
	if v, ok := all[name]; ok {
		return v
	}
	if v, ok := DefaultSettings[name]; ok && def == "" {
		return v
	}
	return def
}

func (s *Settings) GetInt(name string, def int) int {
	n, err := strconv.Atoi(s.Get(name, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return n
}

func (s *Settings) Bool(name string) bool { return s.Get(name, "0") == "1" }

// LeaderCanEdit 是否允许领导修改下属台账（未设置时默认允许）。
func (s *Settings) LeaderCanEdit() bool { return s.Get(SettingLeaderCanEdit, "") == "1" }

// LeaderCanDelete 是否允许领导删除下属台账（未设置时默认不允许）。
func (s *Settings) LeaderCanDelete() bool { return s.Get(SettingLeaderCanDelete, "") == "1" }

// SetLeaderPermissions 保存“允许领导修改 / 删除下属台账”两个开关。
func (s *Settings) SetLeaderPermissions(canEdit, canDelete bool) error {
	flag := func(on bool) string {
		if on {
			return "1"
		}
		return "0"
	}
	return s.SetMany(map[string]string{SettingLeaderCanEdit: flag(canEdit), SettingLeaderCanDelete: flag(canDelete)})
}

// SetLeaderCanEdit 保存“允许领导修改下属台账”开关。
func (s *Settings) SetLeaderCanEdit(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.Set(SettingLeaderCanEdit, v)
}

func (s *Settings) set(q db.Q, name, value string) error {
	_, err := db.Exec(q, `INSERT INTO app_settings (name, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, name, value, now())
	return err
}

// Set 写入单个设置。
func (s *Settings) Set(name, value string) error {
	err := s.set(s.DB, name, value)
	s.ClearCache()
	return err
}

// SetMany 在一个事务内写入多个设置。
func (s *Settings) SetMany(values map[string]string) error {
	err := s.DB.Tx(func(tx db.Q) error {
		for k, v := range values {
			if err := s.set(tx, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	s.ClearCache()
	return err
}

func (s *Settings) ClearCache() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// EnsureDefaults 写入缺失的默认设置。
func (s *Settings) EnsureDefaults() error {
	for k, v := range DefaultSettings {
		if _, err := db.Exec(s.DB, "INSERT OR IGNORE INTO app_settings (name, value, updated_at) VALUES (?, ?, ?)", k, v, now()); err != nil {
			return err
		}
	}
	s.ClearCache()
	return nil
}

// WeekStartDay 周期起始星期（1=周一…7=周日），配置非法时回退周一。
func (s *Settings) WeekStartDay() int {
	day := s.GetInt(SettingWeekStart, 1)
	end := s.GetInt(SettingWeekEnd, 7)
	if day < 1 || day > 7 || ((end-day+7)%7) != 6 {
		return 1
	}
	return day
}

func (s *Settings) WeekEndDay() int { return ((s.WeekStartDay() - 1 + 6) % 7) + 1 }

func (s *Settings) WeekCycleLabel() string {
	return timeutil.DayName(s.WeekStartDay()) + " 至 " + timeutil.DayName(s.WeekEndDay())
}

// SaveWeekCycle 保存周报周期。
func (s *Settings) SaveWeekCycle(start, end int) error {
	if start < 1 || start > 7 || end < 1 || end > 7 {
		return apperr.New("星期设置无效。")
	}
	if ((end - start + 7) % 7) != 6 {
		return apperr.New("周结束日必须是开始日之后的第 6 天，以保证一个完整 7 天周期。")
	}
	return s.SetMany(map[string]string{SettingWeekStart: strconv.Itoa(start), SettingWeekEnd: strconv.Itoa(end)})
}

// StartOfCycleFor 返回 date 所在周期的起始日期。
func (s *Settings) StartOfCycleFor(date string) string {
	t, ok := timeutil.ParseDate(date)
	if !ok {
		t, _ = timeutil.ParseDate(timeutil.Today())
	}
	offset := (timeutil.Weekday(t) - s.WeekStartDay() + 7) % 7
	return t.AddDate(0, 0, -offset).Format(timeutil.Date)
}

// DefaultWeekStart 上一个完整周期的起始日期。
func (s *Settings) DefaultWeekStart() string {
	return timeutil.AddDays(s.StartOfCycleFor(timeutil.Today()), -7)
}

func WeekEndFor(start string) string { return timeutil.AddDays(start, 6) }
