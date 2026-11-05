package controller

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"work-ledger/internal/apperr"
	"work-ledger/internal/service"
	"work-ledger/internal/timeutil"
	"work-ledger/internal/validator"
)

// /api/v1/*：供外部 AI 调用的接口，协议与 PHP 版完全一致（Bearer Token，不依赖会话）。
func (c *Controllers) registerAPI(mux interface {
	Handle(string, http.Handler)
}) {
	mux.Handle("GET /api/v1/health", c.api(false, c.apiHealth))
	mux.Handle("GET /api/v1/weekly", c.api(true, c.apiWeekly))
	mux.Handle("POST /api/v1/weekly-summary", c.api(true, c.apiWeeklySummary))
	mux.Handle("GET /api/v1/yearly", c.api(true, c.apiYearly))
	mux.Handle("POST /api/v1/yearly-summary", c.api(true, c.apiYearlySummary))
	mux.Handle("GET /api/v1/period", c.api(true, c.apiPeriod))
	mux.Handle("POST /api/v1/period-summary", c.api(true, c.apiPeriodSummary))
}

type apiFunc func(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error)

func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func apiFail(w http.ResponseWriter, status int, code, msg string) {
	apiJSON(w, status, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": msg}})
}

var bearerRe = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func (c *Controllers) api(auth bool, h apiFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth {
			expected := c.S.Core.Cfg.AI.APIToken
			if expected == "" {
				apiFail(w, 503, "api_not_configured", "AI_API_TOKEN 尚未配置，AI 接口不可用。")
				return
			}
			m := bearerRe.FindStringSubmatch(strings.TrimSpace(r.Header.Get("Authorization")))
			if m == nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="work-ledger"`)
				apiFail(w, 401, "unauthorized", "缺少 Bearer 访问令牌。")
				return
			}
			if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(m[1]))) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="work-ledger"`)
				apiFail(w, 401, "unauthorized", "访问令牌无效。")
				return
			}
		}
		var in validator.Input
		if r.Method == http.MethodPost {
			ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if ct == "application/json" {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				in = validator.Input{}
				if strings.TrimSpace(string(body)) != "" {
					if err := json.Unmarshal(body, &in); err != nil {
						apiFail(w, 400, "invalid_json", "请求体不是有效的 JSON。")
						return
					}
				}
			} else {
				_ = r.ParseForm()
				in = validator.FromValues(r.PostForm)
			}
		} else {
			in = validator.FromValues(r.URL.Query())
		}
		out, err := h(w, r, in)
		if err != nil {
			if e, ok := apperr.As(err); ok {
				status, code := e.Status, e.Code
				if status == http.StatusUnprocessableEntity {
					code = "invalid_parameter"
				}
				apiFail(w, status, code, e.Message)
				return
			}
			c.S.Core.Log.Error("api error", "path", r.URL.Path, "err", err)
			apiFail(w, 500, "server_error", "服务器内部错误")
			return
		}
		apiJSON(w, 200, out)
	})
}

func (c *Controllers) apiHealth(w http.ResponseWriter, r *http.Request, _ validator.Input) (any, error) {
	return map[string]any{"ok": true, "data": map[string]any{
		"service": "work-ledger", "api_version": "v1", "time": timeutil.Now().Format(time.RFC3339), "timezone": c.S.Core.Cfg.App.Timezone,
	}}, nil
}

func (c *Controllers) apiMeta(d *service.ReportData) map[string]any {
	return map[string]any{"api_version": "v1", "timezone": c.S.Core.Cfg.App.Timezone, "records_truncated": d.Summary.Truncated}
}

func (c *Controllers) apiTarget(in validator.Input) (service.ReportTarget, bool, error) {
	p, err := validator.APIScope(in)
	if err != nil {
		return service.ReportTarget{}, false, err
	}
	return service.ReportTarget{DepartmentID: p.DepartmentID, EmployeeID: p.EmployeeID}, p.IncludeChildren, nil
}

func (c *Controllers) weekRange(in validator.Input) (string, string) {
	v := validator.New(in)
	start, end := v.Raw("week_start"), v.Raw("week_end")
	if start == "" {
		start = c.S.Settings.DefaultWeekStart()
	} else if timeutil.ValidDate(start) {
		start = c.S.Settings.StartOfCycleFor(start)
	}
	if end == "" && timeutil.ValidDate(start) {
		end = service.WeekEndFor(start)
	}
	return start, end
}

func (c *Controllers) apiWeekly(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error) {
	t, inc, err := c.apiTarget(in)
	if err != nil {
		return nil, err
	}
	start, end := c.weekRange(in)
	d, err := c.S.Weekly.Data(start, end, t, inc, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "data": d, "meta": c.apiMeta(d)}, nil
}

// summaryOpts 解析 style / dry_run / save。
func summaryOpts(in validator.Input) (style string, dryRun, save bool) {
	v := validator.New(in)
	style = service.ValidStyle(v.Raw("style"))
	dryRun = v.Bool("dry_run")
	save = true
	if v.Has("save") {
		save = v.Bool("save")
	}
	return
}

func (c *Controllers) aiReady(dryRun bool) error {
	if !dryRun && !c.S.AI.Configured() {
		return apperr.Status(503, "ai_not_configured", "AI 服务未配置，请先设置 AI_API_KEY、AI_BASE_URL 和 AI_MODEL。")
	}
	return nil
}

func aiFailed(kind string, err error) error {
	return apperr.Status(502, "ai_generation_failed", "AI "+kind+"生成失败："+err.Error())
}

