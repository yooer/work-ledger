package controller

import (
	"work-ledger/internal/service"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerSettings(app *web.App) {
	app.Page("/settings", web.Admin, c.settingsPage)
	app.Ajax("/settings/ajax", web.Admin, web.Actions{
		"save_week_cycle": {Validate: validator.WeekCycle, Handle: c.saveWeekCycle},
		"save_auto":       {Validate: validator.AutoReport, Handle: c.saveAuto},
	})
}

func (c *Controllers) settingsPage(ctx *web.Ctx) error {
	counts, err := c.S.Core.SystemCounts()
	if err != nil {
		return err
	}
	cfg := c.S.Core.Cfg
	return ctx.Render("settings/index", map[string]any{
		"PageTitle": "系统与 AI", "Active": "settings",
		"AIReady": c.S.AI.Configured(), "TokenReady": cfg.AI.APIToken != "", "KeyReady": cfg.AI.APIKey != "",
		"AIBaseURL": cfg.AI.BaseURL, "AIModel": cfg.AI.Model, "AppURL": cfg.App.URL, "Timezone": cfg.App.Timezone,
		"ListenAddr": cfg.App.ListenAddr, "Home": cfg.Paths.Home, "EnvFile": cfg.Paths.EnvFile, "DBPath": cfg.DB.SQLitePath,
		"WeekStart": c.S.Settings.WeekStartDay(), "WeekEnd": c.S.Settings.WeekEndDay(), "CycleLabel": c.S.Settings.WeekCycleLabel(),
		"Counts": counts, "Auto": c.S.Scheduler.Auto(), "Last": c.S.Scheduler.LastStatus(), "Styles": reportStyles,
	})
}

func (c *Controllers) saveWeekCycle(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.WeekCycleInput)
	if err := c.S.Settings.SaveWeekCycle(p.Start, p.End); err != nil {
		return nil, err
	}
	return web.Result{Message: "周报统计周期已更新为：" + c.S.Settings.WeekCycleLabel() + "。", Redirect: "/settings", FlashOnRedirect: true}, nil
}

func (c *Controllers) saveAuto(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.AutoReportInput)
	err := c.S.Scheduler.SaveAuto(service.AutoSettings{
		WeeklyEnabled: p.WeeklyEnabled, WeeklyDelay: p.WeeklyDelay, WeeklyTime: p.WeeklyTime, WeeklyStyle: p.WeeklyStyle,
		YearlyEnabled: p.YearlyEnabled, YearlyDay: p.YearlyDay, YearlyTime: p.YearlyTime, YearlyStyle: p.YearlyStyle,
	})
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "自动生成设置已保存。", Redirect: "/settings", FlashOnRedirect: true}, nil
}
