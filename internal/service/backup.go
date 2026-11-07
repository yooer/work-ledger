package service

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/password"
	"work-ledger/internal/session"
	"work-ledger/internal/timeutil"
)

// Backup 整库 JSON 快照与恢复，格式与 PHP 版完全一致（format=work-ledger-backup, version=1）。
type Backup struct{ *Core }

const (
	BackupFormat  = "work-ledger-backup"
	BackupVersion = 1
)

// backupTables 字段顺序同时用于导出、校验和恢复（与 PHP 版 TABLE_COLUMNS 一致）。
// optional 是 Go 版新增的字段：导出时追加在末尾，恢复时可缺失（兼容 PHP 版备份）。
var backupTables = []struct {
	name     string
	cols     []string
	optional []string
}{
	{"departments", []string{"id", "name", "parent_id", "is_active", "sort_order", "created_at", "updated_at"}, nil},
	{"positions", []string{"id", "name", "role", "is_active", "sort_order", "created_at", "updated_at"}, nil}, // Go 版新增（整表可选）
	{"users", []string{"id", "username", "password_hash", "name", "employee_no", "role", "department_id", "phone", "email", "is_active",
		"totp_secret", "totp_enabled", "totp_recovery_codes", "totp_last_used_step", "last_login_at", "created_at", "updated_at"},
		[]string{"supervisor_id", "position_id"}},
	{"work_categories", []string{"id", "name", "is_customer_project", "is_active", "sort_order", "created_at", "updated_at"}, []string{"department_id"}},
	{"project_categories", []string{"id", "work_category_id", "name", "is_active", "sort_order", "created_at", "updated_at"}, nil},
	{"work_records", []string{"id", "work_category_id", "project_category_id", "content", "work_at", "difficulty", "work_count",
		"department_id", "created_by", "created_at", "updated_by", "updated_at", "import_hash"}, nil},
	{"work_record_participants", []string{"work_record_id", "user_id"}, nil},
	{"weekly_reports", []string{"id", "week_start", "week_end", "department_id", "employee_id", "scope_label", "content", "source", "model", "created_by", "created_at"}, []string{"team_leader_id"}},
	{"annual_reports", []string{"id", "report_year", "department_id", "employee_id", "scope_label", "content", "source", "model", "created_by", "created_at"}, []string{"team_leader_id"}},
	{"period_reports", []string{"id", "start_date", "end_date", "period_label", "department_id", "employee_id", "scope_label", "content", "source", "model", "created_by", "created_at"}, []string{"team_leader_id"}},
	{"app_settings", []string{"name", "value", "updated_at"}, nil},
}

// secretSettings 不写入备份文件的设置项；恢复备份时保留当前值。
var secretSettings = []string{SettingFTPPassword}

// optionalTables Go 版新增的整张表：PHP 版备份中没有，恢复时允许缺失。
var optionalTables = map[string]bool{"positions": true}

var deleteOrder = []string{"work_record_participants", "weekly_reports", "annual_reports", "period_reports", "work_records",
	"project_categories", "work_categories", "users", "positions", "app_settings"}
var insertOrder = []string{"positions", "users", "work_categories", "project_categories", "work_records", "work_record_participants",
	"weekly_reports", "annual_reports", "period_reports", "app_settings"}

// tableCols 返回导出/写入使用的全部字段（必需字段 + 可选字段）。
func tableCols(name string) []string {
	for _, t := range backupTables {
		if t.name == name {
			return append(append([]string{}, t.cols...), t.optional...)
		}
	}
	return nil
}

func orderBy(table string) string {
	switch table {
	case "departments":
		return " ORDER BY CASE WHEN parent_id IS NULL THEN 0 ELSE 1 END, id"
	case "app_settings":
		return " ORDER BY name"
	case "work_record_participants":
		return " ORDER BY work_record_id, user_id"
	}
	return " ORDER BY id"
}

