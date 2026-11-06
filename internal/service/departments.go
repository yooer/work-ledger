package service

import (
	"fmt"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
)

// Departments 部门管理（不限层级，不提供删除）。
type Departments struct{ *Core }

// Tree 返回深度优先排序的部门列表（同级按排序号、名称），附加 depth 与 label（完整路径，如“分公司 / 部门 / 班组”）。
func (s *Departments) Tree() ([]Row, error) {
	return departmentTree(s.DB)
}

// maxDeptDepth 防御数据异常（环）时的最大递归深度。
const maxDeptDepth = 50

func departmentTree(q db.Q) ([]Row, error) {
	rows, err := db.All(q, "SELECT * FROM departments ORDER BY sort_order, name")
	if err != nil {
		return nil, err
	}
	byID := map[int64]bool{}
	for _, r := range rows {
		byID[r.Int("id")] = true
	}
	var tops []Row
	children := map[int64][]Row{}
	for _, r := range rows {
		// 上级不存在的部门按顶级处理
		if r.Null("parent_id") || !byID[r.Int("parent_id")] {
			tops = append(tops, r)
		} else {
			children[r.Int("parent_id")] = append(children[r.Int("parent_id")], r)
		}
	}
	out := []Row{}
	seen := map[int64]bool{}
	var walk func(r Row, depth int64, prefix string)
	walk = func(r Row, depth int64, prefix string) {
		if seen[r.Int("id")] || depth > maxDeptDepth {
			return
		}
		seen[r.Int("id")] = true
		r["depth"] = depth
		r["label"] = prefix + r.Str("name")
		out = append(out, r)
		for _, c := range children[r.Int("id")] {
			walk(c, depth+1, r.Str("label")+" / ")
		}
	}
	for _, t := range tops {
		walk(t, 0, "")
	}
	// 数据异常形成环的部门：作为顶级附在末尾，避免在页面上消失
	for _, r := range rows {
		if !seen[r.Int("id")] {
			walk(r, 0, "")
		}
	}
	return out, nil
}

// ListWithCounts 部门管理首页：附加人员数与子部门数。
func (s *Departments) ListWithCounts() ([]Row, error) {
	list, err := s.Tree()
	if err != nil {
		return nil, err
	}
	counts, err := db.All(s.DB, `SELECT department_id, COUNT(*) AS total,
		SUM(CASE WHEN is_active = 1 THEN 1 ELSE 0 END) AS active_total
		FROM users WHERE department_id IS NOT NULL GROUP BY department_id`)
	if err != nil {
		return nil, err
	}
	byDept := map[int64]Row{}
	for _, c := range counts {
		byDept[c.Int("department_id")] = c
	}
	childCount := map[int64]int64{}
	for _, d := range list {
		if !d.Null("parent_id") {
			childCount[d.Int("parent_id")]++ // 直接下级部门数
		}
	}
	for _, d := range list {
		c := byDept[d.Int("id")]
		d["user_count"] = c.Int("total")
		d["active_user_count"] = c.Int("active_total")
		d["child_count"] = childCount[d.Int("id")]
	}
	return list, nil
}

func (s *Departments) Find(id int64) (Row, error) {
	r, err := db.One(s.DB, "SELECT * FROM departments WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, apperr.NotFound("部门不存在。")
	}
	return r, nil
}

