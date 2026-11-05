package service

import (
	"time"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/password"
	"work-ledger/internal/totp"
)

// KV 是会话的最小接口（*session.Session 实现），用于登录状态与失败计数。
type KV interface {
	Get(key string) any
	GetInt(key string) int64
	Set(key string, v any)
	Delete(keys ...string)
	Regenerate()
	Destroy()
}

type Auth struct{ *Core }

const userSelect = `SELECT u.*, d.name AS department_name, d.parent_id AS department_parent_id,
	pd.name AS parent_department_name
	FROM users u
	LEFT JOIN departments d ON d.id = u.department_id
	LEFT JOIN departments pd ON pd.id = d.parent_id`

// LoadActiveUser 加载启用状态的用户（含部门名称），不存在返回 nil。
func (a *Auth) LoadActiveUser(id int64) (Row, error) {
	return db.One(a.DB, userSelect+" WHERE u.id = ? AND u.is_active = 1 LIMIT 1", id)
}

// LoginResult 登录结果。
type LoginResult int

const (
	LoginFailed LoginResult = iota
	LoginOK
	LoginNeed2FA
)

const maxFailures = 8
const lockSeconds = 300

func locked(kv KV, key string) bool {
	m, _ := kv.Get(key).(map[string]any)
	if m == nil {
		return false
	}
	count, _ := m["count"].(float64)
	last, _ := m["last"].(float64)
	return int(count) >= maxFailures && time.Now().Unix()-int64(last) < lockSeconds
}

func addFailure(kv KV, key string) {
	m, _ := kv.Get(key).(map[string]any)
	count := 0.0
	if m != nil {
		count, _ = m["count"].(float64)
	}
	kv.Set(key, map[string]any{"count": count + 1, "last": float64(time.Now().Unix())})
}

// Login 校验账号密码（账号或工号均可），处理失败计数与 2FA 跳转。返回结果与用户（成功时）。
func (a *Auth) Login(kv KV, login, plain string) (LoginResult, Row, error) {
	if locked(kv, "_login_failures") {
		return LoginFailed, nil, apperr.New("登录失败次数过多，请 5 分钟后再试。")
	}
	if login == "" || plain == "" {
		return LoginFailed, nil, apperr.New("请输入账号和密码。")
	}
	row, err := db.One(a.DB, "SELECT * FROM users WHERE username = ? LIMIT 1", login)
	if err != nil {
		return LoginFailed, nil, err
	}
	if row == nil {
		if row, err = db.One(a.DB, "SELECT * FROM users WHERE employee_no = ? LIMIT 1", login); err != nil {
			return LoginFailed, nil, err
		}
	}
	fail := func() (LoginResult, Row, error) {
		time.Sleep(350 * time.Millisecond)
		addFailure(kv, "_login_failures")
		return LoginFailed, nil, apperr.New("账号或密码不正确，或账号已停用。")
	}
	if row == nil || !row.Bool("is_active") || !password.Verify(plain, row.Str("password_hash")) {
		return fail()
	}
	if password.NeedsRehash(row.Str("password_hash")) {
		if h, err := password.Hash(plain); err == nil {
			_, _ = db.Exec(a.DB, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?", h, now(), row.Int("id"))
		}
	}
	if row.Bool("totp_enabled") {
		if row.Str("totp_secret") == "" {
			return fail()
		}
		kv.Regenerate()
		kv.Delete("user_id", "login_at")
		kv.Set("2fa_user_id", row.Int("id"))
		kv.Set("2fa_started_at", time.Now().Unix())
		return LoginNeed2FA, row, nil
	}
	if err := a.completeLogin(kv, row); err != nil {
		return LoginFailed, nil, err
	}
	kv.Delete("_login_failures", "_2fa_failures")
	return LoginOK, row, nil
}

func (a *Auth) completeLogin(kv KV, row Row) error {
	kv.Regenerate()
	kv.Delete("2fa_user_id", "2fa_started_at")
	kv.Set("user_id", row.Int("id"))
	kv.Set("login_at", time.Now().Unix())
	_, err := db.Exec(a.DB, "UPDATE users SET last_login_at = ? WHERE id = ?", now(), row.Int("id"))
	return err
}

// PendingUser 返回等待二次验证的用户（超时或无效时清理并返回 nil）。
func (a *Auth) PendingUser(kv KV) (Row, error) {
	uid := kv.GetInt("2fa_user_id")
	if uid <= 0 {
		return nil, nil
	}
	started := kv.GetInt("2fa_started_at")
	if started <= 0 || time.Now().Unix()-started > int64(a.Cfg.TOTP.PendingTimeout) {
		a.CancelPending(kv)
		return nil, nil
	}
	u, err := db.One(a.DB, userSelect+" WHERE u.id = ? AND u.is_active = 1 LIMIT 1", uid)
	if err != nil {
		return nil, err
	}
	if u == nil {
		a.CancelPending(kv)
	}
	return u, nil
}

func (a *Auth) CancelPending(kv KV) {
	kv.Delete("2fa_user_id", "2fa_started_at", "_2fa_failures")
}

// VerifySecondFactor 校验登录时的动态码或恢复码，成功后完成登录。
func (a *Auth) VerifySecondFactor(kv KV, code string) (Row, error) {
	user, err := a.PendingUser(kv)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, apperr.Status(401, "2fa_expired", "二次验证会话已过期，请重新登录。")
	}
	if locked(kv, "_2fa_failures") {
		return nil, apperr.New("验证码错误次数过多，请 5 分钟后再试，或重新登录。")
	}
	code = totp.CleanCode(code)
	if code == "" {
		return nil, apperr.New("请输入 6 位动态验证码或恢复码。")
	}
	ok, err := a.consumeFactor(user, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		time.Sleep(300 * time.Millisecond)
		addFailure(kv, "_2fa_failures")
		return nil, apperr.New("验证码不正确或已使用，请确认验证器时间并重试。")
	}
	if err := a.completeLogin(kv, user); err != nil {
		return nil, err
	}
	kv.Delete("_2fa_failures", "_login_failures")
	return user, nil
}