func writeJSONValue(buf *bytes.Buffer, v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	buf.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

// ExportJSON 在一个只读事务中读取全部业务表，生成与 PHP 版字段顺序一致的 JSON。
func (s *Backup) ExportJSON() ([]byte, error) {
	var buf bytes.Buffer
	err := s.DB.Tx(func(tx db.Q) error {
		counts := map[string]int{}
		var tables bytes.Buffer
		tables.WriteString("{")
		for ti, t := range backupTables {
			cols := tableCols(t.name)
			where := ""
			if t.name == "app_settings" {
				where = " WHERE name NOT IN ('" + strings.Join(secretSettings, "', '") + "')" // FTP 密码等不进备份
			}
			rows, err := db.All(tx, "SELECT "+strings.Join(cols, ", ")+" FROM "+t.name+where+orderBy(t.name))
			if err != nil {
				return err
			}
			counts[t.name] = len(rows)
			if ti > 0 {
				tables.WriteString(",")
			}
			writeJSONValue(&tables, t.name)
			tables.WriteString(":[")
			for ri, r := range rows {
				if ri > 0 {
					tables.WriteString(",")
				}
				tables.WriteString("{")
				for ci, c := range cols {
					if ci > 0 {
						tables.WriteString(",")
					}
					writeJSONValue(&tables, c)
					tables.WriteString(":")
					writeJSONValue(&tables, r[c])
				}
				tables.WriteString("}")
			}
			tables.WriteString("]")
		}
		tables.WriteString("}")

		buf.WriteString(`{"format":"` + BackupFormat + `","version":1,"created_at":`)
		writeJSONValue(&buf, timeutil.Now().Format(time.RFC3339))
		buf.WriteString(`,"app_name":`)
		writeJSONValue(&buf, s.Cfg.App.Name)
		buf.WriteString(`,"source_db_driver":"sqlite","counts":{`)
		for ti, t := range backupTables {
			if ti > 0 {
				buf.WriteString(",")
			}
			fmt.Fprintf(&buf, `"%s":%d`, t.name, counts[t.name])
		}
		buf.WriteString(`},"tables":`)
		buf.Write(tables.Bytes())
		buf.WriteString("}")
		return nil
	})
	return buf.Bytes(), err
}

// Encode 可选 gzip 压缩。
func Encode(data []byte, gz bool) ([]byte, error) {
	if !gz {
		return data, nil
	}
	var out bytes.Buffer
	w, _ := gzip.NewWriterLevel(&out, 6)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Download 生成可下载的备份。
func (s *Backup) Download(gz bool) (string, []byte, error) {
	data, err := s.ExportJSON()
	if err != nil {
		return "", nil, err
	}
	body, err := Encode(data, gz)
	if err != nil {
		return "", nil, err
	}
	name := "work-ledger-backup-" + timeutil.Now().Format("20060102-150405") + ".json"
	if gz {
		name += ".gz"
	}
	return name, body, nil
}

var backupNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.(?:json|json\.gz|sqlite)$`)

// CreateSnapshot 在 backups/ 下写入压缩快照，返回文件名与大小。
func (s *Backup) CreateSnapshot(prefix string) (string, int, error) {
	data, err := s.ExportJSON()
	if err != nil {
		return "", 0, err
	}
	body, err := Encode(data, true)
	if err != nil {
		return "", 0, err
	}
	prefix = strings.Trim(regexp.MustCompile(`[^a-z0-9_-]`).ReplaceAllString(strings.ToLower(prefix), ""), "-_")
	if prefix == "" {
		prefix = "backup"
	}
	rnd := make([]byte, 3)
	_, _ = rand.Read(rnd)
	name := prefix + "-" + timeutil.Now().Format("20060102-150405") + "-" + hex.EncodeToString(rnd) + ".json.gz"
	if err := os.MkdirAll(s.Cfg.Paths.Backups, 0o750); err != nil {
		return "", 0, fmt.Errorf("无法创建备份目录：%w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Cfg.Paths.Backups, name), body, 0o600); err != nil {
		return "", 0, fmt.Errorf("无法写入本地备份文件：%w", err)
	}
	return name, len(body), nil
}

// LocalFile 本地快照信息。
type LocalFile struct {
	Name       string
	Size       int64
	SizeText   string
	Time       string
	PreRestore bool
	Kind       string
}

// LocalFiles 列出 backups/ 下的快照（最新在前）。
func (s *Backup) LocalFiles(limit int) ([]LocalFile, error) {
	entries, err := os.ReadDir(s.Cfg.Paths.Backups)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []LocalFile
	for _, e := range entries {
		if e.IsDir() || !backupNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "JSON 快照"
		switch {
		case strings.HasSuffix(e.Name(), ".sqlite"):
			kind = "数据库文件（升级前）"
		case autoBackupName.MatchString(e.Name()):
			kind = "每日自动备份"
		}
		out = append(out, LocalFile{Name: e.Name(), Size: info.Size(), SizeText: HumanSize(info.Size()),
			Time: info.ModTime().In(timeutil.Location()).Format("2006-01-02 15:04:05"), PreRestore: strings.HasPrefix(e.Name(), "pre-restore-"), Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// LocalPath 校验文件名并返回绝对路径（防目录穿越）。
func (s *Backup) LocalPath(name string) (string, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if !backupNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", apperr.New("备份文件名无效。")
	}
	p := filepath.Join(s.Cfg.Paths.Backups, name)
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", apperr.NotFound("备份文件不存在。")
	}
	return p, nil
}

// Counts 当前各表行数。
func (s *Backup) Counts() (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range backupTables {
		n, err := db.Int(s.DB, "SELECT COUNT(*) FROM "+t.name)
		if err != nil {
			return nil, err
		}
		out[t.name] = n
	}
	return out, nil
}

// TableNames 备份包含的表（展示用）。
func TableNames() []string {
	out := make([]string, 0, len(backupTables))
	for _, t := range backupTables {
		out = append(out, t.name)
	}
	return out
}

// DBSize 数据库文件（含 WAL）大小。
func (s *Backup) DBSize() string {
	var size int64
	for _, f := range []string{s.DB.Path, s.DB.Path + "-wal"} {
		if info, err := os.Stat(f); err == nil {
			size += info.Size()
		}
	}
	return HumanSize(size)
}

func HumanSize(b int64) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%d B", b)
	case b < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	case b < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
}

// backupData 解码后的备份。
type backupData struct {
	Tables map[string][]map[string]any
}

// Decode 解压（可选）、限制大小并校验结构。
func (s *Backup) Decode(raw []byte) (*backupData, error) {
	maxJSON := int64(s.Cfg.Limits.BackupMaxJSONMB) << 20
	if len(raw) >= 2 && raw[0] == 0x1F && raw[1] == 0x8B {
		r, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, apperr.New("压缩备份文件损坏或格式不正确。")
		}
		dec, err := io.ReadAll(io.LimitReader(r, maxJSON+1))
		if err != nil {
			return nil, apperr.New("压缩备份文件损坏或格式不正确。")
		}
		raw = dec
	}
	if int64(len(raw)) > maxJSON {
		return nil, apperr.New("备份解压后超过允许的最大大小。")
	}
	var top struct {
		Format  any                        `json:"format"`
		Version any                        `json:"version"`
		Tables  map[string]json.RawMessage `json:"tables"`
		Counts  map[string]any             `json:"counts"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, apperr.New("备份 JSON 格式无效：" + err.Error())
	}
	if f, _ := top.Format.(string); f != BackupFormat {
		return nil, apperr.New("不是工作台账系统生成的备份文件。")
	}
	if fmt.Sprint(top.Version) != "1" {
		return nil, apperr.New(fmt.Sprintf("备份版本不兼容，当前系统只支持版本 %d。", BackupVersion))
	}
	if top.Tables == nil {
		return nil, apperr.New("备份缺少数据表内容。")
	}
	if top.Counts == nil {
		return nil, apperr.New("备份缺少行数校验信息。")
	}
	data := &backupData{Tables: map[string][]map[string]any{}}
	for _, t := range backupTables {
		rawTable, ok := top.Tables[t.name]
		if !ok {
			if optionalTables[t.name] {
				data.Tables[t.name] = nil
				continue
			}
			return nil, apperr.New("备份缺少数据表：" + t.name)
		}
		var rows []map[string]any
		d := json.NewDecoder(bytes.NewReader(rawTable))
		d.UseNumber()
		if err := d.Decode(&rows); err != nil {
			return nil, apperr.New("备份数据格式错误：" + t.name)
		}
		if c, ok := top.Counts[t.name]; !ok || fmt.Sprint(c) != fmt.Sprint(len(rows)) {
			return nil, apperr.New("备份表行数校验失败：" + t.name)
		}
		for i, r := range rows {
			if r == nil {
				return nil, apperr.New(fmt.Sprintf("备份数据格式错误：%s 第 %d 行", t.name, i+1))
			}
			for _, c := range t.cols {
				if _, ok := r[c]; !ok {
					return nil, apperr.New("备份字段缺失：" + t.name + "." + c)
				}
			}
		}
		data.Tables[t.name] = rows
	}
	admins := 0
	for _, u := range data.Tables["users"] {
		if fmt.Sprint(u["role"]) == "admin" && fmt.Sprint(u["is_active"]) == "1" &&
			strings.TrimSpace(strVal(u["username"])) != "" && strVal(u["password_hash"]) != "" {
			admins++
		}
	}
	if admins == 0 {
		return nil, apperr.New("备份中没有可用的启用状态管理员，拒绝恢复。")
	}
	return data, nil
}

func strVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// sqlValue 把 JSON 值转为 SQLite 参数。
func sqlValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			if f == math.Trunc(f) && math.Abs(f) < 1e15 {
				return int64(f)
			}
			return f
		}
		return t.String()
	case bool:
		if t {
			return 1
		}
		return 0
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return v
}

// Restore 在一个事务中清空业务表并写入备份；任何错误都会回滚。成功后清空全部会话。
func (s *Backup) Restore(data *backupData) (map[string]int, error) {
	restored := map[string]int{}
	err := s.DB.Tx(func(tx db.Q) error {
		kept, err := db.All(tx, "SELECT name, value FROM app_settings WHERE name IN ("+db.Placeholders(len(secretSettings))+")", anySlice(secretSettings)...)
		if err != nil {
			return err
		}
		for _, t := range deleteOrder {
			if _, err := db.Exec(tx, "DELETE FROM "+t); err != nil {
				return err
			}
		}
		// 多级部门：先解除上下级关系再整体删除，避免外键 RESTRICT
		if _, err := db.Exec(tx, "UPDATE departments SET parent_id = NULL"); err != nil {
			return err
		}
		if _, err := db.Exec(tx, "DELETE FROM departments"); err != nil {
			return err
		}
		if err := insertDepartmentsByLevel(tx, data.Tables["departments"]); err != nil {
			return err
		}
		for _, t := range insertOrder {
			if err := insertRows(tx, t, data.Tables[t]); err != nil {
				return err
			}
		}
		for _, k := range kept { // 备份中不含 FTP 密码等：沿用恢复前的值
			if _, err := db.Exec(tx, "INSERT OR REPLACE INTO app_settings (name, value, updated_at) VALUES (?, ?, ?)", k.Str("name"), k.Str("value"), now()); err != nil {
				return err
			}
		}
		// PHP 版备份没有职务：按原角色对应预置职务
		if err := ensurePositions(tx); err != nil {
			return err
		}
		n, err := db.Int(tx, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = 1 AND password_hash <> ''")
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("恢复后没有可用管理员，已回滚")
		}
		return session.DestroyAll(tx)
	})
	if err != nil {
		return nil, err
	}
	for _, t := range backupTables {
		restored[t.name] = len(data.Tables[t.name])
	}
	return restored, nil
}