// ParentOptions 可作为上级的部门：排除自己及自己的全部下级（防止形成环）、已停用的（当前上级保留）。
func (s *Departments) ParentOptions(selfID, currentParent int64) ([]Row, error) {
	all, err := s.Tree()
	if err != nil {
		return nil, err
	}
	exclude := map[int64]bool{}
	if selfID > 0 {
		ids, err := descendantIDs(s.DB, selfID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			exclude[id] = true
		}
	}
	var out []Row
	for _, r := range all {
		if exclude[r.Int("id")] {
			continue
		}
		if !r.Bool("is_active") && r.Int("id") != currentParent {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// ActiveOptions 可选部门（启用的，以及 keepID 当前值）。
func (s *Departments) ActiveOptions(keepID int64) ([]Row, error) {
	all, err := s.Tree()
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, r := range all {
		if r.Bool("is_active") || r.Int("id") == keepID {
			out = append(out, r)
		}
	}
	return out, nil
}

// DepartmentInput 部门表单（格式已校验）。
type DepartmentInput struct {
	Name      string
	ParentID  int64
	SortOrder int64
	IsActive  bool
}

func (s *Departments) check(id int64, in DepartmentInput, current Row) error {
	fields := map[string]string{}
	if exists, err := db.Value(s.DB, "SELECT id FROM departments WHERE name = ? AND id <> ?", in.Name, id); err != nil {
		return err
	} else if exists != nil {
		fields["name"] = "部门名称已存在（部门名称需全局唯一）。"
	}
	currentParent := current.Int("parent_id")
	if in.ParentID > 0 {
		parent, err := db.One(s.DB, "SELECT * FROM departments WHERE id = ?", in.ParentID)
		if err != nil {
			return err
		}
		switch {
		case parent == nil:
			fields["parent_id"] = "所选上级部门不存在。"
		case !parent.Bool("is_active") && in.ParentID != currentParent:
			fields["parent_id"] = "所选上级部门已停用。"
		}
	}
	if id > 0 && in.ParentID == id {
		fields["parent_id"] = "部门不能把自己设为上级。"
	} else if id > 0 && in.ParentID > 0 {
		subs, err := descendantIDs(s.DB, id)
		if err != nil {
			return err
		}
		if containsID(subs, in.ParentID) {
			fields["parent_id"] = "不能把部门挂到它自己的下级部门下面（会形成循环）。"
		}
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return nil
}

func (s *Departments) Create(in DepartmentInput) (int64, error) {
	if err := s.check(0, in, nil); err != nil {
		return 0, err
	}
	ts := now()
	return db.Insert(s.DB, "INSERT INTO departments (name, parent_id, is_active, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		in.Name, nullInt(in.ParentID), boolInt(in.IsActive), in.SortOrder, ts, ts)
}

func (s *Departments) Update(id int64, in DepartmentInput) error {
	cur, err := s.Find(id)
	if err != nil {
		return err
	}
	if err := s.check(id, in, cur); err != nil {
		return err
	}
	_, err = db.Exec(s.DB, "UPDATE departments SET name = ?, parent_id = ?, is_active = ?, sort_order = ?, updated_at = ? WHERE id = ?",
		in.Name, nullInt(in.ParentID), boolInt(in.IsActive), in.SortOrder, now(), id)
	return err
}

// DescendantIDs 部门及其子部门 ID。
func (s *Departments) DescendantIDs(id int64) ([]int64, error) { return descendantIDs(s.DB, id) }

// descendantIDs 返回部门本身及其全部下级部门（任意层级）的 ID。
func descendantIDs(q db.Q, id int64) ([]int64, error) {
	rows, err := db.All(q, `WITH RECURSIVE sub(id, depth) AS (
			SELECT id, 0 FROM departments WHERE id = ?
			UNION
			SELECT d.id, sub.depth + 1 FROM departments d JOIN sub ON d.parent_id = sub.id WHERE sub.depth < ?
		) SELECT DISTINCT id FROM sub`, id, maxDeptDepth)
	if err != nil {
		return nil, err
	}
	ids := []int64{id}
	for _, r := range rows {
		if r.Int("id") != id {
			ids = append(ids, r.Int("id"))
		}
	}
	return ids, nil
}

// LabelByID 部门完整路径。
func (s *Departments) LabelByID(id int64) (string, error) { return departmentLabel(s.DB, id) }

// departmentLabel 部门完整路径，形如 "分公司 / 集客部 / 一组"。
func departmentLabel(q db.Q, id int64) (string, error) {
	v, err := db.Value(q, "SELECT path FROM department_paths WHERE id = ?", id)
	if err != nil {
		return "", err
	}
	if v == nil {
		name, err := db.Value(q, "SELECT name FROM departments WHERE id = ?", id)
		if err != nil || name == nil {
			return "未分配部门", err
		}
		return fmt.Sprint(name), nil
	}
	return fmt.Sprint(v), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
