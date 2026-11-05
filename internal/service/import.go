package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"work-ledger/internal/apperr"
	"work-ledger/internal/db"
	"work-ledger/internal/password"
	"work-ledger/internal/timeutil"
	"work-ledger/internal/xlsx"
)

// Importer 导入工作记录（CSV / XLSX / JSON），逻辑移植自 PHP 版 WorkRecordImportService。
type Importer struct{ *Core }

// ImportRow 归一化后的一行（字段名已映射为标准键）。
type ImportRow struct {
	Line   int
	Fields map[string]any
}

// ImportOptions 导入选项。
type ImportOptions struct {
	DefaultCreatorID     int64
	AutoCreateCategories bool
	AutoCreateUsers      bool
	ParticipantAsCreator bool
}

// ImportError 失败行。
type ImportError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ImportResult 导入结果。
type ImportResult struct {
	Total             int           `json:"total"`
	Imported          int           `json:"imported"`
	Duplicates        int           `json:"duplicates"`
	Failed            int           `json:"failed"`
	CreatedCategories int           `json:"created_categories"`
	CreatedProjects   int           `json:"created_projects"`
	CreatedUsers      int           `json:"created_users"`
	Errors            []ImportError `json:"errors"`
	Format            string        `json:"format"`
	FileName          string        `json:"file_name"`
}

const maxImportErrors = 200

var importAliases = []struct {
	field   string
	aliases []string
}{
	{"category", []string{"工作分类", "分类", "工作类别", "workcategory", "category"}},
	{"content", []string{"工作内容", "内容", "工作描述", "workcontent", "content"}},
	{"work_at", []string{"日期", "工作日期", "发生时间", "时间", "workat", "datetime", "date"}},
	{"participants", []string{"参与人员", "参与人", "人员", "participants", "participant"}},
	{"difficulty", []string{"难易度", "难度", "difficulty"}},
	{"work_count", []string{"次数", "工作次数", "项目次数", "workcount", "count"}},
	{"project", []string{"集客分类", "集客项目", "项目分类", "集客业务分类", "projectcategory", "project"}},
	{"creator", []string{"记录人", "记录创建者", "创建人", "创建者", "填报人", "录入人", "记录者", "员工姓名", "姓名", "creator", "createdby"}},
	{"created_at", []string{"记录创建时间", "创建时间", "createdat", "createdatetime"}},
	{"updated_at", []string{"最后编辑时间", "更新时间", "updatedat", "updatedatetime"}},
	{"editor", []string{"最后编辑用户", "最后编辑人", "编辑人", "editor", "lasteditor"}},
	{"department", []string{"所属部门", "部门", "department"}},
	{"id", []string{"记录id", "id", "recordid"}},
}

func normalizeKey(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "\uFEFF")
	s = strings.NewReplacer(" ", "", "\t", "", "_", "", "-", "").Replace(s)
	return strings.ToLower(s)
}

func canonicalField(name string) string {
	key := normalizeKey(name)
	for _, a := range importAliases {
		for _, alias := range a.aliases {
			if key == normalizeKey(alias) {
				return a.field
			}
		}
	}
	return ""
}

// Parse 按扩展名解析文件内容。
func (s *Importer) Parse(data []byte, ext string) ([]ImportRow, string, error) {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), ".")) {
	case "csv":
		rows, err := parseCSV(data)
		return rows, "csv", err
	case "xlsx":
		rows, err := parseXLSX(data)
		return rows, "xlsx", err
	case "json":
		rows, err := parseJSONRows(data)
		return rows, "json", err
	}
	return nil, "", apperr.New("不支持的导入格式，请使用 CSV、XLSX 或 JSON。")
}

func toUTF8(raw []byte) (string, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))
	if utf8.Valid(raw) {
		return string(raw), nil
	}
	var dec transform.Transformer = simplifiedchinese.GB18030.NewDecoder()
	if bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) {
		dec = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder()
	} else if bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		dec = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder()
	}
	out, _, err := transform.Bytes(dec, raw)
	if err != nil || !utf8.Valid(out) || len(out) == 0 {
		return "", apperr.New("CSV 编码无法识别，请另存为 UTF-8 或 GBK 后重试。")
	}
	return string(out), nil
}

