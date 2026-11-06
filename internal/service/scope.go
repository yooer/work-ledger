package service

import (
	"work-ledger/internal/db"
)

// Scope 是当前用户可查看的台账范围，所有列表/详情/导出/报告共用。
//   - 系统管理员：全部
//   - 部门经理：本人创建的 + 本部门及其二级子部门（按台账创建时的部门快照 department_id）
//   - 主管领导（任何角色，只要有下属）：另加所有直接、间接下属创建或参与的台账
//   - 员工：本人创建的 + 本人作为参与人员的
//
// 部门范围与下属范围取并集；“本人 / 下属”范围同时包含其作为参与人员的台账（只能查看，编辑仍按创建者判断）。
type Scope struct {
	All     bool
	UserID  int64
	DeptIDs []int64 // 部门经理的部门范围
	UserIDs []int64 // 直接、间接下属（不含本人）
}

// ScopeFor 计算用户的可见范围。
func (c *Core) ScopeFor(user Row) (Scope, error) {
	sc := Scope{UserID: user.Int("id")}
	if user.Str("role") == "admin" {
		sc.All = true
		return sc, nil
	}
	if user.Str("role") == "manager" {
		if dept := user.Int("department_id"); dept > 0 {
			ids, err := descendantIDs(c.DB, dept)
			if err != nil {
				return sc, err
			}
			sc.DeptIDs = ids
		}
	}
	subs, err := subordinateIDs(c.DB, sc.UserID)
	if err != nil {
		return sc, err
	}
	sc.UserIDs = subs
	return sc, nil
}

// subordinateIDs 返回某人的全部直接、间接下属（含已停用人员，其历史台账仍属团队）。
// 递归时排除本人，即使数据异常出现环也能结束。
func subordinateIDs(q db.Q, userID int64) ([]int64, error) {
	if userID <= 0 {
		return nil, nil
	}
	rows, err := db.All(q, `WITH RECURSIVE subs(id) AS (
			SELECT id FROM users WHERE supervisor_id = ? AND id <> ?
			UNION
			SELECT u.id FROM users u JOIN subs s ON u.supervisor_id = s.id WHERE u.id <> ?
		) SELECT id FROM subs ORDER BY id`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Int("id"))
	}
	return ids, nil
}

// InvolvedSQL 台账由 ids 中的人创建，或 ids 中的人是参与人员。
func InvolvedSQL(alias string, ids []int64) (string, []any) {
	ph := db.Placeholders(len(ids))
	args := make([]any, 0, len(ids)*2)
	for i := 0; i < 2; i++ {
		for _, id := range ids {
			args = append(args, id)
		}
	}
	return "(" + alias + ".created_by IN (" + ph + ") OR EXISTS (SELECT 1 FROM work_record_participants scp WHERE scp.work_record_id = " +
		alias + ".id AND scp.user_id IN (" + ph + ")))", args
}

// SQL 生成可见范围条件。
func (sc Scope) SQL(alias string) (string, []any) {
	if sc.All {
		return "1 = 1", nil
	}
	cond, args := InvolvedSQL(alias, append([]int64{sc.UserID}, sc.UserIDs...))
	if len(sc.DeptIDs) > 0 {
		cond += " OR " + alias + ".department_id IN (" + db.Placeholders(len(sc.DeptIDs)) + ")"
		for _, id := range sc.DeptIDs {
			args = append(args, id)
		}
	}
	return "(" + cond + ")", args
}

// CoversDepartment 是否可以查看/总结某部门（含子部门）。
func (sc Scope) CoversDepartment(id int64) bool {
	if sc.All {
		return true
	}
	return containsID(sc.DeptIDs, id)
}

// IsSubordinate 某人是否为当前用户的直接或间接下属。
func (sc Scope) IsSubordinate(id int64) bool { return containsID(sc.UserIDs, id) }

// IsTeamScope 是否拥有部门范围或下属（即“领导”：可看团队台账、生成团队总结）。
func (sc Scope) IsTeamScope() bool { return !sc.All && (len(sc.DeptIDs) > 0 || len(sc.UserIDs) > 0) }

// HasSubordinates 是否有下属。
func (sc Scope) HasSubordinates() bool { return len(sc.UserIDs) > 0 }

// LeadsRecord 当前用户是否以“领导”身份管辖这条台账（下属创建，或属于所管部门）。
func (sc Scope) LeadsRecord(record Row) bool {
	if sc.All || record.Int("created_by") == sc.UserID {
		return false
	}
	return containsID(sc.UserIDs, record.Int("created_by")) || containsID(sc.DeptIDs, record.Int("department_id"))
}

func containsID(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// CanDeleteRecord 管理员、记录创建者可以删除；系统设置允许时，主管领导/部门经理也可以删除所管辖的台账。
// 仅作为参与人员的人不能删除。
func (s *Records) CanDeleteRecord(sc Scope, record, user Row) bool {
	if user.Str("role") == "admin" || record.Int("created_by") == user.Int("id") {
		return true
	}
	return s.Settings.LeaderCanDelete() && sc.LeadsRecord(record)
}

// CanEditRecord 管理员、记录创建者可以编辑；系统设置允许时，主管领导/部门经理也可以编辑所管辖的台账。
func (s *Records) CanEditRecord(sc Scope, record, user Row) bool {
	if user.Str("role") == "admin" || record.Int("created_by") == user.Int("id") {
		return true
	}
	return s.Settings.LeaderCanEdit() && sc.LeadsRecord(record)
}
