package controller

import (
	"net/url"
	"strconv"

	"work-ledger/internal/service"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerOrg(app *web.App) {
	app.Page("/departments", web.Admin, c.departmentsPage)
	app.Page("/departments/create", web.Admin, c.departmentForm)
	app.Page("/departments/{id}/edit", web.Admin, c.departmentForm)
	app.Ajax("/departments/ajax", web.Admin, web.Actions{
		"save": {Validate: validator.Department, Handle: c.saveDepartment},
	})

	app.Page("/people", web.Admin, c.peoplePage)
	app.Page("/people/create", web.Admin, c.personForm)
	app.Page("/people/{id}/edit", web.Admin, c.personForm)
	app.Ajax("/people/ajax", web.Admin, web.Actions{
		"create":         {Validate: validator.UserCreate, Handle: c.createPerson},
		"update":         {Validate: validator.UserUpdate, Handle: c.updatePerson},
		"reset_password": {Validate: validator.ResetPassword, Handle: c.resetPassword},
		"reset_totp":     {Validate: validator.ID, Handle: c.resetTotp},
	})

	app.Page("/categories", web.Admin, c.categoriesPage)
	app.Ajax("/categories/ajax", web.Admin, web.Actions{
		"save_work":    {Validate: validator.WorkCategory, Handle: c.saveWorkCategory},
		"save_project": {Validate: validator.ProjectCategory, Handle: c.saveProjectCategory},
	})
}

// ---------------- 部门 ----------------

func (c *Controllers) departmentsPage(ctx *web.Ctx) error {
	list, err := c.S.Depts.ListWithCounts()
	if err != nil {
		return err
	}
	return ctx.Render("departments/index", map[string]any{"PageTitle": "部门管理", "Active": "departments", "Departments": list})
}

func (c *Controllers) departmentForm(ctx *web.Ctx) error {
	id := ctx.ID()
	old := map[string]any{"id": int64(0), "name": "", "parent_id": int64(0), "sort_order": int64(0), "is_active": int64(1)}
	if id > 0 {
		d, err := c.S.Depts.Find(id)
		if err != nil {
			return err
		}
		old = d
	}
	parents, err := c.S.Depts.ParentOptions(id, toInt(old["parent_id"]))
	if err != nil {
		return err
	}
	title := "新增部门"
	if id > 0 {
		title = "编辑部门"
	}
	return ctx.Render("departments/form", map[string]any{"PageTitle": title, "Active": "departments", "Old": old, "IsEdit": id > 0, "Parents": parents})
}

func (c *Controllers) saveDepartment(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.DepartmentInput)
	data := service.DepartmentInput{Name: p.Name, ParentID: p.ParentID, SortOrder: p.SortOrder, IsActive: p.IsActive}
	if p.ID > 0 {
		if err := c.S.Depts.Update(p.ID, data); err != nil {
			return nil, err
		}
		return web.Result{Message: "部门已更新。", Redirect: "/departments", FlashOnRedirect: true}, nil
	}
	if _, err := c.S.Depts.Create(data); err != nil {
		return nil, err
	}
	return web.Result{Message: "部门已创建。", Redirect: "/departments", FlashOnRedirect: true}, nil
}

// ---------------- 人员 ----------------

func (c *Controllers) peoplePage(ctx *web.Ctx) error {
	q := ctx.Query()
	v := validator.New(q)
	f := service.UserFilter{Q: validator.Truncate(v.Raw("q"), 100), DepartmentID: v.ID("department_id"), Status: v.Raw("status")}
	page := int(v.ID("page"))
	people, total, page, err := c.S.Users.List(f, page, 20)
	if err != nil {
		return err
	}
	depts, err := c.S.Depts.Tree()
	if err != nil {
		return err
	}
	query := url.Values{}
	if f.Q != "" {
		query.Set("q", f.Q)
	}
	if f.DepartmentID > 0 {
		query.Set("department_id", strconv.FormatInt(f.DepartmentID, 10))
	}
	if f.Status != "" {
		query.Set("status", f.Status)
	}
	return ctx.Render("people/index", map[string]any{
		"PageTitle": "人员管理", "Active": "people", "People": people, "Total": total,
		"Pager": web.NewPager(page, total, 20, "/people", query), "Departments": depts, "Filter": f,
	})
}

