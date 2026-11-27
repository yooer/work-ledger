package db_test

import (
	"testing"

	"work-ledger/internal/db"
	"work-ledger/internal/dbtest"
)

// MySQL / PostgreSQL：v2.0/v2.1 建的库没有隐藏字段，Migrate 应补齐且可重复执行。
func TestMigrateAddsLaterColumns(t *testing.T) {
	if dbtest.Driver() == "sqlite" {
		t.Skip("SQLite 由 TestMigrateAddsGoColumnsToOldDatabase 覆盖")
	}
	d, _ := dbtest.Open(t, t.TempDir())
	if _, err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	drops := []string{"ALTER TABLE work_records DROP COLUMN hidden_by", "ALTER TABLE work_records DROP COLUMN hidden_at", "ALTER TABLE work_records DROP COLUMN is_hidden"}
	if d.Dialect() == db.MySQL {
		drops = append([]string{"ALTER TABLE work_records DROP FOREIGN KEY fk_work_records_hidden_by"}, drops...)
	}
	for _, q := range drops {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := 0; i < 2; i++ {
		notes, err := d.Migrate()
		if err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
		if i == 0 && len(notes) != 3 {
			t.Fatalf("notes = %v", notes)
		}
	}
	if _, err := db.All(d, "SELECT is_hidden, hidden_by, hidden_at FROM work_records"); err != nil {
		t.Fatalf("hidden columns missing: %v", err)
	}
}