var sepLine = regexp.MustCompile(`(?i)^sep=(.)\r?\n`)

func detectDelimiter(raw string) rune {
	first := raw
	if i := strings.IndexAny(raw, "\r\n"); i >= 0 {
		first = raw[:i]
	}
	best, bestCount := ',', -1
	for _, d := range []rune{',', ';', '\t', '|'} {
		r := csv.NewReader(strings.NewReader(first))
		r.Comma, r.LazyQuotes, r.FieldsPerRecord = d, true, -1
		rec, err := r.Read()
		count := 1
		if err == nil {
			count = len(rec)
		}
		if count > bestCount {
			best, bestCount = d, count
		}
	}
	return best
}

func parseCSV(data []byte) ([]ImportRow, error) {
	raw, err := toUTF8(data)
	if err != nil {
		return nil, err
	}
	var delim rune
	if m := sepLine.FindStringSubmatch(raw); m != nil {
		delim, _ = utf8.DecodeRuneInString(m[1])
		raw = raw[len(m[0]):]
	} else {
		delim = detectDelimiter(raw)
	}
	r := csv.NewReader(strings.NewReader(raw))
	r.Comma, r.LazyQuotes, r.FieldsPerRecord, r.ReuseRecord = delim, true, -1, false
	header, err := r.Read()
	if err != nil {
		return nil, apperr.New("CSV 文件为空。")
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	var data2 [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, apperr.New("CSV 解析失败：" + err.Error())
		}
		empty := true
		for i, v := range rec {
			rec[i] = unescapeCSVCell(v)
			if strings.TrimSpace(v) != "" {
				empty = false
			}
		}
		if empty && len(rec) <= 1 {
			continue
		}
		data2 = append(data2, rec)
	}
	return tabularRows(header, data2, 2)
}

// unescapeCSVCell 还原导出时为防公式注入而加的单引号。
func unescapeCSVCell(v string) string {
	if len(v) >= 2 && v[0] == '\'' && strings.ContainsRune("=+-@\t\r", rune(v[1])) {
		return v[1:]
	}
	return v
}

func parseXLSX(data []byte) ([]ImportRow, error) {
	table, err := xlsx.Read(data)
	if err != nil {
		return nil, apperr.New(err.Error())
	}
	if len(table) == 0 {
		return nil, apperr.New("Excel 文件为空。")
	}
	header := table[0]
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	return tabularRows(header, table[1:], 2)
}

func tabularRows(header []string, data [][]string, startLine int) ([]ImportRow, error) {
	idx := map[string]int{}
	for i, name := range header {
		if f := canonicalField(name); f != "" {
			if _, exists := idx[f]; !exists {
				idx[f] = i
			}
		}
	}
	var missing []string
	for _, f := range []struct{ key, label string }{{"category", "工作分类"}, {"content", "工作内容"}, {"work_at", "日期"}} {
		if _, ok := idx[f.key]; !ok {
			missing = append(missing, f.label)
		}
	}
	if len(missing) > 0 {
		return nil, apperr.New("缺少必要列：" + strings.Join(missing, "、") + "。当前表头：" + strings.Join(header, "、"))
	}
	rows := make([]ImportRow, 0, len(data))
	for off, rec := range data {
		fields := map[string]any{}
		for f, i := range idx {
			if i < len(rec) {
				fields[f] = rec[i]
			} else {
				fields[f] = ""
			}
		}
		rows = append(rows, ImportRow{Line: startLine + off, Fields: fields})
	}
	return rows, nil
}