func (c *Controllers) personForm(ctx *web.Ctx) error {
	id := ctx.ID()
	old := map[string]any{"id": int64(0), "username": "", "name": "", "employee_no": "", "role": "employee", "department_id": int64(0), "phone": "", "email": "", "is_active": int64(1)}
	if id > 0 {
		p, err := c.S.Users.Find(id)
		if err != nil {
			return err
		}
		old = p
	}
	depts, err := c.S.Depts.ActiveOptions(toInt(old["department_id"]))
	if err != nil {
		return err
	}
	title := "新增员工"
	if id > 0 {
		title = "编辑员工"
	}
	return ctx.Render("people/form", map[string]any{
		"PageTitle": title, "Active": "people", "Old": old, "IsEdit": id > 0, "Departments": depts,
		"IsSelf": id == ctx.UserID(),
	})
}

func toUserInput(p validator.UserInput) service.UserInput {
	return service.UserInput{Username: p.Username, Name: p.Name, EmployeeNo: p.EmployeeNo, Role: p.Role, DepartmentID: p.DepartmentID,
		Phone: p.Phone, Email: p.Email, IsActive: p.IsActive, Password: p.Password}
}

func (c *Controllers) createPerson(ctx *web.Ctx, in any) (any, error) {
	if _, err := c.S.Users.Create(toUserInput(in.(validator.UserInput))); err != nil {
		return nil, err
	}
	return web.Result{Message: "员工账号已创建。", Redirect: "/people", FlashOnRedirect: true}, nil
}

func (c *Controllers) updatePerson(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.UserInput)
	if err := c.S.Users.Update(ctx.UserID(), p.ID, toUserInput(p)); err != nil {
		return nil, err
	}
	return web.Result{Message: "员工信息已更新。", Redirect: "/people/" + strconv.FormatInt(p.ID, 10) + "/edit", FlashOnRedirect: true}, nil
}

func (c *Controllers) resetPassword(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.ResetPasswordInput)
	u, err := c.S.Users.ResetPassword(ctx.UserID(), p.ID, p.Password)
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "已重置 " + u.Str("name") + " 的密码。", Redirect: "/people/" + strconv.FormatInt(p.ID, 10) + "/edit", FlashOnRedirect: true}, nil
}

func (c *Controllers) resetTotp(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.IDInput)
	u, err := c.S.Users.ResetTOTP(p.ID)
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "已重置 " + u.Str("name") + " 的 TOTP 二次验证。该员工下次可重新绑定。", Redirect: "/people/" + strconv.FormatInt(p.ID, 10) + "/edit", FlashOnRedirect: true}, nil
}

// ---------------- 分类 ----------------

func (c *Controllers) categoriesPage(ctx *web.Ctx) error {
	cats, projects, err := c.S.Categories.Overview()
	if err != nil {
		return err
	}
	var customer []service.Row
	names := map[int64]string{}
	for _, cat := range cats {
		names[cat.Int("id")] = cat.Str("name")
		if cat.Bool("is_customer_project") {
			customer = append(customer, cat)
		}
	}
	for _, p := range projects {
		p["category_name"] = names[p.Int("work_category_id")]
	}
	return ctx.Render("categories/index", map[string]any{"PageTitle": "分类管理", "Active": "categories", "Categories": cats, "Projects": projects, "CustomerCategories": customer})
}

func (c *Controllers) saveWorkCategory(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.WorkCategoryInput)
	created, err := c.S.Categories.SaveWork(service.WorkCategoryInput{ID: p.ID, Name: p.Name, SortOrder: p.SortOrder, IsCustomer: p.IsCustomer, IsActive: p.IsActive})
	if err != nil {
		return nil, err
	}
	msg := "工作分类已更新，历史台账不受影响。"
	if created {
		msg = "工作分类已新增。分类不会被物理删除。"
	}
	return web.Result{Message: msg, Redirect: "/categories", FlashOnRedirect: true}, nil
}

func (c *Controllers) saveProjectCategory(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.ProjectCategoryInput)
	created, err := c.S.Categories.SaveProject(service.ProjectInput{ID: p.ID, WorkCategoryID: p.WorkCategoryID, Name: p.Name, SortOrder: p.SortOrder, IsActive: p.IsActive})
	if err != nil {
		return nil, err
	}
	msg := "集客项目子分类已更新，历史台账数据保持不变。"
	if created {
		msg = "集客项目子分类已新增。"
	}
	return web.Result{Message: msg, Redirect: "/categories", FlashOnRedirect: true}, nil
}

func toInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	}
	return 0
}
