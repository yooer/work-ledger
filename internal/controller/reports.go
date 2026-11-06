package controller

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"work-ledger/internal/apperr"
	"work-ledger/internal/service"
	"work-ledger/internal/timeutil"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerReports(app *web.App) {
	app.Page("/reports/weekly", web.Login, c.weeklyPage)
	app.Page("/reports/weekly/export", web.Login, c.weeklyExport)
	app.Page("/reports/weekly/{id}", web.Login, c.weeklyShow)
	app.Ajax("/reports/weekly/ajax", web.Login, web.Actions{
		"generate": {Validate: validator.WeeklyGenerate, Handle: c.weeklyGenerate},
	})
	app.Page("/reports/yearly", web.Login, c.yearlyPage)
	app.Page("/reports/yearly/{id}", web.Login, c.yearlyShow)
	app.Ajax("/reports/yearly/ajax", web.Login, web.Actions{
		"generate": {Validate: validator.YearlyGenerate, Handle: c.yearlyGenerate},
	})
	app.Page("/reports/period", web.Login, c.periodPage)
	app.Page("/reports/period/{id}", web.Login, c.periodShow)
	app.Ajax("/reports/period/ajax", web.Login, web.Actions{
		"generate": {Validate: validator.PeriodGenerate, Handle: c.periodGenerate},
	})
}

// reportCommon 三类报告页共用的数据。
func (c *Controllers) reportCommon(ctx *web.Ctx, t service.ReportTarget, data *service.ReportData) (map[string]any, error) {
	groups, err := c.S.Core.ReportTargetOptions(ctx.User)
	if err != nil {
		return nil, err
	}
	preview := data.Records
	if len(preview) > 12 {
		preview = preview[:12]
	}
	return map[string]any{
		"Data": data, "TargetGroups": groups, "TargetKey": t.Key(ctx.UserID()), "IsPersonal": t.EmployeeID > 0,
		"AIReady": c.S.AI.Configured(), "Preview": preview, "Styles": reportStyles,
	}, nil
}

var reportStyles = []map[string]string{
	{"value": "concise", "label": "精简"}, {"value": "normal", "label": "标准"}, {"value": "detailed", "label": "详细"},
}

func (c *Controllers) requireAI() error {
	if !c.S.AI.Configured() {
		return apperr.New("尚未配置 AI 服务。请在配置文件中设置 AI_API_KEY、AI_BASE_URL、AI_MODEL。")
	}
	return nil
}

// ---------------- 周报 ----------------

func (c *Controllers) weeklyPage(ctx *web.Ctx) error {
	v := validator.New(ctx.Query())
	start := c.S.Settings.DefaultWeekStart()
	if d := v.LenientDate("week_start"); d != "" {
		start = c.S.Settings.StartOfCycleFor(d)
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, validator.LenientTarget(ctx.Query(), "target"), v.ID("department_id"))
	if err != nil {
		return err
	}
	data, err := c.S.Weekly.Data(start, service.WeekEndFor(start), t, true, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return err
	}
	view, err := c.reportCommon(ctx, t, data)
	if err != nil {
		return err
	}
	history, err := c.S.Weekly.History(ctx.User, 8)
	if err != nil {
		return err
	}
	q := url.Values{}
	if key := t.Key(ctx.UserID()); key != "" {
		q.Set("target", key)
	}
	q.Set("week_start", timeutil.AddDays(start, -7))
	prevURL := "/reports/weekly?" + q.Encode()
	q.Set("week_start", timeutil.AddDays(start, 7))
	nextURL := "/reports/weekly?" + q.Encode()
	// 导出本周期台账（与当前统计范围一致）：完整地址在后端拼好，避免模板转义查询串。
	exportURLs := map[string]string{}
	for _, format := range []string{"xlsx", "csv"} {
		q.Set("week_start", start)
		q.Set("format", format)
		exportURLs[format] = "/reports/weekly/export?" + q.Encode()
	}
	view["PageTitle"], view["Active"] = "周报中心", "reports"
	view["WeekStart"], view["WeekEnd"] = start, service.WeekEndFor(start)
	view["CycleLabel"] = c.S.Settings.WeekCycleLabel()
	view["History"], view["PrevURL"], view["NextURL"] = history, prevURL, nextURL
	view["ExportURLs"] = exportURLs
	view["Chart"] = map[string]any{"days": data.Summary.ByDay, "categories": data.Summary.ByCategory}
	return ctx.Render("reports/weekly", view)
}

