// Package web 是 HTTP 基础设施：请求上下文、模板渲染、Ajax 分发、登录/权限守卫。
// 不包含业务逻辑，不直接访问业务表。
package web

import (
	"log/slog"
	"net/http"

	"work-ledger/internal/config"
	"work-ledger/internal/model"
	"work-ledger/internal/session"
)

// UserLoader 根据会话中的用户 ID 加载当前用户（由 service 层提供实现）。
type UserLoader func(id int64) (model.Row, error)

type App struct {
	Cfg      *config.Config
	Sessions *session.Store
	Views    *Views
	LoadUser UserLoader
	Log      *slog.Logger
	Version  string
	Mux      *http.ServeMux
}

func NewApp(cfg *config.Config, store *session.Store, views *Views, loader UserLoader, logger *slog.Logger, version string) *App {
	return &App{Cfg: cfg, Sessions: store, Views: views, LoadUser: loader, Log: logger, Version: version, Mux: http.NewServeMux()}
}

// HandlerFunc 是页面/接口处理函数。
type HandlerFunc func(c *Ctx) error

// Guard 决定访问权限。
type Guard int

const (
	Public       Guard = iota // 无需登录（登录页等）
	Login                     // 需登录
	Admin                     // 仅系统管理员
	AdminManager              // 管理员或部门经理
)

// Page 注册 GET 页面。
func (a *App) Page(pattern string, guard Guard, h HandlerFunc) {
	a.Mux.Handle("GET "+pattern, a.wrap(guard, h, false))
}

// Post 注册普通 POST（文件上传等例外入口），自动校验 CSRF。
func (a *App) Post(pattern string, guard Guard, h HandlerFunc) {
	a.Mux.Handle("POST "+pattern, a.wrap(guard, h, true))
}

// Ajax 注册模块级 Ajax 入口：POST + JSON + action。
func (a *App) Ajax(pattern string, guard Guard, actions Actions) {
	a.Mux.Handle("POST "+pattern, a.wrap(guard, actions.handler(), true))
}

// Raw 注册不经过会话的处理器（/api/v1、静态资源）。
func (a *App) Raw(pattern string, h http.Handler) {
	a.Mux.Handle(pattern, h)
}

func (a *App) secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// requireTwoFactorAllowed 是 REQUIRE_2FA=true 时未绑定用户仍可访问的路径。
var requireTwoFactorAllowed = map[string]bool{
	"/security": true, "/security/ajax": true, "/password": true, "/password/ajax": true, "/logout": true, "/auth/ajax": true,
}
