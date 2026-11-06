package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"work-ledger/internal/model"
	"work-ledger/internal/timeutil"
)

// Views 持有预解析的页面模板。每个页面 = 公共布局/片段 + 页面文件。
type Views struct {
	pages   map[string]*template.Template
	version string
}

// LoadViews 解析 templates 目录：layouts/*.html、partials/*.html 为公共部分，其余为页面。
func LoadViews(fsys fs.FS, version string) (*Views, error) {
	v := &Views{pages: map[string]*template.Template{}, version: version}
	base := template.New("base").Funcs(v.funcs())
	shared, err := fs.Glob(fsys, "templates/layouts/*.html")
	if err != nil {
		return nil, err
	}
	partials, _ := fs.Glob(fsys, "templates/partials/*.html")
	shared = append(shared, partials...)
	if len(shared) > 0 {
		if base, err = base.ParseFS(fsys, shared...); err != nil {
			return nil, err
		}
	}
	err = fs.WalkDir(fsys, "templates/pages", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		t, err := base.Clone()
		if err != nil {
			return err
		}
		if t, err = t.ParseFS(fsys, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/pages/"), ".html")
		v.pages[name] = t
		return nil
	})
	return v, err
}

// Render 渲染页面。page 形如 "records/index"；layout 为 "app"（登录后）或 "auth"（登录页）。
func (c *Ctx) Render(page string, data map[string]any) error {
	return c.renderWith(http.StatusOK, page, "app", data)
}

// RenderAuth 使用登录页布局渲染。
func (c *Ctx) RenderAuth(page string, data map[string]any) error {
	return c.renderWith(http.StatusOK, page, "auth", data)
}

func (c *Ctx) renderWith(status int, page, layout string, data map[string]any) error {
	t, ok := c.App.Views.pages[page]
	if !ok {
		return fmt.Errorf("模板不存在：%s", page)
	}
	if data == nil {
		data = map[string]any{}
	}
	data["App"] = map[string]any{"Name": c.App.Cfg.App.Name, "Version": c.App.Version}
	data["User"] = c.User
	data["IsAdmin"] = c.IsAdmin()
	data["IsManager"] = c.IsManager()
	data["IsLeader"] = c.IsLeader()
	data["CSRF"] = c.CSRFToken()
	data["Flashes"] = c.pullFlashes()
	data["TodayLabel"] = timeutil.TodayLabel()
	data["Path"] = c.R.URL.Path
	if _, ok := data["PageTitle"]; !ok {
		data["PageTitle"] = c.App.Cfg.App.Name
	}
	if layout == "app" {
		data["DepartmentLabel"] = DepartmentLabel(c.User)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout-"+layout, data); err != nil {
		return err
	}
	c.commit()
	c.written = true
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.Header().Set("Cache-Control", "no-store")
	c.W.WriteHeader(status)
	_, err := buf.WriteTo(c.W)
	return err
}

// RenderError 渲染独立错误页。
func (c *Ctx) RenderError(status int, title, message string) {
	data := map[string]any{"Status": status, "Title": title, "Message": message, "PageTitle": title}
	if err := c.renderWith(status, "error", "auth", data); err != nil {
		c.App.Log.Error("render error page", "err", err)
		if !c.written {
			c.written = true
			http.Error(c.W, message, status)
		}
	}
}

// DepartmentLabel 形如 "集客部 / 集客一组"。
func DepartmentLabel(u model.Row) string {
	if u == nil || u.Str("department_name") == "" {
		return "未分配部门"
	}
	if p := u.Str("parent_department_name"); p != "" {
		return p + " / " + u.Str("department_name")
	}
	return u.Str("department_name")
}

func (v *Views) funcs() template.FuncMap {
	return template.FuncMap{
		"asset": func(p string) string {
			return "/assets/" + strings.TrimPrefix(p, "/") + "?v=" + v.version
		},
		"static":     func(p string) string { return path.Join("/static", p) },
		"dt":         timeutil.FormatDateTime,
		"date":       timeutil.FormatDate,
		"dateRange":  timeutil.FormatRange,
		"localInput": timeutil.ToLocalInput,
		"role":       RoleLabel,
		"difficulty": DifficultyLabel,
		"md":         func(s string) template.HTML { return template.HTML(Markdown(s)) },
		"dept":       DepartmentLabel,
		"short":      Shorten,
		"first":      FirstRune,
		"dayName":    func(d any) string { return timeutil.DayName(int(model.ToInt(d))) },
		"icon":       Icon,
		"str":        model.ToStr,
		"int":        model.ToInt,
		"add":        func(a, b any) int64 { return model.ToInt(a) + model.ToInt(b) },
		"sub":        func(a, b any) int64 { return model.ToInt(a) - model.ToInt(b) },
		"pct": func(a, b any) int64 {
			x, y := model.ToInt(a), model.ToInt(b)
			if y <= 0 {
				return 0
			}
			return (x*100 + y/2) / y
		},
		"seq": func(from, to int) []int {
			var out []int
			for i := from; i <= to; i++ {
				out = append(out, i)
			}
			return out
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"list": func(items ...any) []any { return items },
		"inIDs": func(ids any, id any) bool {
			want := model.ToInt(id)
			switch t := ids.(type) {
			case []int64:
				for _, x := range t {
					if x == want {
						return true
					}
				}
			case map[int64]bool:
				return t[want]
			}
			return false
		},
		"same": func(a, b any) bool { return model.ToStr(a) == model.ToStr(b) },
		"join": strings.Join,
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
		"default": func(def string, v any) string {
			s := model.ToStr(v)
			if s == "" {
				return def
			}
			return s
		},
	}
}

func RoleLabel(role any) string {
	switch model.ToStr(role) {
	case "admin":
		return "系统管理员"
	case "manager":
		return "部门经理"
	default:
		return "员工"
	}
}

func DifficultyLabel(level any) string {
	switch model.ToInt(level) {
	case 1:
		return "很简单"
	case 2:
		return "较简单"
	case 3:
		return "一般"
	case 4:
		return "较困难"
	case 5:
		return "很困难"
	}
	return "未设置"
}

func Shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func FirstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// iconMap 把 PHP 版图标名映射到 Tabler Icons。
var iconMap = map[string]string{
	"dashboard": "layout-dashboard", "ledger": "clipboard-list", "report": "chart-bar", "people": "users",
	"building": "building", "tag": "tag", "settings": "adjustments", "logout": "logout", "plus": "plus",
	"search": "search", "calendar": "calendar", "clock": "clock", "edit": "edit", "eye": "eye",
	"arrow": "arrow-right", "print": "printer", "spark": "sparkles", "menu": "menu-2", "check": "check",
	"warning": "alert-triangle", "chevron": "chevron-right", "backup": "database-export", "import": "file-import",
	"export": "file-export", "shield": "shield-lock", "key": "key", "download": "download", "upload": "upload",
	"refresh": "refresh", "trash": "trash", "info": "info-circle", "user": "user", "robot": "robot",
	"org": "sitemap", "team": "users-group", "eraser": "eraser",
}

// Icon 输出 Tabler 图标。
func Icon(name string) template.HTML {
	n, ok := iconMap[name]
	if !ok {
		n = name
	}
	return template.HTML(`<i class="ti ti-` + template.HTMLEscapeString(n) + `" aria-hidden="true"></i>`)
}
