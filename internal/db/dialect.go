package db

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Dialect 数据库方言。业务 SQL 按 SQLite 写法书写，由 translate 转换为目标数据库的写法：
//   - 占位符 ?：PostgreSQL 转为 $1, $2 …
//   - INSERT OR IGNORE：MySQL 转为 INSERT IGNORE，PostgreSQL 追加 ON CONFLICT DO NOTHING
//   - ON CONFLICT(…) DO UPDATE SET x = excluded.x：MySQL 转为 ON DUPLICATE KEY UPDATE x = VALUES(x)
//   - LIKE：PostgreSQL 转为 ILIKE（与 SQLite / MySQL 一样不区分大小写）
//   - 字符串拼接 ||：MySQL 会话开启 PIPES_AS_CONCAT，无需转换
//
// 名称类字段的大小写不敏感比较统一写成 LOWER(col) = LOWER(?)，三种数据库都适用。
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	MySQL    Dialect = "mysql"
	Postgres Dialect = "postgres"
)

// Name 用于页面显示。
func (d Dialect) Name() string {
	switch d {
	case MySQL:
		return "MySQL / MariaDB"
	case Postgres:
		return "PostgreSQL"
	}
	return "SQLite"
}

var (
	reInsertOrIgnore = regexp.MustCompile(`(?i)^\s*INSERT\s+OR\s+IGNORE\s+INTO`)
	reOnConflict     = regexp.MustCompile(`(?is)ON\s+CONFLICT\s*\([^)]*\)\s*DO\s+UPDATE\s+SET`)
	reExcluded       = regexp.MustCompile(`(?i)\bexcluded\.(\w+)`)
	reLike           = regexp.MustCompile(`\bLIKE\b`)
	translateCache   sync.Map // Dialect + query → 转换结果
)

// translate 把 SQLite 写法的 SQL 转换为目标方言（结果缓存）。
func translate(d Dialect, q string) string {
	if d == SQLite || d == "" {
		return q
	}
	key := string(d) + "\x00" + q
	if v, ok := translateCache.Load(key); ok {
		return v.(string)
	}
	out := q
	switch d {
	case MySQL:
		out = reInsertOrIgnore.ReplaceAllString(out, "INSERT IGNORE INTO")
		if reOnConflict.MatchString(out) {
			out = reOnConflict.ReplaceAllString(out, "ON DUPLICATE KEY UPDATE")
			out = reExcluded.ReplaceAllString(out, "VALUES($1)")
		}
	case Postgres:
		if reInsertOrIgnore.MatchString(out) {
			out = reInsertOrIgnore.ReplaceAllString(out, "INSERT INTO") + " ON CONFLICT DO NOTHING"
		}
		out = rebindOutsideQuotes(out)
	}
	translateCache.Store(key, out)
	return out
}

// rebindOutsideQuotes 把字符串字面量之外的 ? 换成 $n，并把 LIKE 换成 ILIKE。
func rebindOutsideQuotes(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	inQuote := false
	seg := 0 // 当前未处理的引号外片段起点
	flush := func(end int) {
		b.WriteString(reLike.ReplaceAllString(q[seg:end], "ILIKE"))
	}
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			if !inQuote {
				flush(i)
				seg = i
			} else {
				b.WriteString(q[seg : i+1])
				seg = i + 1
			}
			inQuote = !inQuote
		case c == '?' && !inQuote:
			flush(i)
			n++
			b.WriteString("$" + strconv.Itoa(n))
			seg = i + 1
		}
	}
	if inQuote {
		b.WriteString(q[seg:])
	} else {
		flush(len(q))
	}
	return b.String()
}
