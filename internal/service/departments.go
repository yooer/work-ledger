package service

import (
	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
)

// Departments 两级部门管理（不提供删除）。
type Departments struct{ *Core }

// Tree 返回按“一级部门 → 其子部门”排序的列表，附加 depth 与 label。
func (s *Departments) Tree() ([]Row, error) {
	return departmentTree(s.DB)
}

func departmentTree(q db.Q) ([]Row, error) {
	rows, err := db.All(q, "SELECT * FROM departments ORDER BY sort_order, name")
	if err != nil {
		return nil, err
	}
	var tops []Row
	children := map[int64][]Row{}
	for _, r := range rows {
		if r.Null("parent_id") {
			tops = append(tops, r)
		} else {
			children[r.Int("parent_id")] = append(children[r.Int("parent_id")], r)
		}
	}
	out := []Row{}
	for _, t := range tops {
		t["depth"] = int64(0)
		t["label"] = t.Str("name")
		out = append(out, t)
		for _, c := range children[t.Int("id")] {
			c["depth"] = int64(1)
			c["label"] = t.Str("name") + " / " + c.Str("name")
			out = append(out, c)
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
			childCount[d.Int("parent_id")]++
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

// ParentOptions 可作为上级的一级部门（排除自己与已停用的，当前上级保留）。
func (s *Departments) ParentOptions(selfID, currentParent int64) ([]Row, error) {
	all, err := s.Tree()
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, r := range all {
		if r.Int("depth") != 0 || (selfID > 0 && r.Int("id") == selfID) {
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
		case !parent.Null("parent_id"):
			fields["parent_id"] = "部门最多两级，只能选择一级部门作为上级。"
		case !parent.Bool("is_active") && in.ParentID != currentParent:
			fields["parent_id"] = "所选上级部门已停用。"
		}
	}
	if id > 0 && in.ParentID == id {
		fields["parent_id"] = "部门不能把自己的部门设为上级。"
	}
	if id > 0 && in.ParentID > 0 {
		n, err := db.Int(s.DB, "SELECT COUNT(*) FROM departments WHERE parent_id = ?", id)
		if err != nil {
			return err
		}
		if n > 0 {
			fields["parent_id"] = "该部门下还有二级子部门，不能再设置为其他部门的子部门。"
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

func descendantIDs(q db.Q, id int64) ([]int64, error) {
	ids := []int64{id}
	rows, err := db.All(q, "SELECT id FROM departments WHERE parent_id = ?", id)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		ids = append(ids, r.Int("id"))
	}
	return ids, nil
}

// LabelByID 形如 "集客部 / 集客一组"。
func (s *Departments) LabelByID(id int64) (string, error) { return departmentLabel(s.DB, id) }

func departmentLabel(q db.Q, id int64) (string, error) {
	r, err := db.One(q, `SELECT d.name, pd.name AS parent_name FROM departments d
		LEFT JOIN departments pd ON pd.id = d.parent_id WHERE d.id = ?`, id)
	if err != nil {
		return "", err
	}
	if r == nil {
		return "未分配部门", nil
	}
	if !r.Null("parent_name") {
		return r.Str("parent_name") + " / " + r.Str("name"), nil
	}
	return r.Str("name"), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
