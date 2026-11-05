// Package controller 只负责：解析请求 → 调用验证器 → 调用 service → 返回响应。
// 本包禁止 import database/sql 与 work-ledger/internal/db（CI 会检查）。
package controller

import (
	"work-ledger/internal/service"
	"work-ledger/internal/web"
)

// Controllers 持有业务服务。
type Controllers struct {
	S *service.Services
}

// Register 注册全部路由。
func Register(app *web.App, s *service.Services) {
	c := &Controllers{S: s}
	c.registerAuth(app)
	c.registerSecurity(app)
	c.registerDashboard(app)
}

// sess 把会话适配为 service.KV。
func sess(ctx *web.Ctx) service.KV { return ctx.Sess }
