// Package ctl 实现 wlctl：服务状态、启停、日志、检查更新、升级/回滚、备份、卸载。
// Linux 使用 systemd，Windows 使用服务控制管理器（sc.exe）。
package ctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"work-ledger/internal/config"
	"work-ledger/internal/timeutil"
)

// ServiceName systemd 单元名 / Windows 服务名。
var ServiceName = map[string]string{"linux": "work-ledger", "windows": "WorkLedger"}[runtime.GOOS]

// Ctl 运行上下文。
type Ctl struct {
	Cfg     *config.Config
	Version string
	Out     io.Writer
	Bin     string // 当前程序绝对路径（bin/work-ledger[.exe]）
}

// New 创建 Ctl。
func New(cfg *config.Config, version string) *Ctl {
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return &Ctl{Cfg: cfg, Version: version, Out: os.Stdout, Bin: exe}
}

func (c *Ctl) printf(format string, a ...any) { fmt.Fprintf(c.Out, format, a...) }

// Usage 帮助。
const Usage = `wlctl —— 工作台账服务管理工具

用法：wlctl <命令> [参数]

  status                 查看服务状态、版本、健康检查、数据库与备份
  start | stop | restart 启动 / 停止 / 重启服务
  logs [-n 200] [-f]     查看日志（Linux 读取 journald，其他读取 logs/app.log）
  version                显示当前版本
  check                  检查 GitHub 上的最新版本
  upgrade [版本]         升级到最新版或指定版本（如 v1.2.0）：下载 → 校验 sha256 →
                         备份数据库 → 替换程序 → 重启 → 健康检查，失败自动回滚
      --file 路径        使用本地离线程序文件升级（需同时提供 --sha256 或跳过校验 --no-verify）
  rollback [--with-db]   回滚到升级前的程序版本（--with-db 同时恢复升级前的数据库）
  backup                 立即备份数据库到 backups/（SQLite 为数据库文件，MySQL/PostgreSQL 为 .json.gz）
  migrate-db --driver …  把全部数据迁移到 MySQL / MariaDB / PostgreSQL（或迁回 SQLite），
                         自动改写配置并重启服务；原数据库不动（详见 wlctl migrate-db -h）
  uninstall [--purge]    停止并移除系统服务；默认保留数据，--purge 同时删除整个运行目录

升级源：配置项 UPDATE_BASE_URL（默认 https://github.com/yooer/work-ledger）。
`

// Run 分发命令。
func (c *Ctl) Run(args []string) error {
	if len(args) == 0 {
		c.printf("%s", Usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return c.Status()
	case "start":
		return c.service("start")
	case "stop":
		return c.service("stop")
	case "restart":
		return c.service("restart")
	case "logs":
		fs := flag.NewFlagSet("logs", flag.ContinueOnError)
		n := fs.Int("n", 200, "行数")
		follow := fs.Bool("f", false, "持续输出")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return c.Logs(*n, *follow)
	case "version":
		c.printf("%s\n", c.Version)
		return nil
	case "check":
		_, err := c.Check()
		return err
	case "upgrade":
		fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
		file := fs.String("file", "", "本地程序文件")
		sum := fs.String("sha256", "", "本地文件的 sha256")
		noVerify := fs.Bool("no-verify", false, "跳过 sha256 校验（仅限离线文件）")
		yes := fs.Bool("y", false, "不再询问")
		if err := fs.Parse(reorder(rest)); err != nil {
			return err
		}
		return c.Upgrade(fs.Arg(0), *file, *sum, *noVerify, *yes)
	case "rollback":
		fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
		withDB := fs.Bool("with-db", false, "同时恢复升级前数据库")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return c.Rollback(*withDB)
	case "migrate-db":
		return c.MigrateDB(rest)
	case "backup":
		p, err := c.BackupDB("manual")
		if err == nil {
			c.printf("数据库已备份到：%s\n", p)
		}
		return err
	case "uninstall":
		fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
		purge := fs.Bool("purge", false, "删除运行目录")
		yes := fs.Bool("y", false, "不再询问")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return c.Uninstall(*purge, *yes)
	case "help", "-h", "--help":
		c.printf("%s", Usage)
		return nil
	}
	return fmt.Errorf("未知命令：%s（执行 wlctl help 查看帮助）", cmd)
}

// reorder 允许“版本号在前、参数在后”的写法。
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if (a == "--file" || a == "-file" || a == "--sha256" || a == "-sha256") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// ---------------------------------------------------------------------------
// 状态与健康检查
// ---------------------------------------------------------------------------

func (c *Ctl) healthURL() string {
	addr := c.Cfg.App.ListenAddr
	host, port := addr, ""
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host, port = addr[:i], addr[i+1:]
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port + "/api/v1/health"
}

// Health 请求健康检查接口。
func (c *Ctl) Health(timeout time.Duration) error {
	cl := &http.Client{Timeout: timeout}
	resp, err := cl.Get(c.healthURL())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Ctl) waitHealthy(d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		if last = c.Health(3 * time.Second); last == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("健康检查失败：%v", last)
}

// Status 打印状态。
func (c *Ctl) Status() error {
	c.printf("工作台账 work-ledger %s（%s/%s）\n", c.Version, runtime.GOOS, runtime.GOARCH)
	c.printf("运行目录：%s\n程序文件：%s\n配置文件：%s\n", c.Cfg.Paths.Home, c.Bin, c.Cfg.Paths.EnvFile)
	c.printf("服务状态：%s\n", serviceState())
	c.printf("监听地址：%s\n", c.Cfg.App.ListenAddr)
	if err := c.Health(3 * time.Second); err != nil {
		c.printf("健康检查：失败（%v）\n", err)
	} else {
		c.printf("健康检查：正常（%s）\n", c.healthURL())
	}
	c.printf("数据库：  %s\n", c.dbStatus())
	if latest := latestFile(c.Cfg.Paths.Backups); latest != "" {
		c.printf("最近备份：%s\n", latest)
	}
	if _, err := os.Stat(c.Bin + ".prev"); err == nil {
		c.printf("可回滚：  是（%s.prev）\n", filepath.Base(c.Bin))
	}
	return nil
}

func latestFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") { // 跳过 .last-upgrade 等内部标记文件
			continue
		}
		if info, err := e.Info(); err == nil && !e.IsDir() && info.ModTime().After(bestTime) {
			best, bestTime = e.Name(), info.ModTime()
		}
	}
	if best == "" {
		return ""
	}
	return best + "（" + bestTime.In(timeutil.Location()).Format("2006-01-02 15:04") + "）"
}

