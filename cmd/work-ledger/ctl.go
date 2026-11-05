package main

import (
	"flag"
	"fmt"
	"os"

	"work-ledger/internal/ctl"
	"work-ledger/internal/server"
	"work-ledger/internal/timeutil"
)

func cmdCtl(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	timeutil.SetLocation(cfg.App.Timezone)
	return ctl.New(cfg, server.Version).Run(args)
}

// cmdService：work-ledger service install|uninstall（由一键部署脚本调用）。
func cmdService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法：work-ledger service install [--user work-ledger] | uninstall")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c := ctl.New(cfg, server.Version)
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ExitOnError)
		user := fs.String("user", "work-ledger", "运行账号（仅 Linux）")
		_ = fs.Parse(args[1:])
		if err := ctl.InstallService(cfg.Paths.Home, c.Bin, *user); err != nil {
			return err
		}
		fmt.Println("服务已注册：", ctl.ServiceName)
		return nil
	case "uninstall":
		return c.Uninstall(false, true)
	}
	fmt.Fprintln(os.Stderr, "未知子命令：", args[0])
	return nil
}
