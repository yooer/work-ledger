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
	TeamLeaderID          *int64  `json:"team_leader_id,omitempty"` // Go 版新增：团队总结
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
	TeamLeaderID    int64
	Restrict        *Scope
	IncludeChildren bool
	MaxRecords      int
}

// deptLabel 部门显示名：优先完整路径。
func deptLabel(name, path any) string {
	if name == nil {
		return "未分配部门"
	}
	if path != nil && fmt.Sprint(path) != "" {
		return fmt.Sprint(path)
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
		// 个人：本人创建的 + 本人参与的
		cond, a := InvolvedSQL("r", []int64{sp.EmployeeID})
		conds = append(conds, cond)
		args = append(args, a...)
		id := sp.EmployeeID
		name := e.Str("name")
		scope.EmployeeID, scope.EmployeeName, scope.EmployeeNo = &id, &name, strPtr(e["employee_no"])
		if no := e.Str("employee_no"); no != "" {
			parts = append(parts, name+"（"+no+"）")
		} else {
			parts = append(parts, name)
		}
	}
	if sp.TeamLeaderID > 0 {
		leader, err := db.One(c.DB, "SELECT id, name FROM users WHERE id = ?", sp.TeamLeaderID)
		if err != nil {
			return nil, err
		}
		if leader == nil {
			return nil, apperr.New("指定的主管领导不存在。")
		}
		subs, err := subordinateIDs(c.DB, sp.TeamLeaderID)
		if err != nil {
			return nil, err
		}
		// 团队：成员创建的 + 成员参与的（按记录去重）
		cond, a := InvolvedSQL("r", append([]int64{sp.TeamLeaderID}, subs...))
		conds = append(conds, cond)
		args = append(args, a...)
		id := sp.TeamLeaderID
		scope.TeamLeaderID = &id
		parts = append(parts, leader.Str("name")+"的团队（含下属 "+strconv.Itoa(len(subs))+" 人）")
	}
	if sp.Restrict != nil {
		cond, rargs := sp.Restrict.SQL("r")
		conds = append(conds, cond)
		args = append(args, rargs...)
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
		`+categoryLabelSQL+` AS category_name, wc.is_customer_project, pc.name AS project_name,
		u.id AS creator_id, u.name AS creator_name, u.username AS creator_username, u.employee_no AS creator_employee_no,
		eu.name AS editor_name, d.name AS department_name, dpp.path AS department_path
		FROM work_records r
		JOIN work_categories wc ON wc.id = r.work_category_id
		LEFT JOIN project_categories pc ON pc.id = r.project_category_id
		JOIN users u ON u.id = r.created_by
		JOIN users eu ON eu.id = r.updated_by
		LEFT JOIN departments d ON d.id = r.department_id
		LEFT JOIN department_paths dpp ON dpp.id = d.id
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
			EmployeeNo: strPtr(r["creator_employee_no"]), Department: deptLabel(r["department_name"], r["department_path"])}
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
			d.name AS department_name, dpp.path AS department_path
			FROM work_record_participants p JOIN users u ON u.id = p.user_id
			LEFT JOIN departments d ON d.id = u.department_id LEFT JOIN department_paths dpp ON dpp.id = d.id
			WHERE p.work_record_id IN (`+db.Placeholders(len(args))+`) ORDER BY u.name`, args...)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			out[r.Int("work_record_id")] = append(out[r.Int("work_record_id")], RecordPerson{
				ID: r.Int("id"), Name: r.Str("name"), Username: r.Str("username"), EmployeeNo: strPtr(r["employee_no"]),
				Department: deptLabel(r["department_name"], r["department_path"]),
			})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 报告范围（谁能为哪个范围生成/查看报告）
// ---------------------------------------------------------------------------

// ReportTarget 是一次报告的统计对象（三者至多一个非零；都为零表示全公司）。
type ReportTarget struct {
	DepartmentID int64
	EmployeeID   int64
	TeamLeaderID int64  // 某人及其全部直接、间接下属
	Restrict     *Scope // 非管理员查看他人时，额外限定在自己的可见范围内
}

// Key 报告页“统计范围”下拉的取值：""（默认）、self、team、dept:ID、user:ID。
func (t ReportTarget) Key(selfID int64) string {
	switch {
	case t.TeamLeaderID > 0:
		return "team"
	case t.DepartmentID > 0:
		return "dept:" + strconv.FormatInt(t.DepartmentID, 10)
	case t.EmployeeID > 0 && t.EmployeeID == selfID:
		return "self"
	case t.EmployeeID > 0:
		return "user:" + strconv.FormatInt(t.EmployeeID, 10)
	}
	return ""
}

// ParseTargetKey 解析统计范围取值，返回类型与 ID。格式不对时返回 ("", 0)。
func ParseTargetKey(key string) (string, int64) {
	switch key {
	case "self", "team":
		return key, 0
	}
	for _, prefix := range []string{"dept:", "user:"} {
		if strings.HasPrefix(key, prefix) {
			if id, err := strconv.ParseInt(key[len(prefix):], 10, 64); err == nil && id > 0 {
				return strings.TrimSuffix(prefix, ":"), id
			}
		}
	}
	return "", 0
}

// ResolveTarget 根据角色确定统计对象：
//   - 管理员：全公司（默认）、任意部门、任意人员、本人；有下属时还可选“我的团队”
//   - 部门经理 / 主管领导：本人（默认）、我的团队、分管部门、团队成员
//   - 员工：仅本人
//
// key 为空时兼容旧参数 department_id。
func (c *Core) ResolveTarget(user Row, key string, legacyDept int64) (ReportTarget, error) {
	selfID := user.Int("id")
	kind, id := ParseTargetKey(key)
	if kind == "" && legacyDept > 0 {
		kind, id = "dept", legacyDept
	}
	isAdmin := user.Str("role") == "admin"
	sc, err := c.ScopeFor(user)
	if err != nil {
		return ReportTarget{}, err
	}
	switch kind {
	case "self":
		return ReportTarget{EmployeeID: selfID}, nil
	case "team":
		subs, err := subordinateIDs(c.DB, selfID)
		if err != nil {
			return ReportTarget{}, err
		}
		if len(subs) == 0 {
			return ReportTarget{}, apperr.Forbidden("你目前没有下属，无法生成团队总结。")
		}
		return ReportTarget{TeamLeaderID: selfID}, nil
	case "dept":
		if v, err := db.Value(c.DB, "SELECT id FROM departments WHERE id = ?", id); err != nil {
			return ReportTarget{}, err
		} else if v == nil {
			return ReportTarget{}, apperr.New("所选部门不存在。")
		}
		if !sc.CoversDepartment(id) {
			return ReportTarget{}, apperr.Forbidden("只能选择自己分管的部门（含子部门）。")
		}
		return ReportTarget{DepartmentID: id}, nil
	case "user":
		if id == selfID {
			return ReportTarget{EmployeeID: selfID}, nil
		}
		u, err := db.One(c.DB, "SELECT id, department_id FROM users WHERE id = ?", id)
		if err != nil {
			return ReportTarget{}, err
		}
		if u == nil {
			return ReportTarget{}, apperr.New("所选人员不存在。")
		}
		if isAdmin {
			return ReportTarget{EmployeeID: id}, nil
		}
		if sc.IsSubordinate(id) {
			return ReportTarget{EmployeeID: id}, nil
		}
		if containsID(sc.DeptIDs, u.Int("department_id")) {
			// 分管部门的成员：只统计其在分管部门期间的台账。
			restrict := sc
			return ReportTarget{EmployeeID: id, Restrict: &restrict}, nil
		}
		return ReportTarget{}, apperr.Forbidden("只能选择自己的下属或分管部门的人员。")
	}
	if isAdmin {
		return ReportTarget{}, nil
	}
	return ReportTarget{EmployeeID: selfID}, nil
}

// TargetRecords 把报告统计对象换算成台账筛选条件（周报中心导出本周期台账用），
// 结果与报告统计的记录范围一致，且不超出当前用户的可见范围。
func (c *Core) TargetRecords(user Row, t ReportTarget, from, to string) (Scope, RecordFilter, error) {
	sc, err := c.ScopeFor(user)
	if err != nil {
		return sc, RecordFilter{}, err
	}
	f := RecordFilter{From: from, To: to, DepartmentID: t.DepartmentID}
	if t.EmployeeID > 0 {
		f.InvolvedIDs = []int64{t.EmployeeID}
	}
	if t.TeamLeaderID > 0 {
		subs, err := subordinateIDs(c.DB, t.TeamLeaderID)
		if err != nil {
			return sc, f, err
		}
		f.InvolvedIDs = append([]int64{t.TeamLeaderID}, subs...)
	}
	return sc, f, nil
}

// TargetOption 报告页“统计范围”下拉选项。
type TargetOption struct {
	Value string
	Label string
}

// TargetGroup 下拉分组。
type TargetGroup struct {
	Label   string
	Options []TargetOption
}

// ReportTargetOptions 报告页的统计范围选项；只有本人可选时返回 nil（页面不显示下拉）。
func (c *Core) ReportTargetOptions(user Row) ([]TargetGroup, error) {
	selfID := user.Int("id")
	isAdmin := user.Str("role") == "admin"
	sc, err := c.ScopeFor(user)
	if err != nil {
		return nil, err
	}
	if !isAdmin && !sc.IsTeamScope() {
		return nil, nil
	}
	common := TargetGroup{Label: "常用"}
	if isAdmin {
		common.Options = append(common.Options, TargetOption{"", "全公司"}, TargetOption{"self", "我本人"})
	} else {
		common.Options = append(common.Options, TargetOption{"", "我本人"})
	}
	if sc.HasSubordinates() {
		common.Options = append(common.Options, TargetOption{"team", "我的团队（含全部下属）"})
	}
	groups := []TargetGroup{common}

	all, err := departmentTree(c.DB)
	if err != nil {
		return nil, err
	}
	depts := TargetGroup{Label: "部门（含下级部门）"}
	for _, d := range all {
		if sc.CoversDepartment(d.Int("id")) {
			depts.Options = append(depts.Options, TargetOption{"dept:" + strconv.FormatInt(d.Int("id"), 10), d.Str("label")})
		}
	}
	if len(depts.Options) > 0 {
		groups = append(groups, depts)
	}

	people, err := (&Users{Core: c}).EmployeeOptions(sc)
	if err != nil {
		return nil, err
	}
	persons := TargetGroup{Label: "人员"}
	if !isAdmin {
		persons.Label = "团队成员"
	}
	for _, p := range people {
		if p.Int("id") == selfID {
			continue
		}
		label := p.Str("name")
		if no := p.Str("employee_no"); no != "" {
			label += "（" + no + "）"
		}
		if dn := p.Str("department_name"); dn != "" {
			label += " · " + dn
		}
		if !p.Bool("is_active") {
			label += " · 已停用"
		}
		persons.Options = append(persons.Options, TargetOption{"user:" + strconv.FormatInt(p.Int("id"), 10), label})
	}
	if len(persons.Options) > 0 {
		groups = append(groups, persons)
	}
	return groups, nil
}

// reportVisibility 报告可见性：管理员全部；其他人看“本人为对象或本人生成”的；
// 领导另可看分管部门的、下属个人的、以及下属作为团队负责人的报告。
func (c *Core) reportVisibility(user Row, alias string) (string, []any, error) {
	if user.Str("role") == "admin" {
		return "1 = 1", nil, nil
	}
	cond := "(" + alias + ".employee_id = ? OR " + alias + ".created_by = ? OR " + alias + ".team_leader_id = ?"
	args := []any{user.Int("id"), user.Int("id"), user.Int("id")}
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
	if len(sc.UserIDs) > 0 {
		ph := db.Placeholders(len(sc.UserIDs))
		cond += " OR " + alias + ".employee_id IN (" + ph + ") OR " + alias + ".team_leader_id IN (" + ph + ")"
		for i := 0; i < 2; i++ {
			for _, id := range sc.UserIDs {
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
