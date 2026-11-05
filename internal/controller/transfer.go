package controller

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"work-ledger/internal/apperr"
	"work-ledger/internal/service"
	"work-ledger/internal/validator"
	"work-ledger/internal/web"
)

func (c *Controllers) registerTransfer(app *web.App) {
	app.Page("/records/export", web.Login, c.exportRecords)
	app.Page("/records/import", web.Admin, c.importPage)
	app.Ajax("/records/import/ajax", web.Admin, web.Actions{
		"import": {Validate: validator.ImportOptions, Handle: c.importRecords, Multipart: true},
	})
}

// exportRecords 导出当前筛选结果（文件下载例外：GET）。mode 决定范围：mine / dept / all。
func (c *Controllers) exportRecords(ctx *web.Ctx) error {
	q := ctx.Query()
	v := validator.New(q)
	format := v.OneOf("format", "导出格式", "csv", "csv", "xlsx", "json")
	if err := v.Err(); err != nil {
		return apperr.New("不支持的导出格式。")
	}
	mode := v.OneOf("mode", "导出范围", "mine", "mine", "dept", "all")
	switch {
	case mode == "all" && !ctx.IsAdmin():
		mode = "mine"
	case mode == "dept" && !ctx.IsAdmin() && !ctx.IsManager():
		mode = "mine"
	}
	sc, f, in, err := c.recordQuery(ctx, mode)
	if err != nil {
		return err
	}
	scopeName := map[string]string{"all": "admin", "dept": "department", "mine": "self"}[mode]
	file, err := c.S.Export.Export(sc, f, format, map[string]any{"scope": scopeName, "filters": in})
	if err != nil {
		return err
	}
	if file.Truncated {
		ctx.W.Header().Set("X-Export-Truncated", "1")
	} else {
		ctx.W.Header().Set("X-Export-Truncated", "0")
	}
	return ctx.Download(file.Name, file.ContentType, file.Body)
}

const sessImportResult = "_import_result"

func (c *Controllers) importPage(ctx *web.Ctx) error {
	var result *service.ImportResult
	if raw, ok := ctx.Sess.Get(sessImportResult).(string); ok && raw != "" {
		result = &service.ImportResult{}
		if json.Unmarshal([]byte(raw), result) != nil {
			result = nil
		}
		ctx.Sess.Delete(sessImportResult)
	}
	users, err := c.S.Users.ParticipantOptions(nil)
	if err != nil {
		return err
	}
	cfg := c.S.Core.Cfg.Limits
	return ctx.Render("records/import", map[string]any{
		"PageTitle": "导入工作记录", "Active": "records-import", "Users": users, "DefaultCreator": ctx.UserID(),
		"Result": result, "MaxRows": cfg.ImportMaxRows, "MaxMB": cfg.ImportMaxMB,
	})
}

func (c *Controllers) importRecords(ctx *web.Ctx, in any) (any, error) {
	p := in.(validator.ImportOptionsInput)
	maxBytes := int64(c.S.Core.Cfg.Limits.ImportMaxMB) << 20
	fh, ok := ctx.R.MultipartForm.File["file"]
	if !ok || len(fh) == 0 {
		return nil, apperr.Validation(map[string]string{"file": "请选择要导入的文件。"})
	}
	header := fh[0]
	if header.Size <= 0 {
		return nil, apperr.Validation(map[string]string{"file": "上传文件为空。"})
	}
	if header.Size > maxBytes {
		return nil, apperr.Validation(map[string]string{"file": "导入文件不能超过 " + strconv.Itoa(c.S.Core.Cfg.Limits.ImportMaxMB) + " MB。"})
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	if ext != "csv" && ext != "xlsx" && ext != "json" {
		return nil, apperr.Validation(map[string]string{"file": "仅支持 CSV、XLSX（Excel）或 JSON 文件。"})
	}
	f, err := header.Open()
	if err != nil {
		return nil, apperr.New("文件上传失败，请重试。")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, apperr.New("读取上传文件失败。")
	}
	rows, format, err := c.S.Import.Parse(data, ext)
	if err != nil {
		return nil, err
	}
	res, err := c.S.Import.Import(rows, service.ImportOptions{
		DefaultCreatorID: p.DefaultCreatorID, AutoCreateCategories: p.AutoCreateCategories,
		AutoCreateUsers: p.AutoCreateUsers, ParticipantAsCreator: p.ParticipantAsCreator,
	})
	if err != nil {
		return nil, err
	}
	res.Format, res.FileName = format, filepath.Base(header.Filename)
	raw, _ := json.Marshal(res)
	ctx.Sess.Set(sessImportResult, string(raw))

	msg := "导入完成：成功 " + strconv.Itoa(res.Imported) + " 条"
	if res.Total == 0 {
		msg = "文件中没有可导入的数据行"
	}
	if res.Duplicates > 0 {
		msg += "，跳过重复 " + strconv.Itoa(res.Duplicates) + " 条"
	}
	if res.Failed > 0 {
		msg += "，失败 " + strconv.Itoa(res.Failed) + " 行"
	}
	kind := "success"
	if res.Failed > 0 || res.Total == 0 {
		kind = "warning"
	}
	ctx.Flash(kind, msg+"。")
	return web.Result{Redirect: "/records/import"}, nil
}
