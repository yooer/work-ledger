package service

import (
	"strings"
	"testing"

	"work-ledger/internal/db"
)

func catID(t *testing.T, s *Services, name string, dept int64) int64 {
	t.Helper()
	v, err := db.Int(s.Core.DB, "SELECT id FROM work_categories WHERE name = ? AND COALESCE(department_id, 0) = ?", name, dept)
	if err != nil || v == 0 {
		t.Fatalf("category %s@%d not found: %v", name, dept, err)
	}
	return v
}

func hasID(rows []Row, id int64) bool {
	for _, r := range rows {
		if r.Int("id") == id {
			return true
		}
	}
	return false
}

// 工作分类按部门：本部门 + 下级部门 + 未指定部门的分类可用，上级、平级部门的不可用。
func TestCategoryDepartmentScope(t *testing.T) {
	s, _ := newTestServices(t)
	admin := userByName(t, s, "admin")
	a, _ := s.Depts.Create(DepartmentInput{Name: "运维部", IsActive: true})
	b, _ := s.Depts.Create(DepartmentInput{Name: "运维一班", ParentID: a, IsActive: true})
	c, _ := s.Depts.Create(DepartmentInput{Name: "市场部", IsActive: true})

	// 所属部门必选；同一部门不能重名，不同部门可以同名
	if _, err := s.Categories.SaveWork(WorkCategoryInput{Name: "无部门", IsActive: true}); err == nil {
		t.Fatal("category without department accepted")
	}
	for _, in := range []WorkCategoryInput{
		{Name: "部门例会", DepartmentID: a, IsActive: true},
		{Name: "机房巡检", DepartmentID: b, IsActive: true},
		{Name: "部门例会", DepartmentID: c, IsActive: true},
	} {
		if _, err := s.Categories.SaveWork(in); err != nil {
			t.Fatalf("save %v: %v", in, err)
		}
	}
	if _, err := s.Categories.SaveWork(WorkCategoryInput{Name: "部门例会", DepartmentID: a, IsActive: true}); err == nil {
		t.Fatal("duplicate name in same department accepted")
	}
	meetA, inspectB, meetC := catID(t, s, "部门例会", a), catID(t, s, "机房巡检", b), catID(t, s, "部门例会", c)
	legacy := catID(t, s, "内部管理", 0) // 升级/安装遗留：未指定部门，所有部门可用

	cats, _, err := s.Categories.UsableFor(a, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !hasID(cats, meetA) || !hasID(cats, inspectB) || !hasID(cats, legacy) || hasID(cats, meetC) {
		t.Fatalf("usable for 运维部 wrong: %v", cats)
	}
	cats, _, _ = s.Categories.UsableFor(b, 0, 0)
	if hasID(cats, meetA) || !hasID(cats, inspectB) || !hasID(cats, legacy) {
		t.Fatalf("lower department must not see upper categories: %v", cats)
	}

	if _, err := s.Users.Create(UserInput{Username: "op1", Name: "值班员", PositionID: positionID(t, s, "员工"), DepartmentID: b, IsActive: true, Password: "Passw0rd!"}); err != nil {
		t.Fatal(err)
	}
	op := userByName(t, s, "op1")
	in := RecordInput{WorkCategoryID: meetA, Content: "上级分类", WorkAt: "2026-11-02 09:00:00", Difficulty: 1, WorkCount: 1, ParticipantIDs: []int64{op.Int("id")}}
	if _, err := s.Records.Create(op, in); err == nil {
		t.Fatal("record with upper department category accepted")
	}
	in.WorkCategoryID, in.Content = inspectB, "本部门分类"
	id, err := s.Records.Create(op, in)
	if err != nil {
		t.Fatal(err)
	}
	// 分类改到其他部门后，已有台账编辑时仍可保留原分类，但不能新选
	if _, err := s.Categories.SaveWork(WorkCategoryInput{ID: inspectB, Name: "机房巡检", DepartmentID: c, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Core.ScopeFor(op)
	in.Content = "编辑保留原分类"
	if err := s.Records.Update(sc, op, id, in); err != nil {
		t.Fatalf("keep current category: %v", err)
	}
	cats, _, _ = s.Categories.UsableFor(b, inspectB, 0)
	if !hasID(cats, inspectB) {
		t.Fatal("current category should be kept in form options")
	}

	// 同名分类在台账和报告中带部门名
	if _, err := s.Users.Create(UserInput{Username: "mk1", Name: "市场员", PositionID: positionID(t, s, "员工"), DepartmentID: c, IsActive: true, Password: "Passw0rd!"}); err != nil {
		t.Fatal(err)
	}
	mk := userByName(t, s, "mk1")
	rid, err := s.Records.Create(mk, RecordInput{WorkCategoryID: meetC, Content: "市场例会", WorkAt: "2026-11-02 10:00:00", Difficulty: 1, WorkCount: 1, ParticipantIDs: []int64{mk.Int("id")}})
	if err != nil {
		t.Fatal(err)
	}
	asc, _ := s.Core.ScopeFor(admin)
	rec, err := s.Records.Find(asc, rid)
	if err != nil || rec.Str("category_name") != "部门例会（市场部）" {
		t.Fatalf("category label = %q %v", rec.Str("category_name"), err)
	}
	d, err := s.Weekly.Data("2026-11-02", "2026-11-08", ReportTarget{}, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cs := range d.Summary.ByCategory {
		found = found || cs.Category == "部门例会（市场部）"
	}
	if !found {
		t.Fatalf("report categories = %+v", d.Summary.ByCategory)
	}

	// 管理页按部门筛选（含下级）、未指定部门
	list, _, err := s.Categories.Overview(a, false)
	if err != nil || !hasID(list, meetA) || hasID(list, meetC) || hasID(list, legacy) {
		t.Fatalf("overview by dept: %v %v", list, err)
	}
	list, _, _ = s.Categories.Overview(0, true)
	if !hasID(list, legacy) || hasID(list, meetA) {
		t.Fatalf("overview unassigned: %v", list)
	}
}

// 导入：按记录人部门匹配分类；同名无法确定时报错，可用“名称（部门）”指定。
func TestImportCategoryByDepartment(t *testing.T) {
	s, _ := newTestServices(t)
	admin := userByName(t, s, "admin")
	a, _ := s.Depts.Create(DepartmentInput{Name: "运维部", IsActive: true})
	c, _ := s.Depts.Create(DepartmentInput{Name: "市场部", IsActive: true})
	for _, d := range []int64{a, c} {
		if _, err := s.Categories.SaveWork(WorkCategoryInput{Name: "部门例会", DepartmentID: d, IsActive: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Users.Create(UserInput{Username: "op1", Name: "值班员", PositionID: positionID(t, s, "员工"), DepartmentID: a, IsActive: true, Password: "Passw0rd!"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(s.Core.DB, "UPDATE users SET department_id = NULL WHERE id = ?", admin.Int("id")); err != nil {
		t.Fatal(err)
	}
	csv := "工作分类,工作内容,日期,记录人\n" +
		"部门例会,运维的例会,2026-11-02 09:00,op1\n" +
		"部门例会,无部门记录人,2026-11-02 10:00,admin\n" +
		"部门例会（市场部）,指定部门,2026-11-02 11:00,admin\n"
	rows, _, err := s.Import.Parse([]byte(csv), "csv")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Import.Import(rows, ImportOptions{DefaultCreatorID: admin.Int("id")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Failed != 1 || !strings.Contains(res.Errors[0].Message, "多个同名分类") {
		t.Fatalf("import result = %+v", res)
	}
	got, _ := db.Int(s.Core.DB, "SELECT work_category_id FROM work_records WHERE content = '运维的例会'")
	if got != catID(t, s, "部门例会", a) {
		t.Fatal("should match own department category")
	}
	got, _ = db.Int(s.Core.DB, "SELECT work_category_id FROM work_records WHERE content = '指定部门'")
	if got != catID(t, s, "部门例会", c) {
		t.Fatal("名称（部门） should match that department")
	}
}

// 备份：分类部门随备份恢复；FTP 密码不进备份文件，恢复后保留当前值。
func TestBackupCategoryDepartmentAndFTPSecret(t *testing.T) {
	s, _ := newTestServices(t)
	a, _ := s.Depts.Create(DepartmentInput{Name: "运维部", IsActive: true})
	if _, err := s.Categories.SaveWork(WorkCategoryInput{Name: "部门例会", DepartmentID: a, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AutoBackup.SaveConfig(AutoBackupConfig{Time: "02:00", KeepLocal: 7, KeepFTP: 30,
		FTP: FTPConfig{Host: "127.0.0.1", Port: 21, User: "u", Password: "SecretFTP!", Dir: "/bk"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Backup.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SecretFTP!") || strings.Contains(string(raw), SettingFTPPassword) {
		t.Fatal("FTP password leaked into backup")
	}
	if !strings.Contains(string(raw), SettingFTPHost) {
		t.Fatal("other FTP settings should be kept in backup")
	}
	data, err := s.Backup.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup.Restore(data); err != nil {
		t.Fatal(err)
	}
	s.Settings.ClearCache()
	if s.Settings.Get(SettingFTPPassword, "") != "SecretFTP!" {
		t.Fatal("FTP password should survive restore")
	}
	catID(t, s, "部门例会", a)
}
