package service

import (
	"path/filepath"
	"testing"

	"work-ledger/internal/db"
	"work-ledger/internal/dbtest"
)

// 迁移往返：当前测试库（SQLite 或 CI 中的 MySQL / PostgreSQL）→ SQLite → 另一个同类型新库，数据与密钥设置一致。
func TestMigrateDatabaseRoundTrip(t *testing.T) {
	s, _ := newTestServices(t)
	a, _ := s.Depts.Create(DepartmentInput{Name: "运维部", IsActive: true})
	b, _ := s.Depts.Create(DepartmentInput{Name: "运维一班", ParentID: a, IsActive: true})
	if _, err := s.Categories.SaveWork(WorkCategoryInput{Name: "机房巡检", DepartmentID: b, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AutoBackup.SaveConfig(AutoBackupConfig{Time: "02:00", KeepLocal: 7, KeepFTP: 30, FTP: FTPConfig{Host: "10.0.0.1", Port: 21, Password: "FtpSecret"}}); err != nil {
		t.Fatal(err)
	}
	want, err := s.Backup.Counts()
	if err != nil {
		t.Fatal(err)
	}

	lite, err := db.Open(filepath.Join(t.TempDir(), "mid.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer lite.Close()
	if _, err := MigrateDatabase(s.Core, s.Core.DB, lite); err != nil {
		t.Fatalf("to sqlite: %v", err)
	}
	if _, err := MigrateDatabase(s.Core, s.Core.DB, lite); err == nil {
		t.Fatal("non-empty target should be refused")
	}

	back, _ := dbtest.Open(t, t.TempDir())
	res, err := MigrateDatabase(s.Core, lite, back)
	if err != nil {
		t.Fatalf("back to %s: %v", dbtest.Driver(), err)
	}
	for k, v := range want {
		if k != "app_settings" && res.Counts[k] != v {
			t.Errorf("%s = %d, want %d", k, res.Counts[k], v)
		}
	}
	if v, _ := db.Value(back, "SELECT value FROM app_settings WHERE name = ?", SettingFTPPassword); v != "FtpSecret" {
		t.Fatalf("ftp password = %v", v)
	}
	if p, _ := db.Value(back, "SELECT path FROM department_paths WHERE id = ?", b); p != "运维部 / 运维一班" {
		t.Fatalf("department path = %v", p)
	}
	// 迁移后新增数据：自增 id 不冲突（PostgreSQL 序列已同步）
	svc := New(back, s.Core.Cfg, s.Core.Log)
	if _, err := svc.Depts.Create(DepartmentInput{Name: "迁移后新部门", IsActive: true}); err != nil {
		t.Fatalf("insert after migrate: %v", err)
	}
	admin, err := svc.Auth.LoadActiveUser(userByName(t, s, "admin").Int("id"))
	if err != nil || admin == nil {
		t.Fatalf("admin after migrate: %v", err)
	}
}

// CI 中设置了 WL_TEST_DRIVER 时，必须真的连到对应数据库（防止测试悄悄退回 SQLite）。
func TestTestDatabaseDriver(t *testing.T) {
	s, _ := newTestServices(t)
	if got := string(s.Core.DB.Dialect()); got != dbtest.Driver() {
		t.Fatalf("dialect = %s, want %s", got, dbtest.Driver())
	}
	t.Logf("测试数据库：%s", s.Core.DB.ServerLabel())
}
