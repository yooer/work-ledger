package service

import (
	"strings"
	"testing"

	"work-ledger/internal/db"
)

// 参与人员只能选台账所属部门及下级部门的人；编辑时原有参与人员保留。
func TestParticipantDepartmentScope(t *testing.T) {
	s, _ := newTestServices(t)
	manager, zhang, lisi := userByName(t, s, "manager"), userByName(t, s, "zhangsan"), userByName(t, s, "lisi")
	in := RecordInput{WorkCategoryID: 3, Content: "参与人员范围", WorkAt: "2026-11-03 09:00:00", Difficulty: 1, WorkCount: 1}

	// 张三（集客一组）不能选李四（集客二组，平级部门）
	in.ParticipantIDs = []int64{zhang.Int("id"), lisi.Int("id")}
	if _, err := s.Records.Create(zhang, in); err == nil || !strings.Contains(err.Error(), "本部门") {
		t.Fatalf("sibling department participant accepted: %v", err)
	}
	opts, err := s.Users.ParticipantOptionsFor(zhang.Int("department_id"), []int64{zhang.Int("id")})
	if err != nil {
		t.Fatal(err)
	}
	if hasID(opts, lisi.Int("id")) || hasID(opts, manager.Int("id")) || !hasID(opts, zhang.Int("id")) {
		t.Fatalf("options for 集客一组 wrong: %v", opts)
	}
	// 经理（集客部）可以选下级部门的张三、李四
	in.ParticipantIDs = []int64{manager.Int("id"), zhang.Int("id"), lisi.Int("id")}
	id, err := s.Records.Create(manager, in)
	if err != nil {
		t.Fatalf("parent department should include sub departments: %v", err)
	}
	// 李四调到运维一组后，编辑旧台账仍可保留他
	ops, _ := db.Int(s.Core.DB, "SELECT id FROM departments WHERE name = '运维一组'")
	if _, err := db.Exec(s.Core.DB, "UPDATE users SET department_id = ? WHERE id = ?", ops, lisi.Int("id")); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Core.ScopeFor(manager)
	in.Content = "编辑保留原参与人员"
	if err := s.Records.Update(sc, manager, id, in); err != nil {
		t.Fatalf("existing participant should be kept: %v", err)
	}
	wangwu := userByName(t, s, "wangwu")
	in.ParticipantIDs = append(in.ParticipantIDs, wangwu.Int("id"))
	if err := s.Records.Update(sc, manager, id, in); err == nil {
		t.Fatal("new out-of-scope participant accepted on edit")
	}
}

// 隐藏台账：权限（管理员 / 创建者 / 开关控制的领导，参与人员不行）、不计入总结与首页统计、列表照常显示、备份保留。
func TestHideRecords(t *testing.T) {
	s, _ := newTestServices(t)
	admin, manager, zhang, lisi := userByName(t, s, "admin"), userByName(t, s, "manager"), userByName(t, s, "zhangsan"), userByName(t, s, "lisi")
	base := RecordInput{WorkCategoryID: 3, WorkAt: "2026-11-03 09:00:00", Difficulty: 1, WorkCount: 1}
	r1in := base
	r1in.Content, r1in.ParticipantIDs = "经理记录、李四参与", []int64{manager.Int("id"), lisi.Int("id")}
	r1, err := s.Records.Create(manager, r1in)
	if err != nil {
		t.Fatal(err)
	}
	r2in := base
	r2in.Content, r2in.ParticipantIDs = "张三的重复记录", []int64{zhang.Int("id")}
	r2, err := s.Records.Create(zhang, r2in)
	if err != nil {
		t.Fatal(err)
	}
	weekCount := func() int {
		d, err := s.Weekly.Data("2026-11-02", "2026-11-08", ReportTarget{}, true, 100)
		if err != nil {
			t.Fatal(err)
		}
		return d.Summary.RecordCount
	}
	before := weekCount()
	dashBefore, _ := s.Dashboard.Build(admin)

	// 参与人员不能隐藏
	lsc, _ := s.Core.ScopeFor(lisi)
	if _, err := s.Records.SetHidden(lsc, lisi, r1, true); err == nil {
		t.Fatal("participant should not hide")
	}
	// 经理（开关默认开启）可以隐藏下属张三的台账
	msc, _ := s.Core.ScopeFor(manager)
	if _, err := s.Records.SetHidden(msc, manager, r2, true); err != nil {
		t.Fatalf("leader hide: %v", err)
	}
	if weekCount() != before-1 {
		t.Fatalf("hidden record still counted in weekly summary")
	}
	if dashAfter, _ := s.Dashboard.Build(admin); dashAfter.Week.RecordCount != dashBefore.Week.RecordCount-1 {
		t.Fatal("hidden record still counted on dashboard")
	}
	zsc, _ := s.Core.ScopeFor(zhang)
	list, err := s.Records.List(zsc, RecordFilter{InvolvedIDs: []int64{zhang.Int("id")}}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range list.Items {
		if r.Int("id") == r2 {
			found = r.Bool("is_hidden") && r.Str("hidden_by_name") == manager.Str("name")
		}
	}
	if !found {
		t.Fatal("hidden record should still be listed with hidden flag and hider")
	}
	// 编辑不会改变隐藏状态
	r2in.Content = "编辑后仍隐藏"
	if err := s.Records.Update(zsc, zhang, r2, r2in); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Int(s.Core.DB, "SELECT is_hidden FROM work_records WHERE id = ?", r2); v != 1 {
		t.Fatal("edit should keep hidden flag")
	}
	// 关闭开关后经理不能操作；创建者本人和管理员可以
	if err := s.Settings.SetLeaderPermissions(true, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Records.SetHidden(msc, manager, r2, false); err == nil {
		t.Fatal("leader should not unhide when switch is off")
	}
	if _, err := s.Records.SetHidden(zsc, zhang, r2, false); err != nil {
		t.Fatalf("creator unhide: %v", err)
	}
	if weekCount() != before {
		t.Fatal("unhidden record should be counted again")
	}
	asc, _ := s.Core.ScopeFor(admin)
	if _, err := s.Records.SetHidden(asc, admin, r1, true); err != nil {
		t.Fatalf("admin hide: %v", err)
	}
	// 备份往返保留隐藏状态
	raw, err := s.Backup.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	data, err := s.Backup.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup.Restore(data); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Int(s.Core.DB, "SELECT is_hidden FROM work_records WHERE id = ?", r1); v != 1 {
		t.Fatal("hidden flag lost after restore")
	}
	if v, _ := db.Int(s.Core.DB, "SELECT hidden_by FROM work_records WHERE id = ?", r1); v != admin.Int("id") {
		t.Fatal("hidden_by lost after restore")
	}
}
