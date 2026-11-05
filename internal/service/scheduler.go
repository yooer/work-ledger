package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

// Scheduler 内置定时生成全公司周报 / 年度总结（默认关闭，由管理员在“系统与 AI”中开启）。
//
// 规则：
//   - 周报：上一个完整周期结束后第 N 天（默认次日）的指定时间之后，若该周期还没有“全公司”周报，则生成一次。
//   - 年度：每年 1 月第 N 天（默认 2 日）指定时间之后，若上一年度还没有“全公司”年度总结，则生成一次。
//   - 服务停机错过时间点的，启动后会自动补生成；失败会在 30 分钟后重试，同一周期最多尝试 5 次。
type Scheduler struct {
	*Core
	Settings *Settings
	Weekly   *WeeklyReports
	Annual   *AnnualReports
	attempts map[string]attempt
}

type attempt struct {
	count int
	last  time.Time
}

// SchedulerStatus 最近一次执行结果（存于 app_settings，供设置页展示）。
type SchedulerStatus struct {
	Time    string `json:"time"`
	Kind    string `json:"kind"`
	Period  string `json:"period"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Run 每分钟检查一次，直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	if s.attempts == nil {
		s.attempts = map[string]attempt{}
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			s.Log.Error("scheduler panic", "err", p)
		}
	}()
	s.Settings.ClearCache()
	n := timeutil.Now()
	if s.Settings.Bool(SettingAutoWeekly) {
		start, due := s.WeeklyDue(n)
		if !n.Before(due) {
			s.maybeRun(ctx, "weekly", start, func() (string, error) { return s.generateWeekly(ctx, start) })
		}
	}
	if s.Settings.Bool(SettingAutoYearly) {
		year, due := s.YearlyDue(n)
		if !n.Before(due) {
			s.maybeRun(ctx, "yearly", strconv.Itoa(year), func() (string, error) { return s.generateYearly(ctx, year) })
		}
	}
}

// WeeklyDue 返回“上一个完整周期”的起始日期，以及它应当生成的时间点。
func (s *Scheduler) WeeklyDue(n time.Time) (string, time.Time) {
	start := s.Settings.DefaultWeekStart()
	end := WeekEndFor(start)
	delay := s.Settings.GetInt(SettingAutoWeeklyDelay, 1)
	if delay < 1 || delay > 6 {
		delay = 1
	}
	h, m := parseHHMM(s.Settings.Get(SettingAutoWeeklyTime, "08:00"))
	endDate, _ := timeutil.ParseDate(end)
	due := time.Date(endDate.Year(), endDate.Month(), endDate.Day()+delay, h, m, 0, 0, timeutil.Location())
	return start, due
}

// YearlyDue 返回上一年度及生成时间点。
func (s *Scheduler) YearlyDue(n time.Time) (int, time.Time) {
	day := s.Settings.GetInt(SettingAutoYearlyDay, 2)
	if day < 1 || day > 31 {
		day = 2
	}
	h, m := parseHHMM(s.Settings.Get(SettingAutoYearlyTime, "08:00"))
	return n.Year() - 1, time.Date(n.Year(), 1, day, h, m, 0, 0, timeutil.Location())
}

func parseHHMM(s string) (int, int) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 8, 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 8, 0
	}
	return h, m
}

func (s *Scheduler) maybeRun(ctx context.Context, kind, period string, fn func() (string, error)) {
	exists, err := s.exists(kind, period)
	if err != nil || exists {
		return
	}
	key := kind + ":" + period
	a := s.attempts[key]
	if a.count >= 5 || (a.count > 0 && time.Since(a.last) < 30*time.Minute) {
		return
	}
	s.attempts[key] = attempt{count: a.count + 1, last: time.Now()}
	msg, err := fn()
	st := SchedulerStatus{Time: timeutil.NowString(), Kind: kind, Period: period, OK: err == nil, Message: msg}
	if err != nil {
		st.Message = err.Error()
		s.Log.Error("自动生成失败", "kind", kind, "period", period, "attempt", a.count+1, "err", err)
	} else {
		s.Log.Info("自动生成完成", "kind", kind, "period", period)
	}
	raw, _ := json.Marshal(st)
	_ = s.Settings.Set(SettingSchedulerLast, string(raw))
}

// exists 判断该周期是否已有全公司报告（任意来源，含手动生成）。
func (s *Scheduler) exists(kind, period string) (bool, error) {
	var n int64
	var err error
	if kind == "weekly" {
		n, err = db.Int(s.DB, "SELECT COUNT(*) FROM weekly_reports WHERE week_start = ? AND department_id IS NULL AND employee_id IS NULL", period)
	} else {
		n, err = db.Int(s.DB, "SELECT COUNT(*) FROM annual_reports WHERE report_year = ? AND department_id IS NULL AND employee_id IS NULL", period)
	}
	return n > 0, err
}

func (s *Scheduler) generateWeekly(ctx context.Context, start string) (string, error) {
	if !s.Weekly.AI.Configured() {
		return "", fmt.Errorf("AI 服务未配置，无法自动生成周报")
	}
	d, err := s.Weekly.Data(start, WeekEndFor(start), ReportTarget{}, true, s.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return "", err
	}
	content, err := s.Weekly.Generate(ctx, d, s.Settings.Get(SettingAutoWeeklyStyle, "normal"))
	if err != nil {
		return "", err
	}
	id, err := s.Weekly.Save(d, content, "ai", 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已生成 %s 至 %s 全公司周报（#%d）", d.WeekStart, d.WeekEnd, id), nil
}

func (s *Scheduler) generateYearly(ctx context.Context, year int) (string, error) {
	if !s.Annual.AI.Configured() {
		return "", fmt.Errorf("AI 服务未配置，无法自动生成年度总结")
	}
	d, err := s.Annual.Data(year, ReportTarget{}, true, s.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return "", err
	}
	content, err := s.Annual.Generate(ctx, d, s.Settings.Get(SettingAutoYearlyStyle, "normal"))
	if err != nil {
		return "", err
	}
	id, err := s.Annual.Save(d, content, "ai", 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已生成 %d 年度全公司总结（#%d）", year, id), nil
}

// LastStatus 读取最近一次执行结果。
func (s *Scheduler) LastStatus() *SchedulerStatus {
	raw := s.Settings.Get(SettingSchedulerLast, "-")
	if raw == "-" || raw == "" {
		return nil
	}
	var st SchedulerStatus
	if json.Unmarshal([]byte(raw), &st) != nil {
		return nil
	}
	return &st
}

// AutoSettings 自动生成设置（供设置页回显）。
type AutoSettings struct {
	WeeklyEnabled bool
	WeeklyDelay   int
	WeeklyTime    string
	WeeklyStyle   string
	YearlyEnabled bool
	YearlyDay     int
	YearlyTime    string
	YearlyStyle   string
	NextWeekly    string
	NextYearly    string
}

func (s *Scheduler) Auto() AutoSettings {
	n := timeutil.Now()
	a := AutoSettings{
		WeeklyEnabled: s.Settings.Bool(SettingAutoWeekly),
		WeeklyDelay:   s.Settings.GetInt(SettingAutoWeeklyDelay, 1),
		WeeklyTime:    s.Settings.Get(SettingAutoWeeklyTime, "08:00"),
		WeeklyStyle:   s.Settings.Get(SettingAutoWeeklyStyle, "normal"),
		YearlyEnabled: s.Settings.Bool(SettingAutoYearly),
		YearlyDay:     s.Settings.GetInt(SettingAutoYearlyDay, 2),
		YearlyTime:    s.Settings.Get(SettingAutoYearlyTime, "08:00"),
		YearlyStyle:   s.Settings.Get(SettingAutoYearlyStyle, "normal"),
	}
	start, due := s.WeeklyDue(n)
	a.NextWeekly = fmt.Sprintf("%s（周期 %s 至 %s）", due.Format("2006-01-02 15:04"), start, WeekEndFor(start))
	year, ydue := s.YearlyDue(n)
	if n.After(ydue) {
		ydue = ydue.AddDate(1, 0, 0)
		year++
	}
	a.NextYearly = fmt.Sprintf("%s（%d 年度）", ydue.Format("2006-01-02 15:04"), year)
	return a
}

// SaveAuto 保存自动生成设置。
func (s *Scheduler) SaveAuto(a AutoSettings) error {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	err := s.Settings.SetMany(map[string]string{
		SettingAutoWeekly: b(a.WeeklyEnabled), SettingAutoWeeklyDelay: strconv.Itoa(a.WeeklyDelay),
		SettingAutoWeeklyTime: a.WeeklyTime, SettingAutoWeeklyStyle: ValidStyle(a.WeeklyStyle),
		SettingAutoYearly: b(a.YearlyEnabled), SettingAutoYearlyDay: strconv.Itoa(a.YearlyDay),
		SettingAutoYearlyTime: a.YearlyTime, SettingAutoYearlyStyle: ValidStyle(a.YearlyStyle),
	})
	s.attempts = map[string]attempt{}
	return err
}