func parseJSONRows(data []byte) ([]ImportRow, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	trimmed := bytes.TrimSpace(data)
	if !json.Valid(trimmed) {
		var probe any
		err := json.Unmarshal(trimmed, &probe)
		msg := "无效的 JSON"
		if err != nil {
			msg = err.Error()
		}
		return nil, apperr.New("JSON 格式无效：" + msg)
	}
	var records []json.RawMessage
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		_ = json.Unmarshal(trimmed, &records)
	case len(trimmed) > 0 && trimmed[0] == '{':
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(trimmed, &obj)
		if raw, ok := obj["records"]; ok && len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '[' {
			_ = json.Unmarshal(raw, &records)
		} else {
			keys, _ := orderedObject(trimmed)
			found := false
			for _, k := range keys {
				if canonicalField(k) != "" {
					found = true
					break
				}
			}
			if !found {
				return nil, apperr.New("JSON 中没有找到工作记录数组。")
			}
			records = []json.RawMessage{trimmed}
		}
	default:
		return nil, apperr.New("JSON 顶层必须是数组或包含 records 数组的对象。")
	}
	var rows []ImportRow
	for i, raw := range records {
		keys, values := orderedObject(raw)
		if keys == nil {
			continue
		}
		fields := map[string]any{}
		for _, k := range keys {
			if f := canonicalField(k); f != "" {
				if _, exists := fields[f]; !exists {
					fields[f] = values[k]
				}
			}
		}
		if len(fields) == 0 {
			continue
		}
		rows = append(rows, ImportRow{Line: i + 1, Fields: fields})
	}
	return rows, nil
}

// orderedObject 按原始顺序返回 JSON 对象的键与值（非对象返回 nil）。
func orderedObject(raw []byte) ([]string, map[string]any) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, nil
	}
	keys := []string{}
	values := map[string]any{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		k, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, nil
		}
		if _, dup := values[k]; !dup {
			keys = append(keys, k)
		}
		values[k] = v
	}
	return keys, values
}

func looksLikeRecord(m map[string]any) bool {
	for k := range m {
		if canonicalField(k) != "" {
			return true
		}
	}
	return false
}

func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return ""
	case []any, map[string]any:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

var participantSplit = regexp.MustCompile(`[、,，;；/|]+`)