func (c *Controllers) weeklyGenerate(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.WeeklyGenerateInput)
	if err := c.requireAI(); err != nil {
		return nil, err
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, p.Target, p.DepartmentID)
	if err != nil {
		return nil, err
	}
	start := c.S.Settings.StartOfCycleFor(p.WeekStart)
	data, err := c.S.Weekly.Data(start, service.WeekEndFor(start), t, true, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	content, err := c.S.Weekly.Generate(ctx.R.Context(), data, p.Style)
	if err != nil {
		c.S.Core.Log.Error("周报生成失败", "err", err)
		return nil, apperr.New("生成失败：" + err.Error())
	}
	id, err := c.S.Weekly.Save(data, content, "ai", ctx.UserID())
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "周工作总结已生成并保存。", Redirect: "/reports/weekly/" + strconv.FormatInt(id, 10), FlashOnRedirect: true}, nil
}

// weeklyExport 导出周报中心当前周期、当前统计范围的台账（文件下载例外：GET）。
func (c *Controllers) weeklyExport(ctx *web.Ctx) error {
	p, err := validator.WeeklyExport(ctx.Query())
	if err != nil {
		return err
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, p.Target, p.DepartmentID)
	if err != nil {
		return err
	}
	start := c.S.Settings.StartOfCycleFor(p.WeekStart)
	end := service.WeekEndFor(start)
	sc, f, err := c.S.Core.TargetRecords(ctx.User, t, start, end)
	if err != nil {
		return err
	}
	meta := map[string]any{"scope": "weekly", "week_start": start, "week_end": end, "target": t.Key(ctx.UserID())}
	file, err := c.S.Export.Export(sc, f, p.Format, meta)
	if err != nil {
		return err
	}
	file.Name = "weekly-records-" + start + "-" + end + file.Name[strings.LastIndex(file.Name, "."):]
	if file.Truncated {
		ctx.W.Header().Set("X-Export-Truncated", "1")
	} else {
		ctx.W.Header().Set("X-Export-Truncated", "0")
	}
	return ctx.Download(file.Name, file.ContentType, file.Body)
}

func (c *Controllers) weeklyShow(ctx *web.Ctx) error {
	r, err := c.S.Weekly.Find(ctx.User, ctx.ID())
	if err != nil {
		return err
	}
	return ctx.Render("reports/show", map[string]any{
		"PageTitle": "周工作总结", "Active": "reports", "Report": r, "Kind": "weekly",
		"Title": r.Str("week_start") + " 至 " + r.Str("week_end") + " 周工作总结", "BackURL": "/reports/weekly",
	})
}

// ---------------- 年度 ----------------

func (c *Controllers) yearlyPage(ctx *web.Ctx) error {
	v := validator.New(ctx.Query())
	year := int(v.Int("year", "年度", int64(timeutil.Year())))
	if year < 2000 || year > 2100 {
		year = timeutil.Year()
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, validator.LenientTarget(ctx.Query(), "target"), v.ID("department_id"))
	if err != nil {
		return err
	}
	data, err := c.S.Annual.Data(year, t, true, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return err
	}
	view, err := c.reportCommon(ctx, t, data)
	if err != nil {
		return err
	}
	history, err := c.S.Annual.History(ctx.User, 8)
	if err != nil {
		return err
	}
	var years []int
	for y := timeutil.Year() + 1; y >= timeutil.Year()-6; y-- {
		years = append(years, y)
	}
	view["PageTitle"], view["Active"] = "年度总结", "annual"
	view["Year"], view["Years"], view["History"] = year, years, history
	view["Chart"] = map[string]any{"months": data.Summary.ByMonth, "quarters": data.Summary.ByQuarter, "categories": data.Summary.ByCategory, "difficulty": data.Summary.ByDifficulty}
	return ctx.Render("reports/yearly", view)
}

