package db

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

var commentLine = regexp.MustCompile(`(?m)^\s*--.*$`)

// Migrate 建表并补齐旧版本缺失的字段/索引（与 PHP 版 install.php 的升级逻辑一致），可重复执行。
// 返回执行过的升级说明。
func (d *DB) Migrate() ([]string, error) {
	var notes []string
	sqlText := commentLine.ReplaceAllString(schemaSQL, "")
	for _, stmt := range strings.Split(sqlText, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := d.Exec(stmt); err != nil {
			return notes, err
		}
	}

	cols, err := d.columns("users")
	if err != nil {
		return notes, err
	}
	userCols := []struct{ name, def string }{
		{"totp_secret", "TEXT NULL"},
		{"totp_enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"totp_recovery_codes", "TEXT NULL"},
		{"totp_last_used_step", "INTEGER NULL"},
		{"supervisor_id", "INTEGER NULL REFERENCES users(id) ON DELETE SET NULL"},   // Go 版新增：主管领导
		{"position_id", "INTEGER NULL REFERENCES positions(id) ON DELETE SET NULL"}, // Go 版新增：职务
	}
	for _, c := range userCols {
		if !cols[c.name] {
			if _, err := d.Exec("ALTER TABLE users ADD COLUMN " + c.name + " " + c.def); err != nil {
				return notes, err
			}
			notes = append(notes, "已补充 users."+c.name+" 字段。")
		}
	}

	// 主管领导索引放在补字段之后创建（旧库先补列）。
	if _, err := d.Exec("CREATE INDEX IF NOT EXISTS ix_users_supervisor ON users(supervisor_id)"); err != nil {
		return notes, err
	}
	if _, err := d.Exec("CREATE INDEX IF NOT EXISTS ix_users_position ON users(position_id)"); err != nil {
		return notes, err
	}

	// Go 版新增：团队总结的主管领导（报告统计对象为“某人及其全部下属”）。
	for _, t := range []string{"weekly_reports", "annual_reports", "period_reports"} {
		cols, err := d.columns(t)
		if err != nil {
			return notes, err
		}
		if !cols["team_leader_id"] {
			if _, err := d.Exec("ALTER TABLE " + t + " ADD COLUMN team_leader_id INTEGER NULL REFERENCES users(id) ON DELETE SET NULL"); err != nil {
				return notes, err
			}
			notes = append(notes, "已补充 "+t+".team_leader_id 字段。")
		}
		if _, err := d.Exec("CREATE INDEX IF NOT EXISTS ix_" + t + "_team ON " + t + "(team_leader_id)"); err != nil {
			return notes, err
		}
	}

	// Go 版新增：工作分类所属部门（NULL = 升级遗留的“未指定部门”，所有部门可用）。
	// 名称改为“同一部门内不重名”，替换原来的全局唯一索引。
	catCols, err := d.columns("work_categories")
	if err != nil {
		return notes, err
	}
	if !catCols["department_id"] {
		if _, err := d.Exec("ALTER TABLE work_categories ADD COLUMN department_id INTEGER NULL REFERENCES departments(id) ON DELETE RESTRICT"); err != nil {
			return notes, err
		}
		notes = append(notes, "已补充 work_categories.department_id 字段。")
	}
	for _, stmt := range []string{
		"DROP INDEX IF EXISTS ux_work_categories_name",
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_work_categories_dept_name ON work_categories(COALESCE(department_id, 0), name COLLATE NOCASE)",
		"CREATE INDEX IF NOT EXISTS ix_work_categories_department ON work_categories(department_id)",
	} {
		if _, err := d.Exec(stmt); err != nil {
			return notes, err
		}
	}

	recCols, err := d.columns("work_records")
	if err != nil {
		return notes, err
	}
	if !recCols["import_hash"] {
		if _, err := d.Exec("ALTER TABLE work_records ADD COLUMN import_hash TEXT NULL"); err != nil {
			return notes, err
		}
		notes = append(notes, "已补充 work_records.import_hash 字段。")
	}
	idx, err := All(d, "PRAGMA index_list(work_records)")
	if err != nil {
		return notes, err
	}
	hasIdx := false
	for _, r := range idx {
		if strings.EqualFold(r.Str("name"), "ux_work_records_import_hash") {
			hasIdx = true
		}
	}
	if !hasIdx {
		if _, err := d.Exec("CREATE UNIQUE INDEX ux_work_records_import_hash ON work_records (import_hash)"); err != nil {
			return notes, err
		}
		notes = append(notes, "已创建 work_records.import_hash 唯一索引。")
	}
	return notes, nil
}

func (d *DB) columns(table string) (map[string]bool, error) {
	rows, err := All(d, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[strings.ToLower(r.Str("name"))] = true
	}
	return out, nil
}
