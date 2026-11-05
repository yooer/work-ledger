// Package password 提供与 PHP password_hash/password_verify 兼容的 bcrypt 哈希。
package password

import (
	"crypto/rand"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Cost 与 PHP PASSWORD_DEFAULT 的默认 cost 一致。
const Cost = 10

// Hash 生成 $2y$ 前缀的 bcrypt 哈希（PHP 原生格式，PHP 版也能直接校验）。
func Hash(plain string) (string, error) {
	if len(plain) > 72 {
		plain = plain[:72] // PHP bcrypt 同样只取前 72 字节
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), Cost)
	if err != nil {
		return "", err
	}
	s := string(h)
	if strings.HasPrefix(s, "$2a$") {
		s = "$2y$" + s[4:]
	}
	return s, nil
}

// Verify 校验密码，兼容 $2y$/$2a$/$2b$。
func Verify(plain, hash string) bool {
	if hash == "" {
		return false
	}
	if len(plain) > 72 {
		plain = plain[:72]
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// NeedsRehash 当 cost 低于当前默认值时返回 true。
func NeedsRehash(hash string) bool {
	cost, err := bcrypt.Cost([]byte(hash))
	return err != nil || cost < Cost
}

const randomAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789@#$%"

// Random 生成随机初始密码（与 PHP 版 random_password 字符集一致）。
func Random(length int) string {
	max := big.NewInt(int64(len(randomAlphabet)))
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, max)
		b[i] = randomAlphabet[n.Int64()]
	}
	return string(b)
}
