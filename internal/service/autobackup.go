package service

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"

	"work-ledger/internal/apperr"
	"work-ledger/internal/timeutil"
)

// 自动备份设置键名（app_settings）。FTP 密码不会写进备份文件。
const (
	SettingAutoBackup          = "auto_backup_enabled"
	SettingAutoBackupTime      = "auto_backup_time" // HH:MM
	SettingAutoBackupKeepLocal = "auto_backup_keep_local"
	SettingAutoBackupKeepFTP   = "auto_backup_keep_remote"
	SettingFTPHost             = "backup_ftp_host"
	SettingFTPPort             = "backup_ftp_port"
	SettingFTPUser             = "backup_ftp_user"
	SettingFTPPassword         = "backup_ftp_password"
	SettingFTPDir              = "backup_ftp_dir"
	SettingFTPTLS              = "backup_ftp_tls"
	SettingAutoBackupLast      = "auto_backup_last_result"
	SettingAutoBackupLastDate  = "auto_backup_last_date" // 最近一次定时备份的日期（每天一次）
)

func init() {
	for k, v := range map[string]string{
		SettingAutoBackup: "0", SettingAutoBackupTime: "02:00", SettingAutoBackupKeepLocal: "7", SettingAutoBackupKeepFTP: "30",
		SettingFTPPort: "21", SettingFTPDir: "/", SettingFTPTLS: "0",
	} {
		DefaultSettings[k] = v
	}
}

// autoBackupName 自动备份文件名：auto-YYYYMMDD-HHMMSS.json.gz。清理时只匹配这种文件。
var autoBackupName = regexp.MustCompile(`^auto-\d{8}-\d{6}\.json\.gz$`)

// AutoBackup 每日定时备份（与页面“下载压缩备份”同格式）并上传到 FTP。
type AutoBackup struct {
	*Core
	Settings *Settings
	Backup   *Backup
	mu       sync.Mutex // 定时任务与“立即备份”不并发执行
	tries    map[string]attempt
}

// FTPConfig FTP 连接参数。
type FTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Dir      string
	TLS      bool
}

func (f FTPConfig) Enabled() bool { return strings.TrimSpace(f.Host) != "" }

// AutoBackupConfig 自动备份设置（供页面回显；密码只回显“是否已设置”）。
type AutoBackupConfig struct {
	Enabled     bool
	Time        string
	KeepLocal   int
	KeepFTP     int
	FTP         FTPConfig
	HasPassword bool
	NextRun     string
}

// AutoBackupStatus 最近一次执行结果。
type AutoBackupStatus struct {
	Time     string `json:"time"`
	Trigger  string `json:"trigger"` // schedule / manual
	File     string `json:"file"`
	Size     string `json:"size"`
	OK       bool   `json:"ok"`       // 本地备份是否成功
	Uploaded bool   `json:"uploaded"` // 是否已上传 FTP
	FTPUsed  bool   `json:"ftp_used"` // 是否配置了 FTP
	Message  string `json:"message"`
}

func (s *AutoBackup) Config() AutoBackupConfig {
	st := s.Settings
	c := AutoBackupConfig{
		Enabled:   st.Bool(SettingAutoBackup),
		Time:      st.Get(SettingAutoBackupTime, "02:00"),
		KeepLocal: st.GetInt(SettingAutoBackupKeepLocal, 7),
		KeepFTP:   st.GetInt(SettingAutoBackupKeepFTP, 30),
		FTP: FTPConfig{
			Host: st.Get(SettingFTPHost, ""), Port: st.GetInt(SettingFTPPort, 21), User: st.Get(SettingFTPUser, ""),
			Password: st.Get(SettingFTPPassword, ""), Dir: st.Get(SettingFTPDir, "/"), TLS: st.Bool(SettingFTPTLS),
		},
	}
	c.HasPassword = c.FTP.Password != ""
	if c.Enabled {
		due := s.dueToday(timeutil.Now())
		if s.Settings.Get(SettingAutoBackupLastDate, "") == due.Format(timeutil.Date) || timeutil.Now().After(due.Add(time.Minute)) {
			due = due.AddDate(0, 0, 1)
		}
		c.NextRun = due.Format("2006-01-02 15:04")
	}
	return c
}

// SaveConfig 保存设置。cfg.FTP.Password 为空表示不修改原密码。
func (s *AutoBackup) SaveConfig(cfg AutoBackupConfig) error {
	m := map[string]string{
		SettingAutoBackup: boolStr(cfg.Enabled), SettingAutoBackupTime: cfg.Time,
		SettingAutoBackupKeepLocal: strconv.Itoa(cfg.KeepLocal), SettingAutoBackupKeepFTP: strconv.Itoa(cfg.KeepFTP),
		SettingFTPHost: strings.TrimSpace(cfg.FTP.Host), SettingFTPPort: strconv.Itoa(cfg.FTP.Port), SettingFTPUser: cfg.FTP.User,
		SettingFTPDir: normalizeFTPDir(cfg.FTP.Dir), SettingFTPTLS: boolStr(cfg.FTP.TLS),
	}
	if cfg.FTP.Password != "" {
		m[SettingFTPPassword] = cfg.FTP.Password
	}
	if cfg.FTP.Host == "" {
		m[SettingFTPPassword] = "" // 清空 FTP 地址即停用上传，同时清除密码
	}
	s.mu.Lock()
	s.tries = nil
	s.mu.Unlock()
	return s.Settings.SetMany(m)
}

