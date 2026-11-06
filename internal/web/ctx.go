package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"work-ledger/internal/apperr"
	"work-ledger/internal/model"
	"work-ledger/internal/session"
	"work-ledger/internal/validator"
)

// Ctx 是一次请求的上下文。
type Ctx struct {
	App       *App
	W         http.ResponseWriter
	R         *http.Request
	Sess      *session.Session
	User      model.Row
	Action    string // 当前 Ajax action（用于日志）
	isAjax    bool
	committed bool
	written   bool
}

// ID 读取路径参数 {id}。
func (c *Ctx) ID() int64 {
	n, _ := strconv.ParseInt(c.R.PathValue("id"), 10, 64)
	return n
}

// UserID 当前登录用户 ID。
func (c *Ctx) UserID() int64 { return c.User.Int("id") }

func (c *Ctx) IsAdmin() bool   { return c.User.Str("role") == "admin" }
func (c *Ctx) IsManager() bool { return c.User.Str("role") == "manager" }

// IsLeader 部门经理或有下属的主管领导（可看团队台账、生成团队总结）。
func (c *Ctx) IsLeader() bool { return c.IsManager() || c.User.Bool("has_subordinates") }

// Query 返回查询参数作为验证器输入。
func (c *Ctx) Query() validator.Input { return validator.FromValues(c.R.URL.Query()) }

// QueryString 读取单个查询参数。
func (c *Ctx) QueryString(key string) string { return strings.TrimSpace(c.R.URL.Query().Get(key)) }

// Flash 写入一次性提示，下次渲染页面时显示。
func (c *Ctx) Flash(kind, msg string) {
	list, _ := c.Sess.Get("_flash").([]any)
	list = append(list, map[string]any{"type": kind, "message": msg})
	c.Sess.Set("_flash", list)
}

func (c *Ctx) pullFlashes() []map[string]string {
	list, _ := c.Sess.Get("_flash").([]any)
	if len(list) == 0 {
		return nil
	}
	c.Sess.Delete("_flash")
	out := make([]map[string]string, 0, len(list))
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			t, _ := m["type"].(string)
			msg, _ := m["message"].(string)
			switch t {
			case "success", "error", "warning", "info":
			default:
				t = "info"
			}
			out = append(out, map[string]string{"type": t, "message": msg})
		}
	}
	return out
}

// CSRFToken 返回（必要时生成）当前会话的 CSRF 令牌。
func (c *Ctx) CSRFToken() string {
	tok := c.Sess.GetString("_csrf")
	if tok == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		tok = hex.EncodeToString(b)
		c.Sess.Set("_csrf", tok)
	}
	return tok
}

func (c *Ctx) csrfValid() bool {
	expected := c.Sess.GetString("_csrf")
	actual := c.R.Header.Get("X-CSRF-Token")
	if actual == "" {
		actual = c.R.FormValue("_csrf")
	}
	return expected != "" && actual != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

// commit 在写响应前保存会话（Set-Cookie 必须先于正文）。
func (c *Ctx) commit() {
	if c.committed {
		return
	}
	c.committed = true
	c.App.Sessions.Save(c.W, c.R, c.Sess, c.App.secure(c.R))
}

func (c *Ctx) securityHeaders() {
	h := c.W.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("X-Robots-Tag", "noindex, nofollow")
}

// Redirect 302 跳转。
func (c *Ctx) Redirect(path string) error {
	c.commit()
	c.written = true
	http.Redirect(c.W, c.R, path, http.StatusFound)
	return nil
}

// JSON 输出 JSON。
func (c *Ctx) JSON(status int, v any) error {
	c.commit()
	c.written = true
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.Header().Set("Cache-Control", "no-store")
	c.W.WriteHeader(status)
	enc := json.NewEncoder(c.W)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Download 输出附件。
func (c *Ctx) Download(filename, contentType string, data []byte) error {
	c.commit()
	c.written = true
	h := c.W.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", ContentDisposition(filename))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store")
	_, err := c.W.Write(data)
	return err
}

// ContentDisposition 生成兼容中文文件名的 attachment 头。
func ContentDisposition(filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, filename)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(filename))
}

func (a *App) wrap(guard Guard, h HandlerFunc, isPost bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		c := &Ctx{App: a, W: w, R: r, isAjax: isPost}
		c.securityHeaders()
		c.Sess = a.Sessions.Load(r)

		defer func() {
			if p := recover(); p != nil {
				a.Log.Error("panic", "path", r.URL.Path, "err", p, "stack", string(debug.Stack()))
				if !c.written {
					c.fail(fmt.Errorf("panic: %v", p))
				}
			}
			if !c.written {
				c.commit()
			}
			if isPost {
				a.Log.Info("request", "method", r.Method, "path", r.URL.Path, "action", c.Action, "user", c.UserID(), "ms", time.Since(start).Milliseconds())
			}
		}()

		if isPost && !c.csrfValid() {
			c.fail(apperr.Status(419, "csrf_expired", "页面安全令牌已过期，请刷新页面后重新提交。"))
			return
		}

		if uid := c.Sess.GetInt("user_id"); uid > 0 {
			user, err := a.LoadUser(uid)
			if err != nil {
				c.fail(err)
				return
			}
			if user == nil {
				c.Sess.Delete("user_id")
			}
			c.User = user
		}

		if guard != Public {
			if c.User == nil {
				if isPost {
					_ = c.JSON(http.StatusUnauthorized, map[string]any{"ok": false, "code": "unauthorized", "message": "登录状态已失效，请重新登录。", "redirect": "/login"})
					return
				}
				c.Flash("warning", "请先登录后再继续操作。")
				_ = c.Redirect("/login")
				return
			}
			if a.Cfg.TOTP.Require && !c.User.Bool("totp_enabled") && !requireTwoFactorAllowed[r.URL.Path] {
				if isPost {
					c.fail(apperr.Forbidden("管理员要求启用二次验证，请先在“账号安全”中完成 TOTP 绑定。"))
					return
				}
				c.Flash("warning", "管理员要求启用二次验证，请先完成 TOTP 绑定。")
				_ = c.Redirect("/security")
				return
			}
			switch guard {
			case Admin:
				if !c.IsAdmin() {
					c.fail(apperr.Forbidden("该功能仅系统管理员可以使用。"))
					return
				}
			case AdminManager:
				if !c.IsAdmin() && !c.IsLeader() {
					c.fail(apperr.Forbidden("该功能仅系统管理员、部门经理或主管领导可以使用。"))
					return
				}
			}
		}

		if err := h(c); err != nil && !c.written {
			c.fail(err)
		}
	})
}

// fail 统一输出错误：Ajax 返回 JSON，页面渲染错误页。
func (c *Ctx) fail(err error) {
	e, ok := apperr.As(err)
	if !ok {
		c.App.Log.Error("server error", "path", c.R.URL.Path, "err", err)
		msg := "系统处理请求时出现异常，请稍后重试或联系管理员。"
		if c.App.Cfg.App.Debug {
			msg = err.Error()
		}
		e = &apperr.Error{Status: http.StatusInternalServerError, Code: "server_error", Message: msg}
	}
	if c.isAjax {
		body := map[string]any{"ok": false, "code": e.Code, "message": e.Message}
		if len(e.Fields) > 0 {
			body["errors"] = e.Fields
		}
		_ = c.JSON(e.Status, body)
		return
	}
	title := map[int]string{403: "无权访问", 404: "页面不存在", 419: "页面已过期", 503: "服务不可用"}[e.Status]
	if title == "" {
		title = "出现错误"
	}
	c.RenderError(e.Status, title, e.Message)
}
