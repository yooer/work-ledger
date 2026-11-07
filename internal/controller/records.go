package controller

import (
	"net/url"
	"strconv"

	"work-ledger/internal/apperr"
	"work-ledger/internal/model"
	"work-ledger/internal/service"
	"work-ledger/internal/timeutil"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerRecords(app *web.App) {
	app.Page("/records", web.Admin, func(ctx *web.Ctx) error { return c.recordsPage(ctx, "all") })
	app.Page("/my-records", web.Login, func(ctx *web.Ctx) error { return c.recordsPage(ctx, "mine") })
	app.Page("/team-records", web.AdminManager, func(ctx *web.Ctx) error { return c.recordsPage(ctx, "team") })
	app.Page("/department-records", web.Login, func(ctx *web.Ctx) error { return ctx.Redirect("/team-records") }) // 旧地址
	app.Page("/records/create", web.Login, c.recordForm)
	app.Page("/records/{id}", web.Login, c.recordShow)
	app.Page("/records/{id}/edit", web.Login, c.recordForm)
	app.Ajax("/records/ajax", web.Login, web.Actions{
		"create": {Validate: validator.RecordCreate, Handle: c.createRecord},
		"update": {Validate: validator.RecordUpdate, Handle: c.updateRecord},
		"delete": {Validate: validator.ID, Handle: c.deleteRecord},
	})
}

// recordMode 描述三种台账列表：全部（管理员）、我的、团队（部门经理 / 主管领导）。
type recordMode struct {
	Key, Title, BaseURL, Active string
	ShowEmployee, ShowDept      bool
}

var recordModes = map[string]recordMode{
	"all":  {Key: "all", Title: "全部工作台账", BaseURL: "/records", Active: "records", ShowEmployee: true, ShowDept: true},
	"mine": {Key: "mine", Title: "我的台账", BaseURL: "/my-records", Active: "my-records"},
	"team": {Key: "team", Title: "团队台账", BaseURL: "/team-records", Active: "team-records", ShowEmployee: true},
}

// recordQuery 解析筛选条件并按模式限定范围。
func (c *Controllers) recordQuery(ctx *web.Ctx, mode string) (service.Scope, service.RecordFilter, validator.RecordFilterInput, error) {
	in := validator.RecordFilter(ctx.Query())
	sc, err := c.S.Core.ScopeFor(ctx.User)
	if err != nil {
		return sc, service.RecordFilter{}, in, err
	}
	f := service.RecordFilter{From: in.From, To: in.To, CategoryID: in.CategoryID, ProjectID: in.ProjectID, Q: in.Q}
	switch mode {
	case "mine":
		f.InvolvedIDs = []int64{ctx.UserID()} // 本人创建的 + 本人参与的
		in.EmployeeID, in.DepartmentID = 0, 0
	case "team":
		if !sc.All && !sc.IsTeamScope() {
			return sc, f, in, apperr.Forbidden("你目前没有下属，也没有分管部门，无法查看团队台账。")
		}
		f.EmployeeID = in.EmployeeID
		in.DepartmentID = 0
	default:
		f.EmployeeID, f.DepartmentID = in.EmployeeID, in.DepartmentID
	}
	return sc, f, in, nil
}

func filterQuery(in validator.RecordFilterInput) url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" && v != "0" {
			q.Set(k, v)
		}
	}
	set("from", in.From)
	set("to", in.To)
	set("category_id", strconv.FormatInt(in.CategoryID, 10))
	set("project_id", strconv.FormatInt(in.ProjectID, 10))
	set("employee_id", strconv.FormatInt(in.EmployeeID, 10))
	set("department_id", strconv.FormatInt(in.DepartmentID, 10))
	set("q", in.Q)
	return q
}

// exportURLs 生成各格式的完整导出地址。整条地址在后端拼好再交给模板，
// 避免模板把查询串里的 = 和 & 当作参数值转义（曾导致筛选条件丢失、导出全部记录）。
func exportURLs(in validator.RecordFilterInput, mode string) map[string]string {
	out := map[string]string{}
	for _, format := range []string{"csv", "xlsx", "json"} {
		q := filterQuery(in)
		q.Set("mode", mode)
		q.Set("format", format)
		out[format] = "/records/export?" + q.Encode()
	}
	return out
}

func (c *Controllers) recordsPage(ctx *web.Ctx, mode string) error {
	m := recordModes[mode]
	sc, f, in, err := c.recordQuery(ctx, mode)
	if err != nil {
		return err
	}
	res, err := c.S.Records.List(sc, f, in.Page, 15)
	if err != nil {
		return err
	}
	cats, projects, err := c.S.Categories.FilterOptions(sc, ctx.User)
	if err != nil {
		return err
	}
	data := map[string]any{
		"PageTitle": m.Title, "Active": m.Active, "Mode": m, "Filter": in, "Result": res,
		"Categories": cats, "Projects": projects,
		"Pager": web.NewPager(res.Page, res.Total, res.PerPage, m.BaseURL, filterQuery(in)),
	}
	data["ExportURLs"] = exportURLs(in, mode)
	if m.ShowEmployee {
		if data["Employees"], err = c.S.Users.EmployeeOptions(sc); err != nil {
			return err
		}
	}
	if m.ShowDept {
		if data["Departments"], err = c.S.Depts.Tree(); err != nil {
			return err
		}
	}
	if mode == "team" {
		if len(sc.DeptIDs) > 0 {
			label, _ := c.S.Depts.LabelByID(sc.DeptIDs[0])
			data["DeptLabel"] = label
		}
		data["SubordinateCount"] = len(sc.UserIDs)
		data["LeaderCanEdit"] = c.S.Settings.LeaderCanEdit()
	}
	return ctx.Render("records/index", data)
}

