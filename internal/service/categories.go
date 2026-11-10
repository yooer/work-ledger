package service

import (
	"fmt"
	"strings"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
)

// Categories 工作分类与子分类（只增改、不删除）。勾选了“有子分类”的一级分类才能建子分类、填台账时才使用子分类；
// 数据库字段 is_customer_project 沿用自旧版“集客业务”，现在表示“有子分类”（查询中以 has_subcategories 别名返回）。
//
// 每个一级分类属于一个部门（子分类跟随一级分类）。某部门可用的分类 = 本部门 + 全部下级部门的分类，
// 以及升级遗留、尚未指定部门的分类（department_id 为 NULL，所有部门可用）。
type Categories struct{ *Core }

// categoryLabelSQL 分类显示名：不同部门有同名分类时追加部门名，如“日常维护（运维一班）”。需要别名 wc。
const categoryLabelSQL = `CASE WHEN wc.department_id IS NOT NULL AND EXISTS (SELECT 1 FROM work_categories wcx WHERE LOWER(wcx.name) = LOWER(wc.name) AND wcx.id <> wc.id)
	THEN wc.name || '（' || COALESCE((SELECT cdx.name FROM departments cdx WHERE cdx.id = wc.department_id), '') || '）' ELSE wc.name END`

const categorySelect = `SELECT wc.*, wc.is_customer_project AS has_subcategories, ` + categoryLabelSQL + ` AS label, dpp.path AS department_path
	FROM work_categories wc LEFT JOIN department_paths dpp ON dpp.id = wc.department_id`

func (s *Categories) Work(activeOnly bool) ([]Row, error) {
	q := categorySelect
	if activeOnly {
		q += " WHERE wc.is_active = 1"
	}
	return db.All(s.DB, q+" ORDER BY wc.sort_order, wc.id")
}

// categoryDeptScope 某部门可使用哪些部门的分类：本部门及全部下级部门。all=true 表示不限（未分配部门的人员）。
func categoryDeptScope(q db.Q, deptID int64) (ids []int64, all bool, err error) {
	if deptID <= 0 {
		return nil, true, nil
	}
	ids, err = descendantIDs(q, deptID)
	return ids, false, err
}

func categoryUsable(cat Row, ids []int64, all bool) bool {
	return all || cat.Int("department_id") == 0 || containsID(ids, cat.Int("department_id"))
}

// UsableFor 某部门可用的分类与子分类（填写台账用）。停用项、范围外的项只在为当前值（keepCat/keepProj）时保留。
func (s *Categories) UsableFor(deptID, keepCat, keepProj int64) (cats, projects []Row, err error) {
	ids, all, err := categoryDeptScope(s.DB, deptID)
	if err != nil {
		return nil, nil, err
	}
	allCats, err := s.Work(false)
	if err != nil {
		return nil, nil, err
	}
	in := map[int64]bool{}
	for _, c := range allCats {
		if c.Int("id") == keepCat || (c.Bool("is_active") && categoryUsable(c, ids, all)) {
			cats = append(cats, c)
			in[c.Int("id")] = c.Bool("is_customer_project") // 只有“有子分类”的分类提供子分类选项
		}
	}
	allP, err := s.Projects(0, false)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range allP {
		if p.Int("id") == keepProj || (p.Bool("is_active") && in[p.Int("work_category_id")]) {
			projects = append(projects, p)
		}
	}
	return relabel(cats), projects, nil
}

// FilterOptions 台账列表“工作分类/子分类”筛选项：管理员为全部；其他人为本部门可用的分类，加上可见台账中实际用到的分类。
func (s *Categories) FilterOptions(sc Scope, user Row) (cats, projects []Row, err error) {
	allCats, err := s.Work(false)
	if err != nil {
		return nil, nil, err
	}
	allP, err := s.Projects(0, false)
	if err != nil {
		return nil, nil, err
	}
	if sc.All {
		return relabel(allCats), allP, nil
	}
	ids, all, err := categoryDeptScope(s.DB, user.Int("department_id"))
	if err != nil {
		return nil, nil, err
	}
	cond, args := sc.SQL("r")
	used, err := db.All(s.DB, "SELECT DISTINCT r.work_category_id AS id FROM work_records r WHERE "+cond, args...)
	if err != nil {
		return nil, nil, err
	}
	usedIDs := map[int64]bool{}
	for _, u := range used {
		usedIDs[u.Int("id")] = true
	}
	in := map[int64]bool{}
	for _, c := range allCats {
		if usedIDs[c.Int("id")] || categoryUsable(c, ids, all) {
			cats = append(cats, c)
			in[c.Int("id")] = true
		}
	}
	for _, p := range allP {
		if in[p.Int("work_category_id")] {
			projects = append(projects, p)
		}
	}
	return relabel(cats), projects, nil
}

