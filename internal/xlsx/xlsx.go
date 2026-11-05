// Package xlsx 是无第三方依赖的最小 XLSX 读写实现（移植自 PHP 版 Xlsx.php）：
// 写出单个工作表（首行冻结 + 自动筛选），读取第一个工作表为二维字符串数组。
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Write 生成 xlsx 文件内容。值为 int/int64/float64 时写为数字，其余写为内联字符串。
func Write(headers []string, rows [][]any, sheetName string) ([]byte, error) {
	maxCols := max(1, len(headers))
	lastCol := ColumnName(maxCols - 1)
	dimension := "A1:" + lastCol + strconv.Itoa(max(1, len(rows)+1))

	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	sheet.WriteString(`<dimension ref="` + dimension + `"/>`)
	sheet.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	sheet.WriteString(`<sheetFormatPr defaultRowHeight="18"/><cols>`)
	for i := 0; i < maxCols; i++ {
		width := 18
		if i == 1 {
			width = 48 // 工作内容列加宽
		}
		fmt.Fprintf(&sheet, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, width)
	}
	sheet.WriteString(`</cols><sheetData>`)
	hdr := make([]any, len(headers))
	for i, h := range headers {
		hdr[i] = h
	}
	writeRow(&sheet, 1, hdr)
	for i, r := range rows {
		writeRow(&sheet, i+2, r)
	}
	sheet.WriteString(`</sheetData><autoFilter ref="` + dimension + `"/></worksheet>`)

	files := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + esc(sheetName) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"xl/styles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeRow(b *strings.Builder, n int, values []any) {
	fmt.Fprintf(b, `<row r="%d">`, n)
	for i, v := range values {
		ref := ColumnName(i) + strconv.Itoa(n)
		switch t := v.(type) {
		case int:
			fmt.Fprintf(b, `<c r="%s"><v>%d</v></c>`, ref, t)
		case int64:
			fmt.Fprintf(b, `<c r="%s"><v>%d</v></c>`, ref, t)
		case float64:
			fmt.Fprintf(b, `<c r="%s"><v>%s</v></c>`, ref, strconv.FormatFloat(t, 'f', -1, 64))
		default:
			fmt.Fprintf(b, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, esc(fmt.Sprint(t)))
		}
	}
	b.WriteString(`</row>`)
}

// esc 去除 XML 不允许的控制字符并转义。
func esc(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == 0x09 || r == 0x0A || r == 0x0D || (r >= 0x20 && r != 0xFFFE && r != 0xFFFF) {
			return r
		}
		return -1
	}, strings.ToValidUTF8(s, ""))
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ColumnName 0 → A，25 → Z，26 → AA。
func ColumnName(i int) string {
	name := ""
	i++
	for i > 0 {
		mod := (i - 1) % 26
		name = string(rune('A'+mod)) + name
		i = (i - 1) / 26
	}
	return name
}

var colRe = regexp.MustCompile(`^([A-Z]+)`)

// ColumnIndex "A1" → 0，"AA3" → 26；无法识别返回 -1。
func ColumnIndex(ref string) int {
	m := colRe.FindStringSubmatch(strings.ToUpper(ref))
	if m == nil {
		return -1
	}
	idx := 0
	for _, c := range m[1] {
		idx = idx*26 + int(c-'A'+1)
	}
	return idx - 1
}

var sheetRe = regexp.MustCompile(`^xl/worksheets/sheet(\d+)\.xml$`)

// Read 读取第一个工作表（sheetN.xml 中编号最小的），返回非空行。
func Read(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("无法打开 Excel 文件，请确认是有效的 .xlsx 文件")
	}
	var shared []string
	type sheetFile struct {
		n int
		f *zip.File
	}
	var sheets []sheetFile
	var sharedFile *zip.File
	for _, f := range zr.File {
		if f.Name == "xl/sharedStrings.xml" {
			sharedFile = f
		}
		if m := sheetRe.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			sheets = append(sheets, sheetFile{n, f})
		}
	}
	if len(sheets) == 0 {
		return nil, errors.New("Excel 文件中没有工作表")
	}
	if sharedFile != nil {
		if shared, err = readShared(sharedFile); err != nil {
			return nil, err
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].n < sheets[j].n })
	return readSheet(sheets[0].f, shared)
}

func open(f *zip.File) (*xml.Decoder, func(), error) {
	if f.UncompressedSize64 > 200<<20 {
		return nil, nil, errors.New("Excel 文件过大")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil, errors.New("无法读取 Excel 工作表")
	}
	dec := xml.NewDecoder(io.LimitReader(rc, 200<<20))
	dec.Strict = false
	return dec, func() { rc.Close() }, nil
}

// readShared 每个 <si> 拼接其下所有 <t> 文本。
func readShared(f *zip.File) ([]string, error) {
	dec, closeFn, err := open(f)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	var out []string
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("Excel 文件 XML 结构无效")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = inSI
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
				inSI = false
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out, nil
}

func readSheet(f *zip.File, shared []string) ([][]string, error) {
	dec, closeFn, err := open(f)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	var result [][]string
	var cells map[int]string
	var cellRef, cellType string
	var val, text strings.Builder
	inSheetData, inCell, inV, inT := false, false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("Excel 文件 XML 结构无效")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sheetData":
				inSheetData = true
			case "row":
				if inSheetData {
					cells = map[int]string{}
				}
			case "c":
				if cells != nil {
					inCell = true
					cellRef, cellType = attr(t, "r"), attr(t, "t")
					val.Reset()
					text.Reset()
				}
			case "v":
				inV = inCell
			case "t":
				inT = inCell
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "sheetData":
				inSheetData = false
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				if inCell {
					col := ColumnIndex(cellRef)
					if col < 0 {
						col = len(cells)
					}
					cells[col] = cellValue(cellType, val.String(), text.String(), shared)
					inCell = false
				}
			case "row":
				if cells != nil && len(cells) > 0 {
					width := 0
					for k := range cells {
						width = max(width, k+1)
					}
					row := make([]string, width)
					nonEmpty := false
					for k, v := range cells {
						row[k] = v
						if strings.TrimSpace(v) != "" {
							nonEmpty = true
						}
					}
					if nonEmpty {
						result = append(result, row)
					}
				}
				cells = nil
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			} else if inT {
				text.Write(t)
			}
		}
	}
	return result, nil
}

func cellValue(typ, v, inline string, shared []string) string {
	switch typ {
	case "inlineStr":
		return inline
	case "s":
		if v == "" {
			return ""
		}
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || i < 0 || i >= len(shared) {
			return ""
		}
		return shared[i]
	case "b":
		if v == "1" {
			return "TRUE"
		}
		return "FALSE"
	}
	if v == "" && inline != "" {
		return inline
	}
	return v
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// ValidUTF8 供调用方检查。
func ValidUTF8(s string) bool { return utf8.ValidString(s) }
