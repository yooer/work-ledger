package service

import (
	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/password"
	"work-ledger/internal/totp"
)

// Security 处理用户自助 TOTP 绑定、停用与恢复码。
type Security struct{ *Core }

const sessSetupSecret = "_totp_setup_secret"
const sessRecoveryPlain = "_recovery_codes_plain"

// SecurityState 账号安全页面数据。
type SecurityState struct {
	TOTPEnabled       bool
	SetupSecret       string
	SetupSecretFmt    string
	ProvisioningURI   string
	RecoveryCodes     []string
	RecoveryCodeCount int
}

// State 读取页面状态；一次性恢复码读取后即从会话移除。
func (s *Security) State(kv KV, user Row) (SecurityState, error) {
	st := SecurityState{TOTPEnabled: user.Bool("totp_enabled") && user.Str("totp_secret") != ""}
	if secret, _ := kv.Get(sessSetupSecret).(string); secret != "" {
		st.SetupSecret = secret
		st.SetupSecretFmt = totp.FormatSecret(secret)
		st.ProvisioningURI = totp.ProvisioningURI(s.Cfg.TOTP.Issuer, user.Str("username"), secret)
	}
	if list, ok := kv.Get(sessRecoveryPlain).([]any); ok {
		for _, c := range list {
			if cs, ok := c.(string); ok {
				st.RecoveryCodes = append(st.RecoveryCodes, cs)
			}
		}
		kv.Delete(sessRecoveryPlain)
	}
	row, err := db.One(s.DB, "SELECT totp_recovery_codes FROM users WHERE id = ?", user.Int("id"))
	if err != nil {
		return st, err
	}
	st.RecoveryCodeCount = totp.CountRecoveryCodes(row.Str("totp_recovery_codes"))
	return st, nil
}

func hasSecondFactor(u Row) bool { return u.Bool("totp_enabled") && u.Str("totp_secret") != "" }

// StartSetup 生成待绑定的密钥并存入会话。
func (s *Security) StartSetup(kv KV, user Row) error {
	if hasSecondFactor(user) {
		return apperr.New("TOTP 二次验证已经启用，如需重新绑定请先停用。")
	}
	kv.Set(sessSetupSecret, totp.GenerateSecret())
	return nil
}

func (s *Security) CancelSetup(kv KV) { kv.Delete(sessSetupSecret) }

// Enable 校验首个动态码并启用 2FA，生成 8 个恢复码（仅显示一次）。
func (s *Security) Enable(kv KV, user Row, code string) error {
	if hasSecondFactor(user) {
		return apperr.New("TOTP 二次验证已经启用。")
	}
	secret, _ := kv.Get(sessSetupSecret).(string)
	if secret == "" {
		return apperr.New("绑定会话已过期，请重新生成 TOTP 密钥。")
	}
	step := totp.Verify(secret, code, s.Cfg.TOTP.Window)
	if step < 0 {
		return apperr.Validation(map[string]string{"code": "动态验证码不正确，请确认验证器时间后重试。"})
	}
	codes := totp.GenerateRecoveryCodes(8)
	hashed, err := totp.HashRecoveryCodes(codes)
	if err != nil {
		return err
	}
	if _, err := db.Exec(s.DB, `UPDATE users SET totp_secret = ?, totp_enabled = 1, totp_recovery_codes = ?,
		totp_last_used_step = ?, updated_at = ? WHERE id = ?`, secret, hashed, step, now(), user.Int("id")); err != nil {
		return err
	}
	kv.Delete(sessSetupSecret)
	kv.Set(sessRecoveryPlain, toAny(codes))
	return nil
}

// verifyPasswordAndFactor 停用/重新生成恢复码前的二次确认。
func (s *Security) verifyPasswordAndFactor(userID int64, plain, code string) (bool, error) {
	row, err := db.One(s.DB, "SELECT password_hash FROM users WHERE id = ?", userID)
	if err != nil {
		return false, err
	}
	if row == nil || plain == "" || !password.Verify(plain, row.Str("password_hash")) {
		return false, nil
	}
	code = totp.CleanCode(code)
	if code == "" {
		return false, nil
	}
	return consumeSecondFactor(s.Core, userID, code)
}

// Disable 停用 2FA。
func (s *Security) Disable(kv KV, user Row, plain, code string) error {
	if !hasSecondFactor(user) {
		return apperr.New("当前账号尚未启用 TOTP 二次验证。")
	}
	ok, err := s.verifyPasswordAndFactor(user.Int("id"), plain, code)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.New("密码或动态验证码不正确，未能停用二次验证。")
	}
	if _, err := db.Exec(s.DB, `UPDATE users SET totp_secret = NULL, totp_enabled = 0, totp_recovery_codes = NULL,
		totp_last_used_step = NULL, updated_at = ? WHERE id = ?`, now(), user.Int("id")); err != nil {
		return err
	}
	kv.Delete(sessSetupSecret, sessRecoveryPlain)
	return nil
}

// RegenerateRecoveryCodes 重新生成恢复码，旧码全部失效。
func (s *Security) RegenerateRecoveryCodes(kv KV, user Row, plain, code string) error {
	if !hasSecondFactor(user) {
		return apperr.New("请先启用 TOTP 二次验证。")
	}
	ok, err := s.verifyPasswordAndFactor(user.Int("id"), plain, code)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.New("密码或动态验证码不正确，未能重新生成恢复码。")
	}
	codes := totp.GenerateRecoveryCodes(8)
	hashed, err := totp.HashRecoveryCodes(codes)
	if err != nil {
		return err
	}
	if _, err := db.Exec(s.DB, "UPDATE users SET totp_recovery_codes = ?, updated_at = ? WHERE id = ?", hashed, now(), user.Int("id")); err != nil {
		return err
	}
	kv.Set(sessRecoveryPlain, toAny(codes))
	return nil
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
