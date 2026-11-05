package validator

import (
	"unicode/utf8"

	"work-ledger/internal/timeutil"
)

// RecordInput 台账表单。
type RecordInput struct {
	ID                int64
	WorkCategoryID    int64
	ProjectCategoryID int64
	Content           string
	WorkAt            string // 已规范为 YYYY-MM-DD HH:MM:00
	Difficulty        int64
	WorkCount         int64
	ParticipantIDs    []int64
}

func record(in Input) (RecordInput, *V) {
	v := New(in)
	out := RecordInput{
		ID:                v.ID("id"),
		WorkCategoryID:    v.RequiredID("work_category_id", "工作分类"),
		ProjectCategoryID: v.ID("project_category_id"),
		Content:           v.Raw("content"),
		Difficulty:        v.IntRange("difficulty", "难易度", 3, 1, 5),
		WorkCount:         v.IntRange("work_count", "工作次数", 1, 1, 100),
		ParticipantIDs:    v.IDList("participant_ids"),
	}
	n := utf8.RuneCountInString(out.Content)
	if n < 2 {
		v.Fail("content", "请填写具体工作内容（至少 2 个字）。")
	} else if n > 5000 {
		v.Fail("content", "工作内容不能超过 5000 个字。")
	}
	if at, ok := timeutil.NormalizeLocal(v.Raw("work_at")); ok {
		out.WorkAt = at
	} else {
		v.Fail("work_at", "请填写有效的工作日期和时间，精确到分钟。")
	}
	if len(out.ParticipantIDs) == 0 {
		v.Fail("participant_ids", "请至少选择一名参与人员。")
	} else if len(out.ParticipantIDs) > 200 {
		v.Fail("participant_ids", "参与人员不能超过 200 人。")
	}
	return out, v
}

func RecordCreate(in Input) (any, error) {
	out, v := record(in)
	return out, v.Err()
}

func RecordUpdate(in Input) (any, error) {
	out, v := record(in)
	if out.ID <= 0 {
		v.Fail("id", "缺少记录 ID。")
	}
	return out, v.Err()
}

// RecordFilterInput 列表/导出筛选（格式错误的值静默忽略，与 PHP 版一致）。
type RecordFilterInput struct {
	From, To     string
	CategoryID   int64
	ProjectID    int64
	EmployeeID   int64
	DepartmentID int64
	Q            string
	Page         int
}

func RecordFilter(in Input) RecordFilterInput {
	v := New(in)
	return RecordFilterInput{
		From:         v.LenientDate("from"),
		To:           v.LenientDate("to"),
		CategoryID:   v.ID("category_id"),
		ProjectID:    v.ID("project_id"),
		EmployeeID:   v.ID("employee_id"),
		DepartmentID: v.ID("department_id"),
		Q:            Truncate(v.Raw("q"), 100),
		Page:         int(max(1, v.ID("page"))),
	}
}

// ImportOptionsInput 导入选项（文件本身在 controller 中按上传规则检查）。
type ImportOptionsInput struct {
	DefaultCreatorID     int64
	AutoCreateCategories bool
	AutoCreateUsers      bool
	ParticipantAsCreator bool
}

func ImportOptions(in Input) (any, error) {
	v := New(in)
	out := ImportOptionsInput{
		DefaultCreatorID:     v.RequiredID("default_creator_id", "默认记录人"),
		AutoCreateCategories: v.Bool("auto_create_categories"),
		AutoCreateUsers:      v.Bool("auto_create_users"),
		ParticipantAsCreator: v.Bool("participant_as_creator"),
	}
	return out, v.Err()
}
