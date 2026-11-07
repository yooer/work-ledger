// Package config 读取 .env 与环境变量，字段与 PHP 版 config/app.php 保持一致。
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type App struct {
	Name          string
	Env           string
	Debug         bool
	Timezone      string
	URL           string
	SessionName   string
	SessionHours  int
	ListenAddr    string
	UpdateBaseURL string
	UpdateMirror  string // 备用下载地址（官方地址连不上时使用），为空时按内置规则推导
}

// DB 数据库连接。Driver 为 sqlite（默认）、mysql（MySQL 8.0+ / MariaDB 10.5+）或 postgres（PostgreSQL 12+）。
type DB struct {
	Driver     string
	SQLitePath string
	Host       string
	Port       int
	Name       string
	User       string
	Password   string
	SSLMode    string // postgres：disable/prefer/require/verify-full（默认 prefer）；mysql：true/false/skip-verify（默认不加密）
}

// IsSQLite 是否使用 SQLite。
func (d DB) IsSQLite() bool { return d.Driver == "sqlite" }

// Label 用于状态显示，如 “MySQL 192.168.1.10:3306/work_ledger”。
func (d DB) Label() string {
	switch d.Driver {
	case "mysql":
		return fmt.Sprintf("MySQL/MariaDB %s:%d/%s", d.Host, d.Port, d.Name)
	case "postgres":
		return fmt.Sprintf("PostgreSQL %s:%d/%s", d.Host, d.Port, d.Name)
	}
	return "SQLite " + d.SQLitePath
}

// NormalizeDriver 统一驱动名：mariadb → mysql，pgsql/postgresql/pg → postgres，空 → sqlite。
func NormalizeDriver(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "sqlite", "sqlite3":
		return "sqlite", nil
	case "mysql", "mariadb":
		return "mysql", nil
	case "postgres", "postgresql", "pgsql", "pg":
		return "postgres", nil
	}
	return "", fmt.Errorf("不支持的数据库类型 DB_DRIVER=%s（可选 sqlite / mysql / postgres）", v)
}

// DefaultPort 各数据库默认端口。
func DefaultPort(driver string) int {
	switch driver {
	case "mysql":
		return 3306
	case "postgres":
		return 5432
	}
	return 0
}

// Validate 检查连接参数是否齐全。
func (d DB) Validate() error {
	if d.IsSQLite() {
		return nil
	}
	var miss []string
	for k, v := range map[string]string{"DB_HOST": d.Host, "DB_NAME": d.Name, "DB_USER": d.User} {
		if strings.TrimSpace(v) == "" {
			miss = append(miss, k)
		}
	}
	if len(miss) > 0 {
		sort.Strings(miss)
		return fmt.Errorf("使用 %s 时必须配置 %s", d.Driver, strings.Join(miss, "、"))
	}
	return nil
}

type AI struct {
	APIToken string
	BaseURL  string
	Model    string
	APIKey   string
	Timeout  int
}

type TOTP struct {
	Issuer         string
	Digits         int
	Period         int
	Window         int
	PendingTimeout int
	Require        bool
}

type Limits struct {
	MaxRecordsForAI       int
	MaxRecordsForAIYearly int
	MaxRecordsForAIPeriod int
	MaxRecordsForAPI      int
	ImportMaxMB           int
	ImportMaxRows         int
	ExportMaxRows         int
	BackupMaxUploadMB     int
	BackupMaxJSONMB       int
}

// Paths 是运行时目录布局：{Home}/bin, config, data, backups, logs, tmp。
type Paths struct {
	Home    string
	Data    string
	Backups string
	Logs    string
	Tmp     string
	EnvFile string
}

type Config struct {
	App    App
	DB     DB
	AI     AI
	TOTP   TOTP
	Limits Limits
	Paths  Paths
}

var envLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)

