package service

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"time"

	"work-ledger/internal/model"
	"work-ledger/internal/timeutil"
	"work-ledger/internal/xlsx"
)

// Exporter 导出 CSV / Excel / JSON，列与 PHP 版一致（可再次导入）。
type Exporter struct {
	*Core
	Records *Records
}

// ExportHeaders 导出列。
var ExportHeaders = []string{"工作分类", "工作内容", "日期", "参与人员", "难易度", "次数", "子分类", "记录人", "记录创建时间", "最后编辑用户", "最后编辑时间", "所属部门", "记录ID"}

// ExportFile 导出结果。
type ExportFile struct {
	Name        string
	ContentType string
	Body        []byte
	Truncated   bool
}

func participantNames(r Row) []string {
	names := []string{}
	if parts, ok := r["participants"].([]Row); ok {
		for _, p := range parts {
			names = append(names, p.Str("name"))
		}
	}
	return names
}

func recordDept(r Row) string {
	if r.Str("department_name") == "" {
		return ""
	}
	if r.Str("parent_department_name") != "" {
		return r.Str("parent_department_name") + " / " + r.Str("department_name")
	}
	return r.Str("department_name")
}

func tableRows(records []Row) [][]any {
	out := make([][]any, 0, len(records))
	for _, r := range records {
		dept := recordDept(r)
		if dept == "" {
			dept = "未分配部门"
		}
		out = append(out, []any{
			r.Str("category_name"), r.Str("content"), timeutil.FormatDateTime(r.Str("work_at")),
			strings.Join(participantNames(r), "、"), r.Int("difficulty"), r.Int("work_count"), r.Str("project_name"),
			r.Str("creator_name"), timeutil.FormatDateTime(r.Str("created_at")), r.Str("editor_name"),
			timeutil.FormatDateTime(r.Str("updated_at")), dept, r.Int("id"),
		})
	}
	return out
}

// csvSafe 防止 Excel 公式注入。
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// Export 生成导出文件。
func (s *Exporter) Export(sc Scope, f RecordFilter, format string, meta map[string]any) (*ExportFile, error) {
	maxRows := s.Cfg.Limits.ExportMaxRows
	records, truncated, err := s.Records.ExportRows(sc, f, maxRows)
	if err != nil {
		return nil, err
	}
	name := "work-records-" + timeutil.Now().Format("20060102-150405")
	switch format {
	case "json":
		type jsonRecord struct {
			ID              int64    `json:"id"`
			WorkCategory    string   `json:"work_category"`
			WorkContent     string   `json:"work_content"`
			WorkAt          string   `json:"work_at"`
			Participants    []string `json:"participants"`
			Difficulty      int64    `json:"difficulty"`
			WorkCount       int64    `json:"work_count"`
			ProjectCategory *string  `json:"project_category"`
			Creator         string   `json:"creator"`
			Department      *string  `json:"department"`
			CreatedAt       string   `json:"created_at"`
			LastEditor      string   `json:"last_editor"`
			UpdatedAt       string   `json:"updated_at"`
		}
		list := make([]jsonRecord, 0, len(records))
		for _, r := range records {
			var dept *string
			if d := recordDept(r); d != "" {
				dept = &d
			}
			list = append(list, jsonRecord{ID: r.Int("id"), WorkCategory: r.Str("category_name"), WorkContent: r.Str("content"),
				WorkAt: timeutil.FormatDateTime(r.Str("work_at")), Participants: participantNames(r), Difficulty: r.Int("difficulty"),
				WorkCount: r.Int("work_count"), ProjectCategory: strPtr(r["project_name"]), Creator: r.Str("creator_name"), Department: dept,
				CreatedAt: timeutil.FormatDateTime(r.Str("created_at")), LastEditor: r.Str("editor_name"), UpdatedAt: timeutil.FormatDateTime(r.Str("updated_at"))})
		}
		meta["max_rows"], meta["truncated"] = maxRows, truncated
		payload := map[string]any{"exported_at": timeutil.Now().Format(time.RFC3339), "count": len(list), "meta": meta, "records": list}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "    ")
		if err := enc.Encode(payload); err != nil {
			return nil, err
		}
		return &ExportFile{Name: name + ".json", ContentType: "application/json; charset=utf-8", Body: buf.Bytes(), Truncated: truncated}, nil
	case "xlsx":
		body, err := xlsx.Write(ExportHeaders, tableRows(records), "工作记录")
		if err != nil {
			return nil, err
		}
		return &ExportFile{Name: name + ".xlsx", ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Body: body, Truncated: truncated}, nil
	default:
		var buf bytes.Buffer
		buf.WriteString("\xEF\xBB\xBF")
		w := csv.NewWriter(&buf)
		w.UseCRLF = false
		_ = w.Write(ExportHeaders)
		for _, row := range tableRows(records) {
			rec := make([]string, len(row))
			for i, v := range row {
				if s, ok := v.(string); ok {
					rec[i] = csvSafe(s)
				} else {
					rec[i] = model.ToStr(v)
				}
			}
			_ = w.Write(rec)
		}
		w.Flush()
		return &ExportFile{Name: name + ".csv", ContentType: "text/csv; charset=utf-8", Body: buf.Bytes(), Truncated: truncated}, w.Error()
	}
}
