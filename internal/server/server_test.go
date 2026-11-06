package server

import (
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"work-ledger/internal/config"
	"work-ledger/internal/service"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

var csrfRe = regexp.MustCompile(`name="csrf-token" content="([0-9a-f]+)"`)

func (c *client) get(path string) (int, string) {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if m := csrfRe.FindStringSubmatch(string(b)); m != nil {
		c.csrf = m[1]
	}
	return resp.StatusCode, string(b)
}

func (c *client) post(path, ctype, body string, withCSRF bool) (int, string) {
	req, _ := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	if withCSRF {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func setup(t *testing.T) (*httptest.Server, string) {
	home := t.TempDir()
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AI.APIToken = "tok"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, s, err := Bootstrap(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	res, err := service.Install(s.Core, service.InstallOptions{AdminPassword: "Admin12345", Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(cfg, s, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, res.DemoPassword
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func TestAjaxConventions(t *testing.T) {
	srv, _ := setup(t)
	c := newClient(t, srv.URL)
	if code, _ := c.get("/login"); code != 200 || c.csrf == "" {
		t.Fatalf("login page %d", code)
	}
	if code, _ := c.post("/auth/ajax", "application/json", `{"action":"login","username":"admin","password":"Admin12345"}`, false); code != 419 {
		t.Fatalf("missing CSRF should be 419, got %d", code)
	}
	if code, _ := c.post("/auth/ajax", "application/x-www-form-urlencoded", "action=login", true); code != 415 {
		t.Fatalf("non-JSON should be 415, got %d", code)
	}
	if code, body := c.post("/auth/ajax", "application/json", `{"username":"admin"}`, true); code != 400 || !strings.Contains(body, "action") {
		t.Fatalf("missing action: %d %s", code, body)
	}
	if code, body := c.post("/auth/ajax", "application/json", `{"action":"login","username":"","password":""}`, true); code != 422 || !strings.Contains(body, `"errors"`) {
		t.Fatalf("validator: %d %s", code, body)
	}
	if code, body := c.post("/auth/ajax", "application/json", `{"action":"login","username":"admin","password":"Admin12345"}`, true); code != 200 || !strings.Contains(body, `"redirect":"/"`) {
		t.Fatalf("login: %d %s", code, body)
	}
	for _, p := range []string{"/", "/records", "/my-records", "/records/create", "/people", "/departments", "/categories", "/reports/weekly", "/reports/yearly", "/reports/period", "/settings", "/backup", "/records/import", "/security", "/password", "/org-chart", "/team-records", "/people/3/edit"} {
		if code, body := c.get(p); code != 200 {
			t.Errorf("GET %s = %d %.200s", p, code, body)
		}
	}
	if code, body := c.post("/people/ajax", "application/json", `{"action":"nope"}`, true); code != 400 || !strings.Contains(body, "不支持的操作") {
		t.Fatalf("unknown action: %d %s", code, body)
	}
}

func TestPermissionsAndAPI(t *testing.T) {
	srv, demo := setup(t)
	c := newClient(t, srv.URL)
	c.get("/login")
	c.post("/auth/ajax", "application/json", `{"action":"login","username":"wangwu","password":"`+demo+`"}`, true)
	c.get("/")
	for p, want := range map[string]int{"/records": 403, "/people": 403, "/backup": 403, "/team-records": 403, "/org-chart": 403, "/my-records": 200} {
		if code, _ := c.get(p); code != want {
			t.Errorf("employee GET %s = %d, want %d", p, code, want)
		}
	}
	if code, _ := c.post("/people/ajax", "application/json", `{"action":"reset_totp","id":1}`, true); code != 403 {
		t.Errorf("employee ajax admin = %d", code)
	}
	resp, _ := http.Get(srv.URL + "/api/v1/weekly")
	if resp.StatusCode != 401 {
		t.Errorf("api without token = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/weekly?week_start=2026-01-05", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, _ = http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"week_start":"2026-01-05"`) {
		t.Errorf("api weekly = %d %.200s", resp.StatusCode, body)
	}
	resp, _ = http.Get(srv.URL + "/assets/libs/tabler/css/tabler.min.css")
	if resp.StatusCode != 200 {
		t.Errorf("static asset = %d", resp.StatusCode)
	}
}

func login(t *testing.T, srv *httptest.Server, user, pass string) *client {
	c := newClient(t, srv.URL)
	c.get("/login")
	if code, body := c.post("/auth/ajax", "application/json", `{"action":"login","username":"`+user+`","password":"`+pass+`"}`, true); code != 200 {
		t.Fatalf("login %s: %d %s", user, code, body)
	}
	c.get("/")
	return c
}

var hrefRe = regexp.MustCompile(`href="(/(?:records|reports/weekly)/export\?[^"]+)"`)

// exportLinks 从页面中取出导出链接（已反转义 HTML）。
func exportLinks(body string) []string {
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// 回归：导出链接必须带上当前筛选条件（曾因模板转义 = 和 & 导致导出全部记录）。
func TestExportHonorsFilters(t *testing.T) {
	srv, _ := setup(t)
	c := login(t, srv, "admin", "Admin12345")
	_, all := c.get("/records/export?format=csv&mode=all")
	if n := strings.Count(all, "\n"); n != 7 { // 表头 + 6 条演示台账
		t.Fatalf("unfiltered export lines = %d", n)
	}
	code, page := c.get("/records?from=2000-01-01&to=2000-01-31&q=" + "巡检")
	if code != 200 {
		t.Fatal(code)
	}
	links := exportLinks(page)
	if len(links) != 3 {
		t.Fatalf("export links = %v", links)
	}
	for _, l := range links {
		if !strings.Contains(l, "from=2000-01-01") || !strings.Contains(l, "to=2000-01-31") || !strings.Contains(l, "mode=all") || strings.Contains(l, "%3D") {
			t.Fatalf("export link lost filters: %s", l)
		}
	}
	_, filtered := c.get(links[0])
	if n := strings.Count(filtered, "\n"); n != 1 {
		t.Fatalf("filtered export should be header only, got %d lines", n)
	}
	if !strings.Contains(filtered, "子分类") {
		t.Fatal("export header should use 子分类")
	}
}

func TestWeeklyExportAndTeamPages(t *testing.T) {
	srv, demo := setup(t)
	admin := login(t, srv, "admin", "Admin12345")
	_, page := admin.get("/reports/weekly?week_start=2000-01-05")
	links := exportLinks(page)
	if len(links) != 2 {
		t.Fatalf("weekly export links = %v", links)
	}
	resp, err := admin.http.Get(srv.URL + links[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "spreadsheet") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "weekly-records-") {
		t.Fatalf("weekly xlsx = %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}
	_, csv := admin.get(links[1])
	if n := strings.Count(csv, "\n"); n != 1 {
		t.Fatalf("weekly csv for empty week = %d lines", n)
	}
	// 员工不能越权导出他人范围
	emp := login(t, srv, "wangwu", demo)
	if code, _ := emp.get("/reports/weekly/export?format=csv&week_start=2026-01-05&target=dept:1"); code != 403 {
		t.Fatalf("employee export dept = %d", code)
	}
	if code, _ := emp.get("/reports/weekly/export?format=pdf&week_start=2026-01-05"); code != 422 && code != 400 {
		t.Fatalf("bad format = %d", code)
	}
	// 部门经理：团队台账可访问；旧地址跳转
	mgr := login(t, srv, "manager", demo)
	if code, _ := mgr.get("/team-records"); code != 200 {
		t.Fatalf("manager team records = %d", code)
	}
	if code, _ := mgr.get("/department-records"); code != 302 {
		t.Fatalf("legacy department-records = %d", code)
	}
}

func TestClearDataAjax(t *testing.T) {
	srv, _ := setup(t)
	c := login(t, srv, "admin", "Admin12345")
	c.get("/backup")
	if code, body := c.post("/backup/ajax", "application/json", `{"action":"clear","password":"Admin12345","confirm_text":"清空"}`, true); code != 422 || !strings.Contains(body, "confirm_text") {
		t.Fatalf("confirm text: %d %s", code, body)
	}
	if code, body := c.post("/backup/ajax", "application/json", `{"action":"clear","password":"bad","confirm_text":"确认清空","clear_categories":true,"clear_reports":true}`, true); code != 422 || !strings.Contains(body, "password") {
		t.Fatalf("bad password: %d %s", code, body)
	}
	if code, body := c.post("/backup/ajax", "application/json", `{"action":"clear","password":"Admin12345","confirm_text":"确认清空","clear_categories":false,"clear_reports":true}`, true); code != 200 || !strings.Contains(body, "pre-clear-") {
		t.Fatalf("clear: %d %s", code, body)
	}
	_, all := c.get("/records/export?format=csv&mode=all")
	if n := strings.Count(all, "\n"); n != 1 {
		t.Fatalf("records after clear = %d lines", n)
	}
}

func TestParticipantListAndDeleteAjax(t *testing.T) {
	srv, demo := setup(t)
	emp := login(t, srv, "wangwu", demo)
	_, page := emp.get("/my-records")
	if !strings.Contains(page, ">参与</span>") || !strings.Contains(page, "共 3 条记录") {
		t.Fatalf("my records should include participated records with badge")
	}
	// 参与记录：详情可看、无编辑/删除按钮，删除请求被拒绝
	m := regexp.MustCompile(`<span class="badge bg-azure-lt me-1">参与</span><a href="/records/(\d+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("participated record link not found")
	}
	code, detail := emp.get("/records/" + m[1])
	if code != 200 || strings.Contains(detail, `data-action="delete"`) || strings.Contains(detail, "/edit\"") {
		t.Fatalf("participant detail: %d", code)
	}
	if code, _ := emp.post("/records/ajax", "application/json", `{"action":"delete","id":`+m[1]+`}`, true); code != 403 {
		t.Fatalf("participant delete = %d", code)
	}
	// 创建者删除自己的记录
	own := regexp.MustCompile(`<td class="wl-table-content"><a href="/records/(\d+)"`).FindStringSubmatch(page)
	if own == nil {
		t.Fatal("own record not found")
	}
	emp.get("/records/" + own[1])
	if code, body := emp.post("/records/ajax", "application/json", `{"action":"delete","id":`+own[1]+`}`, true); code != 200 || !strings.Contains(body, "已删除") {
		t.Fatalf("creator delete: %d %s", code, body)
	}
	if code, _ := emp.get("/records/" + own[1]); code != 404 {
		t.Fatalf("deleted record = %d", code)
	}
}
