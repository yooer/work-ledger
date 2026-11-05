//go:build windows

package ctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func sc(args ...string) (string, error) {
	out, err := exec.Command("sc.exe", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func serviceState() string {
	out, err := sc("query", ServiceName)
	if err != nil {
		return "未安装或无法查询"
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "STATE") {
			f := strings.Fields(line)
			return f[len(f)-1]
		}
	}
	return "未知"
}

func controlService(action string) error {
	switch action {
	case "restart":
		_ = controlService("stop")
		return controlService("start")
	case "stop":
		if _, err := sc("stop", ServiceName); err != nil && !strings.Contains(serviceState(), "STOPPED") {
			return fmt.Errorf("停止服务失败")
		}
		for i := 0; i < 30 && !strings.Contains(serviceState(), "STOPPED"); i++ {
			time.Sleep(time.Second)
		}
		return nil
	case "start":
		if out, err := sc("start", ServiceName); err != nil && !strings.Contains(serviceState(), "RUNNING") {
			return fmt.Errorf("启动服务失败：%s", out)
		}
		return nil
	}
	return fmt.Errorf("不支持的操作：%s", action)
}

func requireAdmin() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return errors.New("该命令需要管理员权限，请在“以管理员身份运行”的 PowerShell 中执行")
}

func fixOwner(path, home string) {}

// InstallService 注册 Windows 服务（自动启动、失败自动重启），并在 bin 目录生成 wlctl.cmd。
func InstallService(home, bin, user string) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	_, _ = sc("stop", ServiceName)
	_, _ = sc("delete", ServiceName)
	time.Sleep(time.Second)
	binPath := fmt.Sprintf(`"%s" serve`, bin)
	if out, err := sc("create", ServiceName, "binPath=", binPath, "start=", "auto", "DisplayName=", "Work Ledger 工作台账"); err != nil {
		return fmt.Errorf("注册服务失败：%s", out)
	}
	_, _ = sc("description", ServiceName, "工作台账系统（Go + SQLite）")
	_, _ = sc("failure", ServiceName, "reset=", "86400", "actions=", "restart/5000/restart/5000/restart/30000")
	cmd := "@echo off\r\n\"" + bin + "\" ctl %*\r\n"
	return os.WriteFile(filepath.Join(filepath.Dir(bin), "wlctl.cmd"), []byte(cmd), 0o644)
}

func removeService() error {
	_ = controlService("stop")
	if out, err := sc("delete", ServiceName); err != nil {
		return fmt.Errorf("删除服务失败：%s", out)
	}
	return nil
}

func removeShortcut() {
	exe, _ := os.Executable()
	_ = os.Remove(filepath.Join(filepath.Dir(exe), "wlctl.cmd"))
}