// consumeFactor 校验动态码（防重放）或消耗恢复码。
func (a *Auth) consumeFactor(user Row, code string) (bool, error) {
	return consumeSecondFactor(a.Core, user.Int("id"), code)
}

func consumeSecondFactor(c *Core, userID int64, code string) (bool, error) {
	row, err := db.One(c.DB, "SELECT totp_secret, totp_recovery_codes, totp_last_used_step FROM users WHERE id = ?", userID)
	if err != nil || row == nil {
		return false, err
	}
	if totp.IsSixDigits(code) {
		secret := row.Str("totp_secret")
		if secret == "" {
			return false, nil
		}
		step := totp.Verify(secret, code, c.Cfg.TOTP.Window)
		if step < 0 || step <= row.Int("totp_last_used_step") {
			return false, nil
		}
		_, err := db.Exec(c.DB, "UPDATE users SET totp_last_used_step = ?, updated_at = ? WHERE id = ?", step, now(), userID)
		return err == nil, err
	}
	matched, remaining := totp.ConsumeRecoveryCode(row.Str("totp_recovery_codes"), code)
	if !matched {
		return false, nil
	}
	_, err = db.Exec(c.DB, "UPDATE users SET totp_recovery_codes = ?, updated_at = ? WHERE id = ?", remaining, now(), userID)
	return err == nil, err
}

// Logout 销毁会话。
func (a *Auth) Logout(kv KV) { kv.Destroy() }

// ChangePasswordInput 修改密码参数（已通过验证器格式校验）。
type ChangePasswordInput struct {
	Current, New, Confirm string
}

// ChangePassword 校验当前密码后更新。
func (a *Auth) ChangePassword(userID int64, in ChangePasswordInput) error {
	row, err := db.One(a.DB, "SELECT password_hash FROM users WHERE id = ?", userID)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	if row == nil || !password.Verify(in.Current, row.Str("password_hash")) {
		fields["current_password"] = "当前密码不正确。"
	}
	if in.New != "" && in.Current != "" && in.New == in.Current {
		fields["new_password"] = "新密码不能与当前密码相同。"
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	h, err := password.Hash(in.New)
	if err != nil {
		return err
	}
	_, err = db.Exec(a.DB, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?", h, now(), userID)
	return err
}
