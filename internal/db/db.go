// Package db 封装数据库访问（SQLite / MySQL / MariaDB / PostgreSQL）。只允许 service 层与基础设施层（session、install）引用，
// controller 层禁止 import 本包与 database/sql（由 CI 检查）。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"work-ledger/internal/config"
	"work-ledger/internal/model"

	_ "modernc.org/sqlite"
)

// Q 同时被 *DB 与事务实现，service 函数接收 Q 即可在事务内外复用。SQL 统一按 SQLite 写法书写，执行前按方言转换。
type Q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Dialect() Dialect
}

type DB struct {
	*sql.DB
	Path    string // SQLite 文件路径
	dialect Dialect
	Version string // 数据库服务器版本（MySQL / PostgreSQL）
}

// Options 连接参数。
type Options struct {
	Driver   string // sqlite / mysql / postgres
	Path     string // SQLite 文件
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

func (d *DB) Dialect() Dialect { return d.dialect }

func (d *DB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, translate(d.dialect, q), args...)
}
func (d *DB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, translate(d.dialect, q), args...)
}
func (d *DB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, translate(d.dialect, q), args...)
}
func (d *DB) Exec(q string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), q, args...)
}
func (d *DB) Query(q string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), q, args...)
}
func (d *DB) QueryRow(q string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), q, args...)
}

// txQ 事务内的 Q。
type txQ struct {
	tx      *sql.Tx
	dialect Dialect
}

func (t *txQ) Dialect() Dialect { return t.dialect }
func (t *txQ) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, translate(t.dialect, q), args...)
}
func (t *txQ) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, translate(t.dialect, q), args...)
}
func (t *txQ) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, translate(t.dialect, q), args...)
}

// OpenConfig 按 config.DB 打开数据库。
func OpenConfig(c config.DB) (*DB, error) {
	return OpenWith(Options{Driver: c.Driver, Path: c.SQLitePath, Host: c.Host, Port: c.Port, Name: c.Name, User: c.User, Password: c.Password, SSLMode: c.SSLMode})
}

// Open 打开 SQLite 数据库（兼容旧调用）。
func Open(path string) (*DB, error) { return OpenWith(Options{Driver: "sqlite", Path: path}) }

// OpenWith 按配置打开数据库并检查服务器版本。
func OpenWith(o Options) (*DB, error) {
	switch o.Driver {
	case "", "sqlite":
		return openSQLite(o.Path)
	case "mysql":
		return openMySQL(o)
	case "postgres":
		return openPostgres(o)
	}
	return nil, fmt.Errorf("不支持的数据库类型：%s", o.Driver)
}

// openSQLite 开启外键、WAL 与 busy_timeout；写事务使用 BEGIN IMMEDIATE 避免锁升级死锁。
func openSQLite(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("无法创建数据库目录：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &DB{DB: sqlDB, Path: path, dialect: SQLite}, nil
}

// mysqlSQLMode 统一 MySQL / MariaDB 的会话模式：严格模式 + PIPES_AS_CONCAT（|| 为字符串拼接）。
const mysqlSQLMode = "'STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,PIPES_AS_CONCAT'"

func openMySQL(o Options) (*DB, error) {
	c := mysql.NewConfig()
	c.User, c.Passwd, c.DBName = o.User, o.Password, o.Name
	c.Net, c.Addr = "tcp", net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	c.Collation = "utf8mb4_unicode_ci"
	c.ParseTime = false
	c.Timeout = 10 * time.Second
	c.MultiStatements = false
	c.Params = map[string]string{"sql_mode": mysqlSQLMode, "time_zone": "'+00:00'"}
	switch strings.ToLower(o.SSLMode) {
	case "", "false", "disable", "off":
	case "true", "require", "on":
		c.TLSConfig = "true"
	case "skip-verify", "preferred":
		c.TLSConfig = o.SSLMode
	default:
		return nil, fmt.Errorf("DB_SSLMODE=%s 无效（MySQL 可选 true / false / skip-verify）", o.SSLMode)
	}
	sqlDB, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return nil, err
	}
	d := &DB{DB: sqlDB, dialect: MySQL}
	if err := d.connect(); err != nil {
		return nil, err
	}
	return d, d.checkVersion()
}

func openPostgres(o Options) (*DB, error) {
	ssl := o.SSLMode
	if ssl == "" {
		ssl = "prefer"
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(o.User, o.Password), Host: net.JoinHostPort(o.Host, strconv.Itoa(o.Port)), Path: "/" + o.Name}
	q := url.Values{"sslmode": {ssl}, "connect_timeout": {"10"}, "application_name": {"work-ledger"}}
	u.RawQuery = q.Encode()
	sqlDB, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, err
	}
	d := &DB{DB: sqlDB, dialect: Postgres}
	if err := d.connect(); err != nil {
		return nil, err
	}
	return d, d.checkVersion()
}

func (d *DB) connect() error {
	d.DB.SetMaxOpenConns(16)
	d.DB.SetMaxIdleConns(4)
	d.DB.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.DB.PingContext(ctx); err != nil {
		_ = d.DB.Close()
		return fmt.Errorf("无法连接 %s 数据库：%w", d.dialect.Name(), err)
	}
	return nil
}

var reVersion = regexp.MustCompile(`(\d+)\.(\d+)`)

