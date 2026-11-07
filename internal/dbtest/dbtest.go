// Package dbtest 为测试提供数据库：默认 SQLite 临时文件；设置环境变量 WL_TEST_DRIVER=mysql|postgres
// （以及 WL_TEST_HOST、WL_TEST_PORT、WL_TEST_USER、WL_TEST_PASSWORD）时，为每个测试创建独立的临时数据库并在结束后删除。
package dbtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"

	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-sql-driver/mysql"

	"work-ledger/internal/config"
	"work-ledger/internal/db"
)

// Driver 当前测试使用的数据库类型。
func Driver() string {
	d, err := config.NormalizeDriver(os.Getenv("WL_TEST_DRIVER"))
	if err != nil {
		return "sqlite"
	}
	return d
}

// Options 返回本测试专用的连接参数（非 SQLite 时已建好空库）。
func Options(t testing.TB, dir string) db.Options {
	t.Helper()
	driver := Driver()
	if driver == "sqlite" {
		return db.Options{Driver: "sqlite", Path: filepath.Join(dir, "data", "app.sqlite")}
	}
	o := db.Options{Driver: driver, Host: env("WL_TEST_HOST", "127.0.0.1"), User: env("WL_TEST_USER", "root"), Password: os.Getenv("WL_TEST_PASSWORD"), SSLMode: os.Getenv("WL_TEST_SSLMODE")}
	o.Port, _ = strconv.Atoi(env("WL_TEST_PORT", strconv.Itoa(config.DefaultPort(driver))))
	if driver == "postgres" && o.SSLMode == "" {
		o.SSLMode = "disable"
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	o.Name = "wltest_" + hex.EncodeToString(b)
	admin, err := adminConn(o)
	if err != nil {
		t.Fatalf("connect test database server: %v", err)
	}
	create := "CREATE DATABASE " + o.Name
	if driver == "mysql" {
		create += " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
	}
	if _, err := admin.Exec(create); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if driver == "postgres" {
			if _, err := admin.Exec("DROP DATABASE IF EXISTS " + o.Name + " WITH (FORCE)"); err == nil {
				return
			}
		}
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + o.Name)
	})
	return o
}

// Open 打开本测试专用数据库（调用方无需关闭，测试结束自动关闭）。
func Open(t testing.TB, dir string) (*db.DB, db.Options) {
	t.Helper()
	o := Options(t, dir)
	d, err := db.OpenWith(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, o
}

// Apply 把连接参数写入配置（服务端测试使用 config.Load 后调用）。
func Apply(cfg *config.Config, o db.Options) {
	cfg.DB.Driver = o.Driver
	if o.Driver == "sqlite" || o.Driver == "" {
		cfg.DB.Driver = "sqlite"
		cfg.DB.SQLitePath = o.Path
		return
	}
	cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, cfg.DB.User, cfg.DB.Password, cfg.DB.SSLMode = o.Host, o.Port, o.Name, o.User, o.Password, o.SSLMode
}

func adminConn(o db.Options) (*sql.DB, error) {
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	if o.Driver == "mysql" {
		c := mysql.NewConfig()
		c.User, c.Passwd, c.Net, c.Addr = o.User, o.Password, "tcp", addr
		return sql.Open("mysql", c.FormatDSN())
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(o.User, o.Password), Host: addr, Path: "/postgres", RawQuery: "sslmode=" + o.SSLMode}
	return sql.Open("pgx", u.String())
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
