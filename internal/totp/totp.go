// Package totp 实现 RFC 6238 TOTP、Base32 与恢复码，算法与 PHP 版 Totp.php 完全一致，
// 已绑定的验证器与恢复码可以直接沿用。
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"

	"work-ledger/internal/password"
)

const (
	alphabet         = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	Digits           = 6
	Period           = 30
)

var now = time.Now

// GenerateSecret 生成 20 字节随机密钥的 Base32 表示（32 个字符）。
func GenerateSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return Base32Encode(b)
}

// Code 计算指定时间戳的动态码。
func Code(secret string, timestamp int64) string {
	counter := uint64(timestamp / Period)
	key := Base32Decode(secret)
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	h := mac.Sum(nil)
	off := h[19] & 0x0F
	v := (uint32(h[off]&0x7F) << 24) | (uint32(h[off+1]) << 16) | (uint32(h[off+2]) << 8) | uint32(h[off+3])
	return fmt.Sprintf("%06d", v%1000000)
}

var sixDigits = regexp.MustCompile(`^\d{6}$`)
var spaces = regexp.MustCompile(`\s+`)

// CleanCode 去掉空白。
func CleanCode(code string) string { return spaces.ReplaceAllString(strings.TrimSpace(code), "") }

// IsSixDigits 判断是否 6 位数字。
func IsSixDigits(code string) bool { return sixDigits.MatchString(code) }

// Verify 校验动态码（允许 ±window 个时间步），返回匹配的时间步；未匹配返回 -1。
func Verify(secret, code string, window int) int64 {
	code = CleanCode(code)
	if !IsSixDigits(code) {
		return -1
	}
	if window < 0 {
		window = 0
	}
	current := now().Unix() / Period
	for off := -int64(window); off <= int64(window); off++ {
		step := current + off
		if subtle.ConstantTimeCompare([]byte(Code(secret, step*Period)), []byte(code)) == 1 {
			return step
		}
	}
	return -1
}

// ProvisioningURI 生成 otpauth:// 链接（与 PHP http_build_query RFC3986 编码一致）。
func ProvisioningURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := "secret=" + url.QueryEscape(secret) +
		"&issuer=" + strings.ReplaceAll(url.QueryEscape(issuer), "+", "%20") +
		"&algorithm=SHA1&digits=6&period=30"
	return "otpauth://totp/" + label + "?" + q
}

// FormatSecret 每 4 位加空格便于手工输入。
func FormatSecret(secret string) string {
	var parts []string
	for i := 0; i < len(secret); i += 4 {
		end := min(i+4, len(secret))
		parts = append(parts, secret[i:end])
	}
	return strings.Join(parts, " ")
}

// GenerateRecoveryCodes 生成 count 个形如 ABCDE-FGHJK 的恢复码。
func GenerateRecoveryCodes(count int) []string {
	codes := make([]string, 0, count)
	max := big.NewInt(int64(len(recoveryAlphabet)))
	for i := 0; i < count; i++ {
		raw := make([]byte, 10)
		for j := range raw {
			n, _ := rand.Int(rand.Reader, max)
			raw[j] = recoveryAlphabet[n.Int64()]
		}
		codes = append(codes, string(raw[:5])+"-"+string(raw[5:]))
	}
	return codes
}

// HashRecoveryCodes 返回 bcrypt 哈希数组的 JSON（与 PHP 版存储格式一致）。
func HashRecoveryCodes(codes []string) (string, error) {
	hashes := make([]string, 0, len(codes))
	for _, c := range codes {
		h, err := password.Hash(NormalizeRecoveryCode(c))
		if err != nil {
			return "", err
		}
		hashes = append(hashes, h)
	}
	b, err := json.Marshal(hashes)
	return string(b), err
}

// ConsumeRecoveryCode 匹配并移除一个恢复码，返回剩余哈希的 JSON。
func ConsumeRecoveryCode(stored, input string) (bool, string) {
	if stored == "" {
		return false, ""
	}
	var hashes []any
	if err := json.Unmarshal([]byte(stored), &hashes); err != nil {
		return false, ""
	}
	normalized := NormalizeRecoveryCode(input)
	if normalized == "" {
		return false, ""
	}
	for i, h := range hashes {
		hs, ok := h.(string)
		if ok && password.Verify(normalized, hs) {
			rest := append([]any{}, hashes[:i]...)
			rest = append(rest, hashes[i+1:]...)
			b, _ := json.Marshal(rest)
			return true, string(b)
		}
	}
	return false, ""
}

// CountRecoveryCodes 统计剩余恢复码数量。
func CountRecoveryCodes(stored string) int {
	var hashes []any
	if stored == "" || json.Unmarshal([]byte(stored), &hashes) != nil {
		return 0
	}
	return len(hashes)
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

func NormalizeRecoveryCode(code string) string {
	return strings.ToUpper(nonAlnum.ReplaceAllString(code, ""))
}

// Base32Encode 无填充的 RFC 4648 Base32。
func Base32Encode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var sb strings.Builder
	var buffer uint32
	bits := 0
	for _, c := range b {
		buffer = buffer<<8 | uint32(c)
		bits += 8
		for bits >= 5 {
			sb.WriteByte(alphabet[(buffer>>(bits-5))&0x1F])
			bits -= 5
		}
	}
	if bits > 0 {
		sb.WriteByte(alphabet[(buffer<<(5-bits))&0x1F])
	}
	return sb.String()
}

var nonBase32 = regexp.MustCompile(`[^A-Z2-7]`)

// Base32Decode 与 PHP 版一致：先去掉非 [A-Z2-7] 字符（小写字母也会被去掉），再解码。
func Base32Decode(s string) []byte {
	s = strings.ToUpper(nonBase32.ReplaceAllString(s, ""))
	if s == "" {
		return nil
	}
	var out []byte
	var buffer uint32
	bits := 0
	for i := 0; i < len(s); i++ {
		idx := strings.IndexByte(alphabet, s[i])
		if idx < 0 {
			continue
		}
		buffer = buffer<<5 | uint32(idx)
		bits += 5
		if bits >= 8 {
			out = append(out, byte(buffer>>(bits-8)))
			bits -= 8
		}
	}
	return out
}
