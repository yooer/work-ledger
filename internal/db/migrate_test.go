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