func anySlice(list []string) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = v
	}
	return out
}

// insertDepartmentsByLevel 按层级写入部门（上级先于下级），支持任意层级。
// 上级不存在或成环的部门最后按顶级写入，保证恢复不中断。
func insertDepartmentsByLevel(tx db.Q, rows []map[string]any) error {
	done := map[string]bool{}
	pending := rows
	for len(pending) > 0 {
		var batch, rest []map[string]any
		for _, d := range pending {
			if d["parent_id"] == nil || done[fmt.Sprint(d["parent_id"])] {
				batch = append(batch, d)
			} else {
				rest = append(rest, d)
			}
		}
		if len(batch) == 0 {
			for _, d := range rest {
				d["parent_id"] = nil
			}
			batch, rest = rest, nil
		}
		if err := insertRows(tx, "departments", batch); err != nil {
			return err
		}
		for _, d := range batch {
			done[fmt.Sprint(d["id"])] = true
		}
		pending = rest
	}
	return nil
}

func insertRows(tx db.Q, table string, rows []map[string]any) error {
	if table == "users" {
		// 主管领导可能引用 id 更大的用户：先写 NULL，全部写入后再补主管关系。
		if err := insertRowsWith(tx, table, rows, map[string]bool{"supervisor_id": true}); err != nil {
			return err
		}
		for i, r := range rows {
			if v := sqlValue(r["supervisor_id"]); v != nil {
				if _, err := db.Exec(tx, "UPDATE users SET supervisor_id = ? WHERE id = ?", v, sqlValue(r["id"])); err != nil {
					return fmt.Errorf("写入 users 第 %d 行主管领导失败：%w", i+1, err)
				}
			}
		}
		return nil
	}
	return insertRowsWith(tx, table, rows, nil)
}

