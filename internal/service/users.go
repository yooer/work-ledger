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
	rows, err := db.All(s.DB, `SELECT u.*, d.name AS department_name, dpp.path AS department_path, su.name AS supervisor_name, pos.name AS position_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN department_paths dpp ON dpp.id = d.id
		LEFT JOIN users su ON su.id = u.supervisor_id LEFT JOIN positions pos ON pos.id = u.position_id
		WHERE `+where+` ORDER BY u.is_active DESC, COALESCE(dpp.path, u.name), u.name
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
	r, err := db.One(s.DB, `SELECT u.*, d.name AS department_name, dpp.path AS department_path, pos.name AS position_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN department_paths dpp ON dpp.id = d.id
		LEFT JOIN positions pos ON pos.id = u.position_id
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
	Role         string // 由职务决定；仅内部调用未给职务时按此匹配默认职务
	PositionID   int64  // 职务
	DepartmentID int64
	SupervisorID int64 // 主管领导，0 表示无
	Phone        string
	Email        string
	IsActive     bool
	Password     string // 仅新建时
}

// resolvePosition 按职务确定权限级别（in.Role）。职务必须存在；新选择的职务不能是已停用的。
func (s *Users) resolvePosition(in *UserInput, current Row) (string, error) {
	if in.PositionID <= 0 && in.Role != "" {
		// 内部调用（如测试、脚本）只给了权限级别：使用该级别的默认职务
		id, err := db.Int(s.DB, "SELECT id FROM positions WHERE role = ? ORDER BY is_active DESC, sort_order, id LIMIT 1", in.Role)
		if err != nil {
			return "", err
		}
		in.PositionID = id
	}
	if in.PositionID <= 0 {
		return "请选择职务。", nil
	}
	p, err := db.One(s.DB, "SELECT * FROM positions WHERE id = ?", in.PositionID)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "所选职务不存在。", nil
	}
	if !p.Bool("is_active") && in.PositionID != current.Int("position_id") {
		return "所选职务已停用。", nil
	}
	in.Role = p.Str("role")
	return "", nil
}