func (c *Controllers) recordShow(ctx *web.Ctx) error {
	sc, err := c.S.Core.ScopeFor(ctx.User)
	if err != nil {
		return err
	}
	rec, err := c.S.Records.Find(sc, ctx.ID())
	if err != nil {
		return err
	}
	return ctx.Render("records/show", map[string]any{
		"PageTitle": "工作详情", "Active": "my-records", "Record": rec, "CanEdit": c.S.Records.CanEditRecord(sc, rec, ctx.User),
		"CanDelete": c.S.Records.CanDeleteRecord(sc, rec, ctx.User), "IsCreator": rec.Int("created_by") == ctx.UserID(),
	})
}

func (c *Controllers) recordForm(ctx *web.Ctx) error {
	id := ctx.ID()
	old := map[string]any{
		"id": int64(0), "work_category_id": int64(0), "project_category_id": int64(0), "content": "",
		"work_at": timeutil.Now().Format(timeutil.Local), "difficulty": int64(1), "work_count": int64(1),
	}
	selected := []int64{ctx.UserID()}
	var rec model.Row
	if id > 0 {
		sc, err := c.S.Core.ScopeFor(ctx.User)
		if err != nil {
			return err
		}
		if rec, err = c.S.Records.Find(sc, id); err != nil {
			return err
		}
		if !c.S.Records.CanEditRecord(sc, rec, ctx.User) {
			return apperr.Forbidden("你没有修改这条工作记录的权限。")
		}
		old = map[string]any{
			"id": rec.Int("id"), "work_category_id": rec.Int("work_category_id"), "project_category_id": rec.Int("project_category_id"),
			"content": rec.Str("content"), "work_at": timeutil.ToLocalInput(rec.Str("work_at")),
			"difficulty": rec.Int("difficulty"), "work_count": rec.Int("work_count"),
		}
		selected = nil
		if parts, ok := rec["participants"].([]model.Row); ok {
			for _, p := range parts {
				selected = append(selected, p.Int("id"))
			}
		}
	}
	deptID := ctx.User.Int("department_id")
	if rec != nil {
		deptID = rec.Int("department_id")
	}
	cats, projects, people, err := c.S.Records.FormOptions(deptID, model.ToInt(old["work_category_id"]), model.ToInt(old["project_category_id"]), selected)
	if err != nil {
		return err
	}
	title := "填写工作记录"
	cancel := "/my-records"
	if ctx.IsAdmin() {
		cancel = "/records"
	}
	if id > 0 {
		title = "编辑工作记录"
		cancel = "/records/" + strconv.FormatInt(id, 10)
	}
	return ctx.Render("records/form", map[string]any{
		"PageTitle": title, "Active": "my-records", "Old": old, "IsEdit": id > 0, "Record": rec,
		"Categories": cats, "Projects": projects, "People": people, "Selected": selected, "CancelURL": cancel,
	})
}

func toRecordInput(p validator.RecordInput) service.RecordInput {
	return service.RecordInput{WorkCategoryID: p.WorkCategoryID, ProjectCategoryID: p.ProjectCategoryID, Content: p.Content,
		WorkAt: p.WorkAt, Difficulty: p.Difficulty, WorkCount: p.WorkCount, ParticipantIDs: p.ParticipantIDs}
}

func (c *Controllers) createRecord(ctx *web.Ctx, in any) (any, error) {
	id, err := c.S.Records.Create(ctx.User, toRecordInput(in.(validator.RecordInput)))
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "工作记录已保存。", Redirect: "/records/" + strconv.FormatInt(id, 10), FlashOnRedirect: true}, nil
}

func (c *Controllers) updateRecord(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.RecordInput)
	sc, err := c.S.Core.ScopeFor(ctx.User)
	if err != nil {
		return nil, err
	}
	if err := c.S.Records.Update(sc, ctx.User, p.ID, toRecordInput(p)); err != nil {
		return nil, err
	}
	return web.Result{Message: "工作记录已更新。", Redirect: "/records/" + strconv.FormatInt(p.ID, 10), FlashOnRedirect: true}, nil
}

// deleteRecord 删除台账（二次确认在页面完成），并写审计日志。
func (c *Controllers) deleteRecord(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.IDInput)
	sc, err := c.S.Core.ScopeFor(ctx.User)
	if err != nil {
		return nil, err
	}
	rec, err := c.S.Records.Delete(sc, ctx.User, p.ID)
	if err != nil {
		return nil, err
	}
	content := []rune(rec.Str("content"))
	if len(content) > 60 {
		content = append(content[:60], []rune("…")...)
	}
	c.S.Core.Log.Warn("work record deleted", "record_id", rec.Int("id"), "by_id", ctx.UserID(), "by", ctx.User.Str("name"),
		"creator", rec.Str("creator_name"), "work_at", rec.Str("work_at"), "category", rec.Str("category_name"), "content", string(content))
	back := "/my-records"
	if rec.Int("created_by") != ctx.UserID() {
		if ctx.IsAdmin() {
			back = "/records"
		} else {
			back = "/team-records"
		}
	}
	return web.Result{Message: "工作记录已删除。", Redirect: back, FlashOnRedirect: true}, nil
}
