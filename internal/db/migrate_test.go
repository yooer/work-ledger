package db

import (
	"path/filepath"
	"regexp"
	"testing"
)

// 旧库（v1.0.x / PHP 版）没有 users.supervisor_id 和报告表的 team_leader_id，Migrate 应补齐且可重复执行。
func TestMigrateAddsGoColumnsToOldDatabase(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "old.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	oldSchema := regexp.MustCompile(`(?m)^\s*(supervisor_id|team_leader_id|position_id) .*\n`).ReplaceAllString(schemaSQL, "")
	oldSchema = commentLine.ReplaceAllString(oldSchema, "")
	for _, stmt := range regexp.MustCompile(`;\s*\n`).Split(oldSchema, -1) {
		if stmt = regexp.MustCompile(`^\s+|\s+$`).ReplaceAllString(stmt, ""); stmt != "" {
			if _, err := d.Exec(stmt); err != nil {
				t.Fatalf("old schema: %v\n%s", err, stmt)
			}
		}
	}
	if cols, _ := d.columns("users"); cols["supervisor_id"] {
		t.Fatal("test setup should start without supervisor_id")
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Migrate(); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}
	if cols, _ := d.columns("users"); !cols["supervisor_id"] || !cols["position_id"] {
		t.Fatal("supervisor_id / position_id not added")
	}
	for _, tbl := range []string{"weekly_reports", "annual_reports", "period_reports"} {
		if cols, _ := d.columns(tbl); !cols["team_leader_id"] {
			t.Fatalf("%s.team_leader_id not added", tbl)
		}
	}
}

// v1.3.x 的工作分类没有 department_id，名称全局唯一；升级后改为“同一部门内不重名”。
func TestMigrateWorkCategoryDepartment(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "old.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, stmt := range []string{
		`CREATE TABLE work_categories (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL COLLATE NOCASE,
			is_customer_project INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX ux_work_categories_name ON work_categories(name COLLATE NOCASE)`,
		`INSERT INTO work_categories (name, created_at, updated_at) VALUES ('日常维护', 'x', 'x')`,
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Migrate(); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}
	if cols, _ := d.columns("work_categories"); !cols["department_id"] {
		t.Fatal("department_id not added")
	}
	for _, stmt := range []string{
		`INSERT INTO departments (id, name, is_active, sort_order, created_at, updated_at) VALUES (1, 'A', 1, 0, 'x', 'x'), (2, 'B', 1, 0, 'x', 'x')`,
		`INSERT INTO work_categories (name, department_id, created_at, updated_at) VALUES ('日常维护', 1, 'x', 'x'), ('日常维护', 2, 'x', 'x')`,
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Fatalf("same name in different departments should be allowed: %v", err)
		}
	}
	if _, err := d.Exec(`INSERT INTO work_categories (name, department_id, created_at, updated_at) VALUES ('日常维护', 1, 'x', 'x')`); err == nil {
		t.Fatal("duplicate name in the same department should fail")
	}
	if _, err := d.Exec(`INSERT INTO work_categories (name, created_at, updated_at) VALUES ('日常维护', 'x', 'x')`); err == nil {
		t.Fatal("duplicate unassigned name should fail")
	}
}
