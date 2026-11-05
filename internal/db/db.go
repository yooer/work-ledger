// Package db 封装 SQLite 访问。只允许 service 层与基础设施层（session、install）引用，
// controller 层禁止 import 本包与 database/sql（由 CI 检查）。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"work-ledger/internal/model"

	_ "modernc.org/sqlite"
)

// Q 同时被 *sql.DB 与 *sql.Tx 实现，service 函数接收 Q 即可在事务内外复用。
type Q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type DB struct {
	*sql.DB
	Path string
}

// Open 打开数据库，开启外键、WAL 与 busy_timeout；写事务使用 BEGIN IMMEDIATE 避免锁升级死锁。
func Open(path string) (*DB, error) {
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
	return &DB{DB: sqlDB, Path: path}, nil
}

// Tx 在事务中执行 fn，fn 返回错误或 panic 时回滚。
func (d *DB) Tx(fn func(tx Q) error) (err error) {
	tx, err := d.BeginTx(context.Background(), nil)
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
	return fn(tx)
}

// Installed 判断核心表是否存在。
func (d *DB) Installed() bool {
	var n int
	err := d.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'").Scan(&n)
	return err == nil && n > 0
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

func Insert(q Q, query string, args ...any) (int64, error) {
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
			row[c] = normalize(values[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func normalize(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

// IsUniqueViolation 判断是否唯一约束冲突。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed: unique")
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