// WithStoredPassword 表单未填密码时使用已保存的密码（用于“测试连接”）。
func (s *AutoBackup) WithStoredPassword(f FTPConfig) FTPConfig {
	if f.Password == "" {
		f.Password = s.Settings.Get(SettingFTPPassword, "")
	}
	return f
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func normalizeFTPDir(d string) string {
	d = strings.TrimSpace(strings.ReplaceAll(d, "\\", "/"))
	if d == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimLeft(d, "/"))
}

func (s *AutoBackup) dueToday(n time.Time) time.Time {
	h, m := parseHHMM(s.Settings.Get(SettingAutoBackupTime, "02:00"))
	return time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, timeutil.Location())
}

// Tick 由内置定时器每分钟调用：到点且当天还没备份就执行一次；失败 30 分钟后重试，当天最多 3 次。
func (s *AutoBackup) Tick(n time.Time) {
	if !s.Settings.Bool(SettingAutoBackup) || n.Before(s.dueToday(n)) {
		return
	}
	today := n.Format(timeutil.Date)
	if s.Settings.Get(SettingAutoBackupLastDate, "") == today {
		return
	}
	s.mu.Lock()
	if s.tries == nil {
		s.tries = map[string]attempt{}
	}
	a := s.tries[today]
	if a.count >= 3 || (a.count > 0 && time.Since(a.last) < 30*time.Minute) {
		s.mu.Unlock()
		return
	}
	s.tries[today] = attempt{count: a.count + 1, last: time.Now()}
	s.mu.Unlock()

	st := s.Run("schedule")
	if st.OK {
		_ = s.Settings.Set(SettingAutoBackupLastDate, today) // 本地备份成功即算当天完成；上传失败见状态信息
	}
}

// Run 立即执行一次：生成本地备份 → 清理本地旧文件 → 上传 FTP → 清理 FTP 旧文件。结果写入状态。
func (s *AutoBackup) Run(trigger string) AutoBackupStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.Config()
	st := AutoBackupStatus{Time: timeutil.NowString(), Trigger: trigger, FTPUsed: cfg.FTP.Enabled()}
	defer func() {
		raw, _ := json.Marshal(st)
		_ = s.Settings.Set(SettingAutoBackupLast, string(raw))
	}()

	name, full, size, err := s.writeLocal()
	if err != nil {
		st.Message = "本地备份失败：" + err.Error()
		s.Log.Error("自动备份失败", "err", err)
		return st
	}
	st.OK, st.File, st.Size = true, name, HumanSize(int64(size))
	msgs := []string{"本地备份已生成"}
	if removed, err := pruneLocal(s.Cfg.Paths.Backups, cfg.KeepLocal); err != nil {
		msgs = append(msgs, "清理本地旧备份失败："+err.Error())
	} else if removed > 0 {
		msgs = append(msgs, fmt.Sprintf("已清理本地旧备份 %d 个", removed))
	}
	if cfg.FTP.Enabled() {
		removed, err := uploadFTP(cfg.FTP, full, name, cfg.KeepFTP)
		if err != nil {
			msgs = append(msgs, "上传 FTP 失败："+err.Error())
			s.Log.Error("备份上传 FTP 失败", "file", name, "err", err)
		} else {
			st.Uploaded = true
			msgs = append(msgs, "已上传 FTP")
			if removed > 0 {
				msgs = append(msgs, fmt.Sprintf("已清理 FTP 旧备份 %d 个", removed))
			}
		}
	}
	st.Message = strings.Join(msgs, "；") + "。"
	s.Log.Info("自动备份完成", "file", name, "trigger", trigger, "uploaded", st.Uploaded)
	return st
}

func (s *AutoBackup) writeLocal() (name, full string, size int, err error) {
	data, err := s.Backup.ExportJSON()
	if err != nil {
		return
	}
	body, err := Encode(data, true)
	if err != nil {
		return
	}
	if err = os.MkdirAll(s.Cfg.Paths.Backups, 0o750); err != nil {
		return
	}
	n := timeutil.Now()
	for i := 0; i < 5; i++ { // 同一秒重复执行时顺延一秒，避免覆盖
		name = "auto-" + n.Add(time.Duration(i)*time.Second).Format("20060102-150405") + ".json.gz"
		full = filepath.Join(s.Cfg.Paths.Backups, name)
		if _, e := os.Stat(full); errors.Is(e, os.ErrNotExist) {
			break
		}
	}
	if err = os.WriteFile(full, body, 0o600); err != nil {
		return
	}
	return name, full, len(body), nil
}

