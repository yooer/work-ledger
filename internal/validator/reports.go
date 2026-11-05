package validator

import (
	"regexp"
	"strings"

	"work-ledger/internal/apperr"
)

// APIScopeInput /api/v1 的统计范围参数。
type APIScopeInput struct {
	DepartmentID    int64
	EmployeeID      int64
	IncludeChildren bool
}

// APIScope 校验 department_id / employee_id（必须为正整数）与 include_sub_departments。
func APIScope(in Input) (APIScopeInput, error) {
	v := New(in)
	out := APIScopeInput{IncludeChildren: true}
	for _, key := range []string{"department_id", "employee_id"} {
		raw := v.Raw(key)
		if raw == "" {
			continue
		}
		n, ok := parseInt(in[key])
		if !ok || n < 1 {
			return out, apperr.New("department_id / employee_id 必须为正整数。")
		}
		if key == "department_id" {
			out.DepartmentID = n
		} else {
			out.EmployeeID = n
		}
	}
	if b, ok := in["include_sub_departments"].(bool); ok {
		out.IncludeChildren = b
	} else if raw := strings.ToLower(v.Raw("include_sub_departments")); raw == "0" || raw == "false" || raw == "no" || raw == "off" {
		out.IncludeChildren = false
	}
	return out, nil
}

// WeeklyGenerateInput 周报页面生成参数。
type WeeklyGenerateInput struct {
	WeekStart    string
	DepartmentID int64
	Style        string
}

func WeeklyGenerate(in Input) (any, error) {
	v := New(in)
	out := WeeklyGenerateInput{
		WeekStart:    v.Date("week_start", "周起始日期", true),
		DepartmentID: v.ID("department_id"),
		Style:        v.OneOf("style", "写作风格", "normal", "concise", "normal", "detailed"),
	}
	return out, v.Err()
}

// YearlyGenerateInput 年度总结生成参数。
type YearlyGenerateInput struct {
	Year         int
	DepartmentID int64
	Style        string
}

func YearlyGenerate(in Input) (any, error) {
	v := New(in)
	out := YearlyGenerateInput{
		Year:         int(v.IntRange("year", "年度", 0, 2000, 2100)),
		DepartmentID: v.ID("department_id"),
		Style:        v.OneOf("style", "写作风格", "normal", "concise", "normal", "detailed"),
	}
	return out, v.Err()
}

// PeriodGenerateInput 时段总结生成参数。
type PeriodGenerateInput struct {
	StartDate, EndDate string
	DepartmentID       int64
	Style              string
}

func PeriodGenerate(in Input) (any, error) {
	v := New(in)
	out := PeriodGenerateInput{
		StartDate:    v.Date("start_date", "开始日期", true),
		EndDate:      v.Date("end_date", "结束日期", true),
		DepartmentID: v.ID("department_id"),
		Style:        v.OneOf("style", "写作风格", "normal", "concise", "normal", "detailed"),
	}
	if out.StartDate != "" && out.EndDate != "" && out.EndDate < out.StartDate {
		v.Fail("end_date", "结束日期不能早于开始日期。")
	}
	return out, v.Err()
}

// WeekCycleInput 周报周期设置。
type WeekCycleInput struct{ Start, End int }

func WeekCycle(in Input) (any, error) {
	v := New(in)
	out := WeekCycleInput{Start: int(v.IntRange("week_start_weekday", "开始星期", 1, 1, 7)), End: int(v.IntRange("week_end_weekday", "结束星期", 7, 1, 7))}
	if v.OK() && ((out.End-out.Start+7)%7) != 6 {
		v.Fail("week_end_weekday", "周结束日必须是开始日之后的第 6 天，以保证一个完整 7 天周期。")
	}
	return out, v.Err()
}

// AutoReportInput 自动生成设置。
type AutoReportInput struct {
	WeeklyEnabled bool
	WeeklyDelay   int
	WeeklyTime    string
	WeeklyStyle   string
	YearlyEnabled bool
	YearlyDay     int
	YearlyTime    string
	YearlyStyle   string
}

func AutoReport(in Input) (any, error) {
	v := New(in)
	out := AutoReportInput{
		WeeklyEnabled: v.Bool("auto_weekly_enabled"),
		WeeklyDelay:   int(v.IntRange("auto_weekly_delay_days", "生成日", 1, 1, 6)),
		WeeklyTime:    v.Raw("auto_weekly_time"),
		WeeklyStyle:   v.OneOf("auto_weekly_style", "周报风格", "normal", "concise", "normal", "detailed"),
		YearlyEnabled: v.Bool("auto_yearly_enabled"),
		YearlyDay:     int(v.IntRange("auto_yearly_day", "生成日期", 2, 1, 31)),
		YearlyTime:    v.Raw("auto_yearly_time"),
		YearlyStyle:   v.OneOf("auto_yearly_style", "年度总结风格", "normal", "concise", "normal", "detailed"),
	}
	for key, val := range map[string]string{"auto_weekly_time": out.WeeklyTime, "auto_yearly_time": out.YearlyTime} {
		if !hhmm.MatchString(val) {
			v.Fail(key, "时间格式应为 HH:MM（24 小时制）。")
		}
	}
	return out, v.Err()
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
