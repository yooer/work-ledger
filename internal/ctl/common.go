package ctl

import (
	"work-ledger/internal/config"
	"work-ledger/internal/db"
	"work-ledger/internal/service"
)

// snapshotDB 用 VACUUM INTO 生成一致的数据库副本（服务运行中也可执行）。
func snapshotDB(cfg *config.Config, prefix string) (string, error) {
	d, err := db.Open(cfg.DB.SQLitePath)
	if err != nil {
		return "", err
	}
	defer d.Close()
	b := &service.Backup{Core: &service.Core{DB: d, Cfg: cfg}}
	p, err := b.SQLiteSnapshot(prefix)
	if err != nil {
		return "", err
	}
	fixOwner(p, cfg.Paths.Home)
	return p, nil
}
