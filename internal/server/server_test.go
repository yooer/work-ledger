package server

import (
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
	for _, p := range []string{"/", "/records", "/my-records", "/records/create", "/people", "/departments", "/categories", "/reports/weekly", "/reports/yearly", "/reports/period", "/settings", "/backup", "/records/import", "/security", "/password"} {
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
	for p, want := range map[string]int{"/records": 403, "/people": 403, "/backup": 403, "/department-records": 403, "/my-records": 200} {
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
