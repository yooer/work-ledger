package controller

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"work-ledger/internal/apperr"
	"work-ledger/internal/service"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerBackup(app *web.App) {
	app.Page("/backup", web.Admin, c.backupPage)
	app.Page("/backup/download", web.Admin, c.backupDownload)
	app.Page("/backup/local", web.Admin, c.backupDownloadLocal)
	app.Ajax("/backup/ajax", web.Admin, web.Actions{
		"snapshot":  {Validate: validator.None, Handle: c.backupSnapshot},
		"restore":   {Validate: validator.Restore, Handle: c.backupRestore, Multipart: true},
		"clear":     {Validate: validator.ClearData, Handle: c.clearData},
		"save_auto": {Validate: validator.AutoBackup, Handle: c.saveAutoBackup},
		"test_ftp":  {Validate: validator.AutoBackup, Handle: c.testFTP},
		"run_auto":  {Validate: validator.None, Handle: c.runAutoBackup},
	})
}

func (c *Controllers) backupPage(ctx *web.Ctx) error {
	counts, err := c.S.Backup.Counts()
	if err != nil {
		return err
	}
	files, err := c.S.Backup.LocalFiles(50)
	if err != nil {
		return err
	}
	return ctx.Render("backup/index", map[string]any{
		"PageTitle": "数据备份与恢复", "Active": "backup", "DBSize": c.S.Backup.DBSize(), "Counts": counts,
		"Tables": service.TableNames(), "Files": files, "MaxUploadMB": c.S.Core.Cfg.Limits.BackupMaxUploadMB,
		"MaxJSONMB": c.S.Core.Cfg.Limits.BackupMaxJSONMB, "BackupDir": c.S.Core.Cfg.Paths.Backups,
		"Auto": c.S.AutoBackup.Config(), "AutoLast": c.S.AutoBackup.LastStatus(),
	})
}

func (c *Controllers) backupDownload(ctx *web.Ctx) error {
	gz := ctx.QueryString("format") != "json"
	name, body, err := c.S.Backup.Download(gz)
	if err != nil {
		return err
	}
	ct := "application/json; charset=utf-8"
	if gz {
		ct = "application/gzip"
	}
	return ctx.Download(name, ct, body)
}

func (c *Controllers) backupDownloadLocal(ctx *web.Ctx) error {
	p, err := c.S.Backup.LocalPath(ctx.QueryString("file"))
	if err != nil {
		return err
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	ct := "application/json; charset=utf-8"
	switch {
	case strings.HasSuffix(p, ".gz"):
		ct = "application/gzip"
	case strings.HasSuffix(p, ".sqlite"):
		ct = "application/vnd.sqlite3"
	}
	return ctx.Download(filepath.Base(p), ct, body)
}

func (c *Controllers) backupSnapshot(ctx *web.Ctx, _ any) (any, error) {
	name, size, err := c.S.Backup.CreateSnapshot("manual")
	if err != nil {
		return nil, err
	}
	return web.Result{Message: "本地快照已创建：" + name + "（" + service.HumanSize(int64(size)) + "）。", Redirect: "/backup", FlashOnRedirect: true}, nil
}

// clearData 一键清空台账数据（清空前自动备份，需管理员密码 + 确认文字）。
func (c *Controllers) clearData(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.ClearDataInput)
	res, err := c.S.Backup.ClearData(ctx.User, p.Password, service.ClearOptions{Categories: p.Categories, Reports: p.Reports})
	if err != nil {
		return nil, err
	}
	c.S.Core.Log.Warn("business data cleared", "by", ctx.UserID(), "snapshot", res.Snapshot, "deleted", res.Deleted)
	msg := "已清空工作记录 " + strconv.FormatInt(res.Deleted["work_records"], 10) + " 条"
	if p.Reports {
		msg += "、已生成总结 " + strconv.FormatInt(res.Deleted["weekly_reports"]+res.Deleted["annual_reports"]+res.Deleted["period_reports"], 10) + " 份"
	}
	if p.Categories {
		msg += "、工作分类 " + strconv.FormatInt(res.Deleted["work_categories"], 10) + " 个、子分类 " + strconv.FormatInt(res.Deleted["project_categories"], 10) + " 个"
	}
	msg += "。清空前备份：" + res.Snapshot + "（可在下方本地快照中下载，或用“从备份恢复”找回）。"
	return web.Result{Message: msg, Redirect: "/backup", FlashOnRedirect: true}, nil
}

