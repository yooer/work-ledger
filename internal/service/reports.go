package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

// ---------------------------------------------------------------------------
// 报告数据结构：JSON 字段顺序与 PHP 版一致（/api/v1 输出与提示词中的汇总统计都依赖它）。
// ---------------------------------------------------------------------------

type ReportScope struct {
	DepartmentID          *int64  `json:"department_id"`
	DepartmentName        *string `json:"department_name"`
	IncludeSubDepartments bool    `json:"include_sub_departments"`
	EmployeeID            *int64  `json:"employee_id"`
	EmployeeName          *string `json:"employee_name"`
	EmployeeNo            *string `json:"employee_no"`
	Label                 string  `json:"label"`
}

type CategoryStat struct {
	Category          string `json:"category"`
	IsCustomerProject bool   `json:"is_customer_project"`
	RecordCount       int    `json:"record_count"`
	WorkCount         int    `json:"work_count"`
	DifficultyPoints  int    `json:"difficulty_points"`
}

type ProjectStat struct {
	Project     string `json:"project"`
	RecordCount int    `json:"record_count"`
	WorkCount   int    `json:"work_count"`
}

type PersonStat struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	EmployeeNo  *string `json:"employee_no"`
	Department  string  `json:"department"`
	RecordCount int     `json:"record_count"`
	WorkCount   int     `json:"work_count"`
}

type BucketStat struct {
	Date        string `json:"date,omitempty"`
	Month       string `json:"month,omitempty"`
	Quarter     string `json:"quarter,omitempty"`
	Label       string `json:"label,omitempty"`
	ShortLabel  string `json:"short_label,omitempty"`
	RecordCount int    `json:"record_count"`
	WorkCount   int    `json:"work_count"`
}

type DifficultyStat struct {
	Level       int    `json:"level"`
	Label       string `json:"label"`
	RecordCount int    `json:"record_count"`
}

type ReportSummary struct {
	RecordCount              int              `json:"record_count"`
	TotalRecordCount         int              `json:"total_record_count"`
	ReturnedRecordCount      int              `json:"returned_record_count"`
	Truncated                bool             `json:"truncated"`
	TotalWorkCount           int              `json:"total_work_count"`
	InvolvedEmployeeCount    int              `json:"involved_employee_count"`
	ActiveDays               *int             `json:"active_days,omitempty"`
	ActiveMonths             *int             `json:"active_months,omitempty"`
	AvgDifficulty            float64          `json:"avg_difficulty"`
	DifficultyPoints         int              `json:"difficulty_points"`
	HighDifficultyRecords    int              `json:"high_difficulty_records"`
	CustomerProjectRecords   int              `json:"customer_project_records"`
	CustomerProjectWorkCount int              `json:"customer_project_work_count"`
	ByDay                    *[]BucketStat    `json:"by_day,omitempty"`
	ByMonth                  *[]BucketStat    `json:"by_month,omitempty"`
	ByQuarter                *[]BucketStat    `json:"by_quarter,omitempty"`
	ByCategory               []CategoryStat   `json:"by_category"`
	ByProject                []ProjectStat    `json:"by_project"`
	ByEmployee               []PersonStat     `json:"by_employee"`
	ByDifficulty             []DifficultyStat `json:"by_difficulty"`
}

type RecordPerson struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Username   string  `json:"username"`
	EmployeeNo *string `json:"employee_no"`
	Department string  `json:"department"`
}

type ReportCategory struct {
	Name              string `json:"name"`
	IsCustomerProject bool   `json:"is_customer_project"`
}