func (s *Users) check(id int64, in UserInput, current Row) error {
	fields := map[string]string{}
	if v, err := db.Value(s.DB, "SELECT id FROM users WHERE LOWER(username) = LOWER(?) AND id <> ?", in.Username, id); err != nil {
		return err
	} else if v != nil {
		fields["username"] = "登录账号已存在。"
	}
	if in.EmployeeNo != "" {
		if v, err := db.Value(s.DB, "SELECT id FROM users WHERE LOWER(employee_no) = LOWER(?) AND id <> ?", in.EmployeeNo, id); err != nil {
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
	if msg, err := s.checkSupervisor(id, in.SupervisorID, current); err != nil {
		return err
	} else if msg != "" {
		fields["supervisor_id"] = msg
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return nil
}

// checkSupervisor 校验主管领导：必须存在；新指定的不能是已停用人员；不能形成循环汇报（不能选本人或自己的下属）。
func (s *Users) checkSupervisor(id, supervisorID int64, current Row) (string, error) {
	if supervisorID <= 0 {
		return "", nil
	}
	if id > 0 && supervisorID == id {
		return "主管领导不能是本人。", nil
	}
	sup, err := db.One(s.DB, "SELECT id, name, is_active FROM users WHERE id = ?", supervisorID)
	if err != nil {
		return "", err
	}
	if sup == nil {
		return "所选主管领导不存在。", nil
	}
	if !sup.Bool("is_active") && supervisorID != current.Int("supervisor_id") {
		return "所选主管领导已停用。", nil
	}
	if id > 0 {
		subs, err := subordinateIDs(s.DB, id)
		if err != nil {
			return "", err
		}
		if containsID(subs, supervisorID) {
			return "“" + sup.Str("name") + "”是该员工的下属，不能设为其主管领导（会形成循环汇报）。", nil
		}
	}
	return "", nil
}

// SupervisorOptions 主管领导下拉：启用人员（排除本人及其下属），加上当前已选的停用人员。
func (s *Users) SupervisorOptions(id, current int64) ([]Row, error) {
	var exclude []int64
	if id > 0 {
		subs, err := subordinateIDs(s.DB, id)
		if err != nil {
			return nil, err
		}
		exclude = append(subs, id)
	}
	rows, err := db.All(s.DB, `SELECT u.id, u.name, u.employee_no, u.role, u.is_active, d.name AS department_name, dpp.path AS department_path
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN department_paths dpp ON dpp.id = d.id
		WHERE u.is_active = 1 OR u.id = ? ORDER BY u.is_active DESC, COALESCE(dpp.path, u.name), u.name`, current)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if !containsID(exclude, r.Int("id")) {
			out = append(out, r)
		}
	}
	return out, nil
}

// DirectReports 直接下属列表。
func (s *Users) DirectReports(id int64) ([]Row, error) {
	return db.All(s.DB, `SELECT u.id, u.name, u.employee_no, u.is_active, d.name AS department_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id
		WHERE u.supervisor_id = ? AND u.id <> ? ORDER BY u.is_active DESC, u.name`, id, id)
}

func (s *Users) Create(in UserInput) (int64, error) {
	if msg, err := s.resolvePosition(&in, nil); err != nil {
		return 0, err
	} else if msg != "" {
		return 0, apperr.Validation(map[string]string{"position_id": msg})
	}
	if err := s.check(0, in, nil); err != nil {
		return 0, err
	}
	h, err := password.Hash(in.Password)
	if err != nil {
		return 0, err
	}
	ts := now()
	return db.Insert(s.DB, `INSERT INTO users (username, password_hash, name, employee_no, role, position_id, department_id, supervisor_id, phone, email, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Username, h, in.Name, nullStr(in.EmployeeNo), in.Role, nullInt(in.PositionID), nullInt(in.DepartmentID), nullInt(in.SupervisorID), nullStr(in.Phone), nullStr(in.Email), boolInt(in.IsActive), ts, ts)
}

// Update 编辑人员，包含“不能停用自己/改自己角色、至少保留一名管理员”规则。
func (s *Users) Update(actorID, id int64, in UserInput) error {
	cur, err := s.Find(id)
	if err != nil {
		return err
	}
	if msg, err := s.resolvePosition(&in, cur); err != nil {
		return err
	} else if msg != "" {
		return apperr.Validation(map[string]string{"position_id": msg})
	}
	if err := s.check(id, in, cur); err != nil {
		return err
	}
	fields := map[string]string{}
	isSelf := id == actorID
	if isSelf && !in.IsActive {
		fields["is_active"] = "不能停用当前登录的账号。"
	}
	if isSelf && (in.Role != cur.Str("role") || (cur.Int("position_id") > 0 && in.PositionID != cur.Int("position_id"))) {
		fields["position_id"] = "不能修改自己的职务，以免误操作导致无法管理系统。"
	}
	wasActiveAdmin := cur.Str("role") == "admin" && cur.Bool("is_active")
	willRemain := in.Role == "admin" && in.IsActive
	if wasActiveAdmin && !willRemain {
		n, err := db.Int(s.DB, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = 1")
		if err != nil {
			return err
		}
		if n <= 1 {
			fields["position_id"] = "系统必须至少保留一名启用状态的管理员（系统管理员级别的职务）。"
		}
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return s.DB.Tx(func(tx db.Q) error {
		if _, err := db.Exec(tx, `UPDATE users SET username = ?, name = ?, employee_no = ?, role = ?, position_id = ?, department_id = ?, supervisor_id = ?,
			phone = ?, email = ?, is_active = ?, updated_at = ? WHERE id = ?`,
			in.Username, in.Name, nullStr(in.EmployeeNo), in.Role, nullInt(in.PositionID), nullInt(in.DepartmentID), nullInt(in.SupervisorID), nullStr(in.Phone), nullStr(in.Email), boolInt(in.IsActive), now(), id); err != nil {
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
		d.name AS department_name, dpp.path AS department_path
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN department_paths dpp ON dpp.id = d.id
		WHERE `+cond+` ORDER BY COALESCE(dpp.path, u.name), u.name`, args...)
}

// EmployeeOptions 筛选用的员工列表（scope 限定可见范围：本人 + 下属 + 分管部门人员）。
func (s *Users) EmployeeOptions(sc Scope) ([]Row, error) {
	base := `SELECT u.id, u.name, u.username, u.employee_no, u.is_active, d.name AS department_name
		FROM users u LEFT JOIN departments d ON d.id = u.department_id`
	order := " ORDER BY u.is_active DESC, u.name"
	if sc.All {
		return db.All(s.DB, base+order)
	}
	cond := "u.id = ?"
	args := []any{sc.UserID}
	if len(sc.UserIDs) > 0 {
		cond += " OR u.id IN (" + db.Placeholders(len(sc.UserIDs)) + ")"
		for _, id := range sc.UserIDs {
			args = append(args, id)
		}
	}
	if len(sc.DeptIDs) > 0 {
		cond += " OR u.department_id IN (" + db.Placeholders(len(sc.DeptIDs)) + ")"
		for _, id := range sc.DeptIDs {
			args = append(args, id)
		}
	}
	return db.All(s.DB, base+" WHERE "+cond+order, args...)
}

// ActiveAdminOptions 用于导入默认记录人等。
func (s *Users) AllBrief() ([]Row, error) {
	return db.All(s.DB, "SELECT id, name, username, employee_no, is_active FROM users ORDER BY is_active DESC, name")
}

// OrgNode 组织架构图节点（按主管领导关系）。
type OrgNode struct {
	ID         int64
	Name       string
	EmployeeNo string
	Role       string
	Position   string // 职务名称
	Department string
	Active     bool
	TeamSize   int // 直接 + 间接下属人数
	Children   []*OrgNode
}

// OrgChart 返回按主管领导关系组织的树：Trees 为有下属或有上级的人员组成的树，
// Unassigned 为既无上级也无下属的人员。数据异常形成环时，环上人员作为根节点展示，不会死循环。
func (s *Users) OrgChart() (trees []*OrgNode, unassigned []*OrgNode, err error) {
	rows, err := db.All(s.DB, `SELECT u.id, u.name, u.employee_no, u.role, u.is_active, u.supervisor_id,
		(SELECT name FROM positions WHERE id = u.position_id) AS position_name,
		d.name AS department_name, dpp.path AS department_path
		FROM users u LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN department_paths dpp ON dpp.id = d.id
		ORDER BY u.is_active DESC, COALESCE(dpp.path, u.name), u.name`)
	if err != nil {
		return nil, nil, err
	}
	nodes := map[int64]*OrgNode{}
	parent := map[int64]int64{}
	var order []int64
	for _, r := range rows {
		id := r.Int("id")
		nodes[id] = &OrgNode{ID: id, Name: r.Str("name"), EmployeeNo: r.Str("employee_no"), Role: r.Str("role"), Position: r.Str("position_name"),
			Department: deptLabel(r["department_name"], r["department_path"]), Active: r.Bool("is_active")}
		if sid := r.Int("supervisor_id"); sid > 0 && sid != id {
			parent[id] = sid
		}
		order = append(order, id)
	}
	// 判断某节点沿上级链能否走到根（无环）。
	reachesRoot := func(id int64) bool {
		seen := map[int64]bool{}
		for {
			if seen[id] {
				return false
			}
			seen[id] = true
			p, ok := parent[id]
			if !ok || nodes[p] == nil {
				return true
			}
			id = p
		}
	}
	isRoot := map[int64]bool{}
	for _, id := range order {
		p, ok := parent[id]
		if !ok || nodes[p] == nil || !reachesRoot(id) {
			isRoot[id] = true
			continue
		}
		nodes[p].Children = append(nodes[p].Children, nodes[id])
	}
	var count func(n *OrgNode, depth int) int
	count = func(n *OrgNode, depth int) int {
		if depth > 1000 {
			return 0
		}
		total := 0
		for _, c := range n.Children {
			total += 1 + count(c, depth+1)
		}
		n.TeamSize = total
		return total
	}
	for _, id := range order {
		if !isRoot[id] {
			continue
		}
		n := nodes[id]
		count(n, 0)
		if len(n.Children) == 0 {
			unassigned = append(unassigned, n)
		} else {
			trees = append(trees, n)
		}
	}
	return trees, unassigned, nil
}
