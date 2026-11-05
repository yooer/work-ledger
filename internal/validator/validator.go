// Package validator 负责接口入参的格式/范围/必填校验。
// "记录是否存在"这类业务校验放在 service 层。
package validator

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"work-ledger/internal/apperr"
	"work-ledger/internal/timeutil"
)

// Input 是一次请求的参数（JSON body 或 multipart 表单字段）。
type Input map[string]any

// FromValues 把表单/查询参数转为 Input；name[] 形式的键转为数组。
func FromValues(values url.Values) Input {
	in := Input{}
	for k, vs := range values {
		if strings.HasSuffix(k, "[]") {
			in[strings.TrimSuffix(k, "[]")] = toAnySlice(vs)
			continue
		}
		if len(vs) > 0 {
			in[k] = vs[len(vs)-1]
		}
	}
	return in
}

func toAnySlice(vs []string) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// V 收集字段错误。
type V struct {
	In   Input
	errs map[string]string
	keys []string
}

func New(in Input) *V { return &V{In: in, errs: map[string]string{}} }

func (v *V) Fail(key, msg string) {
	if _, exists := v.errs[key]; !exists {
		v.errs[key] = msg
		v.keys = append(v.keys, key)
	}
}

func (v *V) HasError(key string) bool { _, ok := v.errs[key]; return ok }

func (v *V) OK() bool { return len(v.errs) == 0 }

// Err 有错误时返回 *apperr.Error。
func (v *V) Err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return apperr.Validation(v.errs)
}

// Raw 取原始字符串（去首尾空白）。
func (v *V) Raw(key string) string {
	return strings.TrimSpace(scalar(v.In[key]))
}

// RawKeep 取原始字符串（不去空白，用于密码）。
func (v *V) RawKeep(key string) string { return scalar(v.In[key]) }

func (v *V) Has(key string) bool { _, ok := v.In[key]; return ok }

// Str 可选字符串，超过 maxLen（按字符）报错；maxLen<=0 不限。
func (v *V) Str(key, label string, maxLen int) string {
	s := v.Raw(key)
	if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
		v.Fail(key, fmt.Sprintf("%s不能超过 %d 个字符。", label, maxLen))
	}
	return s
}

// Required 必填字符串。
func (v *V) Required(key, label string, maxLen int) string {
	s := v.Str(key, label, maxLen)
	if s == "" {
		v.Fail(key, "请填写"+label+"。")
	}
	return s
}

// Match 正则校验（值非空时）。
func (v *V) Match(key, value string, re *regexp.Regexp, msg string) {
	if value != "" && !re.MatchString(value) {
		v.Fail(key, msg)
	}
}

// Int 解析整数，未提供或为空时返回 def；格式错误时报错。
func (v *V) Int(key, label string, def int64) int64 {
	raw, present := v.In[key]
	if !present || raw == nil || scalar(raw) == "" {
		return def
	}
	n, ok := parseInt(raw)
	if !ok {
		v.Fail(key, label+"格式不正确。")
		return def
	}
	return n
}

// IntRange 整数并限制范围。
func (v *V) IntRange(key, label string, def, min, max int64) int64 {
	n := v.Int(key, label, def)
	if !v.HasError(key) && (n < min || n > max) {
		v.Fail(key, fmt.Sprintf("%s必须在 %d 到 %d 之间。", label, min, max))
	}
	return n
}

// ID 可选正整数 ID（0 表示未选择）。
func (v *V) ID(key string) int64 {
	n, ok := parseInt(v.In[key])
	if !ok || n < 0 {
		return 0
	}
	return n
}

// RequiredID 必选 ID。
func (v *V) RequiredID(key, label string) int64 {
	n := v.ID(key)
	if n <= 0 {
		v.Fail(key, "请选择"+label+"。")
	}
	return n
}

// IDList 整数数组（去重、去 0）。
func (v *V) IDList(key string) []int64 {
	var items []any
	switch t := v.In[key].(type) {
	case []any:
		items = t
	case []string:
		items = toAnySlice(t)
	case nil:
		return nil
	default:
		s := scalar(t)
		for _, part := range strings.Split(s, ",") {
			items = append(items, part)
		}
	}
	seen := map[int64]bool{}
	var out []int64
	for _, it := range items {
		if n, ok := parseInt(it); ok && n > 0 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// Bool 复选框/开关。
func (v *V) Bool(key string) bool {
	switch t := v.In[key].(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case nil:
		return false
	default:
		s := strings.ToLower(strings.TrimSpace(scalar(t)))
		return s == "1" || s == "true" || s == "on" || s == "yes"
	}
}

// Date 可选日期 YYYY-MM-DD；格式错误时报错（required=false 时空值返回 ""）。
func (v *V) Date(key, label string, required bool) string {
	s := v.Raw(key)
	if s == "" {
		if required {
			v.Fail(key, "请选择"+label+"。")
		}
		return ""
	}
	if !timeutil.ValidDate(s) {
		v.Fail(key, label+"格式不正确。")
		return ""
	}
	return s
}

// LenientDate 格式错误时静默忽略（用于列表筛选，与 PHP 版一致）。
func (v *V) LenientDate(key string) string {
	s := v.Raw(key)
	if timeutil.ValidDate(s) {
		return s
	}
	return ""
}

// OneOf 枚举值。
func (v *V) OneOf(key, label, def string, allowed ...string) string {
	s := v.Raw(key)
	if s == "" {
		return def
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	v.Fail(key, label+"取值无效。")
	return def
}

// Truncate 按字符截断（用于关键词等非报错场景）。
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	case json.Number:
		return t.String()
	case []any:
		if len(t) > 0 {
			return scalar(t[len(t)-1])
		}
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func parseInt(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		if t != math.Trunc(t) {
			return 0, false
		}
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	return 0, false
}
