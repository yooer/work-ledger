package ctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"work-ledger/internal/config"
)

func TestSetEnvValuesKeepsOtherLines(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".env")
	orig := "# 注释\nAPP_NAME=\"工作台账\"\nDB_DRIVER=sqlite\nAI_API_KEY=abc\n"
	if err := os.WriteFile(f, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := setEnvValues(f, map[string]string{"DB_DRIVER": "mysql", "DB_HOST": "10.0.0.5", "DB_PASSWORD": `p@ss "w" #1`})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bak); string(b) != orig {
		t.Fatal("backup should keep original content")
	}
	b, _ := os.ReadFile(f)
	got := string(b)
	for _, want := range []string{"# 注释\n", "APP_NAME=\"工作台账\"\n", "DB_DRIVER=mysql\n", "AI_API_KEY=abc\n", "DB_HOST=10.0.0.5\n", `DB_PASSWORD='p@ss "w" #1'`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "DB_DRIVER=") != 1 {
		t.Fatal("DB_DRIVER should be replaced in place")
	}
	// 写入的密码能被配置加载原样读回
	os.Unsetenv("DB_PASSWORD")
	os.Unsetenv("DB_DRIVER")
	os.Unsetenv("DB_HOST")
	t.Cleanup(func() { os.Unsetenv("DB_PASSWORD"); os.Unsetenv("DB_DRIVER"); os.Unsetenv("DB_HOST") })
	if err := config.LoadEnvFile(f); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DB_PASSWORD") != `p@ss "w" #1` {
		t.Fatalf("password read back = %q", os.Getenv("DB_PASSWORD"))
	}
}

func TestQuoteEnv(t *testing.T) {
	for in, want := range map[string]string{"plain": "plain", "a b": `"a b"`, `x"y`: `'x"y'`, "": ""} {
		if got, err := quoteEnv(in); err != nil || got != want {
			t.Errorf("quoteEnv(%q) = %q %v", in, got, err)
		}
	}
	if _, err := quoteEnv(`a'b"c`); err == nil {
		t.Error("both quotes should be rejected")
	}
	if _, err := quoteEnv("a\nb"); err == nil {
		t.Error("newline should be rejected")
	}
}

func TestSameDB(t *testing.T) {
	a := config.DB{Driver: "mysql", Host: "H", Port: 3306, Name: "x"}
	if !sameDB(a, config.DB{Driver: "mysql", Host: "h", Port: 3306, Name: "x"}) || sameDB(a, config.DB{Driver: "postgres", Host: "h", Port: 3306, Name: "x"}) {
		t.Fatal("sameDB wrong")
	}
}