// relabel 选项列表内没有重名时只显示分类名；有重名时显示“名称（部门）”，未指定部门的显示“名称（未指定部门）”。
func relabel(cats []Row) []Row {
	count := map[string]int{}
	for _, c := range cats {
		count[strings.ToLower(c.Str("name"))]++
	}
	for _, c := range cats {
		switch {
		case count[strings.ToLower(c.Str("name"))] <= 1:
			c["label"] = c.Str("name")
		case c.Int("department_id") == 0:
			c["label"] = c.Str("name") + "（未指定部门）"
		}
	}
	return cats
}

func (s *Categories) Projects(categoryID int64, activeOnly bool) ([]Row, error) {
	q := "SELECT * FROM project_categories WHERE 1 = 1"
	var args []any
	if categoryID > 0 {
		q += " AND work_category_id = ?"
		args = append(args, categoryID)
	}
	if activeOnly {
		q += " AND is_active = 1"
	}
	return db.All(s.DB, q+" ORDER BY sort_order, name", args...)
}

// UnassignedCount 未指定部门的工作分类数量（升级遗留，提示管理员逐个指定）。
func (s *Categories) UnassignedCount() (int64, error) {
	return db.Int(s.DB, "SELECT COUNT(*) FROM work_categories WHERE department_id IS NULL")
}

// Overview 分类管理页：附加子分类数与台账数。dept>0 只看该部门及下级部门的分类；unassigned 只看未指定部门的分类。
func (s *Categories) Overview(dept int64, unassigned bool) ([]Row, []Row, error) {
	allCats, err := s.Work(false)
	if err != nil {
		return nil, nil, err
	}
	var deptIDs []int64
	if dept > 0 {
		if deptIDs, err = descendantIDs(s.DB, dept); err != nil {
			return nil, nil, err
		}
	}
	cats := []Row{}
	in := map[int64]bool{}
	for _, c := range allCats {
		d := c.Int("department_id")
		if (unassigned && d != 0) || (dept > 0 && !containsID(deptIDs, d)) {
			continue
		}
		cats = append(cats, c)
		in[c.Int("id")] = true
	}
	allP, err := s.Projects(0, false)
	if err != nil {
		return nil, nil, err
	}
	projects := []Row{}
	for _, p := range allP {
		if in[p.Int("work_category_id")] {
			projects = append(projects, p)
		}
	}
	countMap := func(q string) (map[int64]int64, error) {
		rows, err := db.All(s.DB, q)
		if err != nil {
			return nil, err
		}
		m := map[int64]int64{}
		for _, r := range rows {
			m[r.Int("k")] = r.Int("total")
		}
		return m, nil
	}
	pc, err := countMap("SELECT work_category_id AS k, COUNT(*) AS total FROM project_categories GROUP BY work_category_id")
	if err != nil {
		return nil, nil, err
	}
	rc, err := countMap("SELECT work_category_id AS k, COUNT(*) AS total FROM work_records GROUP BY work_category_id")
	if err != nil {
		return nil, nil, err
	}
	prc, err := countMap("SELECT project_category_id AS k, COUNT(*) AS total FROM work_records WHERE project_category_id IS NOT NULL GROUP BY project_category_id")
	if err != nil {
		return nil, nil, err
	}
	for _, c := range cats {
		c["project_count"] = pc[c.Int("id")]
		c["record_count"] = rc[c.Int("id")]
	}
	labels := map[int64]string{}
	for _, c := range cats {
		labels[c.Int("id")] = c.Str("label")
	}
	for _, p := range projects {
		p["record_count"] = prc[p.Int("id")]
		p["category_name"] = labels[p.Int("work_category_id")]
	}
	return cats, projects, nil
}

