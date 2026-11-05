package controller

import (
	"work-ledger/internal/service"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerAuth(app *web.App) {
	app.Page("/login", web.Public, c.loginPage)
	app.Page("/login/2fa", web.Public, c.totpPage)
	app.Page("/password", web.Login, c.passwordPage)

	app.Ajax("/auth/ajax", web.Public, web.Actions{
		"login":      {Validate: validator.Login, Handle: c.login},
		"verify_2fa": {Validate: validator.Code, Handle: c.verify2FA},
		"cancel_2fa": {Validate: validator.None, Handle: c.cancel2FA},
		"logout":     {Validate: validator.None, Handle: c.logout},
	})
	app.Ajax("/password/ajax", web.Login, web.Actions{
		"update": {Validate: validator.ChangePassword, Handle: c.changePassword},
	})
}

func (c *Controllers) loginPage(ctx *web.Ctx) error {
	if ctx.User != nil {
		return ctx.Redirect("/")
	}
	if u, err := c.S.Auth.PendingUser(sess(ctx)); err != nil {
		return err
	} else if u != nil {
		return ctx.Redirect("/login/2fa")
	}
	return ctx.RenderAuth("auth/login", map[string]any{"PageTitle": "登录"})
}

func (c *Controllers) totpPage(ctx *web.Ctx) error {
	if ctx.User != nil {
		return ctx.Redirect("/")
	}
	u, err := c.S.Auth.PendingUser(sess(ctx))
	if err != nil {
		return err
	}
	if u == nil {
		ctx.Flash("warning", "二次验证会话已过期，请重新登录。")
		return ctx.Redirect("/login")
	}
	return ctx.RenderAuth("auth/totp", map[string]any{"PageTitle": "二次验证", "Pending": u})
}

func (c *Controllers) login(ctx *web.Ctx, in any) (any, error) {
	if ctx.User != nil {
		return web.Result{Redirect: "/"}, nil
	}
	p := in.(validator.LoginInput)
	result, user, err := c.S.Auth.Login(sess(ctx), p.Username, p.Password)
	if err != nil {
		return nil, err
	}
	if result == service.LoginNeed2FA {
		return web.Result{Message: "密码验证通过，请继续完成 TOTP 二次验证。", Redirect: "/login/2fa", FlashOnRedirect: true}, nil
	}
	return web.Result{Message: "欢迎回来，" + user.Str("name"), Redirect: "/", FlashOnRedirect: true}, nil
}

func (c *Controllers) verify2FA(ctx *web.Ctx, in any) (any, error) {
	user, err := c.S.Auth.VerifySecondFactor(sess(ctx), in.(validator.CodeInput).Code)
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "欢迎回来，" + user.Str("name"), Redirect: "/", FlashOnRedirect: true}, nil
}

func (c *Controllers) cancel2FA(ctx *web.Ctx, _ any) (any, error) {
	c.S.Auth.CancelPending(sess(ctx))
	return web.Result{Message: "已取消二次验证，请重新输入账号密码。", Redirect: "/login", FlashOnRedirect: true}, nil
}

func (c *Controllers) logout(ctx *web.Ctx, _ any) (any, error) {
	c.S.Auth.Logout(sess(ctx))
	return web.Result{Redirect: "/login"}, nil
}

func (c *Controllers) passwordPage(ctx *web.Ctx) error {
	return ctx.Render("auth/password", map[string]any{"PageTitle": "修改密码", "Active": "password"})
}

func (c *Controllers) changePassword(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.ChangePasswordInput)
	if err := c.S.Auth.ChangePassword(ctx.UserID(), service.ChangePasswordInput{Current: p.Current, New: p.New, Confirm: p.Confirm}); err != nil {
		return nil, err
	}
	return web.Result{Message: "密码已更新，请妥善保管新密码。", Redirect: "/", FlashOnRedirect: true}, nil
}
