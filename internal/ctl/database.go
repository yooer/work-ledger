package ctl

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"work-ledger/internal/config"
	"work-ledger/internal/db"
	"work-ledger/internal/service"
	"work-ledger/internal/timeutil"
)

// snapshotDB 生成一致的数据库备份：SQLite 用 VACUUM INTO 复制数据库文件；
// MySQL / PostgreSQL 生成与页面“下载压缩备份”同格式的 .json.gz（服务运行中也可执行）。
func snapshotDB(cfg *config.Config, prefix string) (string, error) {
	d, err := db.OpenConfig(cfg.DB)
	if err != nil {
		return "", err
	}
	defer d.Close()
	b := &service.Backup{Core: &service.Core{DB: d, Cfg: cfg, Log: quietLog()}}
	var p string
	if d.Dialect() == db.SQLite {
		if p, err = b.SQLiteSnapshot(prefix); err != nil {
			return "", err
		}
	} else {
		name, _, err := b.CreateSnapshot(prefix)
		if err != nil {
			return "", err
		}
		p = filepath.Join(cfg.Paths.Backups, name)
	}
	fixOwner(p, cfg.Paths.Home)
	return p, nil
}

// restoreBackupFile 恢复 snapshotDB 生成的备份：.sqlite 直接替换数据库文件，.json.gz 用整库恢复写回当前数据库。
func restoreBackupFile(cfg *config.Config, path string) error {
	if strings.HasSuffix(path, ".sqlite") {
		if !cfg.DB.IsSQLite() {
			return errors.New("备份是 SQLite 数据库文件，但当前配置的数据库不是 SQLite")
		}
		if err := restoreDBFile(path, cfg.DB.SQLitePath); err != nil {
			return err
		}
		fixOwner(cfg.DB.SQLitePath, cfg.Paths.Home)
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := db.OpenConfig(cfg.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	b := &service.Backup{Core: &service.Core{DB: d, Cfg: cfg, Log: quietLog()}}
	data, err := b.Decode(raw)
	if err != nil {
		return err
	}
	_, err = b.Restore(data)
	return err
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// dbStatus 状态命令中的数据库一行。
func (c *Ctl) dbStatus() string {
	if c.Cfg.DB.IsSQLite() {
		if info, err := os.Stat(c.Cfg.DB.SQLitePath); err == nil {
			return fmt.Sprintf("SQLite %s（%.1f MB）", c.Cfg.DB.SQLitePath, float64(info.Size())/(1<<20))
		}
		return "SQLite " + c.Cfg.DB.SQLitePath + "（不存在）"
	}
	d, err := db.OpenConfig(c.Cfg.DB)
	if err != nil {
		return c.Cfg.DB.Label() + "（连接失败：" + err.Error() + "）"
	}
	defer d.Close()
	b := &service.Backup{Core: &service.Core{DB: d, Cfg: c.Cfg, Log: quietLog()}}
	return fmt.Sprintf("%s（%s，%s）", c.Cfg.DB.Label(), d.ServerLabel(), b.DBSize())
}

// migrateDBUsage 迁移命令帮助。
const migrateDBUsage = `用法：sudo wlctl migrate-db --driver mysql|postgres|sqlite [参数]

把当前数据库的全部数据迁移到另一个数据库，并改写配置、重启服务。原数据库保持不动，可随时改回。
目标库必须是空库（MySQL 需 utf8mb4 字符集）；迁移前服务会暂停，完成后自动启动。

  --driver   目标数据库类型：mysql（MySQL 8.0+ / MariaDB 10.5+）、postgres（PostgreSQL 12+）或 sqlite
  --host     数据库地址            --port  端口（默认 3306 / 5432）
  --name     数据库名              --user  用户名
  --sslmode  可选：PostgreSQL 为 disable/prefer/require/verify-full；MySQL 为 true/false/skip-verify
  --path     目标为 sqlite 时的数据库文件（默认 data/app.sqlite；文件已有数据时拒绝）
  -y         不再询问确认

密码会提示输入（不回显）；也可以从标准输入传入：echo '密码' | sudo wlctl migrate-db …
`

// MigrateDB 迁移命令：检查目标库 → 暂停服务 → 导出并写入目标库 → 核对行数 → 改写配置 → 启动服务。
func (c *Ctl) MigrateDB(args []string) error {
	fs := flag.NewFlagSet("migrate-db", flag.ContinueOnError)
	fs.SetOutput(c.Out)
	fs.Usage = func() { c.printf("%s", migrateDBUsage) }
	driver := fs.String("driver", "", "")
	host := fs.String("host", "", "")
	port := fs.Int("port", 0, "")
	name := fs.String("name", "", "")
	user := fs.String("user", "", "")
	sslmode := fs.String("sslmode", "", "")
	path := fs.String("path", "", "")
	yes := fs.Bool("y", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *driver == "" {
		fs.Usage()
		return errors.New("请用 --driver 指定目标数据库类型")
	}
	if err := requireAdmin(); err != nil {
		return err
	}
	target := c.Cfg.DB
	d, err := config.NormalizeDriver(*driver)
	if err != nil {
		return err
	}
	target.Driver = d
	if d == "sqlite" {
		target.SQLitePath = filepath.Join(c.Cfg.Paths.Data, "app.sqlite")
		if *path != "" {
			if target.SQLitePath, err = filepath.Abs(*path); err != nil {
				return err
			}
		}
	} else {
		target.Host, target.Name, target.User, target.SSLMode = *host, *name, *user, *sslmode
		target.Port = *port
		if target.Port == 0 {
			target.Port = config.DefaultPort(d)
		}
		if err := target.Validate(); err != nil {
			return fmt.Errorf("%v（请用 --host、--name、--user 指定）", strings.ReplaceAll(strings.ReplaceAll(err.Error(), "DB_", "--"), "_", "-"))
		}
		if target.Password, err = c.readPassword(); err != nil {
			return err
		}
	}
	if sameDB(c.Cfg.DB, target) {
		return errors.New("目标数据库与当前数据库相同，无需迁移")
	}

	c.printf("当前数据库：%s\n目标数据库：%s\n", c.Cfg.DB.Label(), target.Label())
	dst, err := db.OpenConfig(target)
	if err != nil {
		return fmt.Errorf("无法使用目标数据库：%w", err)
	}
	defer dst.Close()
	if empty, err := service.TargetEmpty(dst); err != nil {
		return err
	} else if !empty {
		return errors.New("目标库已有数据（users 表不为空），为避免覆盖已拒绝迁移；请换一个空库")
	}
	c.printf("目标库连接正常（%s），且为空库。\n", dst.ServerLabel())
	if !*yes && !c.confirm("迁移期间服务会暂停（通常不超过 1 分钟），确定开始吗？") {
		return errors.New("已取消")
	}

	st := serviceState()
	wasRunning := strings.HasPrefix(st, "active") || strings.HasPrefix(st, "RUNNING")
	if wasRunning {
		c.printf("正在暂停服务 …\n")
		if err := controlService("stop"); err != nil {
			return fmt.Errorf("暂停服务失败：%w", err)
		}
	}
	restart := func() {
		if wasRunning {
			if err := controlService("start"); err != nil {
				c.printf("启动服务失败：%v\n", err)
			}
		}
	}

	src, err := db.OpenConfig(c.Cfg.DB)
	if err != nil {
		restart()
		return fmt.Errorf("打开当前数据库失败：%w", err)
	}
	defer src.Close()
	core := &service.Core{DB: src, Cfg: c.Cfg, Log: quietLog()}
	// 迁移前在本地留一份完整备份（与页面备份同格式）
	if name, _, err := (&service.Backup{Core: core}).CreateSnapshot("pre-migrate"); err != nil {
		restart()
		return fmt.Errorf("迁移前备份失败：%w", err)
	} else {
		p := filepath.Join(c.Cfg.Paths.Backups, name)
		fixOwner(p, c.Cfg.Paths.Home)
		c.printf("迁移前备份：%s\n", p)
	}
	c.printf("正在迁移数据 …\n")
	res, err := service.MigrateDatabase(core, src, dst)
	if err != nil {
		restart()
		return fmt.Errorf("迁移失败，配置未修改，服务仍使用原数据库：%w", err)
	}
	keys := make([]string, 0, len(res.Counts))
	for k := range res.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.printf("  %-26s %d\n", k, res.Counts[k])
	}
	// 先关闭两个库再启动服务：本命令以 root 运行，SQLite 的 -wal/-shm 文件在关闭前属于 root，
	// 服务账号会因无法写入而报 “readonly database”。关闭后再统一修正属主。
	_ = dst.Close()
	_ = src.Close()
	for _, cfgDB := range []config.DB{c.Cfg.DB, target} {
		if cfgDB.IsSQLite() {
			fixSQLiteOwner(cfgDB.SQLitePath, c.Cfg.Paths.Home)
		}
	}

	values := map[string]string{"DB_DRIVER": d}
	if d == "sqlite" {
		values["DB_SQLITE_PATH"] = target.SQLitePath
	} else {
		values["DB_HOST"], values["DB_PORT"], values["DB_NAME"] = target.Host, strconv.Itoa(target.Port), target.Name
		values["DB_USER"], values["DB_PASSWORD"], values["DB_SSLMODE"] = target.User, target.Password, target.SSLMode
	}
	bak, err := setEnvValues(c.Cfg.Paths.EnvFile, values)
	if err != nil {
		restart()
		return fmt.Errorf("数据已写入目标库，但改写配置失败（服务仍使用原数据库）：%w", err)
	}
	fixOwner(c.Cfg.Paths.EnvFile, c.Cfg.Paths.Home)
	c.printf("配置已更新：%s（原配置备份为 %s）\n", c.Cfg.Paths.EnvFile, bak)
	if wasRunning {
		c.printf("正在启动服务 …\n")
		if err := controlService("start"); err != nil {
			return fmt.Errorf("启动服务失败：%w；如需改回原数据库，把 %s 复制回 %s 后重启", err, bak, c.Cfg.Paths.EnvFile)
		}
		if err := c.waitHealthy(30 * time.Second); err != nil {
			return fmt.Errorf("%w；如需改回原数据库，把 %s 复制回 %s 后重启", err, bak, c.Cfg.Paths.EnvFile)
		}
	}
	c.printf("迁移完成，服务已使用 %s。原数据库未做任何修改。\n", target.Label())
	return nil
}

// fixSQLiteOwner 修正 SQLite 数据库文件及其 -wal / -shm 附属文件的属主。
func fixSQLiteOwner(path, home string) {
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(f); err == nil {
			fixOwner(f, home)
		}
	}
}

func sameDB(a, b config.DB) bool {
	if a.Driver != b.Driver {
		return false
	}
	if a.Driver == "sqlite" {
		pa, _ := filepath.Abs(a.SQLitePath)
		pb, _ := filepath.Abs(b.SQLitePath)
		return pa == pb
	}
	return strings.EqualFold(a.Host, b.Host) && a.Port == b.Port && a.Name == b.Name
}

func (c *Ctl) readPassword() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(c.Out, "目标数据库密码：")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(c.Out)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *Ctl) confirm(question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Fprintf(c.Out, "%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是":
		return true
	}
	return false
}

var envKeyLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// setEnvValues 改写 .env 中的指定键（不存在则追加），其余内容原样保留；改写前备份原文件，返回备份路径。
func setEnvValues(file string, values map[string]string) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	bak := file + ".bak-" + timeutil.Now().Format("20060102-150405")
	if err == nil {
		if err := os.WriteFile(bak, raw, 0o600); err != nil {
			return "", err
		}
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	done := map[string]bool{}
	for i, l := range lines {
		if m := envKeyLine.FindStringSubmatch(l); m != nil {
			if v, ok := values[m[1]]; ok {
				q, err := quoteEnv(v)
				if err != nil {
					return "", fmt.Errorf("%s：%w", m[1], err)
				}
				lines[i] = m[1] + "=" + q
				done[m[1]] = true
			}
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !done[k] {
			q, err := quoteEnv(values[k])
			if err != nil {
				return "", fmt.Errorf("%s：%w", k, err)
			}
			lines = append(lines, k+"="+q)
		}
	}
	return bak, os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o640)
}

// quoteEnv .env 的值：含空白、# 或引号时加引号（读取时只去掉首尾成对引号，不处理转义）。
func quoteEnv(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n") {
		return "", errors.New("值不能包含换行")
	}
	if v == "" || !strings.ContainsAny(v, " \t#'\"") {
		return v, nil
	}
	if !strings.Contains(v, `"`) {
		return `"` + v + `"`, nil
	}
	if !strings.Contains(v, "'") {
		return "'" + v + "'", nil
	}
	return "", errors.New("值同时包含单引号和双引号，无法写入配置文件，请更换密码")
}
