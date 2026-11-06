package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"work-ledger/internal/apperr"
	"work-ledger/internal/config"
	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

// fixedNow 2026-11-05（周四）12:00，演示数据落在 2026-10-26 所在的一周。
var fixedNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))

func newTestServices(t *testing.T) (*Services, string) {
	t.Helper()
	timeutil.SetLocation("Asia/Shanghai")
	timeutil.SetNowFunc(func() time.Time { return fixedNow })
	t.Cleanup(func() { timeutil.SetNowFunc(time.Now) })
	home := t.TempDir()
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AI.APIKey = ""
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(home, "data", "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s := New(d, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := Install(s.Core, InstallOptions{AdminPassword: "Admin12345", Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, res.DemoPassword
}

func userByName(t *testing.T, s *Services, username string) Row {
	t.Helper()
	r, err := db.One(s.Core.DB, "SELECT id FROM users WHERE username = ?", username)
	if err != nil || r == nil {
		t.Fatalf("user %s: %v", username, err)
	}
	u, err := s.Auth.LoadActiveUser(r.Int("id"))
	if err != nil || u == nil {
		t.Fatalf("load %s: %v", username, err)
	}
	return u
}

// fakeKV 模拟会话。
type fakeKV map[string]any

func (f fakeKV) Get(k string) any { return f[k] }
func (f fakeKV) GetInt(k string) int64 {
	switch v := f[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}
func (f fakeKV) Set(k string, v any) { f[k] = v }
func (f fakeKV) Delete(keys ...string) {
	for _, k := range keys {
		delete(f, k)
	}
}
func (f fakeKV) Regenerate() {}
func (f fakeKV) Destroy() {
	for k := range f {
		delete(f, k)
	}
}

func TestImportHashMatchesPHP(t *testing.T) {
	// 期望值由 json_encode(JSON_UNESCAPED_UNICODE|JSON_UNESCAPED_SLASHES) + sha256 计算。
	content := "完成专线开通/验收 \"A\"\n<测试>"
	if got := importHash("2026-03-02 09:30:00", 1, nil, content, 3, 2, 5, []int64{9, 2, 5, 2}); got != "d85a17645c6f21866fc28dd255756c946e3f25291190e10dda8e2fabf83c6fcd" {
		t.Errorf("hash(null project) = %s", got)
	}
	p := int64(7)
	if got := importHash("2026-03-02 09:30:00", 1, &p, content, 3, 2, 5, []int64{5, 9, 2}); got != "187b3f73791556c691c03caedbfa2ade1b669b666b06c3913befef90f174066f" {
		t.Errorf("hash(project 7) = %s", got)
	}
}

func TestLoginAndChangePassword(t *testing.T) {
	s, demo := newTestServices(t)
	kv := fakeKV{}
	if res, _, err := s.Auth.Login(kv, "zhangsan", demo); err != nil || res != LoginOK || kv.GetInt("user_id") == 0 {
		t.Fatalf("login: %v %v", res, err)
	}
	if _, _, err := s.Auth.Login(fakeKV{}, "E1001", demo); err != nil {
		t.Fatalf("login by employee_no: %v", err)
	}
	kv2 := fakeKV{}
	for i := 0; i < 8; i++ {
		_, _, _ = s.Auth.Login(kv2, "zhangsan", "bad-password")
	}
	if _, _, err := s.Auth.Login(kv2, "zhangsan", demo); err == nil || !strings.Contains(err.Error(), "次数过多") {
		t.Fatalf("expected lockout, got %v", err)
	}
	u := userByName(t, s, "zhangsan")
	if err := s.Auth.ChangePassword(u.Int("id"), ChangePasswordInput{Current: "x", New: "NewPass123"}); err == nil {
		t.Fatal("wrong current password accepted")
	}
	if err := s.Auth.ChangePassword(u.Int("id"), ChangePasswordInput{Current: demo, New: "NewPass123"}); err != nil {
		t.Fatal(err)
	}
	if res, _, err := s.Auth.Login(fakeKV{}, "zhangsan", "NewPass123"); err != nil || res != LoginOK {
		t.Fatal("new password should work")
	}
}

func TestManagerScopeAndEditRules(t *testing.T) {
	s, _ := newTestServices(t)
	manager := userByName(t, s, "manager")
	sc, err := s.Core.ScopeFor(manager)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Records.List(sc, RecordFilter{}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 { // 集客部及其子部门：张三 2 条、李四 2 条、经理 1 条；王五（运维）不可见
		t.Fatalf("manager sees %d records", res.Total)
	}
	wangwu := userByName(t, s, "wangwu")
	wsc, _ := s.Core.ScopeFor(wangwu)
	own, _ := s.Records.List(wsc, RecordFilter{}, 1, 50)
	if own.Total != 1 {
		t.Fatalf("employee sees %d", own.Total)
	}
	// 经理编辑本部门他人记录：默认允许（领导可修改下属台账开关默认开启）
	other := res.Items[0]
	if other.Int("created_by") == manager.Int("id") {
		other = res.Items[1]
	}
	edit := RecordInput{WorkCategoryID: 2, Content: "经理修正", WorkAt: "2026-10-27 10:00:00", Difficulty: 3, WorkCount: 1, ParticipantIDs: []int64{manager.Int("id")}}
	if err := s.Records.Update(sc, manager, other.Int("id"), edit); err != nil {
		t.Fatalf("manager edit with switch on: %v", err)
	}
	after, _ := s.Records.Find(sc, other.Int("id"))
	if after.Int("created_by") != other.Int("created_by") || after.Int("updated_by") != manager.Int("id") {
		t.Fatalf("creator must stay, editor must be manager: %v / %v", after["created_by"], after["updated_by"])
	}
	// 关闭开关后 → 403
	if err := s.Settings.SetLeaderCanEdit(false); err != nil {
		t.Fatal(err)
	}
	err = s.Records.Update(sc, manager, other.Int("id"), edit)
	if e, ok := apperr.As(err); !ok || e.Status != 403 {
		t.Fatalf("expected 403 with switch off, got %v", err)
	}
	// 员工查看他人记录 → 404
	if _, err := s.Records.Find(wsc, other.Int("id")); err == nil {
		t.Fatal("employee should not see others' record")
	}
	// 生成范围：经理只能选择本部门
	if _, err := s.Core.ResolveTarget(manager, "", wangwu.Int("department_id")); err == nil {
		t.Fatal("manager should not target other department")
	}
	if _, err := s.Core.ResolveTarget(manager, "dept:"+fmt.Sprint(wangwu.Int("department_id")), 0); err == nil {
		t.Fatal("manager should not target other department via target key")
	}
	if _, err := s.Core.ResolveTarget(manager, "user:"+fmt.Sprint(wangwu.Int("id")), 0); err == nil {
		t.Fatal("manager should not target a person outside team")
	}
}

func setSupervisor(t *testing.T, s *Services, username string, supervisor Row) {
	t.Helper()
	u := userByName(t, s, username)
	in := UserInput{Username: u.Str("username"), Name: u.Str("name"), EmployeeNo: u.Str("employee_no"), Role: u.Str("role"),
		DepartmentID: u.Int("department_id"), IsActive: true}
	if supervisor != nil {
		in.SupervisorID = supervisor.Int("id")
	}
	admin := userByName(t, s, "admin")
	if err := s.Users.Update(admin.Int("id"), u.Int("id"), in); err != nil {
		t.Fatalf("set supervisor of %s: %v", username, err)
	}
}

func TestSupervisorScopeTeamReportsAndExport(t *testing.T) {
	s, _ := newTestServices(t)
	wangwu := userByName(t, s, "wangwu") // 运维一组，1 条台账
	zhangsan := userByName(t, s, "zhangsan")
	lisi := userByName(t, s, "lisi")
	// 跨部门：张三向王五汇报，李四向张三汇报（王五的间接下属）
	setSupervisor(t, s, "zhangsan", wangwu)
	setSupervisor(t, s, "lisi", zhangsan)
	wangwu = userByName(t, s, "wangwu")
	if !wangwu.Bool("has_subordinates") {
		t.Fatal("has_subordinates should be true")
	}
	sc, err := s.Core.ScopeFor(wangwu)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.UserIDs) != 2 || !sc.IsTeamScope() {
		t.Fatalf("subordinates = %v", sc.UserIDs)
	}
	res, _ := s.Records.List(sc, RecordFilter{}, 1, 50)
	if res.Total != 5 { // 王五 1 + 张三 2 + 李四 2
		t.Fatalf("supervisor sees %d", res.Total)
	}
	// 循环汇报被拒绝：王五的主管不能设为李四（王五的间接下属）
	u := userByName(t, s, "wangwu")
	admin := userByName(t, s, "admin")
	err = s.Users.Update(admin.Int("id"), u.Int("id"), UserInput{Username: u.Str("username"), Name: u.Str("name"), EmployeeNo: u.Str("employee_no"),
		Role: u.Str("role"), DepartmentID: u.Int("department_id"), IsActive: true, SupervisorID: lisi.Int("id")})
	if e, ok := apperr.As(err); !ok || e.Fields["supervisor_id"] == "" {
		t.Fatalf("expected cycle rejection, got %v", err)
	}
	// 主管可以修改下属台账（开关默认开启）
	var lisiRec Row
	for _, r := range res.Items {
		if r.Int("created_by") == lisi.Int("id") {
			lisiRec = r
		}
	}
	if !s.Records.CanEditRecord(sc, lisiRec, wangwu) {
		t.Fatal("supervisor should edit indirect subordinate's record")
	}
	// 团队周报：范围 = 王五 + 全部下属
	tgt, err := s.Core.ResolveTarget(wangwu, "team", 0)
	if err != nil || tgt.TeamLeaderID != wangwu.Int("id") {
		t.Fatalf("team target: %+v %v", tgt, err)
	}
	data, err := s.Weekly.Data("2026-10-26", "2026-11-01", tgt, true, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary.RecordCount != 5 || !strings.Contains(data.Scope.Label, "团队") {
		t.Fatalf("team weekly: %d %q", data.Summary.RecordCount, data.Scope.Label)
	}
	id, err := s.Weekly.Save(data, "# 团队周报", "ai", wangwu.Int("id"))
	if err != nil {
		t.Fatal(err)
	}
	// 张三（团队成员之一，也是李四的主管）看不到上级的团队周报；管理员可以
	if _, err := s.Weekly.Find(userByName(t, s, "zhangsan"), id); err == nil {
		t.Fatal("subordinate should not see leader's team report")
	}
	if _, err := s.Weekly.Find(admin, id); err != nil {
		t.Fatal(err)
	}
	// 选择单个下属
	if _, err := s.Core.ResolveTarget(wangwu, "user:"+fmt.Sprint(lisi.Int("id")), 0); err != nil {
		t.Fatalf("supervisor should target indirect subordinate: %v", err)
	}
	// 周报中心导出：记录范围与周报统计一致
	esc, f, err := s.Core.TargetRecords(wangwu, tgt, "2026-10-26", "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.Records.ExportRows(esc, f, 1000)
	if err != nil || len(rows) != 5 {
		t.Fatalf("export rows = %d %v", len(rows), err)
	}
	// 下拉选项含“我的团队”与两名下属
	groups, err := s.Core.ReportTargetOptions(wangwu)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, g := range groups {
		for _, o := range g.Options {
			if o.Value == "team" || strings.HasPrefix(o.Value, "user:") {
				n++
			}
		}
	}
	if n != 3 {
		t.Fatalf("target options = %+v", groups)
	}
	// 员工（无下属）不能选团队
	if _, err := s.Core.ResolveTarget(userByName(t, s, "lisi"), "team", 0); err == nil {
		t.Fatal("employee without subordinates should not target team")
	}
}

func TestOrgChart(t *testing.T) {
	s, _ := newTestServices(t)
	wangwu := userByName(t, s, "wangwu")
	setSupervisor(t, s, "zhangsan", wangwu)
	setSupervisor(t, s, "lisi", userByName(t, s, "zhangsan"))
	trees, unassigned, err := s.Users.OrgChart()
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].Name != wangwu.Str("name") || trees[0].TeamSize != 2 || len(trees[0].Children) != 1 || len(trees[0].Children[0].Children) != 1 {
		t.Fatalf("trees = %+v", trees)
	}
	if len(unassigned) != 2 { // admin、经理
		t.Fatalf("unassigned = %d", len(unassigned))
	}
	// 数据异常形成环时不会死循环
	if _, err := s.Core.DB.Exec("UPDATE users SET supervisor_id = ? WHERE id = ?", userByName(t, s, "lisi").Int("id"), wangwu.Int("id")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Users.OrgChart(); err != nil {
		t.Fatal(err)
	}
	if ids, err := subordinateIDs(s.Core.DB, wangwu.Int("id")); err != nil || len(ids) != 2 {
		t.Fatalf("cyclic subordinates = %v %v", ids, err)
	}
}

func TestClearData(t *testing.T) {
	s, _ := newTestServices(t)
	admin := userByName(t, s, "admin")
	if _, err := s.Backup.ClearData(admin, "wrong", ClearOptions{Categories: true, Reports: true}); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.Backup.ClearData(userByName(t, s, "manager"), "x", ClearOptions{}); err == nil {
		t.Fatal("non-admin allowed")
	}
	// 只清台账、保留分类
	res, err := s.Backup.ClearData(admin, "Admin12345", ClearOptions{Categories: false, Reports: false})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted["work_records"] != 6 || res.Snapshot == "" {
		t.Fatalf("clear result = %+v", res)
	}
	counts, _ := s.Backup.Counts()
	if counts["work_records"] != 0 || counts["work_record_participants"] != 0 || counts["work_categories"] == 0 || counts["users"] == 0 || counts["departments"] == 0 {
		t.Fatalf("counts after clear = %v", counts)
	}
	// 清空分类
	if _, err := s.Backup.ClearData(admin, "Admin12345", ClearOptions{Categories: true, Reports: true}); err != nil {
		t.Fatal(err)
	}
	counts, _ = s.Backup.Counts()
	if counts["work_categories"] != 0 || counts["project_categories"] != 0 || counts["app_settings"] == 0 {
		t.Fatalf("counts after full clear = %v", counts)
	}
	// 清空前备份可以恢复
	p, err := s.Backup.LocalPath(res.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	data, err := s.Backup.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup.Restore(data); err != nil {
		t.Fatal(err)
	}
	counts, _ = s.Backup.Counts()
	if counts["work_records"] != 6 {
		t.Fatalf("restored records = %d", counts["work_records"])
	}
}

func TestSubcategoryForAnyCategory(t *testing.T) {
	s, _ := newTestServices(t)
	admin := userByName(t, s, "admin")
	// 非集客分类“日常维护”（id 2）新建子分类
	if _, err := s.Categories.SaveProject(ProjectInput{WorkCategoryID: 2, Name: "机房巡检", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	pid, _ := db.Int(s.Core.DB, "SELECT id FROM project_categories WHERE work_category_id = 2 AND name = '机房巡检'")
	base := RecordInput{WorkCategoryID: 2, Content: "巡检", WorkAt: "2026-11-01 09:00:00", Difficulty: 2, WorkCount: 1, ParticipantIDs: []int64{admin.Int("id")}}
	if _, err := s.Records.Create(admin, base); err == nil {
		t.Fatal("subcategory should be required when category has subcategories")
	}
	base.ProjectCategoryID = pid
	id, err := s.Records.Create(admin, base)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Core.ScopeFor(admin)
	rec, _ := s.Records.Find(sc, id)
	if rec.Int("project_category_id") != pid {
		t.Fatal("subcategory not saved")
	}
	// 没有子分类的分类不需要选
	if _, err := s.Records.Create(admin, RecordInput{WorkCategoryID: 3, Content: "例会", WorkAt: "2026-11-01 10:00:00", Difficulty: 1, WorkCount: 1, ParticipantIDs: []int64{admin.Int("id")}}); err != nil {
		t.Fatal(err)
	}
	// 子分类必须属于所选分类
	base.ProjectCategoryID = 1 // 集客业务的子分类
	if _, err := s.Records.Create(admin, base); err == nil {
		t.Fatal("foreign subcategory accepted")
	}
}

func TestRecordBusinessValidation(t *testing.T) {
	s, _ := newTestServices(t)
	admin := userByName(t, s, "admin")
	_, err := s.Records.Create(admin, RecordInput{WorkCategoryID: 1, Content: "集客缺项目", WorkAt: "2026-11-01 09:00:00", Difficulty: 3, WorkCount: 1, ParticipantIDs: []int64{admin.Int("id")}})
	if e, ok := apperr.As(err); !ok || e.Fields["project_category_id"] == "" {
		t.Fatalf("expected subcategory validation, got %v", err)
	}
	// 子分类 3 属于“集客业务”，不能挂到“日常维护”
	_, err = s.Records.Create(admin, RecordInput{WorkCategoryID: 2, ProjectCategoryID: 3, Content: "日常巡检", WorkAt: "2026-11-01 09:00:00", Difficulty: 2, WorkCount: 1, ParticipantIDs: []int64{admin.Int("id")}})
	if e, ok := apperr.As(err); !ok || e.Fields["project_category_id"] == "" {
		t.Fatalf("expected foreign subcategory rejection, got %v", err)
	}
}

func TestWeekCycleFridayToThursday(t *testing.T) {
	s, _ := newTestServices(t)
	if err := s.Settings.SaveWeekCycle(5, 3); err == nil {
		t.Fatal("invalid cycle accepted")
	}
	if err := s.Settings.SaveWeekCycle(5, 4); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings.StartOfCycleFor("2026-11-05"); got != "2026-10-30" {
		t.Fatalf("start = %s", got)
	}
	if got := s.Settings.DefaultWeekStart(); got != "2026-10-23" {
		t.Fatalf("default = %s", got)
	}
	if s.Settings.WeekCycleLabel() != "周五 至 周四" {
		t.Fatal(s.Settings.WeekCycleLabel())
	}
}

func TestWeeklyAnnualPeriodDataAndPrompt(t *testing.T) {
	s, _ := newTestServices(t)
	d, err := s.Weekly.Data("2026-10-26", "", ReportTarget{}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary.RecordCount != 6 || d.Summary.TotalWorkCount != 8 || len(*d.Summary.ByDay) != 7 || d.Scope.Label != "全公司" {
		t.Fatalf("weekly summary = %+v", d.Summary)
	}
	msgs := s.Weekly.Prompt(d, "concise")
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "【逐条工作台账】") || !strings.Contains(msgs[1].Content, "6. ") || !strings.Contains(msgs[1].Content, "500-700 字") {
		t.Fatalf("prompt = %s", msgs[1].Content)
	}
	if strings.Contains(msgs[1].Content, `"by_employee"`) {
		t.Fatal("prompt summary should drop by_employee")
	}
	a, err := s.Annual.Data(2026, ReportTarget{DepartmentID: 1}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(*a.Summary.ByMonth) != 12 || len(*a.Summary.ByQuarter) != 4 || a.Summary.RecordCount != 5 || a.Summary.ByDay != nil {
		t.Fatalf("annual = %+v", a.Summary)
	}
	p, err := s.Period.Data("2026-10-01", "2026-12-31", ReportTarget{}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Period.Label != "2026 年第4季度" || !*p.Period.Partial || p.Period.Days != 92 {
		t.Fatalf("period = %+v", p.Period)
	}
	raw, _ := json.Marshal(d)
	if !strings.HasPrefix(string(raw), `{"week_start":"2026-10-26","week_end":"2026-11-01","scope":`) {
		t.Fatalf("json order = %.120s", raw)
	}
	if PeriodLabel("2026-02-01", "2026-02-28") != "2026 年 2 月" || PeriodLabel("2026-01-01", "2026-12-31") != "2026 年度" {
		t.Fatal("period label")
	}
}

func TestBackupRoundTrip(t *testing.T) {
	s, _ := newTestServices(t)
	raw, err := s.Backup.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := Encode(raw, true)
	data, err := s.Backup.Decode(gz)
	if err != nil {
		t.Fatal(err)
	}
	admin := userByName(t, s, "admin")
	if _, err := s.Records.Create(admin, RecordInput{WorkCategoryID: 2, Content: "恢复后应消失", WorkAt: "2026-11-01 09:00:00", Difficulty: 2, WorkCount: 1, ParticipantIDs: []int64{admin.Int("id")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup.Restore(data); err != nil {
		t.Fatal(err)
	}
	n, _ := db.Int(s.Core.DB, "SELECT COUNT(*) FROM work_records")
	if n != 6 {
		t.Fatalf("records after restore = %d", n)
	}
	raw2, _ := s.Backup.ExportJSON()
	var a, b map[string]any
	_ = json.Unmarshal(raw, &a)
	_ = json.Unmarshal(raw2, &b)
	if fmt.Sprint(a["tables"]) != fmt.Sprint(b["tables"]) {
		t.Fatal("tables differ after round trip")
	}
}

// 按 PHP 版 BackupService 格式构造的备份：PHP 原生 $2y$ 密码、TOTP 字段、整数与 null 混合。
const phpBackupSample = `{"format":"work-ledger-backup","version":1,"created_at":"2026-03-01T10:00:00+08:00","app_name":"工作台账","source_db_driver":"sqlite",
"counts":{"departments":2,"users":2,"work_categories":2,"project_categories":1,"work_records":1,"work_record_participants":2,"weekly_reports":1,"annual_reports":0,"period_reports":0,"app_settings":2},
"tables":{
"departments":[{"id":1,"name":"集客部","parent_id":null,"is_active":1,"sort_order":10,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"},{"id":2,"name":"集客一组","parent_id":1,"is_active":1,"sort_order":10,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"}],
"users":[{"id":1,"username":"rasmus","password_hash":"$2y$10$.vGA1O9wmRjrwAVXD98HNOgsNpDczlqm3Jq7KnEd1rVAGv3Fykk1a","name":"管理员","employee_no":null,"role":"admin","department_id":1,"phone":null,"email":null,"is_active":1,"totp_secret":null,"totp_enabled":0,"totp_recovery_codes":null,"totp_last_used_step":null,"last_login_at":null,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"},
{"id":7,"username":"zhang","password_hash":"$2y$10$.vGA1O9wmRjrwAVXD98HNOgsNpDczlqm3Jq7KnEd1rVAGv3Fykk1a","name":"张三","employee_no":"E1","role":"employee","department_id":2,"phone":"13800000000","email":null,"is_active":1,"totp_secret":"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ","totp_enabled":1,"totp_recovery_codes":"[]","totp_last_used_step":59000000,"last_login_at":"2026-02-01 08:00:00","created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"}],
"work_categories":[{"id":1,"name":"集客业务","is_customer_project":1,"is_active":1,"sort_order":1,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"},{"id":2,"name":"日常维护","is_customer_project":0,"is_active":1,"sort_order":2,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"}],
"project_categories":[{"id":3,"work_category_id":1,"name":"集团专线","is_active":1,"sort_order":1,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"}],
"work_records":[{"id":10,"work_category_id":1,"project_category_id":3,"content":"开通专线","work_at":"2026-02-02 09:30:00","difficulty":4,"work_count":2,"department_id":2,"created_by":7,"created_at":"2026-02-02 10:00:00","updated_by":7,"updated_at":"2026-02-02 10:00:00","import_hash":null}],
"work_record_participants":[{"work_record_id":10,"user_id":1},{"work_record_id":10,"user_id":7}],
"weekly_reports":[{"id":1,"week_start":"2026-02-02","week_end":"2026-02-08","department_id":null,"employee_id":null,"scope_label":"全公司","content":"## 周报","source":"ai","model":"deepseek-chat","created_by":1,"created_at":"2026-02-09 09:00:00"}],
"annual_reports":[],"period_reports":[],
"app_settings":[{"name":"week_end_weekday","value":"4","updated_at":"2026-01-01 00:00:00"},{"name":"week_start_weekday","value":"5","updated_at":"2026-01-01 00:00:00"}]}}`

func TestRestorePHPBackupSample(t *testing.T) {
	s, _ := newTestServices(t)
	data, err := s.Backup.Decode([]byte(phpBackupSample))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup.Restore(data); err != nil {
		t.Fatal(err)
	}
	s.Settings.ClearCache()
	if s.Settings.WeekCycleLabel() != "周五 至 周四" {
		t.Fatal("settings not restored")
	}
	if res, _, err := s.Auth.Login(fakeKV{}, "rasmus", "rasmuslerdorf"); err != nil || res != LoginOK {
		t.Fatalf("PHP password should log in: %v %v", res, err)
	}
	kv := fakeKV{}
	if res, _, err := s.Auth.Login(kv, "zhang", "rasmuslerdorf"); err != nil || res != LoginNeed2FA {
		t.Fatalf("2FA user should need second factor: %v %v", res, err)
	}
	admin := userByName(t, s, "rasmus")
	sc, _ := s.Core.ScopeFor(admin)
	rec, err := s.Records.Find(sc, 10)
	if err != nil || rec.Str("project_name") != "集团专线" || len(rec["participants"].([]Row)) != 2 {
		t.Fatalf("record = %v %v", rec, err)
	}
	// 缺少管理员的备份必须拒绝
	bad := strings.Replace(phpBackupSample, `"role":"admin"`, `"role":"employee"`, 1)
	if _, err := s.Backup.Decode([]byte(bad)); err == nil {
		t.Fatal("backup without admin accepted")
	}
	// 行数不符必须拒绝
	bad = strings.Replace(phpBackupSample, `"work_records":1,`, `"work_records":2,`, 1)
	if _, err := s.Backup.Decode([]byte(bad)); err == nil {
		t.Fatal("count mismatch accepted")
	}
}

func TestImportLegacyGBKCSV(t *testing.T) {
	s, _ := newTestServices(t)
	csvText := "工作分类,工作内容,日期,参与人员,难易度,次数,集客分类\n" +
		"集客业务,开通专线,2026/10/12 9:30,张三、李四,4,2,集团专线\n" +
		"日常维护,巡检机房,2026-10-13,赵六,,,\n" +
		"集客业务,缺项目,2026-10-14 10:00,张三,3,1,\n" +
		"其他工作,错误难度,2026-10-15,张三,9,1,\n" +
		"其他工作,,2026-10-15,张三,1,1,\n"
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(csvText))
	if err != nil {
		t.Fatal(err)
	}
	rows, format, err := s.Import.Parse(gbk, "csv")
	if err != nil || format != "csv" || len(rows) != 5 {
		t.Fatalf("parse: %v %s %d", err, format, len(rows))
	}
	admin := userByName(t, s, "admin")
	opt := ImportOptions{DefaultCreatorID: admin.Int("id"), AutoCreateCategories: true, AutoCreateUsers: true, ParticipantAsCreator: true}
	res, err := s.Import.Import(rows, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 3 || res.Failed != 2 || res.CreatedUsers != 1 || res.CreatedProjects != 1 || res.Errors[0].Row != 5 {
		t.Fatalf("result = %+v", res)
	}
	res2, _ := s.Import.Import(rows, opt)
	if res2.Duplicates != 3 || res2.Imported != 0 {
		t.Fatalf("second import = %+v", res2)
	}
	if v, ok := ParseImportDateTime("45658.5"); !ok || v != "2025-01-01 12:00:00" {
		t.Fatalf("excel serial = %s", v)
	}
}

func TestSchedulerAutoWeeklyOnce(t *testing.T) {
	s, _ := newTestServices(t)
	var calls int
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"## 自动周报"}}]}`))
	}))
	defer ai.Close()
	s.Core.Cfg.AI.APIKey, s.Core.Cfg.AI.BaseURL = "k", ai.URL
	sch := s.Scheduler
	sch.tick(context.Background())
	if calls != 0 {
		t.Fatal("auto weekly is disabled by default and must not run")
	}
	_ = s.Settings.SetMany(map[string]string{SettingAutoWeekly: "1", SettingAutoWeeklyTime: "08:00"})
	sch.attempts = map[string]attempt{}
	sch.tick(context.Background())
	sch.tick(context.Background())
	n, _ := db.Int(s.Core.DB, "SELECT COUNT(*) FROM weekly_reports WHERE week_start = '2026-10-26' AND department_id IS NULL AND employee_id IS NULL")
	if calls != 1 || n != 1 {
		t.Fatalf("calls=%d reports=%d", calls, n)
	}
	if st := sch.LastStatus(); st == nil || !st.OK {
		t.Fatalf("status = %+v", st)
	}
}

func TestBackupRoundTripWithSupervisorsAndTeamReports(t *testing.T) {
	s, _ := newTestServices(t)
	wangwu := userByName(t, s, "wangwu") // id 比张三大：恢复时需两阶段写入主管关系
	setSupervisor(t, s, "zhangsan", wangwu)
	tgt, _ := s.Core.ResolveTarget(userByName(t, s, "wangwu"), "team", 0)
	d, _ := s.Weekly.Data("2026-10-26", "2026-11-01", tgt, true, 100)
	if _, err := s.Weekly.Save(d, "团队", "ai", wangwu.Int("id")); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Backup.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"supervisor_id":`+fmt.Sprint(wangwu.Int("id"))) || !strings.Contains(string(raw), `"team_leader_id":`) {
		t.Fatal("backup should include supervisor_id and team_leader_id")
	}
	data, err := s.Backup.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup.Restore(data); err != nil {
		t.Fatal(err)
	}
	if got := userByName(t, s, "zhangsan").Int("supervisor_id"); got != wangwu.Int("id") {
		t.Fatalf("supervisor after restore = %d", got)
	}
	n, _ := db.Int(s.Core.DB, "SELECT COUNT(*) FROM weekly_reports WHERE team_leader_id = ?", wangwu.Int("id"))
	if n != 1 {
		t.Fatalf("team report after restore = %d", n)
	}
}
