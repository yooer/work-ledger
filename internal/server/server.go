// Package server 组装依赖并启动 HTTP 服务。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"work-ledger/internal/config"
	"work-ledger/internal/controller"
	"work-ledger/internal/db"
	"work-ledger/internal/service"
	"work-ledger/internal/session"
	"work-ledger/internal/timeutil"
	"work-ledger/internal/web"
	webassets "work-ledger/web"
)

// Version 由构建时 -ldflags 注入。
var Version = "dev"

// Logger 输出到 stderr 与 logs/app.log。
func Logger(cfg *config.Config) (*slog.Logger, io.Closer) {
	var w io.Writer = os.Stderr
	var closer io.Closer = io.NopCloser(nil)
	if err := os.MkdirAll(cfg.Paths.Logs, 0o750); err == nil {
		if f, err := os.OpenFile(filepath.Join(cfg.Paths.Logs, "app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err == nil {
			w = io.MultiWriter(os.Stderr, f)
			closer = f
		}
	}
	level := slog.LevelInfo
	if cfg.App.Debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), closer
}

// Bootstrap 打开数据库并执行迁移，返回服务集合。
func Bootstrap(cfg *config.Config, log *slog.Logger) (*db.DB, *service.Services, error) {
	timeutil.SetLocation(cfg.App.Timezone)
	if err := cfg.EnsureDirs(); err != nil {
		return nil, nil, err
	}
	d, err := db.Open(cfg.DB.SQLitePath)
	if err != nil {
		return nil, nil, err
	}
	return d, service.New(d, cfg, log), nil
}

// Handler 构建完整 HTTP 处理器（也用于测试）。
func Handler(cfg *config.Config, s *service.Services, log *slog.Logger) (http.Handler, error) {
	assetVersion := Version
	if Version == "dev" {
		assetVersion = "dev" + strconv.FormatInt(time.Now().Unix(), 10) // 开发版每次启动刷新静态资源缓存
	}
	views, err := web.LoadViews(webassets.FS, assetVersion)
	if err != nil {
		return nil, fmt.Errorf("模板解析失败：%w", err)
	}
	store := &session.Store{DB: s.Core.DB, CookieName: cfg.App.SessionName, Lifetime: time.Duration(cfg.App.SessionHours) * time.Hour}
	app := web.NewApp(cfg, store, views, s.Auth.LoadActiveUser, log, Version)

	assets, _ := fs.Sub(webassets.FS, "assets")
	static, _ := fs.Sub(webassets.FS, "static")
	app.Raw("GET /assets/", cacheStatic(http.StripPrefix("/assets/", http.FileServerFS(assets))))
	app.Raw("GET /static/", cacheStatic(http.StripPrefix("/static/", http.FileServerFS(static))))
	app.Raw("GET /favicon.ico", http.RedirectHandler("/assets/image/favicon.svg", http.StatusMovedPermanently))

	controller.Register(app, s)
	return app.Mux, nil
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h.ServeHTTP(w, r)
	})
}

// Run 启动服务并在 ctx 取消时优雅退出。
func Run(ctx context.Context, cfg *config.Config) error {
	log, closer := Logger(cfg)
	defer closer.Close()

	d, s, err := Bootstrap(cfg, log)
	if err != nil {
		return err
	}
	defer d.Close()
	if !d.Installed() {
		return errors.New("系统尚未初始化，请先执行：work-ledger install --admin-password=至少8位密码")
	}
	// 每次启动自动补齐升级所需的表/字段（幂等）。
	if notes, err := d.Migrate(); err != nil {
		return fmt.Errorf("数据库升级失败：%w", err)
	} else {
		for _, n := range notes {
			log.Info("migrate", "note", n)
		}
	}
	_ = s.Settings.EnsureDefaults()
	if err := s.Positions.EnsureDefaults(); err != nil {
		return fmt.Errorf("职务初始化失败：%w", err)
	}

	h, err := Handler(cfg, s, log)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.App.ListenAddr,
		Handler:           h,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      time.Duration(cfg.AI.Timeout+60) * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	startBackground(ctx, s, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("server started", "addr", cfg.App.ListenAddr, "version", Version, "home", cfg.Paths.Home)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		log.Info("server stopping")
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// startBackground 启动后台任务（自动生成报告的调度器）。
func startBackground(ctx context.Context, s *service.Services, log *slog.Logger) {
	go s.Scheduler.Run(ctx)
}
