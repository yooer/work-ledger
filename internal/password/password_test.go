package password

import (
	"strings"
	"testing"
)

// PHP 手册中的 password_verify 示例哈希。
func TestVerifyPHPHash(t *testing.T) {
	if !Verify("rasmuslerdorf", "$2y$10$.vGA1O9wmRjrwAVXD98HNOgsNpDczlqm3Jq7KnEd1rVAGv3Fykk1a") {
		t.Fatal("PHP $2y$ hash should verify")
	}
	if Verify("wrong", "$2y$10$.vGA1O9wmRjrwAVXD98HNOgsNpDczlqm3Jq7KnEd1rVAGv3Fykk1a") {
		t.Fatal("wrong password verified")
	}
}

func TestHashUsesPHPPrefix(t *testing.T) {
	h, err := Hash("Admin12345")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2y$10$") || !Verify("Admin12345", h) || NeedsRehash(h) {
		t.Fatalf("hash = %s", h)
	}
	if len(Random(16)) != 16 {
		t.Fatal("random length")
	}
}