type ReportRecord struct {
	ID              int64          `json:"id"`
	WorkAt          string         `json:"work_at"`
	Category        ReportCategory `json:"category"`
	ProjectCategory *string        `json:"project_category"`
	Content         string         `json:"content"`
	Difficulty      int            `json:"difficulty"`
	WorkCount       int            `json:"work_count"`
	Creator         RecordPerson   `json:"creator"`
	Participants    []RecordPerson `json:"participants"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	LastEditor      string         `json:"last_editor"`
}

type ReportPeriod struct {
	Start   string `json:"start"`
	End     string `json:"end"`
	Days    int    `json:"days,omitempty"`
	Label   string `json:"label"`
	Partial *bool  `json:"partial,omitempty"`
}

// ReportData 是周报/年度/时段的统一数据（按类型输出不同的顶层字段）。
type ReportData struct {
	WeekStart   string         `json:"week_start,omitempty"`
	WeekEnd     string         `json:"week_end,omitempty"`
	Year        int            `json:"year,omitempty"`
	StartDate   string         `json:"start_date,omitempty"`
	EndDate     string         `json:"end_date,omitempty"`
	Period      *ReportPeriod  `json:"period,omitempty"`
	Scope       ReportScope    `json:"scope"`
	Summary     ReportSummary  `json:"summary"`
	Records     []ReportRecord `json:"records"`
	GeneratedAt string         `json:"generated_at"`
}

// ReportKind 报告类型。
type ReportKind int

const (
	KindWeekly ReportKind = iota
	KindAnnual
	KindPeriod
)

// collectSpec 是一次统计的参数。
type collectSpec struct {
	Kind            ReportKind
	Start, End      string // 含首尾的日期
	DepartmentID    int64
	EmployeeID      int64
	IncludeChildren bool
	MaxRecords      int
}

func deptLabel(name, parent any) string {
	if name == nil {
		return "未分配部门"
	}
	if parent != nil {
		return fmt.Sprint(parent) + " / " + fmt.Sprint(name)
	}
	return fmt.Sprint(name)
}

func strPtr(v any) *string {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	return &s
}

// collect 统计指定时间与范围内的台账（与 PHP 版 collect 逻辑一致）。
func collect(c *Core, sp collectSpec) (*ReportData, error) {
	if sp.MaxRecords <= 0 {
		sp.MaxRecords = c.Cfg.Limits.MaxRecordsForAPI
	}
	conds := []string{"r.work_at >= ?", "r.work_at < ?"}
	args := []any{sp.Start + " 00:00:00", timeutil.AddDays(sp.End, 1) + " 00:00:00"}
	scope := ReportScope{IncludeSubDepartments: sp.IncludeChildren}
	var parts []string

	if sp.DepartmentID > 0 {
		d, err := db.One(c.DB, "SELECT * FROM departments WHERE id = ?", sp.DepartmentID)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, apperr.New("指定的部门不存在。")
		}
		label, err := departmentLabel(c.DB, sp.DepartmentID)
		if err != nil {
			return nil, err
		}
		ids := []int64{sp.DepartmentID}
		if sp.IncludeChildren {
			if ids, err = descendantIDs(c.DB, sp.DepartmentID); err != nil {
				return nil, err
			}
		}
		conds = append(conds, "r.department_id IN ("+db.Placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
		id := sp.DepartmentID
		scope.DepartmentID, scope.DepartmentName = &id, &label
		suffix := ""
		if sp.IncludeChildren {
			suffix = "（含下级部门）"
		}
		parts = append(parts, label+suffix)
	}
	if sp.EmployeeID > 0 {
		e, err := db.One(c.DB, "SELECT id, name, employee_no FROM users WHERE id = ?", sp.EmployeeID)
		if err != nil {
			return nil, err
		}
		if e == nil {
			return nil, apperr.New("指定的员工不存在。")
		}
		conds = append(conds, "r.created_by = ?")
		args = append(args, sp.EmployeeID)
		id := sp.EmployeeID
		name := e.Str("name")
		scope.EmployeeID, scope.EmployeeName, scope.EmployeeNo = &id, &name, strPtr(e["employee_no"])
		if no := e.Str("employee_no"); no != "" {
			parts = append(parts, name+"（"+no+"）")
		} else {
			parts = append(parts, name)
		}
	}
	scope.Label = "全公司"
	if len(parts) > 0 {
		scope.Label = strings.Join(parts, " · ")
	}

	where := strings.Join(conds, " AND ")
	total, err := db.Int(c.DB, "SELECT COUNT(*) FROM work_records r WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	rows, err := db.All(c.DB, `SELECT r.id, r.content, r.work_at, r.difficulty, r.work_count, r.created_at, r.updated_at,
		wc.name AS category_name, wc.is_customer_project, pc.name AS project_name,
		u.id AS creator_id, u.name AS creator_name, u.username AS creator_username, u.employee_no AS creator_employee_no,
		eu.name AS editor_name, d.name AS department_name, pd.name AS parent_department_name
		FROM work_records r
		JOIN work_categories wc ON wc.id = r.work_category_id
		LEFT JOIN project_categories pc ON pc.id = r.project_category_id
		JOIN users u ON u.id = r.created_by
		JOIN users eu ON eu.id = r.updated_by
		LEFT JOIN departments d ON d.id = r.department_id
		LEFT JOIN departments pd ON pd.id = d.parent_id
		WHERE `+where+` ORDER BY r.work_at ASC, r.id ASC LIMIT ?`, append(args, sp.MaxRecords)...)
	if err != nil {
		return nil, err
	}
	partMap, err := participantMap(c.DB, rows)
	if err != nil {
		return nil, err
	}

	data := &ReportData{Scope: scope, GeneratedAt: timeutil.Now().Format(time.RFC3339), Records: []ReportRecord{}}
	sum := &data.Summary
	days := map[string]*BucketStat{}
	months := map[string]*BucketStat{}
	quarters := map[string]*BucketStat{}
	categories := map[string]*CategoryStat{}
	projects := map[string]*ProjectStat{}
	people := map[int64]*PersonStat{}
	var catOrder, projOrder []string
	var peopleOrder []int64
	diff := [6]int{}
	diffSum := 0

	// 预置桶：周报按日全量列出；年度 12 个月 + 4 个季度。
	switch sp.Kind {
	case KindWeekly:
		for d := sp.Start; d <= sp.End; d = timeutil.AddDays(d, 1) {
			days[d] = &BucketStat{Date: d}
		}
	case KindAnnual:
		for m := 1; m <= 12; m++ {
			key := fmt.Sprintf("%02d", m)
			months[key] = &BucketStat{Month: key, Label: strconv.Itoa(m) + "月"}
		}
		for q := 1; q <= 4; q++ {
			quarters[strconv.Itoa(q)] = &BucketStat{Quarter: "Q" + strconv.Itoa(q), Label: "第" + strconv.Itoa(q) + "季度"}
		}
	}

	for _, r := range rows {
		wc := int(r.Int("work_count"))
		difficulty := int(r.Int("difficulty"))
		creator := RecordPerson{ID: r.Int("creator_id"), Name: r.Str("creator_name"), Username: r.Str("creator_username"),
			EmployeeNo: strPtr(r["creator_employee_no"]), Department: deptLabel(r["department_name"], r["parent_department_name"])}
		parts := partMap[r.Int("id")]
		if parts == nil {
			parts = []RecordPerson{}
		}
		rec := ReportRecord{ID: r.Int("id"), WorkAt: r.Str("work_at"),
			Category:        ReportCategory{Name: r.Str("category_name"), IsCustomerProject: r.Bool("is_customer_project")},
			ProjectCategory: strPtr(r["project_name"]), Content: r.Str("content"), Difficulty: difficulty, WorkCount: wc,
			Creator: creator, Participants: parts, CreatedAt: r.Str("created_at"), UpdatedAt: r.Str("updated_at"), LastEditor: r.Str("editor_name")}
		data.Records = append(data.Records, rec)

		dayKey := rec.WorkAt
		if len(dayKey) >= 10 {
			dayKey = dayKey[:10]
		}
		monthNum, _ := strconv.Atoi(safeSub(dayKey, 5, 7))
		quarterNum := (monthNum + 2) / 3
		switch sp.Kind {
		case KindWeekly:
			b := days[dayKey]
			if b == nil {
				b = &BucketStat{Date: dayKey}
				days[dayKey] = b
			}
			b.RecordCount++
			b.WorkCount += wc
		case KindAnnual:
			if b := months[fmt.Sprintf("%02d", monthNum)]; b != nil {
				b.RecordCount++
				b.WorkCount += wc
			}
			if b := quarters[strconv.Itoa(quarterNum)]; b != nil {
				b.RecordCount++
				b.WorkCount += wc
			}
		case KindPeriod:
			b := days[dayKey]
			if b == nil {
				b = &BucketStat{Date: dayKey, Label: dayKey, ShortLabel: safeSub(dayKey, 5, 10)}
				days[dayKey] = b
			}
			b.RecordCount++
			b.WorkCount += wc
			mk := safeSub(dayKey, 0, 7)
			m := months[mk]
			if m == nil {
				m = &BucketStat{Month: mk, Label: mk, ShortLabel: strconv.Itoa(monthNum) + "月"}
				months[mk] = m
			}
			m.RecordCount++
			m.WorkCount += wc
			qk := safeSub(dayKey, 0, 4) + "-Q" + strconv.Itoa(quarterNum)
			q := quarters[qk]
			if q == nil {
				q = &BucketStat{Quarter: qk, Label: safeSub(dayKey, 0, 4) + " 年 Q" + strconv.Itoa(quarterNum)}
				quarters[qk] = q
			}
			q.RecordCount++
			q.WorkCount += wc
		}

		cs := categories[rec.Category.Name]
		if cs == nil {
			cs = &CategoryStat{Category: rec.Category.Name, IsCustomerProject: rec.Category.IsCustomerProject}
			categories[rec.Category.Name] = cs
			catOrder = append(catOrder, rec.Category.Name)
		}
		cs.RecordCount++
		cs.WorkCount += wc
		cs.DifficultyPoints += difficulty * wc
		if rec.ProjectCategory != nil {
			ps := projects[*rec.ProjectCategory]
			if ps == nil {
				ps = &ProjectStat{Project: *rec.ProjectCategory}
				projects[*rec.ProjectCategory] = ps
				projOrder = append(projOrder, *rec.ProjectCategory)
			}
			ps.RecordCount++
			ps.WorkCount += wc
		}
		if rec.Category.IsCustomerProject {
			sum.CustomerProjectRecords++
			sum.CustomerProjectWorkCount += wc
		}
		if difficulty >= 1 && difficulty <= 5 {
			diff[difficulty]++
		}
		if difficulty >= 4 {
			sum.HighDifficultyRecords++
		}
		sum.TotalWorkCount += wc
		sum.DifficultyPoints += difficulty * wc
		diffSum += difficulty

		involved := []RecordPerson{creator}
		seen := map[int64]bool{creator.ID: true}
		for _, p := range parts {
			if !seen[p.ID] {
				seen[p.ID] = true
				involved = append(involved, p)
			} else if p.ID == creator.ID {
				involved[0] = RecordPerson{ID: p.ID, Name: p.Name, Username: p.Username, EmployeeNo: p.EmployeeNo, Department: p.Department}
			}
		}
		for _, p := range involved {
			ps := people[p.ID]
			if ps == nil {
				ps = &PersonStat{ID: p.ID, Name: p.Name, EmployeeNo: p.EmployeeNo, Department: p.Department}
				people[p.ID] = ps
				peopleOrder = append(peopleOrder, p.ID)
			}
			ps.RecordCount++
			ps.WorkCount += wc
		}
	}

	n := len(data.Records)
	sum.RecordCount, sum.ReturnedRecordCount, sum.TotalRecordCount = n, n, int(total)
	sum.Truncated = int(total) > n
	sum.InvolvedEmployeeCount = len(people)
	if n > 0 {
		sum.AvgDifficulty = float64(int(float64(diffSum)/float64(n)*10+0.5)) / 10
	}
	for _, name := range catOrder {
		sum.ByCategory = append(sum.ByCategory, *categories[name])
	}
	sort.SliceStable(sum.ByCategory, func(i, j int) bool { return sum.ByCategory[i].WorkCount > sum.ByCategory[j].WorkCount })
	for _, name := range projOrder {
		sum.ByProject = append(sum.ByProject, *projects[name])
	}
	sort.SliceStable(sum.ByProject, func(i, j int) bool { return sum.ByProject[i].WorkCount > sum.ByProject[j].WorkCount })
	for _, id := range peopleOrder {
		sum.ByEmployee = append(sum.ByEmployee, *people[id])
	}
	sort.SliceStable(sum.ByEmployee, func(i, j int) bool {
		a, b := sum.ByEmployee[i], sum.ByEmployee[j]
		if a.WorkCount != b.WorkCount {
			return a.WorkCount > b.WorkCount
		}
		return a.RecordCount > b.RecordCount
	})
	if sum.ByCategory == nil {
		sum.ByCategory = []CategoryStat{}
	}
	if sum.ByProject == nil {
		sum.ByProject = []ProjectStat{}
	}
	if sum.ByEmployee == nil {
		sum.ByEmployee = []PersonStat{}
	}
	for lvl := 1; lvl <= 5; lvl++ {
		sum.ByDifficulty = append(sum.ByDifficulty, DifficultyStat{Level: lvl, Label: difficultyLabel(lvl), RecordCount: diff[lvl]})
	}
	byDay := sortedBuckets(days)
	if sp.Kind != KindAnnual {
		sum.ByDay = &byDay
	}
	switch sp.Kind {
	case KindAnnual:
		byMonth := sortedBuckets(months)
		qs := make([]BucketStat, 0, 4)
		for q := 1; q <= 4; q++ {
			qs = append(qs, *quarters[strconv.Itoa(q)])
		}
		sum.ByMonth, sum.ByQuarter = &byMonth, &qs
		active := 0
		for _, m := range byMonth {
			if m.RecordCount > 0 {
				active++
			}
		}
		sum.ActiveMonths = &active
	case KindPeriod:
		byMonth, byQuarter := sortedBuckets(months), sortedBuckets(quarters)
		sum.ByMonth, sum.ByQuarter = &byMonth, &byQuarter
		ad, am := len(byDay), len(byMonth)
		sum.ActiveDays, sum.ActiveMonths = &ad, &am
	}
	return data, nil
}

func sortedBuckets(m map[string]*BucketStat) []BucketStat {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]BucketStat, 0, len(keys))
	for _, k := range keys {
		out = append(out, *m[k])
	}
	return out
}

func safeSub(s string, a, b int) string {
	if a >= len(s) {
		return ""
	}
	if b > len(s) {
		b = len(s)
	}
	return s[a:b]
}

func difficultyLabel(l int) string {
	switch l {
	case 1:
		return "很简单"
	case 2:
		return "较简单"
	case 3:
		return "一般"
	case 4:
		return "较困难"
	case 5:
		return "很困难"
	}
	return "未设置"
}

func participantMap(q db.Q, rows []Row) (map[int64][]RecordPerson, error) {
	out := map[int64][]RecordPerson{}
	for start := 0; start < len(rows); start += 500 {
		end := min(start+500, len(rows))
		args := make([]any, 0, end-start)
		for _, r := range rows[start:end] {
			args = append(args, r.Int("id"))
		}
		list, err := db.All(q, `SELECT p.work_record_id, u.id, u.name, u.username, u.employee_no,
			d.name AS department_name, pd.name AS parent_department_name
			FROM work_record_participants p JOIN users u ON u.id = p.user_id
			LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN departments pd ON pd.id = d.parent_id
			WHERE p.work_record_id IN (`+db.Placeholders(len(args))+`) ORDER BY u.name`, args...)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			out[r.Int("work_record_id")] = append(out[r.Int("work_record_id")], RecordPerson{
				ID: r.Int("id"), Name: r.Str("name"), Username: r.Str("username"), EmployeeNo: strPtr(r["employee_no"]),
				Department: deptLabel(r["department_name"], r["parent_department_name"]),
			})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 报告范围（谁能为哪个范围生成/查看报告）
// ---------------------------------------------------------------------------

// ReportTarget 是一次报告的统计对象。
type ReportTarget struct {
	DepartmentID int64
	EmployeeID   int64
}

// ResolveTarget 根据角色确定统计对象：
//   - 管理员：全公司或任意部门（department_id=0 表示全公司）
//   - 部门经理：本人，或本部门/子部门
//   - 员工：仅本人
func (c *Core) ResolveTarget(user Row, requestedDept int64) (ReportTarget, error) {
	switch user.Str("role") {
	case "admin":
		if requestedDept > 0 {
			if v, err := db.Value(c.DB, "SELECT id FROM departments WHERE id = ?", requestedDept); err != nil {
				return ReportTarget{}, err
			} else if v == nil {
				return ReportTarget{}, apperr.New("所选部门不存在。")
			}
		}
		return ReportTarget{DepartmentID: requestedDept}, nil
	case "manager":
		if requestedDept > 0 {
			sc, err := c.ScopeFor(user)
			if err != nil {
				return ReportTarget{}, err
			}
			if !sc.CoversDepartment(requestedDept) {
				return ReportTarget{}, apperr.Forbidden("只能生成本部门（含子部门）的工作总结。")
			}
			return ReportTarget{DepartmentID: requestedDept}, nil
		}
	}
	return ReportTarget{EmployeeID: user.Int("id")}, nil
}

// ReportDeptOptions 报告页的部门下拉选项（员工为空）。
func (c *Core) ReportDeptOptions(user Row) ([]Row, error) {
	switch user.Str("role") {
	case "admin":
		return departmentTree(c.DB)
	case "manager":
		sc, err := c.ScopeFor(user)
		if err != nil || len(sc.DeptIDs) == 0 {
			return nil, err
		}
		all, err := departmentTree(c.DB)
		if err != nil {
			return nil, err
		}
		var out []Row
		for _, d := range all {
			if sc.CoversDepartment(d.Int("id")) {
				out = append(out, d)
			}
		}
		return out, nil
	}
	return nil, nil
}

// reportVisibility 报告可见性：管理员全部；其他人看“本人为对象或本人生成”的；经理另可看本部门的。
func (c *Core) reportVisibility(user Row, alias string) (string, []any, error) {
	if user.Str("role") == "admin" {
		return "1 = 1", nil, nil
	}
	cond := "(" + alias + ".employee_id = ? OR " + alias + ".created_by = ?"
	args := []any{user.Int("id"), user.Int("id")}
	if user.Str("role") == "manager" {
		sc, err := c.ScopeFor(user)
		if err != nil {
			return "", nil, err
		}
		if len(sc.DeptIDs) > 0 {
			cond += " OR " + alias + ".department_id IN (" + db.Placeholders(len(sc.DeptIDs)) + ")"
			for _, id := range sc.DeptIDs {
				args = append(args, id)
			}
		}
	}
	return cond + ")", args, nil
}

// ValidStyle 规范化写作风格。
func ValidStyle(s string) string {
	switch s {
	case "concise", "normal", "detailed":
		return s
	}
	return "normal"
}