// CategoryTree 分类管理页的树：部门（按层级）→ 一级分类 → 子分类；未指定部门的分类单独一组。
type CategoryTree struct {
	Roots          []*CatDeptNode
	Unassigned     []*CatNode
	ShowUnassigned bool
}

// CatDeptNode 部门节点。Total 为本部门及全部下级部门的一级分类数。
type CatDeptNode struct {
	Dept       Row
	Categories []*CatNode
	Children   []*CatDeptNode
	Total      int
}

// CatNode 一级分类节点。Hidden 为“未勾选有子分类、但仍有子分类”（升级遗留，填台账时不显示）。
type CatNode struct {
	Cat      Row
	Projects []Row
	Hidden   bool
}

// Tree 组装分类树。dept>0 只显示该部门子树；unassigned 只显示未指定部门的分类。
// 停用的部门只在其子树下有分类时显示。
func (s *Categories) Tree(dept int64, unassigned bool) (*CategoryTree, error) {
	cats, projects, err := s.Overview(0, false)
	if err != nil {
		return nil, err
	}
	depts, err := departmentTree(s.DB)
	if err != nil {
		return nil, err
	}
	byCat := map[int64]*CatNode{}
	byDept := map[int64][]*CatNode{}
	tree := &CategoryTree{ShowUnassigned: dept == 0}
	for _, c := range cats {
		n := &CatNode{Cat: c}
		byCat[c.Int("id")] = n
		if d := c.Int("department_id"); d > 0 {
			byDept[d] = append(byDept[d], n)
		} else {
			tree.Unassigned = append(tree.Unassigned, n)
		}
	}
	for _, p := range projects {
		if n := byCat[p.Int("work_category_id")]; n != nil {
			n.Projects = append(n.Projects, p)
			n.Hidden = !n.Cat.Bool("is_customer_project")
		}
	}
	// departmentTree 按先序遍历返回并带 depth：用栈还原父子关系
	var stack []*CatDeptNode
	var all []*CatDeptNode
	for _, d := range depts {
		node := &CatDeptNode{Dept: d, Categories: byDept[d.Int("id")]}
		depth := int(d.Int("depth"))
		if depth > len(stack) {
			depth = len(stack)
		}
		stack = stack[:depth]
		if depth == 0 {
			tree.Roots = append(tree.Roots, node)
		} else {
			parent := stack[depth-1]
			parent.Children = append(parent.Children, node)
		}
		stack = append(stack, node)
		all = append(all, node)
	}
	var total func(n *CatDeptNode) int
	total = func(n *CatDeptNode) int {
		t := len(n.Categories)
		var kids []*CatDeptNode
		for _, c := range n.Children {
			if ct := total(c); ct > 0 || c.Dept.Bool("is_active") {
				kids = append(kids, c)
				t += ct
			}
		}
		n.Children = kids
		n.Total = t
		return t
	}
	var roots []*CatDeptNode
	for _, r := range tree.Roots {
		if t := total(r); t > 0 || r.Dept.Bool("is_active") {
			roots = append(roots, r)
		}
	}
	tree.Roots = roots
	switch {
	case unassigned:
		tree.Roots, tree.ShowUnassigned = nil, true
	case dept > 0:
		tree.Roots = nil
		for _, n := range all {
			if n.Dept.Int("id") == dept {
				tree.Roots = []*CatDeptNode{n}
			}
		}
	}
	return tree, nil
}

// WorkCategoryInput 工作分类表单。
type WorkCategoryInput struct {
	ID               int64
	DepartmentID     int64 // 必选
	Name             string
	SortOrder        int64
	HasSubcategories bool // 有子分类
	IsActive         bool
}