func (c *Ctl) service(action string) error {
	if err := controlService(action); err != nil {
		return err
	}
	c.printf("已执行：%s %s\n", action, ServiceName)
	if action != "stop" {
		if err := c.waitHealthy(30 * time.Second); err != nil {
			return err
		}
		c.printf("服务运行正常。\n")
	}
	return nil
}

// Logs 输出日志。
func (c *Ctl) Logs(n int, follow bool) error {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("journalctl"); err == nil {
			args := []string{"-u", ServiceName, "-n", strconv.Itoa(n), "--no-pager"}
			if follow {
				args = append(args, "-f")
			}
			cmd := exec.Command("journalctl", args...)
			cmd.Stdout, cmd.Stderr = c.Out, os.Stderr
			return cmd.Run()
		}
	}
	return tailFile(filepath.Join(c.Cfg.Paths.Logs, "app.log"), n, follow, c.Out)
}

func tailFile(path string, n int, follow bool, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("无法打开日志：%w", err)
	}
	defer f.Close()
	info, _ := f.Stat()
	size := info.Size()
	start := max(0, size-int64(n)*400)
	_, _ = f.Seek(start, io.SeekStart)
	data, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Fprintln(out, strings.Join(lines, "\n"))
	if !follow {
		return nil
	}
	for {
		time.Sleep(time.Second)
		chunk, _ := io.ReadAll(f)
		if len(chunk) > 0 {
			_, _ = out.Write(chunk)
		}
	}
}

// ---------------------------------------------------------------------------
// 版本检查与升级
// ---------------------------------------------------------------------------

// ReleaseInfo 每个 Release 附带的 version.json。
type ReleaseInfo struct {
	Version     string            `json:"version"`
	PublishedAt string            `json:"published_at"`
	Assets      map[string]string `json:"assets"`
	Notes       string            `json:"notes"`
}

// httpClient 连接/握手超时较短，便于官方地址不可达时尽快切换到备用地址。
var httpClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// releasePath 相对仓库地址的下载路径。
func releasePath(tag, file string) string {
	if tag == "" || tag == "latest" {
		return "/releases/latest/download/" + file
	}
	return "/releases/download/" + tag + "/" + file
}

// mirrorBase 备用地址：配置 UPDATE_MIRROR_URL 优先；否则官方地址为 github.com 时使用内置备用域名。
func (c *Ctl) mirrorBase() string {
	if c.Cfg.App.UpdateMirror != "" {
		return c.Cfg.App.UpdateMirror
	}
	base := c.Cfg.App.UpdateBaseURL
	if strings.HasPrefix(base, "https://github.com/") {
		return "https://github.tool.do/" + strings.TrimPrefix(base, "https://github.com/")
	}
	return ""
}