func splitParticipants(v any) []string {
	var values []string
	if list, ok := v.([]any); ok {
		for _, it := range list {
			switch it.(type) {
			case []any, map[string]any:
				continue
			}
			values = append(values, cellText(it))
		}
	} else {
		text := cellText(v)
		if text == "" {
			return nil
		}
		values = participantSplit.Split(text, -1)
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range values {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

var digitsRe = regexp.MustCompile(`^\d+$`)

type rowErr struct{ msg string }

func (e rowErr) Error() string { return e.msg }

func rowFail(format string, a ...any) error { return rowErr{fmt.Sprintf(format, a...)} }

func parseIntField(v any, def, min, max int, label string) (int, error) {
	text := cellText(v)
	if text == "" {
		return def, nil
	}
	if !digitsRe.MatchString(text) {
		return 0, rowFail("%s必须是整数。", label)
	}
	n, _ := strconv.Atoi(text)
	if n < min || n > max {
		return 0, rowFail("%s必须在 %d-%d 之间。", label, min, max)
	}
	return n, nil
}

var multiSpace = regexp.MustCompile(`\s+`)

// ParseImportDateTime 识别 YYYY-MM-DD[ HH:MM[:SS]]、斜杠分隔、紧凑格式与 Excel 日期序列。
func ParseImportDateTime(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 20000 && f < 100000 {
		days := math.Floor(f)
		secs := math.Round((f - days) * 86400)
		base := time.Date(1899, 12, 30, 0, 0, 0, 0, timeutil.Location())
		return base.AddDate(0, 0, int(days)).Add(time.Duration(secs) * time.Second).Format(timeutil.DateTime), true
	}
	v := strings.NewReplacer("/", "-", "T", " ").Replace(raw)
	v = multiSpace.ReplaceAllString(strings.TrimSpace(v), " ")
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "20060102150405", "20060102",
		"2006-1-2 15:04:05", "2006-1-2 15:04", "2006-1-2", "2006-1-2 15:4", "2006年1月2日 15:04", "2006年1月2日"} {
		if t, err := time.ParseInLocation(layout, v, timeutil.Location()); err == nil {
			return t.Format(timeutil.DateTime), true
		}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.In(timeutil.Location()).Format(timeutil.DateTime), true
	}
	return "", false
}

// importHash 与 PHP 版 json_encode + sha256 完全一致，保证新旧系统导入去重互通。
func importHash(workAt string, categoryID int64, projectID *int64, content string, difficulty, workCount int, creatorID int64, participants []int64) string {
	ids := append([]int64{}, participants...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	uniq := ids[:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			uniq = append(uniq, id)
		}
	}
	payload := struct {
		WorkAt       string  `json:"work_at"`
		CategoryID   int64   `json:"category_id"`
		ProjectID    *int64  `json:"project_id"`
		Content      string  `json:"content"`
		Difficulty   int     `json:"difficulty"`
		WorkCount    int     `json:"work_count"`
		CreatorID    int64   `json:"creator_id"`
		Participants []int64 `json:"participants"`
	}{workAt, categoryID, projectID, content, difficulty, workCount, creatorID, uniq}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}

// Import 在一个事务中逐行导入；每行使用 SAVEPOINT，失败行回滚并记录原因，不影响其他行。
func (s *Importer) Import(rows []ImportRow, opt ImportOptions) (*ImportResult, error) {
	maxRows := s.Cfg.Limits.ImportMaxRows
	if maxRows > 0 && len(rows) > maxRows {
		return nil, apperr.New(fmt.Sprintf("单次最多导入 %d 行，请拆分文件后重试。", maxRows))
	}
	defCreator, err := db.One(s.DB, "SELECT * FROM users WHERE id = ? AND is_active = 1 LIMIT 1", opt.DefaultCreatorID)
	if err != nil {
		return nil, err
	}
	if defCreator == nil {
		return nil, apperr.Validation(map[string]string{"default_creator_id": "请选择有效的默认记录人。"})
	}
	res := &ImportResult{Total: len(rows), Errors: []ImportError{}}
	err = s.DB.Tx(func(tx db.Q) error {
		for i, row := range rows {
			sp := "import_row_" + strconv.Itoa(i)
			if _, err := db.Exec(tx, "SAVEPOINT "+sp); err != nil {
				return err
			}
			before := [3]int{res.CreatedCategories, res.CreatedProjects, res.CreatedUsers}
			dup, err := s.importRow(tx, row, opt, defCreator, res)
			var re rowErr
			if err != nil && !errors.As(err, &re) {
				return err // 数据库等非业务错误：整体回滚
			}
			if err != nil {
				if _, e := db.Exec(tx, "ROLLBACK TO SAVEPOINT "+sp); e != nil {
					return e
				}
				res.CreatedCategories, res.CreatedProjects, res.CreatedUsers = before[0], before[1], before[2]
				res.Failed++
				if len(res.Errors) < maxImportErrors {
					res.Errors = append(res.Errors, ImportError{Row: row.Line, Message: re.msg})
				}
			} else if dup {
				res.Duplicates++
			} else {
				res.Imported++
			}
			if _, err := db.Exec(tx, "RELEASE SAVEPOINT "+sp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Importer) importRow(tx db.Q, row ImportRow, opt ImportOptions, defCreator Row, res *ImportResult) (bool, error) {
	f := row.Fields
	categoryName, content := cellText(f["category"]), cellText(f["content"])
	workAtRaw, projectName, creatorToken := cellText(f["work_at"]), cellText(f["project"]), cellText(f["creator"])
	if categoryName == "" {
		return false, rowFail("工作分类为空。")
	}
	if utf8.RuneCountInString(categoryName) > 100 {
		return false, rowFail("工作分类名称超过 100 个字符。")
	}
	if content == "" {
		return false, rowFail("工作内容为空。")
	}
	if utf8.RuneCountInString(content) > 5000 {
		return false, rowFail("工作内容超过 5000 个字符。")
	}
	workAt, ok := ParseImportDateTime(workAtRaw)
	if !ok {
		if workAtRaw == "" {
			workAtRaw = "(空)"
		}
		return false, rowFail("日期格式无法识别：%s", workAtRaw)
	}
	difficulty, err := parseIntField(f["difficulty"], 3, 1, 5, "难易度")
	if err != nil {
		return false, err
	}
	workCount, err := parseIntField(f["work_count"], 1, 1, 100, "次数")
	if err != nil {
		return false, err
	}
	tokens := splitParticipants(f["participants"])
	if creatorToken == "" && opt.ParticipantAsCreator && len(tokens) == 1 {
		creatorToken = tokens[0]
	}
	creator := defCreator
	if creatorToken != "" {
		if creator, err = s.resolveUser(tx, creatorToken, opt.AutoCreateUsers, true, res); err != nil {
			return false, err
		}
		if creator == nil {
			return false, rowFail("找不到记录人：%s", creatorToken)
		}
	}
	if projectName != "" && utf8.RuneCountInString(projectName) > 100 {
		return false, rowFail("集客项目名称超过 100 个字符。")
	}
	cat, err := s.resolveCategory(tx, categoryName, projectName != "", opt.AutoCreateCategories, res)
	if err != nil {
		return false, err
	}
	var projectID *int64
	if projectName != "" {
		if !cat.Bool("is_customer_project") {
			return false, rowFail("工作分类“%s”不是集客业务，不能填写集客项目。", categoryName)
		}
		id, err := s.resolveProject(tx, cat.Int("id"), projectName, opt.AutoCreateCategories, res)
		if err != nil {
			return false, err
		}
		projectID = &id
	} else if cat.Bool("is_customer_project") {
		if !opt.AutoCreateCategories {
			return false, rowFail("集客业务分类“%s”缺少集客项目。", categoryName)
		}
		id, err := s.resolveProject(tx, cat.Int("id"), "未分类（历史导入）", true, res)
		if err != nil {
			return false, err
		}
		projectID = &id
	}
	var participantIDs []int64
	seen := map[int64]bool{}
	if len(tokens) == 0 {
		participantIDs = []int64{creator.Int("id")}
	} else {
		for _, t := range tokens {
			p, err := s.resolveUser(tx, t, opt.AutoCreateUsers, true, res)
			if err != nil {
				return false, err
			}
			if p == nil {
				return false, rowFail("找不到参与人员：%s", t)
			}
			if !seen[p.Int("id")] {
				seen[p.Int("id")] = true
				participantIDs = append(participantIDs, p.Int("id"))
			}
		}
	}
	editor := creator
	if et := cellText(f["editor"]); et != "" {
		if cand, err := s.resolveUser(tx, et, false, false, res); err == nil && cand != nil {
			editor = cand
		}
	}
	createdAt, ok := ParseImportDateTime(cellText(f["created_at"]))
	if !ok {
		createdAt = workAt
	}
	updatedAt, ok := ParseImportDateTime(cellText(f["updated_at"]))
	if !ok {
		updatedAt = createdAt
	}
	hash := importHash(workAt, cat.Int("id"), projectID, content, difficulty, workCount, creator.Int("id"), participantIDs)
	if v, err := db.Value(tx, "SELECT id FROM work_records WHERE import_hash = ? LIMIT 1", hash); err != nil {
		return false, err
	} else if v != nil {
		return true, nil
	}
	var pid any
	if projectID != nil {
		pid = *projectID
	}
	recordID, err := db.Insert(tx, `INSERT INTO work_records (work_category_id, project_category_id, content, work_at, difficulty, work_count,
		department_id, created_by, created_at, updated_by, updated_at, import_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cat.Int("id"), pid, content, workAt, difficulty, workCount, nullInt(creator.Int("department_id")), creator.Int("id"),
		createdAt, editor.Int("id"), updatedAt, hash)
	if err != nil {
		return false, err
	}
	for _, uid := range participantIDs {
		if _, err := db.Exec(tx, "INSERT INTO work_record_participants (work_record_id, user_id) VALUES (?, ?)", recordID, uid); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (s *Importer) resolveCategory(tx db.Q, name string, customer, autoCreate bool, res *ImportResult) (Row, error) {
	cat, err := db.One(tx, "SELECT * FROM work_categories WHERE name = ? LIMIT 1", name)
	if err != nil || cat != nil {
		return cat, err
	}
	if !autoCreate {
		return nil, rowFail("工作分类不存在：%s", name)
	}
	ts := now()
	id, err := db.Insert(tx, "INSERT INTO work_categories (name, is_customer_project, is_active, sort_order, created_at, updated_at) VALUES (?, ?, 1, 500, ?, ?)",
		name, boolInt(customer), ts, ts)
	if err != nil {
		return nil, err
	}
	res.CreatedCategories++
	return db.One(tx, "SELECT * FROM work_categories WHERE id = ?", id)
}

func (s *Importer) resolveProject(tx db.Q, categoryID int64, name string, autoCreate bool, res *ImportResult) (int64, error) {
	p, err := db.One(tx, "SELECT id FROM project_categories WHERE work_category_id = ? AND name = ? LIMIT 1", categoryID, name)
	if err != nil {
		return 0, err
	}
	if p != nil {
		return p.Int("id"), nil
	}
	if !autoCreate {
		return 0, rowFail("集客项目不存在：%s", name)
	}
	ts := now()
	id, err := db.Insert(tx, "INSERT INTO project_categories (work_category_id, name, is_active, sort_order, created_at, updated_at) VALUES (?, ?, 1, 500, ?, ?)",
		categoryID, name, ts, ts)
	if err != nil {
		return 0, err
	}
	res.CreatedProjects++
	return id, nil
}

// resolveUser 按姓名/工号/登录名查找；strict 时找不到或重名会报错；允许时自动创建停用的历史人员。
func (s *Importer) resolveUser(tx db.Q, token string, autoCreate, strict bool, res *ImportResult) (Row, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	matches, err := db.All(tx, "SELECT * FROM users WHERE name = ? OR employee_no = ? OR username = ? LIMIT 10", token, token, token)
	if err != nil {
		return nil, err
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		var exact []Row
		for _, u := range matches {
			if strings.EqualFold(u.Str("employee_no"), token) || strings.EqualFold(u.Str("username"), token) {
				exact = append(exact, u)
			}
		}
		if len(exact) == 1 {
			return exact[0], nil
		}
		if strict {
			return nil, rowFail("“%s”对应多名员工，请改用唯一工号或登录名。", token)
		}
		return nil, nil
	}
	if !autoCreate {
		if strict {
			return nil, rowFail("人员不存在：%s", token)
		}
		return nil, nil
	}
	var username string
	for attempt := 0; attempt < 20; attempt++ {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		cand := "legacy_" + hex.EncodeToString(b)
		if v, err := db.Value(tx, "SELECT id FROM users WHERE username = ?", cand); err != nil {
			return nil, err
		} else if v == nil {
			username = cand
			break
		}
	}
	if username == "" {
		return nil, errors.New("无法生成历史人员登录名")
	}
	h, err := password.Hash(password.Random(24))
	if err != nil {
		return nil, err
	}
	ts := now()
	id, err := db.Insert(tx, `INSERT INTO users (username, password_hash, name, employee_no, role, department_id, phone, email, is_active,
		totp_enabled, created_at, updated_at) VALUES (?, ?, ?, NULL, 'employee', NULL, NULL, NULL, 0, 0, ?, ?)`, username, h, token, ts, ts)
	if err != nil {
		return nil, err
	}
	res.CreatedUsers++
	return db.One(tx, "SELECT * FROM users WHERE id = ?", id)
}