func (s *Categories) SaveWork(in WorkCategoryInput) (bool, error) {
	var existing Row
	var err error
	if in.ID > 0 {
		if existing, err = db.One(s.DB, "SELECT * FROM work_categories WHERE id = ?", in.ID); err != nil {
			return false, err
		}
		if existing == nil {
			return false, apperr.NotFound("工作分类不存在。")
		}
	}
	fields := map[string]string{}
	dept, err := db.One(s.DB, "SELECT id, is_active FROM departments WHERE id = ?", in.DepartmentID)
	if err != nil {
		return false, err
	}
	switch {
	case dept == nil:
		fields["department_id"] = "请选择所属部门。"
	case !dept.Bool("is_active") && (existing == nil || existing.Int("department_id") != in.DepartmentID):
		fields["department_id"] = "所选部门已停用，请重新选择。"
	}
	if dup, err := db.Value(s.DB, "SELECT id FROM work_categories WHERE LOWER(name) = LOWER(?) AND COALESCE(department_id, 0) = ? AND id <> ?", in.Name, in.DepartmentID, in.ID); err != nil {
		return false, err
	} else if dup != nil {
		fields["name"] = "该部门已有同名工作分类。"
	}
	// 已勾选“有子分类”且仍有启用的子分类时，不允许取消勾选（需先停用全部子分类）
	if existing != nil && existing.Bool("is_customer_project") && !in.HasSubcategories {
		n, err := activeSubCount(s.DB, in.ID)
		if err != nil {
			return false, err
		}
		if n > 0 {
			fields["has_subcategories"] = fmt.Sprintf("该分类下还有 %d 个启用的子分类，请先停用全部子分类再取消“有子分类”。", n)
		}
	}
	if len(fields) > 0 {
		return false, apperr.Validation(fields)
	}
	ts := now()
	if existing == nil {
		_, err = db.Insert(s.DB, `INSERT INTO work_categories (name, department_id, is_customer_project, is_active, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, in.Name, in.DepartmentID, boolInt(in.HasSubcategories), boolInt(in.IsActive), in.SortOrder, ts, ts)
		return true, err
	}
	_, err = db.Exec(s.DB, "UPDATE work_categories SET name = ?, department_id = ?, is_customer_project = ?, is_active = ?, sort_order = ?, updated_at = ? WHERE id = ?",
		in.Name, in.DepartmentID, boolInt(in.HasSubcategories), boolInt(in.IsActive), in.SortOrder, ts, in.ID)
	return false, err
}

// ProjectInput 子分类表单。
type ProjectInput struct {
	ID             int64
	WorkCategoryID int64
	Name           string
	SortOrder      int64
	IsActive       bool
}

func (s *Categories) SaveProject(in ProjectInput) (bool, error) {
	var existing Row
	var err error
	if in.ID > 0 {
		if existing, err = db.One(s.DB, "SELECT * FROM project_categories WHERE id = ?", in.ID); err != nil {
			return false, err
		}
		if existing == nil {
			return false, apperr.NotFound("子分类不存在。")
		}
		if in.WorkCategoryID != existing.Int("work_category_id") {
			return false, apperr.New("已有子分类不能更换所属的一级工作分类，以免历史台账分类错位。")
		}
	}
	cat, err := db.One(s.DB, "SELECT * FROM work_categories WHERE id = ?", in.WorkCategoryID)
	if err != nil {
		return false, err
	}
	fields := map[string]string{}
	if cat == nil {
		fields["work_category_id"] = "请选择所属的一级工作分类。"
	} else if existing == nil && !cat.Bool("is_customer_project") {
		fields["work_category_id"] = "该工作分类未勾选“有子分类”，不能新建子分类。"
	} else if dup, err := db.Value(s.DB, "SELECT id FROM project_categories WHERE work_category_id = ? AND LOWER(name) = LOWER(?) AND id <> ?", in.WorkCategoryID, in.Name, in.ID); err != nil {
		return false, err
	} else if dup != nil {
		fields["name"] = "该工作分类下已存在同名子分类。"
	}
	if len(fields) > 0 {
		return false, apperr.Validation(fields)
	}
	ts := now()
	if existing == nil {
		_, err = db.Insert(s.DB, `INSERT INTO project_categories (work_category_id, name, is_active, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, in.WorkCategoryID, in.Name, boolInt(in.IsActive), in.SortOrder, ts, ts)
		return true, err
	}
	_, err = db.Exec(s.DB, "UPDATE project_categories SET name = ?, is_active = ?, sort_order = ?, updated_at = ? WHERE id = ?",
		in.Name, boolInt(in.IsActive), in.SortOrder, ts, in.ID)
	return false, err
}
