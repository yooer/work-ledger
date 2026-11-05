// Package service 承载全部业务规则与数据库访问。controller 只能调用这里暴露的方法。
package service

import (
	"log/slog"

	"work-ledger/internal/config"
	"work-ledger/internal/db"
	"work-ledger/internal/model"
	"work-ledger/internal/timeutil"
)

// Core 是各业务服务共享的依赖。
type Core struct {
	DB  *db.DB
	Cfg *config.Config
	Log *slog.Logger
}

// Services 汇总所有业务服务。
type Services struct {
	Core       *Core
	Settings   *Settings
	Auth       *Auth
	Security   *Security
	Users      *Users
	Depts      *Departments
	Categories *Categories
	Records    *Records
	AI         *AIClient
	Weekly     *WeeklyReports
	Annual     *AnnualReports
	Period     *PeriodReports
	Scheduler  *Scheduler
	Dashboard  *Dashboard
	Import     *Importer
	Export     *Exporter
	Backup     *Backup
}

func New(d *db.DB, cfg *config.Config, log *slog.Logger) *Services {
	core := &Core{DB: d, Cfg: cfg, Log: log}
	s := &Services{Core: core}
	s.Settings = &Settings{Core: core}
	s.Auth = &Auth{Core: core}
	s.Security = &Security{Core: core}
	s.Users = &Users{Core: core}
	s.Depts = &Departments{Core: core}
	s.Categories = &Categories{Core: core}
	s.Records = &Records{Core: core, Settings: s.Settings}
	s.AI = &AIClient{Core: core}
	s.Weekly = &WeeklyReports{Core: core, Settings: s.Settings, AI: s.AI}
	s.Annual = &AnnualReports{Core: core, AI: s.AI}
	s.Period = &PeriodReports{Core: core, AI: s.AI}
	s.Scheduler = &Scheduler{Core: core, Settings: s.Settings, Weekly: s.Weekly, Annual: s.Annual}
	s.Dashboard = &Dashboard{Core: core, Records: s.Records, Settings: s.Settings}
	s.Import = &Importer{Core: core}
	s.Export = &Exporter{Core: core, Records: s.Records}
	s.Backup = &Backup{Core: core}
	return s
}

// Row 别名，便于本包书写。
type Row = model.Row

func now() string { return timeutil.NowString() }

// nullable 把 0/"" 转为 NULL。
func nullInt(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
