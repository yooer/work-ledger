// work-ledger：工作台账系统（单文件程序）。
//
//	work-ledger serve                 启动 Web 服务
//	work-ledger install [选项]         初始化/升级数据库
//	work-ledger version               显示版本
//	work-ledger ctl <命令>             服务管理（wlctl）
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"work-ledger/internal/config"
	"work-ledger/internal/server"
	"work-ledger/internal/service"
)

func main() {
	args := os.Args[1:]
	// 通过 wlctl 软链接/别名调用时直接进入 ctl。
	base := strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	if base == "wlctl" {
		args = append([]string{"ctl"}, args...)
	}
	if len(args) == 0 {
		args = []string{"serve"}
	}
	var err error
	switch args[0] {
	case "serve":
		err = cmdServe(args[1:])
	case "install":
		err = cmdInstall(args[1:])
	case "version", "-v", "--version":
		fmt.Println(server.Version)
	case "ctl":
		err = cmdCtl(args[1:])
	case "service":
		err = cmdService(args[1:])
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[错误]", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`工作台账系统 work-ledger ` + server.Version + `

用法：
  work-ledger serve                      启动 Web 服务（默认）
  work-ledger install [选项]              初始化或升级数据库（可重复执行，不会清空数据）
      --admin-user=admin                 管理员登录名
      --admin-password=密码              管理员密码（至少 8 位；不填则随机生成并打印）
      --demo                             同时创建演示部门、人员和台账
  work-ledger version                    显示版本
  work-ledger ctl <命令>                  服务管理，等同 wlctl <命令>，详见 wlctl help

运行目录：环境变量 WL_HOME，或程序所在 bin/ 目录的上一级。
配置文件：{运行目录}/config/.env
`)
}

func loadConfig() (*config.Config, error) {
	return config.Load(config.ResolveHome())
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "监听地址，覆盖 LISTEN_ADDR")
	_ = fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.App.ListenAddr = *listen
	}
	if handled, err := runAsWindowsService(cfg); handled {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, cfg)
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	user := fs.String("admin-user", "admin", "管理员登录名")
	pwd := fs.String("admin-password", os.Getenv("ADMIN_PASSWORD"), "管理员密码")
	demo := fs.Bool("demo", false, "创建演示数据")
	_ = fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log, closer := server.Logger(cfg)
	defer closer.Close()
	d, s, err := server.Bootstrap(cfg, log)
	if err != nil {
		return err
	}
	defer d.Close()
	fmt.Println("数据库：", cfg.DB.Label())
	res, err := service.Install(s.Core, service.InstallOptions{AdminUser: *user, AdminPassword: *pwd, Demo: *demo})
	if err != nil {
		return err
	}
	for _, n := range res.Notes {
		fmt.Println(n)
	}
	fmt.Println()
	fmt.Println("安装完成。")
	if res.GeneratedPassword != "" {
		fmt.Println("管理员账号：", res.AdminUser)
		fmt.Println("初始密码：  ", res.GeneratedPassword)
		fmt.Println("请立即登录并修改密码。")
	}
	if res.DemoPassword != "" {
		fmt.Println("演示账号密码（manager / zhangsan / lisi / wangwu 通用）：", res.DemoPassword)
	}
	if cfg.AI.APIToken == "" {
		fmt.Println()
		fmt.Println("提示：尚未配置 AI_API_TOKEN，/api/v1 接口暂时不可用。")
	}
	return nil
}