// pruneLocal 只保留最新的 keep 个 auto-* 备份；keep<=0 不清理。
func pruneLocal(dir string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && autoBackupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	removed := 0
	for _, n := range oldest(names, keep) {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// oldest 文件名带时间戳，按名称排序即按时间排序；返回超出 keep 的最旧文件。
func oldest(names []string, keep int) []string {
	sort.Strings(names)
	if len(names) <= keep {
		return nil
	}
	return names[:len(names)-keep]
}

const ftpTimeout = 30 * time.Second

func dialFTP(f FTPConfig) (*ftp.ServerConn, error) {
	host := strings.TrimSpace(f.Host)
	port := f.Port
	if port <= 0 {
		port = 21
	}
	opts := []ftp.DialOption{ftp.DialWithTimeout(ftpTimeout)}
	if f.TLS {
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}))
	}
	c, err := ftp.Dial(net.JoinHostPort(host, strconv.Itoa(port)), opts...)
	if err != nil {
		return nil, fmt.Errorf("连接失败：%w", err)
	}
	user := f.User
	if user == "" {
		user = "anonymous"
	}
	if err := c.Login(user, f.Password); err != nil {
		_ = c.Quit()
		return nil, fmt.Errorf("登录失败：%w", err)
	}
	if err := enterDir(c, normalizeFTPDir(f.Dir)); err != nil {
		_ = c.Quit()
		return nil, err
	}
	return c, nil
}

// enterDir 进入远程目录，不存在则逐级创建。
func enterDir(c *ftp.ServerConn, dir string) error {
	if c.ChangeDir(dir) == nil {
		return nil
	}
	if err := c.ChangeDir("/"); err != nil {
		return fmt.Errorf("无法进入根目录：%w", err)
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" {
			continue
		}
		if c.ChangeDir(part) != nil {
			if err := c.MakeDir(part); err != nil {
				return fmt.Errorf("无法创建远程目录 %s：%w", dir, err)
			}
			if err := c.ChangeDir(part); err != nil {
				return fmt.Errorf("无法进入远程目录 %s：%w", dir, err)
			}
		}
	}
	return nil
}

// TestFTP 测试连接、登录、进入（必要时创建）远程目录并列出文件。
func (s *AutoBackup) TestFTP(f FTPConfig) (string, error) {
	if !f.Enabled() {
		return "", apperr.Validation(map[string]string{"ftp_host": "请填写 FTP 服务器地址。"})
	}
	c, err := dialFTP(f)
	if err != nil {
		return "", apperr.New("FTP 测试失败：" + err.Error())
	}
	defer c.Quit()
	names, err := c.NameList(".")
	if err != nil {
		return "", apperr.New("FTP 已登录，但无法列出远程目录：" + err.Error())
	}
	n := 0
	for _, name := range names {
		if autoBackupName.MatchString(path.Base(name)) {
			n++
		}
	}
	return fmt.Sprintf("FTP 连接成功，远程目录 %s 中现有自动备份 %d 个。", normalizeFTPDir(f.Dir), n), nil
}

// uploadFTP 先上传为 .part 再改名（避免残缺文件被当作备份），然后按 keep 清理远程旧备份。
func uploadFTP(f FTPConfig, full, name string, keep int) (int, error) {
	body, err := os.ReadFile(full)
	if err != nil {
		return 0, err
	}
	c, err := dialFTP(f)
	if err != nil {
		return 0, err
	}
	defer c.Quit()
	tmp := name + ".part"
	if err := c.Stor(tmp, bytes.NewReader(body)); err != nil {
		return 0, fmt.Errorf("上传失败：%w", err)
	}
	if err := c.Rename(tmp, name); err != nil {
		_ = c.Delete(tmp)
		return 0, fmt.Errorf("上传后改名失败：%w", err)
	}
	if keep <= 0 {
		return 0, nil
	}
	list, err := c.NameList(".")
	if err != nil {
		return 0, nil // 已上传成功，列目录失败只是本次不清理
	}
	var names []string
	for _, n := range list {
		if b := path.Base(n); autoBackupName.MatchString(b) {
			names = append(names, b)
		}
	}
	removed := 0
	for _, n := range oldest(names, keep) {
		if c.Delete(n) == nil {
			removed++
		}
	}
	return removed, nil
}

// LastStatus 最近一次执行结果。
func (s *AutoBackup) LastStatus() *AutoBackupStatus {
	raw := s.Settings.Get(SettingAutoBackupLast, "")
	if raw == "" {
		return nil
	}
	var st AutoBackupStatus
	if json.Unmarshal([]byte(raw), &st) != nil {
		return nil
	}
	return &st
}
