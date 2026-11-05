package service

import (
	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
)

// Categories 工作分类与集客项目子分类（只增改、不删除）。
type Categories struct{ *Core }

func (s *Categories) Work(activeOnly bool) ([]Row, error) {
	q := "SELECT * FROM work_categories"
	if activeOnly {
		q += " WHERE is_active = 1"
	}
	return db.All(s.DB, q+" ORDER BY sort_order, id")
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

// Overview 分类管理页：附加子分类数与台账数。
func (s *Categories) Overview() ([]Row, []Row, error) {
	cats, err := s.Work(false)
	if err != nil {
		return nil, nil, err
	}
	projects, err := s.Projects(0, false)
	if err != nil {
		return nil, nil, err
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
	for _, p := range projects {
		p["record_count"] = prc[p.Int("id")]
	}
	return cats, projects, nil
}

// WorkCategoryInput 工作分类表单。
type WorkCategoryInput struct {
	ID         int64
	Name       string
	SortOrder  int64
	IsCustomer bool
	IsActive   bool
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
	if dup, err := db.Value(s.DB, "SELECT id FROM work_categories WHERE name = ? AND id <> ?", in.Name, in.ID); err != nil {
		return false, err
	} else if dup != nil {
		fields["name"] = "工作分类名称已存在。"
	}
	if existing != nil && existing.Bool("is_customer_project") && !in.IsCustomer {
		n, err := db.Int(s.DB, "SELECT COUNT(*) FROM project_categories WHERE work_category_id = ?", in.ID)
		if err != nil {
			return false, err
		}
		if n > 0 {
			fields["is_customer_project"] = "该分类下已有集客项目子分类，不能取消“集客业务”属性（子分类不可删除，以免破坏历史数据）。"
		}
	}
	if len(fields) > 0 {
		return false, apperr.Validation(fields)
	}
	ts := now()
	if existing == nil {
		_, err = db.Insert(s.DB, `INSERT INTO work_categories (name, is_customer_project, is_active, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, in.Name, boolInt(in.IsCustomer), boolInt(in.IsActive), in.SortOrder, ts, ts)
		return true, err
	}
	_, err = db.Exec(s.DB, "UPDATE work_categories SET name = ?, is_customer_project = ?, is_active = ?, sort_order = ?, updated_at = ? WHERE id = ?",
		in.Name, boolInt(in.IsCustomer), boolInt(in.IsActive), in.SortOrder, ts, in.ID)
	return false, err
}

// ProjectInput 集客项目子分类表单。
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
			return false, apperr.NotFound("集客项目子分类不存在。")
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
	if cat == nil || !cat.Bool("is_customer_project") {
		fields["work_category_id"] = "集客项目子分类必须归属于“集客业务”类型的分类。"
	} else if dup, err := db.Value(s.DB, "SELECT id FROM project_categories WHERE work_category_id = ? AND name = ? AND id <> ?", in.WorkCategoryID, in.Name, in.ID); err != nil {
		return false, err
	} else if dup != nil {
		fields["name"] = "该集客分类下已存在同名子分类。"
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
