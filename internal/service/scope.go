package service

import (
	"work-ledger/internal/db"
)

// Scope 是当前用户可查看的台账范围，所有列表/详情/导出/报告共用。
//   - 系统管理员：全部
//   - 部门经理：本人创建的 + 本部门及其二级子部门（按台账创建时的部门快照 department_id）
//   - 员工：仅本人创建
type Scope struct {
	All     bool
	UserID  int64
	DeptIDs []int64
}

// ScopeFor 计算用户的可见范围。
func (c *Core) ScopeFor(user Row) (Scope, error) {
	sc := Scope{UserID: user.Int("id")}
	switch user.Str("role") {
	case "admin":
		sc.All = true
	case "manager":
		if dept := user.Int("department_id"); dept > 0 {
			ids, err := descendantIDs(c.DB, dept)
			if err != nil {
				return sc, err
			}
			sc.DeptIDs = ids
		}
	}
	return sc, nil
}

// SQL 生成可见范围条件。
func (sc Scope) SQL(alias string) (string, []any) {
	if sc.All {
		return "1 = 1", nil
	}
	if len(sc.DeptIDs) > 0 {
		args := []any{sc.UserID}
		for _, id := range sc.DeptIDs {
			args = append(args, id)
		}
		return "(" + alias + ".created_by = ? OR " + alias + ".department_id IN (" + db.Placeholders(len(sc.DeptIDs)) + "))", args
	}
	return alias + ".created_by = ?", []any{sc.UserID}
}

// CoversDepartment 是否可以查看/总结某部门（含子部门）。
func (sc Scope) CoversDepartment(id int64) bool {
	if sc.All {
		return true
	}
	for _, d := range sc.DeptIDs {
		if d == id {
			return true
		}
	}
	return false
}

// IsManagerScope 是否为部门经理的部门范围。
func (sc Scope) IsManagerScope() bool { return !sc.All && len(sc.DeptIDs) > 0 }

// CanEditRecord 只有管理员或记录创建者可以编辑。
func CanEditRecord(record, user Row) bool {
	return user.Str("role") == "admin" || record.Int("created_by") == user.Int("id")
}