// backupRestore 覆盖恢复：校验 → 恢复前安全快照 → 事务恢复 → 清空全部会话并要求重新登录。
func (c *Controllers) backupRestore(ctx *web.Ctx, _ any) (any, error) {
	maxBytes := int64(c.S.Core.Cfg.Limits.BackupMaxUploadMB) << 20
	fh, ok := ctx.R.MultipartForm.File["backup_file"]
	if !ok || len(fh) == 0 {
		return nil, apperr.Validation(map[string]string{"backup_file": "请选择要恢复的备份文件。"})
	}
	h := fh[0]
	if h.Size <= 0 {
		return nil, apperr.Validation(map[string]string{"backup_file": "备份文件为空。"})
	}
	if h.Size > maxBytes {
		return nil, apperr.Validation(map[string]string{"backup_file": "备份文件不能超过 " + strconv.Itoa(c.S.Core.Cfg.Limits.BackupMaxUploadMB) + " MB。"})
	}
	lower := strings.ToLower(h.Filename)
	if !strings.HasSuffix(lower, ".json") && !strings.HasSuffix(lower, ".gz") {
		return nil, apperr.Validation(map[string]string{"backup_file": "备份文件必须是 .json、.json.gz 或 .gz。"})
	}
	f, err := h.Open()
	if err != nil {
		return nil, apperr.New("无法读取上传的备份文件。")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, apperr.New("无法读取上传的备份文件。")
	}
	data, err := c.S.Backup.Decode(raw)
	if err != nil {
		return nil, err
	}
	safety, _, err := c.S.Backup.CreateSnapshot("pre-restore")
	if err != nil {
		return nil, apperr.New("创建恢复前快照失败：" + err.Error())
	}
	restored, err := c.S.Backup.Restore(data)
	if err != nil {
		c.S.Core.Log.Error("backup restore failed", "err", err)
		return nil, apperr.New("恢复失败：" + err.Error() + "。当前数据已回滚。恢复前快照：" + safety)
	}
	c.S.Settings.ClearCache()
	c.S.Core.Log.Warn("backup restored", "by", ctx.UserID(), "pre_restore", safety)
	// 全部会话已在事务中清除；当前浏览器换一个新的匿名会话来显示提示。
	ctx.Sess.Delete("user_id", "login_at", "2fa_user_id", "2fa_started_at")
	ctx.Sess.Regenerate()
	ctx.Flash("success", "数据恢复完成：工作记录 "+strconv.Itoa(restored["work_records"])+" 条，用户 "+strconv.Itoa(restored["users"])+
		" 个。已退出登录，请使用备份中的管理员账号重新登录。恢复前快照："+safety)
	return web.Result{Redirect: "/login"}, nil
}

func autoBackupConfig(p validator.AutoBackupInput) service.AutoBackupConfig {
	return service.AutoBackupConfig{Enabled: p.Enabled, Time: p.Time, KeepLocal: p.KeepLocal, KeepFTP: p.KeepFTP,
		FTP: service.FTPConfig{Host: p.FTPHost, Port: p.FTPPort, User: p.FTPUser, Password: p.FTPPassword, Dir: p.FTPDir, TLS: p.FTPTLS}}
}

func (c *Controllers) saveAutoBackup(ctx *web.Ctx, in any) (any, error) {
	if err := c.S.AutoBackup.SaveConfig(autoBackupConfig(in.(validator.AutoBackupInput))); err != nil {
		return nil, err
	}
	return web.Result{Message: "自动备份设置已保存。", Redirect: "/backup", FlashOnRedirect: true}, nil
}

// testFTP 用表单当前填写的参数测试（密码留空时用已保存的密码），不保存。
func (c *Controllers) testFTP(ctx *web.Ctx, in any) (any, error) {
	cfg := autoBackupConfig(in.(validator.AutoBackupInput))
	msg, err := c.S.AutoBackup.TestFTP(c.S.AutoBackup.WithStoredPassword(cfg.FTP))
	if err != nil {
		return nil, err
	}
	return web.Result{Message: msg}, nil
}

// runAutoBackup 立即按已保存的设置备份并上传。
func (c *Controllers) runAutoBackup(ctx *web.Ctx, _ any) (any, error) {
	st := c.S.AutoBackup.Run("manual")
	if !st.OK || (st.FTPUsed && !st.Uploaded) {
		return nil, apperr.New(st.Message) // 上传失败也按失败提示（本地备份已生成）
	}
	c.S.Core.Log.Info("手动执行自动备份", "user", ctx.User.Str("username"), "file", st.File, "uploaded", st.Uploaded)
	return web.Result{Message: st.Message, Redirect: "/backup", FlashOnRedirect: true}, nil
}