// fetch 先请求官方地址；连接失败或服务端错误时再使用备用地址。404 等明确结果不切换。
func (c *Ctl) fetch(path string, limit int64) ([]byte, error) {
	bases := []string{c.Cfg.App.UpdateBaseURL}
	if m := c.mirrorBase(); m != "" && m != bases[0] {
		bases = append(bases, m)
	}
	var lastErr error
	for i, base := range bases {
		if i > 0 {
			c.printf("官方地址无法访问（%v），改用备用地址下载 …\n", lastErr)
		}
		body, status, err := c.get(base+path, limit)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if status == http.StatusNotFound {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Ctl) get(url string, limit int64) ([]byte, int, error) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "work-ledger-ctl/"+c.Version)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("下载失败（%s）：%v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("下载失败（%s）：HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, 0, fmt.Errorf("下载中断（%s）：%v", url, err)
	}
	return b, 200, nil
}

// Check 检查最新版本。
func (c *Ctl) Check() (*ReleaseInfo, error) {
	info, err := c.release("latest")
	if err != nil {
		return nil, err
	}
	c.printf("当前版本：%s\n最新版本：%s（%s）\n", c.Version, info.Version, info.PublishedAt)
	if CompareVersion(info.Version, c.Version) > 0 {
		c.printf("有新版本可用，执行 wlctl upgrade 升级。\n")
	} else {
		c.printf("已是最新版本。\n")
	}
	return info, nil
}

func (c *Ctl) release(tag string) (*ReleaseInfo, error) {
	body, err := c.fetch(releasePath(tag, "version.json"), 1<<20)
	if err != nil {
		return nil, err
	}
	var info ReleaseInfo
	if err := json.Unmarshal(body, &info); err != nil || info.Version == "" {
		return nil, errors.New("version.json 格式无效")
	}
	return &info, nil
}

// AssetName 当前平台的程序文件名。
func AssetName() string {
	name := "work-ledger-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// CompareVersion 比较 vX.Y.Z（非数字部分忽略）。
func CompareVersion(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func parseSums(body []byte, name string) string {
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// Upgrade 升级流程，失败自动回滚程序与数据库。
func (c *Ctl) Upgrade(tag, localFile, localSum string, noVerify, yes bool) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	var bin []byte
	var target string
	if localFile != "" {
		data, err := os.ReadFile(localFile)
		if err != nil {
			return err
		}
		if !noVerify {
			if localSum == "" {
				return errors.New("离线升级需要提供 --sha256，或使用 --no-verify 跳过校验")
			}
			if sha256Hex(data) != strings.ToLower(localSum) {
				return errors.New("sha256 校验失败，文件可能已损坏")
			}
		}
		bin, target = data, "本地文件 "+localFile
	} else {
		if tag == "" {
			tag = "latest"
		}
		info, err := c.release(tag)
		if err != nil {
			return err
		}
		if tag == "latest" && CompareVersion(info.Version, c.Version) <= 0 && !yes {
			c.printf("当前已是最新版本（%s）。如需重新安装请执行：wlctl upgrade %s -y\n", c.Version, info.Version)
			return nil
		}
		asset := AssetName()
		c.printf("下载 %s %s …\n", info.Version, asset)
		sums, err := c.fetch(releasePath(info.Version, "sha256sums.txt"), 1<<20)
		if err != nil {
			return err
		}
		want := parseSums(sums, asset)
		if want == "" {
			return fmt.Errorf("sha256sums.txt 中没有 %s", asset)
		}
		if bin, err = c.fetch(releasePath(info.Version, asset), 300<<20); err != nil {
			return err
		}
		if got := sha256Hex(bin); got != want {
			return fmt.Errorf("sha256 校验失败：期望 %s，实际 %s", want, got)
		}
		c.printf("sha256 校验通过。\n")
		target = info.Version
	}

	newPath := c.Bin + ".new"
	if err := os.WriteFile(newPath, bin, 0o755); err != nil {
		return fmt.Errorf("写入新程序失败：%w", err)
	}
	if out, err := exec.Command(newPath, "version").Output(); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("新程序无法运行（平台不匹配或文件损坏）：%v", err)
	} else {
		c.printf("新程序版本：%s", string(out))
	}

	dbBackup, err := c.BackupDB("pre-upgrade")
	if err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("升级前备份数据库失败：%w", err)
	}
	c.printf("升级前数据库备份：%s\n", dbBackup)
	_ = os.WriteFile(filepath.Join(c.Cfg.Paths.Backups, ".last-upgrade"), []byte(dbBackup), 0o600)

	if err := controlService("stop"); err != nil {
		c.printf("停止服务失败（继续）：%v\n", err)
	}
	prev := c.Bin + ".prev"
	_ = os.Remove(prev)
	if err := os.Rename(c.Bin, prev); err != nil {
		_ = controlService("start")
		return fmt.Errorf("备份旧程序失败：%w", err)
	}
	if err := os.Rename(newPath, c.Bin); err != nil {
		_ = os.Rename(prev, c.Bin)
		_ = controlService("start")
		return fmt.Errorf("替换程序失败：%w", err)
	}
	fixOwner(c.Bin, c.Cfg.Paths.Home)
	if err := controlService("start"); err == nil {
		if err = c.waitHealthy(30 * time.Second); err == nil {
			c.printf("升级完成：%s → %s。服务运行正常。\n", c.Version, target)
			return nil
		}
		c.printf("新版本启动后健康检查失败：%v\n", err)
	} else {
		c.printf("启动新版本失败：%v\n", err)
	}

	c.printf("正在自动回滚 …\n")
	_ = controlService("stop")
	_ = os.Rename(c.Bin, c.Bin+".failed")
	if err := os.Rename(prev, c.Bin); err != nil {
		return fmt.Errorf("回滚程序失败，请手动处理：%w", err)
	}
	if err := restoreBackupFile(c.Cfg, dbBackup); err != nil {
		c.printf("恢复数据库失败：%v（备份文件：%s）\n", err, dbBackup)
	}
	if err := controlService("start"); err != nil {
		return fmt.Errorf("回滚后启动失败：%w", err)
	}
	if err := c.waitHealthy(30 * time.Second); err != nil {
		return fmt.Errorf("回滚后健康检查仍失败：%w", err)
	}
	return errors.New("升级失败，已回滚到原版本并恢复数据库")
}

