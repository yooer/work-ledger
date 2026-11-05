package service

import (
	"strings"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

// Records 台账查询与写入（不提供物理删除）。
type Records struct {
	*Core
	Settings *Settings
}

// RecordFilter 列表/导出筛选条件。
type RecordFilter struct {
	From         string
	To           string
	CategoryID   int64
	ProjectID    int64
	CreatorID    int64 // “我的台账”固定为本人
	EmployeeID   int64 // 管理员/经理按记录人筛选
	DepartmentID int64 // 按部门快照（含子部门）筛选
	Q            string
}

const recordColumns = `SELECT r.*, wc.name AS category_name, wc.is_customer_project,
	pc.name AS project_name, u.name AS creator_name, u.username AS creator_username,
	u.employee_no AS creator_employee_no, eu.name AS editor_name,
	d.name AS department_name, pd.name AS parent_department_name`

const recordJoins = ` FROM work_records r
	JOIN work_categories wc ON wc.id = r.work_category_id
	LEFT JOIN project_categories pc ON pc.id = r.project_category_id
	JOIN users u ON u.id = r.created_by
	JOIN users eu ON eu.id = r.updated_by
	LEFT JOIN departments d ON d.id = r.department_id
	LEFT JOIN departments pd ON pd.id = d.parent_id`

func (s *Records) where(sc Scope, f RecordFilter) (string, []any, error) {
	scopeSQL, args := sc.SQL("r")
	conds := []string{scopeSQL}
	if f.From != "" {
		conds = append(conds, "r.work_at >= ?")
		args = append(args, f.From+" 00:00:00")
	}
	if f.To != "" {
		conds = append(conds, "r.work_at < ?")
		args = append(args, timeutil.AddDays(f.To, 1)+" 00:00:00")
	}
	if f.CategoryID > 0 {
		conds = append(conds, "r.work_category_id = ?")
		args = append(args, f.CategoryID)
	}
	if f.ProjectID > 0 {
		conds = append(conds, "r.project_category_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.CreatorID > 0 {
		conds = append(conds, "r.created_by = ?")
		args = append(args, f.CreatorID)
	}
	if f.EmployeeID > 0 {
		conds = append(conds, "r.created_by = ?")
		args = append(args, f.EmployeeID)
	}
	if f.DepartmentID > 0 {
		ids, err := descendantIDs(s.DB, f.DepartmentID)
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, "r.department_id IN ("+db.Placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if f.Q != "" {
		like := "%" + f.Q + "%"
		conds = append(conds, `(r.content LIKE ? OR u.name LIKE ? OR COALESCE(u.employee_no, '') LIKE ?
			OR wc.name LIKE ? OR COALESCE(pc.name, '') LIKE ? OR EXISTS (
				SELECT 1 FROM work_record_participants sp JOIN users su ON su.id = sp.user_id
				WHERE sp.work_record_id = r.id AND su.name LIKE ?))`)
		for i := 0; i < 6; i++ {
			args = append(args, like)
		}
	}
	return strings.Join(conds, " AND "), args, nil
}

// ListResult 分页结果。
type ListResult struct {
	Items   []Row
	Total   int
	Page    int
	PerPage int
}

// List 分页查询。
func (s *Records) List(sc Scope, f RecordFilter, page, perPage int) (ListResult, error) {
	where, args, err := s.where(sc, f)
	if err != nil {
		return ListResult{}, err
	}
	perPage = max(1, min(100, perPage))
	total, err := db.Int(s.DB, "SELECT COUNT(*)"+recordJoins+" WHERE "+where, args...)
	if err != nil {
		return ListResult{}, err
	}
	page, offset := clampPage(page, int(total), perPage)
	items, err := db.All(s.DB, recordColumns+recordJoins+" WHERE "+where+
		" ORDER BY r.work_at DESC, r.id DESC LIMIT ? OFFSET ?", append(args, perPage, offset)...)
	if err != nil {
		return ListResult{}, err
	}
	if err := s.attachParticipants(items); err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: items, Total: int(total), Page: page, PerPage: perPage}, nil
}

// ExportRows 导出用（按时间正序，带上限）。返回行与是否截断。
func (s *Records) ExportRows(sc Scope, f RecordFilter, maxRows int) ([]Row, bool, error) {
	where, args, err := s.where(sc, f)
	if err != nil {
		return nil, false, err
	}
	limit := max(1, min(100000, maxRows))
	rows, err := db.All(s.DB, recordColumns+recordJoins+" WHERE "+where+" ORDER BY r.work_at ASC, r.id ASC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	return rows, truncated, s.attachParticipants(rows)
}

// Find 查看单条记录（受可见范围限制），不存在或无权查看返回 404。
func (s *Records) Find(sc Scope, id int64) (Row, error) {
	scopeSQL, args := sc.SQL("r")
	row, err := db.One(s.DB, recordColumns+recordJoins+" WHERE r.id = ? AND "+scopeSQL+" LIMIT 1", append([]any{id}, args...)...)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apperr.NotFound("该工作记录不存在，或你无权查看。")
	}
	parts, err := db.All(s.DB, `SELECT u.id, u.name, u.username, u.employee_no FROM work_record_participants p
		JOIN users u ON u.id = p.user_id WHERE p.work_record_id = ? ORDER BY u.name`, id)
	if err != nil {
		return nil, err
	}
	row["participants"] = parts
	return row, nil
}

func (s *Records) attachParticipants(records []Row) error {
	if len(records) == 0 {
		return nil
	}
	// SQLite 变量上限较高，但仍分批以防超大导出。
	byID := map[int64][]Row{}
	for start := 0; start < len(records); start += 500 {
		end := min(start+500, len(records))
		args := make([]any, 0, end-start)
		for _, r := range records[start:end] {
			args = append(args, r.Int("id"))
		}
		rows, err := db.All(s.DB, `SELECT p.work_record_id, u.id, u.name, u.username, u.employee_no
			FROM work_record_participants p JOIN users u ON u.id = p.user_id
			WHERE p.work_record_id IN (`+db.Placeholders(len(args))+`) ORDER BY u.name`, args...)
		if err != nil {
			return err
		}
		for _, r := range rows {
			rid := r.Int("work_record_id")
			delete(r, "work_record_id")
			byID[rid] = append(byID[rid], r)
		}
	}
	for _, r := range records {
		list := byID[r.Int("id")]
		if list == nil {
			list = []Row{}
		}
		r["participants"] = list
		names := make([]string, 0, len(list))
		for _, p := range list {
			names = append(names, p.Str("name"))
		}
		r["participants_text"] = strings.Join(names, "、")
	}
	return nil
}

// RecordInput 台账表单（格式已校验）。
type RecordInput struct {
	WorkCategoryID    int64
	ProjectCategoryID int64
	Content           string
	WorkAt            string // YYYY-MM-DD HH:MM:00
	Difficulty        int64
	WorkCount         int64
	ParticipantIDs    []int64
}

// check 业务校验：分类/项目是否可用、参与人员是否存在且启用。existing 为编辑前的记录。
func (s *Records) check(in *RecordInput, existing Row) error {
	fields := map[string]string{}
	cat, err := db.One(s.DB, "SELECT * FROM work_categories WHERE id = ?", in.WorkCategoryID)
	if err != nil {
		return err
	}
	curCat, curProj := existing.Int("work_category_id"), existing.Int("project_category_id")
	if cat == nil {
		fields["work_category_id"] = "请选择工作分类。"
	} else if !cat.Bool("is_active") && (existing == nil || in.WorkCategoryID != curCat) {
		fields["work_category_id"] = "所选工作分类已停用，请重新选择。"
	}
	if cat != nil && cat.Bool("is_customer_project") {
		var proj Row
		if in.ProjectCategoryID > 0 {
			if proj, err = db.One(s.DB, "SELECT * FROM project_categories WHERE id = ?", in.ProjectCategoryID); err != nil {
				return err
			}
		}
		if proj == nil || proj.Int("work_category_id") != in.WorkCategoryID {
			fields["project_category_id"] = "选择集客业务时，请选择对应的集客项目子分类。"
		} else if !proj.Bool("is_active") && (existing == nil || in.ProjectCategoryID != curProj) {
			fields["project_category_id"] = "所选集客项目子分类已停用，请重新选择。"
		}
	} else {
		in.ProjectCategoryID = 0
	}
	if len(in.ParticipantIDs) == 0 {
		fields["participant_ids"] = "请至少选择一名参与人员。"
	} else {
		args := make([]any, len(in.ParticipantIDs))
		for i, id := range in.ParticipantIDs {
			args[i] = id
		}
		rows, err := db.All(s.DB, "SELECT id, is_active FROM users WHERE id IN ("+db.Placeholders(len(args))+")", args...)
		if err != nil {
			return err
		}
		active := map[int64]bool{}
		for _, r := range rows {
			active[r.Int("id")] = r.Bool("is_active")
		}
		existingIDs := map[int64]bool{}
		if existing != nil {
			if parts, ok := existing["participants"].([]Row); ok {
				for _, p := range parts {
					existingIDs[p.Int("id")] = true
				}
			}
		}
		for _, id := range in.ParticipantIDs {
			isActive, found := active[id]
			if !found {
				fields["participant_ids"] = "参与人员中包含不存在的员工。"
				break
			}
			if !isActive && !existingIDs[id] {
				fields["participant_ids"] = "参与人员中包含已停用的员工。"
				break
			}
		}
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return nil
}

// Create 新建台账；记录创建者、部门快照、创建/编辑审计字段自动写入。
func (s *Records) Create(user Row, in RecordInput) (int64, error) {
	if err := s.check(&in, nil); err != nil {
		return 0, err
	}
	var id int64
	err := s.DB.Tx(func(tx db.Q) error {
		ts := now()
		var err error
		id, err = db.Insert(tx, `INSERT INTO work_records (work_category_id, project_category_id, content, work_at, difficulty, work_count,
			department_id, created_by, created_at, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.WorkCategoryID, nullInt(in.ProjectCategoryID), in.Content, in.WorkAt, in.Difficulty, in.WorkCount,
			nullInt(user.Int("department_id")), user.Int("id"), ts, user.Int("id"), ts)
		if err != nil {
			return err
		}
		return replaceParticipants(tx, id, in.ParticipantIDs)
	})
	return id, err
}

// Update 编辑台账：只有管理员或创建者可以编辑；部门快照保持不变。
func (s *Records) Update(sc Scope, user Row, id int64, in RecordInput) error {
	existing, err := s.Find(sc, id)
	if err != nil {
		return err
	}
	if !CanEditRecord(existing, user) {
		return apperr.Forbidden("你只能修改自己创建的工作记录。")
	}
	if err := s.check(&in, existing); err != nil {
		return err
	}
	return s.DB.Tx(func(tx db.Q) error {
		if _, err := db.Exec(tx, `UPDATE work_records SET work_category_id = ?, project_category_id = ?, content = ?, work_at = ?,
			difficulty = ?, work_count = ?, updated_by = ?, updated_at = ? WHERE id = ?`,
			in.WorkCategoryID, nullInt(in.ProjectCategoryID), in.Content, in.WorkAt, in.Difficulty, in.WorkCount, user.Int("id"), now(), id); err != nil {
			return err
		}
		return replaceParticipants(tx, id, in.ParticipantIDs)
	})
}

func replaceParticipants(tx db.Q, recordID int64, ids []int64) error {
	if _, err := db.Exec(tx, "DELETE FROM work_record_participants WHERE work_record_id = ?", recordID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, uid := range ids {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		if _, err := db.Exec(tx, "INSERT INTO work_record_participants (work_record_id, user_id) VALUES (?, ?)", recordID, uid); err != nil {
			return err
		}
	}
	return nil
}

// FormOptions 台账表单所需的分类、项目、人员选项（停用项仅在编辑且为当前值时保留）。
func (s *Records) FormOptions(curCategory, curProject int64, selected []int64) (cats, projects, people []Row, err error) {
	all, err := db.All(s.DB, "SELECT * FROM work_categories ORDER BY sort_order, id")
	if err != nil {
		return
	}
	for _, c := range all {
		if c.Bool("is_active") || c.Int("id") == curCategory {
			cats = append(cats, c)
		}
	}
	allP, err := db.All(s.DB, "SELECT * FROM project_categories ORDER BY sort_order, name")
	if err != nil {
		return
	}
	for _, p := range allP {
		if p.Bool("is_active") || p.Int("id") == curProject {
			projects = append(projects, p)
		}
	}
	people, err = (&Users{Core: s.Core}).ParticipantOptions(selected)
	return
}
