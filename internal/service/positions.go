package service

import (
	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
)

// Positions 职务管理：名称可自定义，每个职务绑定一个权限级别（admin / manager / employee）。
// users.role 保存当前职务的权限级别（冗余字段，保证权限判断与 PHP 版备份兼容），随职务变化同步。
type Positions struct{ *Core }

// RoleLevels 权限级别（值与 users.role 一致）。
var RoleLevels = []struct{ Value, Label, Hint string }{
	{"admin", "系统管理员", "管理全部数据与系统设置"},
	{"manager", "部门管理者", "查看、导出本部门及所有下级部门的台账，生成部门总结"},
	{"employee", "普通成员", "查看本人创建和参与的台账"},
}

// DefaultPositions 首次升级 / 安装时预置的职务（名称不存在时才创建）。
var DefaultPositions = []struct {
	Name, Role string
}{
	{"系统管理员", "admin"},
	{"分公司总经理", "manager"},
	{"分公司副总经理", "manager"},
	{"部门经理", "manager"},
	{"部门副经理", "manager"},
	{"主管", "employee"},
	{"员工", "employee"},
}

// legacyRolePosition 旧角色对应的默认职务（升级迁移、恢复 PHP 版备份时使用）。
var legacyRolePosition = map[string]string{"admin": "系统管理员", "manager": "部门经理", "employee": "员工"}

// EnsureDefaults 职务表为空时写入预置职务；并为没有职务的人员按其角色补上对应职务。可重复执行。
func (s *Positions) EnsureDefaults() error {
	return s.DB.Tx(func(tx db.Q) error { return ensurePositions(tx) })
}

func ensurePositions(tx db.Q) error {
	n, err := db.Int(tx, "SELECT COUNT(*) FROM positions")
	if err != nil {
		return err
	}
	ts := now()
	if n == 0 {
		for i, p := range DefaultPositions {
			if _, err := db.Exec(tx, `INSERT INTO positions (name, role, is_active, sort_order, created_at, updated_at) VALUES (?, ?, 1, ?, ?, ?)`,
				p.Name, p.Role, (i+1)*10, ts, ts); err != nil {
				return err
			}
		}
	}
	for role, name := range legacyRolePosition {
		// 默认职务被改名或删除时，退而使用同权限级别的第一个职务；仍没有则新建。
		id, err := db.Int(tx, "SELECT id FROM positions WHERE name = ? AND role = ? LIMIT 1", name, role)
		if err != nil {
			return err
		}
		if id == 0 {
			if id, err = db.Int(tx, "SELECT id FROM positions WHERE role = ? ORDER BY is_active DESC, sort_order, id LIMIT 1", role); err != nil {
				return err
			}
		}
		if id == 0 {
			if id, err = db.Insert(tx, `INSERT INTO positions (name, role, is_active, sort_order, created_at, updated_at) VALUES (?, ?, 1, 999, ?, ?)`,
				name+"（"+role+"）", role, ts, ts); err != nil {
				return err
			}
		}
		if _, err := db.Exec(tx, "UPDATE users SET position_id = ? WHERE position_id IS NULL AND role = ?", id, role); err != nil {
			return err
		}
	}
	// 职务与 users.role 不一致（例如直接改过库）时，以职务为准。
	_, err = db.Exec(tx, `UPDATE users SET role = (SELECT p.role FROM positions p WHERE p.id = users.position_id)
		WHERE position_id IS NOT NULL AND role <> (SELECT p.role FROM positions p WHERE p.id = users.position_id)`)
	return err
}

// List 职务列表（附加在职人数）。
func (s *Positions) List() ([]Row, error) {
	return db.All(s.DB, `SELECT p.*, (SELECT COUNT(*) FROM users u WHERE u.position_id = p.id) AS user_count,
		(SELECT COUNT(*) FROM users u WHERE u.position_id = p.id AND u.is_active = 1) AS active_user_count
		FROM positions p ORDER BY p.sort_order, p.id`)
}

// Options 人员表单的职务下拉：启用的职务，加上当前值（即使已停用）。
func (s *Positions) Options(current int64) ([]Row, error) {
	return db.All(s.DB, "SELECT * FROM positions WHERE is_active = 1 OR id = ? ORDER BY sort_order, id", current)
}

// Find 查找职务。
func (s *Positions) Find(id int64) (Row, error) {
	r, err := db.One(s.DB, "SELECT * FROM positions WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("职务不存在。")
	}
	return r, nil
}

// PositionInput 职务表单（格式已校验）。
type PositionInput struct {
	ID        int64
	Name      string
	Role      string
	SortOrder int64
	IsActive  bool
}

// Save 新增或修改职务；修改权限级别时同步该职务下全部人员的 users.role。
// 保护规则：系统管理员级别的职务仍有人员在用时，不能改为其他级别或停用（避免失去管理员）。
func (s *Positions) Save(in PositionInput) (bool, error) {
	var cur Row
	if in.ID > 0 {
		var err error
		if cur, err = s.Find(in.ID); err != nil {
			return false, err
		}
	}
	fields := map[string]string{}
	if dup, err := db.Value(s.DB, "SELECT id FROM positions WHERE name = ? AND id <> ?", in.Name, in.ID); err != nil {
		return false, err
	} else if dup != nil {
		fields["name"] = "职务名称已存在。"
	}
	if cur != nil && cur.Str("role") == "admin" && (in.Role != "admin" || !in.IsActive) {
		n, err := db.Int(s.DB, "SELECT COUNT(*) FROM users WHERE position_id = ?", in.ID)
		if err != nil {
			return false, err
		}
		if n > 0 {
			if in.Role != "admin" {
				fields["role"] = "该职务下还有人员，不能从“系统管理员”改为其他权限级别，以免系统失去管理员。请先调整这些人员的职务。"
			} else {
				fields["is_active"] = "该职务下还有人员，不能停用系统管理员级别的职务。"
			}
		}
	}
	if len(fields) > 0 {
		return false, apperr.Validation(fields)
	}
	ts := now()
	if cur == nil {
		_, err := db.Insert(s.DB, `INSERT INTO positions (name, role, is_active, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			in.Name, in.Role, boolInt(in.IsActive), in.SortOrder, ts, ts)
		return true, err
	}
	err := s.DB.Tx(func(tx db.Q) error {
		if _, err := db.Exec(tx, "UPDATE positions SET name = ?, role = ?, is_active = ?, sort_order = ?, updated_at = ? WHERE id = ?",
			in.Name, in.Role, boolInt(in.IsActive), in.SortOrder, ts, in.ID); err != nil {
			return err
		}
		if in.Role != cur.Str("role") {
			if _, err := db.Exec(tx, "UPDATE users SET role = ?, updated_at = ? WHERE position_id = ?", in.Role, ts, in.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return false, err
}

// RoleLabel 权限级别名称。
func RoleLabel(role string) string {
	for _, r := range RoleLevels {
		if r.Value == role {
			return r.Label
		}
	}
	return role
}
