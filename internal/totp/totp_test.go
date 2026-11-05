package totp

import (
	"testing"
	"time"
)

// RFC 6238 附录 B 测试向量（SHA1，密钥 "12345678901234567890"），取后 6 位。
func TestCodeRFC6238(t *testing.T) {
	secret := Base32Encode([]byte("12345678901234567890"))
	if secret != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatalf("base32 = %s", secret)
	}
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		if got := Code(secret, ts); got != want {
			t.Errorf("Code(%d) = %s, want %s", ts, got, want)
		}
	}
}

func TestBase32RoundTripAndPHPQuirk(t *testing.T) {
	for _, s := range []string{"", "a", "ab", "abcde", "\x00\xff\x10 hello"} {
		if got := string(Base32Decode(Base32Encode([]byte(s)))); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
	// 与 PHP 版一致：空格等非法字符被忽略。
	if string(Base32Decode("GEZD GNBV")) != string(Base32Decode("GEZDGNBV")) {
		t.Error("spaces should be ignored")
	}
}

func TestVerifyWindowAndReplayStep(t *testing.T) {
	secret := GenerateSecret()
	base := time.Unix(1_800_000_000, 0)
	now = func() time.Time { return base }
	defer func() { now = time.Now }()
	code := Code(secret, base.Unix()-30) // 上一个时间步仍在窗口内
	step := Verify(secret, code, 1)
	if step != base.Unix()/30-1 {
		t.Fatalf("step = %d", step)
	}
	if Verify(secret, Code(secret, base.Unix()-90), 1) != -1 {
		t.Fatal("code outside window should fail")
	}
	if Verify(secret, "12345", 1) != -1 {
		t.Fatal("5 digits should fail")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := GenerateRecoveryCodes(8)
	if len(codes) != 8 || len(codes[0]) != 11 || codes[0][5] != '-' {
		t.Fatalf("codes = %v", codes)
	}
	stored, err := HashRecoveryCodes(codes[:2])
	if err != nil {
		t.Fatal(err)
	}
	ok, rest := ConsumeRecoveryCode(stored, " "+lower(codes[1])+" ")
	if !ok || CountRecoveryCodes(rest) != 1 {
		t.Fatalf("consume failed: %v %s", ok, rest)
	}
	if ok, _ := ConsumeRecoveryCode(rest, codes[1]); ok {
		t.Fatal("recovery code reused")
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
