package service

import (
	"strings"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/password"
	"work-ledger/internal/session"
)

// Users 人员管理（不提供删除，只能停用）。
type Users struct{ *Core }

// UserFilter 人员列表筛选。
type UserFilter struct {
	Q            string
	DepartmentID int64
	Status       string // "", "1", "0"
}

// List 分页查询人员。
func (s *Users) List(f UserFilter, page, perPage int) ([]Row, int, int, error) {
	conds := []string{"1 = 1"}
	var args []any
	if f.Q != "" {
		like := "%" + f.Q + "%"
		conds = append(conds, "(u.name LIKE ? OR u.username LIKE ? OR COALESCE(u.employee_no, '') LIKE ? OR COALESCE(u.phone, '') LIKE ?)")
		args = append(args, like, like, like, like)
	}
	if f.DepartmentID > 0 {
		ids, err := descendantIDs(s.DB, f.DepartmentID)
		if err != nil {
			return nil, 0, 0, err
		}
		conds = append(conds, "u.department_id IN ("+db.Placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if f.Status == "1" || f.Status == "0" {
		conds = append(conds, "u.is_active = ?")
		args = append(args, f.Status)
	}
	where := strings.Join(conds, " AND ")
	total, err := db.Int(s.DB, "SELECT COUNT(*) FROM users u WHERE "+where, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	page, offset := clampPage(page, int(total), perPage)
	rows, err := db.All(s.DB, `SELECT u.*, d.name AS department_name, pd.name AS parent_department_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN departments pd ON pd.id = d.parent_id
		WHERE `+where+` ORDER BY u.is_active DESC, COALESCE(pd.name, d.name, u.name), d.name, u.name
		LIMIT ? OFFSET ?`, append(args, perPage, offset)...)
	return rows, int(total), page, err
}

// clampPage 修正页码并返回偏移。
func clampPage(page, total, perPage int) (int, int) {
	if perPage < 1 {
		perPage = 1
	}
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	return page, (page - 1) * perPage
}

func (s *Users) Find(id int64) (Row, error) {
	r, err := db.One(s.DB, `SELECT u.*, d.name AS department_name, pd.name AS parent_department_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN departments pd ON pd.id = d.parent_id
		WHERE u.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("员工不存在。")
	}
	return r, nil
}

// UserInput 人员表单（格式已校验）。
type UserInput struct {
	Username     string
	Name         string
	EmployeeNo   string
	Role         string
	DepartmentID int64
	Phone        string
	Email        string
	IsActive     bool
	Password     string // 仅新建时
}

func (s *Users) check(id int64, in UserInput, current Row) error {
	fields := map[string]string{}
	if v, err := db.Value(s.DB, "SELECT id FROM users WHERE username = ? AND id <> ?", in.Username, id); err != nil {
		return err
	} else if v != nil {
		fields["username"] = "登录账号已存在。"
	}
	if in.EmployeeNo != "" {
		if v, err := db.Value(s.DB, "SELECT id FROM users WHERE employee_no = ? AND id <> ?", in.EmployeeNo, id); err != nil {
			return err
		} else if v != nil {
			fields["employee_no"] = "员工工号已存在。"
		}
	}
	if in.DepartmentID > 0 {
		d, err := db.One(s.DB, "SELECT * FROM departments WHERE id = ?", in.DepartmentID)
		if err != nil {
			return err
		}
		if d == nil {
			fields["department_id"] = "所选部门不存在。"
		} else if !d.Bool("is_active") && in.DepartmentID != current.Int("department_id") {
			fields["department_id"] = "所选部门已停用。"
		}
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return nil
}

func (s *Users) Create(in UserInput) (int64, error) {
	if err := s.check(0, in, nil); err != nil {
		return 0, err
	}
	h, err := password.Hash(in.Password)
	if err != nil {
		return 0, err
	}
	ts := now()
	return db.Insert(s.DB, `INSERT INTO users (username, password_hash, name, employee_no, role, department_id, phone, email, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Username, h, in.Name, nullStr(in.EmployeeNo), in.Role, nullInt(in.DepartmentID), nullStr(in.Phone), nullStr(in.Email), boolInt(in.IsActive), ts, ts)
}

// Update 编辑人员，包含“不能停用自己/改自己角色、至少保留一名管理员”规则。
func (s *Users) Update(actorID, id int64, in UserInput) error {
	cur, err := s.Find(id)
	if err != nil {
		return err
	}
	if err := s.check(id, in, cur); err != nil {
		return err
	}
	fields := map[string]string{}
	isSelf := id == actorID
	if isSelf && !in.IsActive {
		fields["is_active"] = "不能停用当前登录的账号。"
	}
	if isSelf && in.Role != cur.Str("role") {
		fields["role"] = "不能修改自己的角色，以免误操作导致无法管理系统。"
	}
	wasActiveAdmin := cur.Str("role") == "admin" && cur.Bool("is_active")
	willRemain := in.Role == "admin" && in.IsActive
	if wasActiveAdmin && !willRemain {
		n, err := db.Int(s.DB, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = 1")
		if err != nil {
			return err
		}
		if n <= 1 {
			fields["role"] = "系统必须至少保留一名启用状态的管理员。"
		}
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return s.DB.Tx(func(tx db.Q) error {
		if _, err := db.Exec(tx, `UPDATE users SET username = ?, name = ?, employee_no = ?, role = ?, department_id = ?,
			phone = ?, email = ?, is_active = ?, updated_at = ? WHERE id = ?`,
			in.Username, in.Name, nullStr(in.EmployeeNo), in.Role, nullInt(in.DepartmentID), nullStr(in.Phone), nullStr(in.Email), boolInt(in.IsActive), now(), id); err != nil {
			return err
		}
		if !in.IsActive {
			return session.DestroyUser(tx, id) // 停用后立即踢下线
		}
		return nil
	})
}

// ResetPassword 管理员重置密码，并使该用户已有会话失效（当前管理员自己除外）。
func (s *Users) ResetPassword(actorID, id int64, plain string) (Row, error) {
	u, err := s.Find(id)
	if err != nil {
		return nil, err
	}
	h, err := password.Hash(plain)
	if err != nil {
		return nil, err
	}
	err = s.DB.Tx(func(tx db.Q) error {
		if _, err := db.Exec(tx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?", h, now(), id); err != nil {
			return err
		}
		if id != actorID {
			return session.DestroyUser(tx, id)
		}
		return nil
	})
	return u, err
}

// ResetTOTP 清除员工的 2FA 配置。
func (s *Users) ResetTOTP(id int64) (Row, error) {
	u, err := s.Find(id)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(s.DB, `UPDATE users SET totp_secret = NULL, totp_enabled = 0, totp_recovery_codes = NULL,
		totp_last_used_step = NULL, updated_at = ? WHERE id = ?`, now(), id)
	return u, err
}

// ParticipantOptions 可选参与人员：所有启用用户，加上 selected 中已停用的（编辑历史记录时保留）。
func (s *Users) ParticipantOptions(selected []int64) ([]Row, error) {
	cond := "u.is_active = 1"
	var args []any
	if len(selected) > 0 {
		cond += " OR u.id IN (" + db.Placeholders(len(selected)) + ")"
		for _, id := range selected {
			args = append(args, id)
		}
	}
	return db.All(s.DB, `SELECT u.id, u.username, u.name, u.employee_no, u.role, u.department_id, u.is_active,
		d.name AS department_name, pd.name AS parent_department_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN departments pd ON pd.id = d.parent_id
		WHERE `+cond+` ORDER BY COALESCE(pd.name, d.name, u.name), d.name, u.name`, args...)
}

// EmployeeOptions 筛选用的员工列表（scope 限定可见范围）。
func (s *Users) EmployeeOptions(sc Scope) ([]Row, error) {
	base := `SELECT u.id, u.name, u.username, u.employee_no, u.is_active, d.name AS department_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id`
	order := " ORDER BY u.is_active DESC, u.name"
	switch {
	case sc.All:
		return db.All(s.DB, base+order)
	case len(sc.DeptIDs) > 0:
		args := []any{sc.UserID}
		for _, id := range sc.DeptIDs {
			args = append(args, id)
		}
		return db.All(s.DB, base+" WHERE u.id = ? OR u.department_id IN ("+db.Placeholders(len(sc.DeptIDs))+")"+order, args...)
	default:
		return db.All(s.DB, base+" WHERE u.id = ?", sc.UserID)
	}
}

// ActiveAdminOptions 用于导入默认记录人等。
func (s *Users) AllBrief() ([]Row, error) {
	return db.All(s.DB, "SELECT id, name, username, employee_no, is_active FROM users ORDER BY is_active DESC, name")
}
