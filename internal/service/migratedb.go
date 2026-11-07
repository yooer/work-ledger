package service

import (
	"fmt"
	"strings"

	"work-ledger/internal/db"
)

// MigrateResult 数据库迁移结果。
type MigrateResult struct {
	Counts map[string]int64 // 目标库各表行数（已与源库核对一致）
}

// TargetEmpty 目标库是否可以作为迁移目标：尚未建表，或已建表但没有任何人员（例如上次迁移失败后留下的空表）。
func TargetEmpty(target *db.DB) (bool, error) {
	ok, err := db.TableExists(target, "users")
	if err != nil || !ok {
		return err == nil, err
	}
	n, err := db.Int(target, "SELECT COUNT(*) FROM users")
	return n == 0, err
}

// MigrateDatabase 把 src 的全部业务数据复制到 target：在目标库建表 → 用整库备份格式导出并在一个事务中写入 →
// 复制不进备份文件的密钥类设置（FTP 密码）→ 逐表核对行数。目标库必须为空。
// 源库只读，不做任何修改；调用方负责在迁移前停止服务。
func MigrateDatabase(core *Core, src, target *db.DB) (*MigrateResult, error) {
	empty, err := TargetEmpty(target)
	if err != nil {
		return nil, fmt.Errorf("检查目标库失败：%w", err)
	}
	if !empty {
		return nil, fmt.Errorf("目标库已有数据（users 表不为空），为避免覆盖已拒绝迁移；请换一个空库")
	}
	if _, err := target.Migrate(); err != nil {
		return nil, fmt.Errorf("在目标库建表失败：%w", err)
	}

	from := &Backup{Core: &Core{DB: src, Cfg: core.Cfg, Log: core.Log}}
	to := &Backup{Core: &Core{DB: target, Cfg: core.Cfg, Log: core.Log}}
	raw, err := from.ExportJSON()
	if err != nil {
		return nil, fmt.Errorf("导出源库数据失败：%w", err)
	}
	data, err := to.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("校验导出数据失败：%w", err)
	}
	if _, err := to.Restore(data); err != nil {
		return nil, fmt.Errorf("写入目标库失败（已回滚）：%w", err)
	}
	secrets, err := db.All(src, "SELECT name, value FROM app_settings WHERE name IN ("+db.Placeholders(len(secretSettings))+")", anySlice(secretSettings)...)
	if err != nil {
		return nil, err
	}
	for _, r := range secrets {
		if _, err := db.Exec(target, `INSERT INTO app_settings (name, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, r.Str("name"), r.Str("value"), now()); err != nil {
			return nil, err
		}
	}

	want, err := from.Counts()
	if err != nil {
		return nil, err
	}
	got, err := to.Counts()
	if err != nil {
		return nil, err
	}
	var diff []string
	for _, t := range TableNames() {
		if t == "app_settings" {
			continue // 源库的密钥设置已单独复制，行数相同；此处跳过以免受默认设置补写影响
		}
		if want[t] != got[t] {
			diff = append(diff, fmt.Sprintf("%s：源 %d / 目标 %d", t, want[t], got[t]))
		}
	}
	if len(diff) > 0 {
		return nil, fmt.Errorf("迁移后行数不一致：%s", strings.Join(diff, "；"))
	}
	return &MigrateResult{Counts: got}, nil
}
