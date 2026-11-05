package validator

import "unicode/utf8"

// LoginInput 登录参数。
type LoginInput struct {
	Username string
	Password string
}

func Login(in Input) (any, error) {
	v := New(in)
	out := LoginInput{Username: v.Str("username", "账号", 64), Password: v.RawKeep("password")}
	if out.Username == "" {
		v.Fail("username", "请输入账号。")
	}
	if out.Password == "" {
		v.Fail("password", "请输入密码。")
	}
	return out, v.Err()
}

// CodeInput 动态码/恢复码。
type CodeInput struct{ Code string }

func Code(in Input) (any, error) {
	v := New(in)
	out := CodeInput{Code: v.Str("code", "验证码", 32)}
	if out.Code == "" {
		v.Fail("code", "请输入 6 位动态验证码或恢复码。")
	}
	return out, v.Err()
}

// PasswordCodeInput 密码 + 动态码（停用 2FA、重新生成恢复码）。
type PasswordCodeInput struct{ Password, Code string }

func PasswordCode(in Input) (any, error) {
	v := New(in)
	out := PasswordCodeInput{Password: v.RawKeep("password"), Code: v.Str("code", "验证码", 32)}
	if out.Password == "" {
		v.Fail("password", "请输入当前密码。")
	}
	if out.Code == "" {
		v.Fail("code", "请输入动态验证码或恢复码。")
	}
	return out, v.Err()
}

// ChangePasswordInput 修改密码。
type ChangePasswordInput struct{ Current, New, Confirm string }

func ChangePassword(in Input) (any, error) {
	v := New(in)
	out := ChangePasswordInput{Current: v.RawKeep("current_password"), New: v.RawKeep("new_password"), Confirm: v.RawKeep("new_password_confirmation")}
	if out.Current == "" {
		v.Fail("current_password", "请输入当前密码。")
	}
	if len(out.New) < 8 {
		v.Fail("new_password", "新密码至少需要 8 位。")
	} else if utf8.RuneCountInString(out.New) > 128 {
		v.Fail("new_password", "新密码不能超过 128 个字符。")
	}
	if out.New != out.Confirm {
		v.Fail("new_password_confirmation", "两次输入的新密码不一致。")
	}
	return out, v.Err()
}

// None 用于不需要业务参数的 action（只校验 action 本身）。
func None(in Input) (any, error) { return nil, nil }
