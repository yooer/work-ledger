//go:build linux

package ctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const unitPath = "/etc/systemd/system/work-ledger.service"
const shortcut = "/usr/local/bin/wlctl"

func systemctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func serviceState() string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "未知（系统没有 systemd）"
	}
	state, _ := systemctl("is-active", ServiceName)
	enabled, _ := systemctl("is-enabled", ServiceName)
	return fmt.Sprintf("%s（开机自启：%s）", state, enabled)
}

func controlService(action string) error {
	if out, err := systemctl(action, ServiceName); err != nil {
		return fmt.Errorf("systemctl %s 失败：%s", action, out)
	}
	return nil
}

func requireAdmin() error {
	if os.Geteuid() != 0 {
		return errors.New("该命令需要 root 权限，请使用 sudo wlctl …")
	}
	return nil
}

// fixOwner 把文件属主改为运行目录的属主（服务以 work-ledger 用户运行）。
func fixOwner(path, home string) {
	info, err := os.Stat(home)
	if err != nil {
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
}

// InstallService 写入 systemd 单元并启用开机自启。
func InstallService(home, bin, user string) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	if user == "" {
		user = "work-ledger"
	}
	unit := fmt.Sprintf(`[Unit]
Description=Work Ledger 工作台账
Documentation=https://github.com/yooer/work-ledger
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%[1]s
Group=%[1]s
WorkingDirectory=%[2]s
Environment=WL_HOME=%[2]s
ExecStart=%[3]s serve
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=%[2]s

[Install]
WantedBy=multi-user.target
`, user, home, bin)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if out, err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload 失败：%s", out)
	}
	if out, err := systemctl("enable", ServiceName); err != nil {
		return fmt.Errorf("enable 失败：%s", out)
	}
	_ = os.Remove(shortcut)
	if err := os.Symlink(bin, shortcut); err != nil {
		return fmt.Errorf("创建 %s 失败：%w", shortcut, err)
	}
	_ = filepath.Base(bin)
	return nil
}

func removeService() error {
	_, _ = systemctl("disable", "--now", ServiceName)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, _ = systemctl("daemon-reload")
	return nil
}

func removeShortcut() {
	if target, err := os.Readlink(shortcut); err == nil && strings.Contains(target, "work-ledger") {
		_ = os.Remove(shortcut)
	}
}
