package validator

import (
	"net/mail"
	"regexp"
	"strconv"
	"unicode/utf8"
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{3,64}$`)

func lenBetween(v *V, key, label, s string, min, max int) {
	n := utf8.RuneCountInString(s)
	if n < min || n > max {
		v.Fail(key, label+"长度需为 "+strconv.Itoa(min)+"-"+strconv.Itoa(max)+" 个字。")
	}
}

// IDInput 只有 id 的请求。
type IDInput struct{ ID int64 }

func ID(in Input) (any, error) {
	v := New(in)
	out := IDInput{ID: v.RequiredID("id", "记录")}
	return out, v.Err()
}

// DepartmentInput 部门表单。
type DepartmentInput struct {
	ID        int64
	Name      string
	ParentID  int64
	SortOrder int64
	IsActive  bool
}

func Department(in Input) (any, error) {
	v := New(in)
	out := DepartmentInput{
		ID:        v.ID("id"),
		Name:      v.Raw("name"),
		ParentID:  v.ID("parent_id"),
		SortOrder: v.IntRange("sort_order", "排序值", 0, -999, 999),
		IsActive:  v.Bool("is_active"),
	}
	lenBetween(v, "name", "部门名称", out.Name, 2, 60)
	return out, v.Err()
}

// UserInput 人员表单。
type UserInput struct {
	ID           int64
	Username     string
	Name         string
	EmployeeNo   string
	PositionID   int64
	DepartmentID int64
	SupervisorID int64
	Phone        string
	Email        string
	IsActive     bool
	Password     string
}

func user(in Input, creating bool) (UserInput, *V) {
	v := New(in)
	out := UserInput{
		ID:           v.ID("id"),
		Username:     v.Raw("username"),
		Name:         v.Raw("name"),
		EmployeeNo:   v.Str("employee_no", "员工工号", 30),
		PositionID:   v.ID("position_id"),
		DepartmentID: v.ID("department_id"),
		SupervisorID: v.ID("supervisor_id"),
		Phone:        v.Str("phone", "联系电话", 30),
		Email:        v.Str("email", "电子邮箱", 120),
		IsActive:     v.Bool("is_active"),
		Password:     v.RawKeep("password"),
	}
	if !usernameRe.MatchString(out.Username) {
		v.Fail("username", "登录账号需为 3-64 位字母、数字或 _ . @ -。")
	}
	lenBetween(v, "name", "姓名", out.Name, 2, 50)
	if out.Email != "" {
		if a, err := mail.ParseAddress(out.Email); err != nil || a.Address != out.Email {
			v.Fail("email", "电子邮箱格式不正确。")
		}
	}
	if creating {
		passwordRule(v, "password", out.Password, "初始密码")
	}
	if out.PositionID <= 0 {
		v.Fail("position_id", "请选择职务。")
	}
	if out.ID > 0 && out.SupervisorID == out.ID {
		v.Fail("supervisor_id", "主管领导不能是本人。")
	}
	return out, v
}

func passwordRule(v *V, key, pwd, label string) {
	if len(pwd) < 8 {
		v.Fail(key, label+"至少需要 8 位。")
	} else if utf8.RuneCountInString(pwd) > 128 {
		v.Fail(key, label+"不能超过 128 个字符。")
	}
}

func UserCreate(in Input) (any, error) {
	out, v := user(in, true)
	return out, v.Err()
}

func UserUpdate(in Input) (any, error) {
	out, v := user(in, false)
	if out.ID <= 0 {
		v.Fail("id", "缺少员工 ID。")
	}
	return out, v.Err()
}

// ResetPasswordInput 管理员重置密码。
type ResetPasswordInput struct {
	ID       int64
	Password string
}

func ResetPassword(in Input) (any, error) {
	v := New(in)
	out := ResetPasswordInput{ID: v.RequiredID("id", "员工"), Password: v.RawKeep("new_password")}
	passwordRule(v, "new_password", out.Password, "新密码")
	if out.Password != v.RawKeep("new_password_confirmation") {
		v.Fail("new_password_confirmation", "两次输入的新密码不一致。")
	}
	return out, v.Err()
}

// WorkCategoryInput 工作分类。
type WorkCategoryInput struct {
	ID           int64
	DepartmentID int64
	Name         string
	SortOrder    int64
	IsCustomer   bool
	IsActive     bool
}

func WorkCategory(in Input) (any, error) {
	v := New(in)
	out := WorkCategoryInput{
		ID:           v.ID("id"),
		DepartmentID: v.RequiredID("department_id", "所属部门"),
		Name:         v.Raw("name"),
		SortOrder:    v.IntRange("sort_order", "排序值", 0, -999, 999),
		IsCustomer:   v.Bool("is_customer_project"),
		IsActive:     v.Bool("is_active"),
	}
	lenBetween(v, "name", "分类名称", out.Name, 2, 60)
	return out, v.Err()
}

// CategoryFilterInput 分类管理页的部门筛选：Dept>0 为某部门（含下级），Unassigned 为“未指定部门”。
type CategoryFilterInput struct {
	Dept       int64
	Unassigned bool
	Key        string // 回显用："" / "none" / 部门 ID
}

func CategoryFilter(in Input) CategoryFilterInput {
	v := New(in)
	if v.Raw("dept") == "none" {
		return CategoryFilterInput{Unassigned: true, Key: "none"}
	}
	out := CategoryFilterInput{Dept: v.ID("dept")}
	if out.Dept > 0 {
		out.Key = strconv.FormatInt(out.Dept, 10)
	}
	return out
}

// ProjectCategoryInput 子分类。
type ProjectCategoryInput struct {
	ID             int64
	WorkCategoryID int64
	Name           string
	SortOrder      int64
	IsActive       bool
}

func ProjectCategory(in Input) (any, error) {
	v := New(in)
	out := ProjectCategoryInput{
		ID:             v.ID("id"),
		WorkCategoryID: v.RequiredID("work_category_id", "所属工作分类"),
		Name:           v.Raw("name"),
		SortOrder:      v.IntRange("sort_order", "排序值", 0, -999, 999),
		IsActive:       v.Bool("is_active"),
	}
	lenBetween(v, "name", "子分类名称", out.Name, 2, 60)
	return out, v.Err()
}

// PositionInput 职务表单。
type PositionInput struct {
	ID        int64
	Name      string
	Role      string
	SortOrder int64
	IsActive  bool
}

func Position(in Input) (any, error) {
	v := New(in)
	out := PositionInput{
		ID:        v.ID("id"),
		Name:      v.Raw("name"),
		Role:      v.OneOf("role", "权限级别", "", "admin", "manager", "employee"),
		SortOrder: v.IntRange("sort_order", "排序", 0, -999, 999),
		IsActive:  v.Bool("is_active"),
	}
	lenBetween(v, "name", "职务名称", out.Name, 1, 30)
	if out.Role == "" {
		v.Fail("role", "请选择权限级别。")
	}
	return out, v.Err()
}