func insertRowsWith(tx db.Q, table string, rows []map[string]any, nullCols map[string]bool) error {
	cols := tableCols(table)
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + db.Placeholders(len(cols)) + ")"
	for i, r := range rows {
		args := make([]any, len(cols))
		for j, c := range cols {
			if !nullCols[c] {
				args[j] = sqlValue(r[c])
			}
		}
		if _, err := db.Exec(tx, q, args...); err != nil {
			return fmt.Errorf("写入 %s 第 %d 行失败：%w", table, i+1, err)
		}
	}
	return nil
}

// SQLiteSnapshot 用 VACUUM INTO 生成一致的数据库文件副本（升级前备份、wlctl backup 使用）。
func (s *Backup) SQLiteSnapshot(prefix string) (string, error) {
	if err := os.MkdirAll(s.Cfg.Paths.Backups, 0o750); err != nil {
		return "", err
	}
	name := prefix + "-" + timeutil.Now().Format("20060102-150405") + ".sqlite"
	p := filepath.Join(s.Cfg.Paths.Backups, name)
	if _, err := s.DB.Exec("VACUUM INTO ?", p); err != nil {
		return "", err
	}
	_ = os.Chmod(p, 0o600)
	return p, nil
}

// ClearOptions 一键清空选项：工作记录与参与人员总是清空。
type ClearOptions struct {
	Categories bool // 同时清空工作分类和子分类
	Reports    bool // 同时清空已生成的周报、年度总结、时段总结
}

// ClearResult 清空结果。
type ClearResult struct {
	Snapshot string
	Deleted  map[string]int64
}

// ClearData 一键清空业务数据（仅管理员）：校验管理员密码 → 清空前自动完整备份 → 单事务删除。
// 人员、部门、系统设置、登录会话不受影响。
func (s *Backup) ClearData(admin Row, plainPassword string, opt ClearOptions) (*ClearResult, error) {
	if admin.Str("role") != "admin" {
		return nil, apperr.Forbidden("只有系统管理员可以清空数据。")
	}
	hash, err := db.Value(s.DB, "SELECT password_hash FROM users WHERE id = ? AND is_active = 1", admin.Int("id"))
	if err != nil {
		return nil, err
	}
	if hash == nil || !password.Verify(plainPassword, fmt.Sprint(hash)) {
		return nil, apperr.Validation(map[string]string{"password": "管理员密码不正确。"})
	}
	snapshot, _, err := s.CreateSnapshot("pre-clear")
	if err != nil {
		return nil, apperr.New("清空前自动备份失败，已取消清空：" + err.Error())
	}
	tables := []string{"work_record_participants", "work_records"}
	if opt.Reports {
		tables = append(tables, "weekly_reports", "annual_reports", "period_reports")
	}
	if opt.Categories {
		tables = append(tables, "project_categories", "work_categories")
	}
	res := &ClearResult{Snapshot: snapshot, Deleted: map[string]int64{}}
	err = s.DB.Tx(func(tx db.Q) error {
		for _, t := range tables {
			n, err := db.Exec(tx, "DELETE FROM "+t)
			if err != nil {
				return fmt.Errorf("清空 %s 失败：%w", t, err)
			}
			res.Deleted[t] = n
		}
		return nil
	})
	if err != nil {
		return nil, apperr.New(err.Error() + "。数据未改动，清空前备份：" + snapshot)
	}
	return res, nil
}
