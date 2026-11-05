package controller

import "work-ledger/internal/web"

func (c *Controllers) registerDashboard(app *web.App) {
	app.Page("/{$}", web.Login, c.dashboard)
}

func (c *Controllers) dashboard(ctx *web.Ctx) error {
	return ctx.Render("dashboard", map[string]any{"PageTitle": "工作台", "Active": "dashboard"})
}