// checkVersion 最低版本：MySQL 8.0、MariaDB 10.5、PostgreSQL 12（递归查询、生成列等依赖）。
func (d *DB) checkVersion() error {
	var v string
	q := "SELECT VERSION()"
	if d.dialect == Postgres {
		q = "SHOW server_version"
	}
	if err := d.DB.QueryRow(q).Scan(&v); err != nil {
		_ = d.DB.Close()
		return fmt.Errorf("读取数据库版本失败：%w", err)
	}
	d.Version = v
	m := reVersion.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	var ok bool
	var need string
	switch {
	case d.dialect == Postgres:
		ok, need = major >= 12, "PostgreSQL 12"
	case strings.Contains(strings.ToLower(v), "mariadb"):
		ok, need = major > 10 || (major == 10 && minor >= 5), "MariaDB 10.5"
	default:
		ok, need = major >= 8, "MySQL 8.0"
	}
	if !ok {
		_ = d.DB.Close()
		return fmt.Errorf("数据库版本过低：%s，最低要求 %s", v, need)
	}
	return nil
}

// ServerLabel 页面显示用，如 “PostgreSQL 16.4”。
func (d *DB) ServerLabel() string {
	if d.dialect == SQLite {
		return "SQLite"
	}
	v := d.Version
	if d.dialect == MySQL && strings.Contains(strings.ToLower(v), "mariadb") {
		return "MariaDB " + strings.SplitN(v, "-", 2)[0]
	}
	if d.dialect == MySQL {
		return "MySQL " + v
	}
	return "PostgreSQL " + strings.Fields(v + " ")[0]
}

// Tx 在事务中执行 fn，fn 返回错误或 panic 时回滚。
func (d *DB) Tx(fn func(tx Q) error) (err error) {
	tx, err := d.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	return fn(&txQ{tx: tx, dialect: d.dialect})
}

// Installed 判断核心表是否存在。
func (d *DB) Installed() bool {
	ok, err := TableExists(d, "users")
	return err == nil && ok
}

// ResetSequences 写入指定 id 后同步 PostgreSQL 自增序列（其他数据库无需处理）。
func ResetSequences(q Q, tables ...string) error {
	if q.Dialect() != Postgres {
		return nil
	}
	for _, t := range tables {
		if _, err := q.ExecContext(context.Background(), "SELECT setval(pg_get_serial_sequence('"+t+"', 'id'), COALESCE((SELECT MAX(id) FROM "+t+"), 0) + 1, false)"); err != nil {
			return fmt.Errorf("同步 %s 序列失败：%w", t, err)
		}
	}
	return nil
}

// TableExists 表是否存在。
func TableExists(q Q, table string) (bool, error) {
	var query string
	switch q.Dialect() {
	case MySQL:
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	case Postgres:
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?"
	default:
		query = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	}
	n, err := Int(q, query, table)
	return n > 0, err
}

// ---------------------------------------------------------------------------
// 通用查询辅助：结果以 Row（列名 → 值）返回，与 PHP 版 FETCH_ASSOC 对齐。
// ---------------------------------------------------------------------------

func All(q Q, query string, args ...any) ([]Row, error) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func One(q Q, query string, args ...any) (Row, error) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanRows(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

// Value 返回第一行第一列，无结果返回 nil。
func Value(q Q, query string, args ...any) (any, error) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var v any
	if err := rows.Scan(&v); err != nil {
		return nil, err
	}
	return normalize(v), rows.Err()
}

func Int(q Q, query string, args ...any) (int64, error) {
	v, err := Value(q, query, args...)
	if err != nil {
		return 0, err
	}
	return model.ToInt(v), nil
}

func Exec(q Q, query string, args ...any) (int64, error) {
	res, err := q.ExecContext(context.Background(), query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Insert 执行 INSERT 并返回新记录 id（表需有自增 id 列；PostgreSQL 使用 RETURNING id）。
func Insert(q Q, query string, args ...any) (int64, error) {
	if q.Dialect() == Postgres {
		var id int64
		err := q.QueryRowContext(context.Background(), query+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := q.ExecContext(context.Background(), query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanRows(rows *sql.Rows) ([]Row, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	numeric := make([]bool, len(cols))
	if types, err := rows.ColumnTypes(); err == nil {
		for i, t := range types {
			numeric[i] = numericType(t.DatabaseTypeName())
		}
	}
	out := []Row{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(Row, len(cols))
		for i, c := range cols {
			v := normalize(values[i])
			if numeric[i] {
				v = toNumber(v)
			}
			row[c] = v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// numericType MySQL 文本协议会把整数、DECIMAL（SUM 结果）以字节串返回，PostgreSQL 的 numeric 以字符串返回，统一转为数字。
func numericType(name string) bool {
	switch strings.ToUpper(name) {
	case "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "MEDIUMINT", "INT2", "INT4", "INT8",
		"UNSIGNED INT", "UNSIGNED BIGINT", "UNSIGNED TINYINT", "UNSIGNED SMALLINT", "DECIMAL", "NUMERIC", "NEWDECIMAL":
		return true
	}
	return false
}

func toNumber(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if f == float64(int64(f)) {
			return int64(f)
		}
		return f
	}
	return v
}

func normalize(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case bool: // PostgreSQL 的布尔表达式（如 EXISTS）统一为 0/1，与 SQLite / MySQL 一致
		if t {
			return int64(1)
		}
		return int64(0)
	case int32:
		return int64(t)
	case time.Time:
		return t.Format("2006-01-02 15:04:05")
	default:
		return v
	}
}

// IsUniqueViolation 判断是否唯一约束冲突。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed: unique") ||
		strings.Contains(msg, "duplicate key value") || strings.Contains(msg, "(sqlstate 23505)")
}

// Placeholders 生成 "?, ?, ?"。
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

var ErrNotFound = errors.New("not found")

// Row 是 model.Row 的别名，便于 service 层书写。
type Row = model.Row