func (c *Controllers) yearlyGenerate(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.YearlyGenerateInput)
	if err := c.requireAI(); err != nil {
		return nil, err
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, p.Target, p.DepartmentID)
	if err != nil {
		return nil, err
	}
	data, err := c.S.Annual.Data(p.Year, t, true, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	content, err := c.S.Annual.Generate(ctx.R.Context(), data, p.Style)
	if err != nil {
		c.S.Core.Log.Error("年度总结生成失败", "err", err)
		return nil, apperr.New("生成失败：" + err.Error())
	}
	id, err := c.S.Annual.Save(data, content, "ai", ctx.UserID())
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "年度工作总结已生成并保存。", Redirect: "/reports/yearly/" + strconv.FormatInt(id, 10), FlashOnRedirect: true}, nil
}

func (c *Controllers) yearlyShow(ctx *web.Ctx) error {
	r, err := c.S.Annual.Find(ctx.User, ctx.ID())
	if err != nil {
		return err
	}
	return ctx.Render("reports/show", map[string]any{
		"PageTitle": "年度工作总结", "Active": "annual", "Report": r, "Kind": "yearly",
		"Title": r.Str("report_year") + " 年度工作总结", "BackURL": "/reports/yearly",
	})
}

// ---------------- 时段 ----------------

func (c *Controllers) periodPage(ctx *web.Ctx) error {
	v := validator.New(ctx.Query())
	n := timeutil.Now()
	start, end := v.LenientDate("start_date"), v.LenientDate("end_date")
	if start == "" {
		start = n.Format("2006-01") + "-01"
	}
	if end == "" {
		end = time.Date(n.Year(), n.Month()+1, 0, 0, 0, 0, 0, timeutil.Location()).Format(timeutil.Date)
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, validator.LenientTarget(ctx.Query(), "target"), v.ID("department_id"))
	if err != nil {
		return err
	}
	data, err := c.S.Period.Data(start, end, t, true, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return err
	}
	view, err := c.reportCommon(ctx, t, data)
	if err != nil {
		return err
	}
	history, err := c.S.Period.History(ctx.User, 8)
	if err != nil {
		return err
	}
	view["PageTitle"], view["Active"] = "时段总结", "period"
	view["Start"], view["End"], view["History"] = start, end, history
	view["Presets"] = service.PeriodPresets()
	view["Partial"] = data.Period.Partial != nil && *data.Period.Partial
	byDay := data.Period.Days <= 62
	buckets := data.Summary.ByMonth
	if byDay {
		buckets = data.Summary.ByDay
	}
	view["Chart"] = map[string]any{"byDay": byDay, "buckets": buckets, "categories": data.Summary.ByCategory}
	return ctx.Render("reports/period", view)
}

func (c *Controllers) periodGenerate(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.PeriodGenerateInput)
	if err := c.requireAI(); err != nil {
		return nil, err
	}
	t, err := c.S.Core.ResolveTarget(ctx.User, p.Target, p.DepartmentID)
	if err != nil {
		return nil, err
	}
	data, err := c.S.Period.Data(p.StartDate, p.EndDate, t, true, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	content, err := c.S.Period.Generate(ctx.R.Context(), data, p.Style)
	if err != nil {
		c.S.Core.Log.Error("时段总结生成失败", "err", err)
		return nil, apperr.New("生成失败：" + err.Error())
	}
	id, err := c.S.Period.Save(data, content, "ai", ctx.UserID())
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "时段工作总结已生成并保存。", Redirect: "/reports/period/" + strconv.FormatInt(id, 10), FlashOnRedirect: true}, nil
}

func (c *Controllers) periodShow(ctx *web.Ctx) error {
	r, err := c.S.Period.Find(ctx.User, ctx.ID())
	if err != nil {
		return err
	}
	return ctx.Render("reports/show", map[string]any{
		"PageTitle": "时段工作总结", "Active": "period", "Report": r, "Kind": "period",
		"Title": r.Str("period_label") + " 工作总结", "BackURL": "/reports/period",
	})
}
