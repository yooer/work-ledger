package model

import (
	"fmt"
	"strconv"
	"strings"
)

// Row 是一行查询结果（列名 → 值）。controller/模板只依赖本包，不依赖数据库包。
type Row map[string]any

func (r Row) Str(key string) string {
	if r == nil {
		return ""
	}
	return toStr(r[key])
}

func (r Row) Int(key string) int64 {
	if r == nil {
		return 0
	}
	return toInt(r[key])
}

func (r Row) IntP(key string) *int64 {
	if r == nil || r[key] == nil {
		return nil
	}
	v := toInt(r[key])
	return &v
}

func (r Row) Float(key string) float64 {
	if r == nil {
		return 0
	}
	switch t := r[key].(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}

func (r Row) Bool(key string) bool { return r.Int(key) == 1 }

func (r Row) Null(key string) bool { return r == nil || r[key] == nil }

func toStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(t)
	}
}

func toInt(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
			return int64(f)
		}
		return n
	case []byte:
		return toInt(string(t))
	}
	return 0
}

// ToInt / ToStr 供其他包把任意值转为整数/字符串。
func ToInt(v any) int64  { return toInt(v) }
func ToStr(v any) string { return toStr(v) }
