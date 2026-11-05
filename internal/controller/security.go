package controller

import (
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerSecurity(app *web.App) {
	app.Page("/security", web.Login, c.securityPage)
	app.Ajax("/security/ajax", web.Login, web.Actions{
		"start_totp":          {Validate: validator.None, Handle: c.startTotp},
		"cancel_totp":         {Validate: validator.None, Handle: c.cancelTotp},
		"enable_totp":         {Validate: validator.Code, Handle: c.enableTotp},
		"disable_totp":        {Validate: validator.PasswordCode, Handle: c.disableTotp},
		"regenerate_recovery": {Validate: validator.PasswordCode, Handle: c.regenerateRecovery},
	})
}

func (c *Controllers) securityPage(ctx *web.Ctx) error {
	st, err := c.S.Security.State(sess(ctx), ctx.User)
	if err != nil {
		return err
	}
	return ctx.Render("security/index", map[string]any{
		"PageTitle": "账号安全", "Active": "security", "State": st,
		"Require2FA": c.S.Core.Cfg.TOTP.Require,
	})
}

func (c *Controllers) startTotp(ctx *web.Ctx, _ any) (any, error) {
	if err := c.S.Security.StartSetup(sess(ctx), ctx.User); err != nil {
		return nil, err
	}
	return web.Result{Message: "已生成新的 TOTP 密钥，请在验证器中添加后输入动态码完成启用。", Redirect: "/security", FlashOnRedirect: true}, nil
}

func (c *Controllers) cancelTotp(ctx *web.Ctx, _ any) (any, error) {
	c.S.Security.CancelSetup(sess(ctx))
	return web.Result{Message: "已取消本次 2FA 绑定。", Redirect: "/security", FlashOnRedirect: true}, nil
}

func (c *Controllers) enableTotp(ctx *web.Ctx, in any) (any, error) {
	if err := c.S.Security.Enable(sess(ctx), ctx.User, in.(validator.CodeInput).Code); err != nil {
		return nil, err
	}
	return web.Result{Message: "TOTP 二次验证已启用。请立即保存恢复码，它们只会显示一次。", Redirect: "/security", FlashOnRedirect: true}, nil
}

func (c *Controllers) disableTotp(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.PasswordCodeInput)
	if err := c.S.Security.Disable(sess(ctx), ctx.User, p.Password, p.Code); err != nil {
		return nil, err
	}
	return web.Result{Message: "TOTP 二次验证已停用。建议重新启用并妥善保管恢复码。", Redirect: "/security", FlashOnRedirect: true}, nil
}

func (c *Controllers) regenerateRecovery(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.PasswordCodeInput)
	if err := c.S.Security.RegenerateRecoveryCodes(sess(ctx), ctx.User, p.Password, p.Code); err != nil {
		return nil, err
	}
	return web.Result{Message: "新的恢复码已生成，请立即保存，旧恢复码已全部失效。", Redirect: "/security", FlashOnRedirect: true}, nil
}
