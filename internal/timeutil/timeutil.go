// Package timeutil 统一应用时区与日期格式（数据库内日期一律以字符串存储，与 PHP 版一致）。
package timeutil

import (
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Windows 等环境无系统时区库时使用内嵌数据
)

const (
	DateTime = "2006-01-02 15:04:05"
	Date     = "2006-01-02"
	Local    = "2006-01-02T15:04"
)

var loc = time.Local

// SetLocation 设置应用时区，失败时退回 Asia/Shanghai 再退回系统时区。
func SetLocation(name string) {
	if l, err := time.LoadLocation(name); err == nil {
		loc = l
		return
	}
	if l, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		loc = l
	}
}

func Location() *time.Location { return loc }

// nowFunc 便于测试替换。
var nowFunc = time.Now

func SetNowFunc(f func() time.Time) { nowFunc = f }

func Now() time.Time       { return nowFunc().In(loc) }
func NowString() string    { return Now().Format(DateTime) }
func Today() string        { return Now().Format(Date) }
func Year() int            { return Now().Year() }
func Weekday(t time.Time) int {
	// ISO：周一=1 … 周日=7
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var localRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`)

// ParseDate 严格解析 YYYY-MM-DD。
func ParseDate(s string) (time.Time, bool) {
	if !dateRe.MatchString(s) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(Date, s, loc)
	if err != nil || t.Format(Date) != s {
		return time.Time{}, false
	}
	return t, true
}

func ValidDate(s string) bool { _, ok := ParseDate(s); return ok }

// ValidLocal 校验 datetime-local（YYYY-MM-DDTHH:MM）。
func ValidLocal(s string) bool {
	if !localRe.MatchString(s) {
		return false
	}
	t, err := time.ParseInLocation(Local, s, loc)
	return err == nil && t.Format(Local) == s
}

// NormalizeLocal 把 datetime-local 转为 "YYYY-MM-DD HH:MM:00"。
func NormalizeLocal(s string) (string, bool) {
	if !ValidLocal(s) {
		return "", false
	}
	t, _ := time.ParseInLocation(Local, s, loc)
	return t.Format("2006-01-02 15:04:00"), true
}

// ToLocalInput 把库内 "YYYY-MM-DD HH:MM:SS" 转为 datetime-local 值。
func ToLocalInput(s string) string {
	t, ok := ParseAny(s)
	if !ok {
		return ""
	}
	return t.Format(Local)
}

// ParseAny 解析库内常见日期字符串。
func ParseAny(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{DateTime, "2006-01-02 15:04", Local, "2006-01-02T15:04:05", Date, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func AddDays(date string, days int) string {
	t, ok := ParseDate(date)
	if !ok {
		return date
	}
	return t.AddDate(0, 0, days).Format(Date)
}

func MondayOf(date string) string {
	t, ok := ParseDate(date)
	if !ok {
		t, _ = ParseDate(Today())
	}
	return t.AddDate(0, 0, -(Weekday(t) - 1)).Format(Date)
}

func FormatDateTime(s string) string {
	if t, ok := ParseAny(s); ok {
		return t.Format("2006-01-02 15:04")
	}
	return "—"
}

func FormatDate(s string) string {
	if t, ok := ParseAny(s); ok {
		return t.Format(Date)
	}
	return "—"
}

func FormatRange(start, end string) string {
	return FormatDate(start) + " 至 " + FormatDate(end)
}

// DaysBetween 返回 end-start 的天数。
func DaysBetween(start, end string) int {
	a, ok1 := ParseDate(start)
	b, ok2 := ParseDate(end)
	if !ok1 || !ok2 {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}

var dayNames = map[int]string{1: "周一", 2: "周二", 3: "周三", 4: "周四", 5: "周五", 6: "周六", 7: "周日"}

func DayName(d int) string {
	if n, ok := dayNames[d]; ok {
		return n
	}
	return "周一"
}

// TodayLabel 形如 "11月5日 周四"。
func TodayLabel() string {
	n := Now()
	names := []string{"日", "一", "二", "三", "四", "五", "六"}
	return n.Format("1月2日") + " 周" + names[int(n.Weekday())]
}