// LoadEnvFile 与 PHP 版 env_load 行为一致：已存在的环境变量不会被覆盖。
func LoadEnvFile(file string) error {
	f, err := os.Open(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\xEF\xBB\xBF"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := envLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, value := m[1], strings.TrimSpace(m[2])
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func boolean(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func integer(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// ResolveHome 决定运行根目录：WL_HOME 环境变量 > 程序所在 bin 目录的上一级 > 当前目录。
func ResolveHome() string {
	if h := os.Getenv("WL_HOME"); h != "" {
		abs, _ := filepath.Abs(h)
		return abs
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		if filepath.Base(dir) == "bin" {
			return filepath.Dir(dir)
		}
	}
	wd, _ := os.Getwd()
	return wd
}

// Load 读取 {home}/config/.env（不存在时尝试 {home}/.env）并组装配置。
func Load(home string) (*Config, error) {
	envFile := filepath.Join(home, "config", ".env")
	if _, err := os.Stat(envFile); err != nil {
		if alt := filepath.Join(home, ".env"); fileExists(alt) {
			envFile = alt
		}
	}
	if err := LoadEnvFile(envFile); err != nil {
		return nil, err
	}

	name := str("APP_NAME", "工作台账")
	cfg := &Config{
		App: App{
			Name:          name,
			Env:           str("APP_ENV", "production"),
			Debug:         boolean("APP_DEBUG", false),
			Timezone:      str("APP_TIMEZONE", "Asia/Shanghai"),
			URL:           strings.TrimRight(str("APP_URL", "http://127.0.0.1:8080"), "/"),
			SessionName:   str("SESSION_NAME", "work_ledger_session"),
			SessionHours:  max(1, integer("SESSION_LIFETIME_HOURS", 12)),
			ListenAddr:    str("LISTEN_ADDR", "127.0.0.1:8080"),
			UpdateBaseURL: strings.TrimRight(str("UPDATE_BASE_URL", "https://github.com/yooer/work-ledger"), "/"),
			UpdateMirror:  strings.TrimRight(str("UPDATE_MIRROR_URL", ""), "/"),
		},
		AI: AI{
			APIToken: str("AI_API_TOKEN", ""),
			BaseURL:  strings.TrimRight(str("AI_BASE_URL", "https://api.deepseek.com/v1"), "/"),
			Model:    str("AI_MODEL", "deepseek-chat"),
			APIKey:   str("AI_API_KEY", ""),
			Timeout:  max(10, integer("AI_TIMEOUT", 120)),
		},
		TOTP: TOTP{
			Issuer:         str("TOTP_ISSUER", name),
			Digits:         6,
			Period:         30,
			Window:         1,
			PendingTimeout: 300,
			Require:        boolean("REQUIRE_2FA", false),
		},
		Limits: Limits{
			MaxRecordsForAI:       150,
			MaxRecordsForAIYearly: 160,
			MaxRecordsForAIPeriod: 150,
			MaxRecordsForAPI:      5000,
			ImportMaxMB:           max(1, integer("IMPORT_MAX_MB", 10)),
			ImportMaxRows:         max(1, integer("IMPORT_MAX_ROWS", 5000)),
			ExportMaxRows:         max(1, integer("EXPORT_MAX_ROWS", 20000)),
			BackupMaxUploadMB:     max(1, integer("BACKUP_MAX_UPLOAD_MB", 50)),
			BackupMaxJSONMB:       max(1, integer("BACKUP_MAX_JSON_MB", 200)),
		},
		Paths: Paths{
			Home:    home,
			Data:    filepath.Join(home, "data"),
			Backups: filepath.Join(home, "backups"),
			Logs:    filepath.Join(home, "logs"),
			Tmp:     filepath.Join(home, "tmp"),
			EnvFile: envFile,
		},
	}

	cfg.DB.SQLitePath = str("DB_SQLITE_PATH", filepath.Join(cfg.Paths.Data, "app.sqlite"))
	if !filepath.IsAbs(cfg.DB.SQLitePath) {
		cfg.DB.SQLitePath = filepath.Join(home, cfg.DB.SQLitePath)
	}
	driver, err := NormalizeDriver(str("DB_DRIVER", "sqlite"))
	if err != nil {
		return nil, err
	}
	cfg.DB.Driver = driver
	cfg.DB.Host = strings.TrimSpace(str("DB_HOST", ""))
	cfg.DB.Port = integer("DB_PORT", DefaultPort(driver))
	cfg.DB.Name = strings.TrimSpace(str("DB_NAME", ""))
	cfg.DB.User = strings.TrimSpace(str("DB_USER", ""))
	cfg.DB.Password = str("DB_PASSWORD", "")
	cfg.DB.SSLMode = strings.TrimSpace(str("DB_SSLMODE", ""))
	if err := cfg.DB.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// EnsureDirs 创建运行所需目录。
func (c *Config) EnsureDirs() error {
	for _, dir := range []string{c.Paths.Data, c.Paths.Backups, c.Paths.Logs, c.Paths.Tmp, filepath.Dir(c.DB.SQLitePath)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) IsLocal() bool { return c.App.Env == "local" }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