func (c *Controllers) apiWeeklySummary(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error) {
	style, dryRun, save := summaryOpts(in)
	if err := c.aiReady(dryRun); err != nil {
		return nil, err
	}
	t, inc, err := c.apiTarget(in)
	if err != nil {
		return nil, err
	}
	start, end := c.weekRange(in)
	d, err := c.S.Weekly.Data(start, end, t, inc, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return map[string]any{"ok": true, "data": map[string]any{"week_start": d.WeekStart, "week_end": d.WeekEnd, "scope": d.Scope,
			"summary": d.Summary, "messages": c.S.Weekly.Prompt(d, style), "dry_run": true}}, nil
	}
	content, err := c.S.Weekly.Generate(r.Context(), d, style)
	if err != nil {
		c.S.Core.Log.Error("API 周报生成失败", "err", err)
		return nil, aiFailed("周报", err)
	}
	var reportID any
	if save {
		id, err := c.S.Weekly.Save(d, content, "api", 0)
		if err != nil {
			return nil, err
		}
		reportID = id
	}
	return map[string]any{"ok": true, "data": map[string]any{"report_id": reportID, "week_start": d.WeekStart, "week_end": d.WeekEnd,
		"scope": d.Scope, "style": style, "model": c.S.AI.Model(), "content": content, "summary": d.Summary}}, nil
}

func (c *Controllers) apiYear(in validator.Input) (int, error) {
	v := validator.New(in)
	if v.Raw("year") == "" {
		return timeutil.Year(), nil
	}
	y := v.Int("year", "year", 0)
	if !v.OK() || y < 2000 || y > 2100 {
		return 0, apperr.New("year 必须是 2000 到 2100 之间的整数。")
	}
	return int(y), nil
}

func (c *Controllers) apiYearly(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error) {
	year, err := c.apiYear(in)
	if err != nil {
		return nil, err
	}
	t, inc, err := c.apiTarget(in)
	if err != nil {
		return nil, err
	}
	d, err := c.S.Annual.Data(year, t, inc, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "data": d, "meta": c.apiMeta(d)}, nil
}

func (c *Controllers) apiYearlySummary(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error) {
	style, dryRun, save := summaryOpts(in)
	if err := c.aiReady(dryRun); err != nil {
		return nil, err
	}
	year, err := c.apiYear(in)
	if err != nil {
		return nil, err
	}
	t, inc, err := c.apiTarget(in)
	if err != nil {
		return nil, err
	}
	d, err := c.S.Annual.Data(year, t, inc, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return map[string]any{"ok": true, "data": map[string]any{"year": d.Year, "period": d.Period, "scope": d.Scope,
			"summary": d.Summary, "messages": c.S.Annual.Prompt(d, style), "dry_run": true}}, nil
	}
	content, err := c.S.Annual.Generate(r.Context(), d, style)
	if err != nil {
		c.S.Core.Log.Error("API 年度总结生成失败", "err", err)
		return nil, aiFailed("年度总结", err)
	}
	var reportID any
	if save {
		id, err := c.S.Annual.Save(d, content, "api", 0)
		if err != nil {
			return nil, err
		}
		reportID = id
	}
	return map[string]any{"ok": true, "data": map[string]any{"report_id": reportID, "year": d.Year, "period": d.Period, "scope": d.Scope,
		"style": style, "model": c.S.AI.Model(), "content": content, "summary": d.Summary}}, nil
}

func periodRange(in validator.Input) (string, string) {
	v := validator.New(in)
	start, end := v.Raw("start_date"), v.Raw("end_date")
	n := timeutil.Now()
	if start == "" {
		start = n.Format("2006-01") + "-01"
	}
	if end == "" {
		end = time.Date(n.Year(), n.Month()+1, 0, 0, 0, 0, 0, timeutil.Location()).Format(timeutil.Date)
	}
	return start, end
}

func (c *Controllers) apiPeriod(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error) {
	t, inc, err := c.apiTarget(in)
	if err != nil {
		return nil, err
	}
	start, end := periodRange(in)
	d, err := c.S.Period.Data(start, end, t, inc, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "data": d, "meta": c.apiMeta(d)}, nil
}

func (c *Controllers) apiPeriodSummary(w http.ResponseWriter, r *http.Request, in validator.Input) (any, error) {
	style, dryRun, save := summaryOpts(in)
	if err := c.aiReady(dryRun); err != nil {
		return nil, err
	}
	t, inc, err := c.apiTarget(in)
	if err != nil {
		return nil, err
	}
	start, end := periodRange(in)
	d, err := c.S.Period.Data(start, end, t, inc, c.S.Core.Cfg.Limits.MaxRecordsForAPI)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return map[string]any{"ok": true, "data": map[string]any{"start_date": d.StartDate, "end_date": d.EndDate, "period": d.Period,
			"scope": d.Scope, "summary": d.Summary, "messages": c.S.Period.Prompt(d, style), "dry_run": true}}, nil
	}
	content, err := c.S.Period.Generate(r.Context(), d, style)
	if err != nil {
		c.S.Core.Log.Error("API 时段总结生成失败", "err", err)
		return nil, aiFailed("时段总结", err)
	}
	var reportID any
	if save {
		id, err := c.S.Period.Save(d, content, "api", 0)
		if err != nil {
			return nil, err
		}
		reportID = id
	}
	return map[string]any{"ok": true, "data": map[string]any{"report_id": reportID, "start_date": d.StartDate, "end_date": d.EndDate,
		"period": d.Period, "scope": d.Scope, "style": style, "model": c.S.AI.Model(), "content": content, "summary": d.Summary}}, nil
}