// Rollback 回到 .prev 版本。
func (c *Ctl) Rollback(withDB bool) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	prev := c.Bin + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return errors.New("没有可回滚的旧版本（bin/work-ledger.prev 不存在）")
	}
	_ = controlService("stop")
	tmp := c.Bin + ".rollback"
	if err := os.Rename(c.Bin, tmp); err != nil {
		return err
	}
	if err := os.Rename(prev, c.Bin); err != nil {
		_ = os.Rename(tmp, c.Bin)
		return err
	}
	_ = os.Rename(tmp, prev) // 当前版本变为 .prev，可再次切换回来
	if withDB {
		raw, err := os.ReadFile(filepath.Join(c.Cfg.Paths.Backups, ".last-upgrade"))
		if err != nil {
			c.printf("没有找到升级前数据库备份记录，跳过数据库恢复。\n")
		} else if err := restoreBackupFile(c.Cfg, strings.TrimSpace(string(raw))); err != nil {
			c.printf("恢复数据库失败：%v\n", err)
		} else {
			c.printf("已恢复升级前数据库：%s\n", strings.TrimSpace(string(raw)))
		}
	}
	if err := controlService("start"); err != nil {
		return err
	}
	if err := c.waitHealthy(30 * time.Second); err != nil {
		return err
	}
	c.printf("已回滚，服务运行正常。\n")
	return nil
}

// BackupDB 生成一致的数据库备份（SQLite 为数据库文件，MySQL / PostgreSQL 为 .json.gz）。
func (c *Ctl) BackupDB(prefix string) (string, error) {
	return snapshotDB(c.Cfg, prefix)
}

func restoreDBFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	_ = os.Remove(dst + "-wal")
	_ = os.Remove(dst + "-shm")
	return os.WriteFile(dst, data, 0o640)
}

// Uninstall 移除服务。
func (c *Ctl) Uninstall(purge, yes bool) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	if !yes {
		msg := "将停止并移除系统服务（保留数据目录）。"
		if purge {
			msg = "将停止并移除系统服务，并删除整个运行目录 " + c.Cfg.Paths.Home + "（包括数据库和备份，无法恢复）。"
		}
		c.printf("%s\n确认请输入 yes：", msg)
		var answer string
		_, _ = fmt.Scanln(&answer)
		if answer != "yes" {
			return errors.New("已取消")
		}
	}
	if err := removeService(); err != nil {
		c.printf("移除服务：%v\n", err)
	}
	removeShortcut()
	if purge {
		if err := os.RemoveAll(c.Cfg.Paths.Home); err != nil {
			return err
		}
		c.printf("已删除运行目录 %s。\n", c.Cfg.Paths.Home)
	} else {
		c.printf("服务已移除，数据保留在 %s。\n", c.Cfg.Paths.Home)
	}
	return nil
}
