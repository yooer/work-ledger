package web

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Markdown 把 AI 返回的 Markdown 转为安全 HTML（先转义再处理），逻辑移植自 PHP 版 markdown_to_html。
func Markdown(src string) string {
	lines := regexp.MustCompile(`\r\n|\r|\n`).Split(strings.TrimSpace(src), -1)
	var out []string
	n := len(lines)
	i := 0
	for i < n {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			i++
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			i++
			var code []string
			for i < n && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			i++
			out = append(out, "<pre><code>"+html.EscapeString(strings.Join(code, "\n"))+"</code></pre>")
			continue
		}
		if m := mdHeading.FindStringSubmatch(trimmed); m != nil {
			level := strconv.Itoa(min(6, len(m[1])))
			out = append(out, "<h"+level+">"+mdInline(m[2])+"</h"+level+">")
			i++
			continue
		}
		if mdHr.MatchString(trimmed) {
			out = append(out, "<hr>")
			i++
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			var quote []string
			for i < n && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				quote = append(quote, mdQuote.ReplaceAllString(lines[i], ""))
				i++
			}
			out = append(out, "<blockquote>"+mdInline(strings.Join(quote, " "))+"</blockquote>")
			continue
		}
		if strings.Contains(trimmed, "|") && i+1 < n {
			sep := strings.TrimSpace(lines[i+1])
			if mdTableSep.MatchString(sep) && strings.Contains(sep, "-") {
				header := splitRow(trimmed)
				i += 2
				var rows [][]string
				for i < n && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
					rows = append(rows, splitRow(lines[i]))
					i++
				}
				var b strings.Builder
				b.WriteString(`<div class="table-responsive"><table class="table table-vcenter table-bordered"><thead><tr>`)
				for _, h := range header {
					b.WriteString("<th>" + mdInline(h) + "</th>")
				}
				b.WriteString("</tr></thead><tbody>")
				for _, r := range rows {
					b.WriteString("<tr>")
					for idx := range header {
						cell := ""
						if idx < len(r) {
							cell = r[idx]
						}
						b.WriteString("<td>" + mdInline(cell) + "</td>")
					}
					b.WriteString("</tr>")
				}
				b.WriteString("</tbody></table></div>")
				out = append(out, b.String())
				continue
			}
		}
		if mdUL.MatchString(line) {
			var items []string
			for i < n {
				m := mdUL.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				items = append(items, "<li>"+mdInline(m[1])+"</li>")
				i++
			}
			out = append(out, "<ul>"+strings.Join(items, "")+"</ul>")
			continue
		}
		if mdOL.MatchString(line) {
			var items []string
			for i < n {
				m := mdOL.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				items = append(items, "<li>"+mdInline(m[1])+"</li>")
				i++
			}
			out = append(out, "<ol>"+strings.Join(items, "")+"</ol>")
			continue
		}
		para := []string{mdInline(trimmed)}
		i++
		for i < n && strings.TrimSpace(lines[i]) != "" && !mdBlockStart.MatchString(lines[i]) {
			para = append(para, mdInline(strings.TrimSpace(lines[i])))
			i++
		}
		out = append(out, "<p>"+strings.Join(para, "<br>")+"</p>")
	}
	return strings.Join(out, "\n")
}

var (
	mdHeading    = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	mdHr         = regexp.MustCompile(`^---+\s*$`)
	mdQuote      = regexp.MustCompile(`^\s*>\s?`)
	mdTableSep   = regexp.MustCompile(`^\|?[\s:|-]+\|[\s:|-]*\|?$`)
	mdUL         = regexp.MustCompile(`^\s*[-*+]\s+(.+)$`)
	mdOL         = regexp.MustCompile(`^\s*\d+\.\s+(.+)$`)
	mdBlockStart = regexp.MustCompile(`^\s*(#{1,6}\s|` + "```" + `|>\s?|[-*+]\s|\d+\.\s|---+\s*$)`)
	mdCode       = regexp.MustCompile("`([^`]+)`")
	mdStrong     = regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)
	mdEm         = regexp.MustCompile(`(?s)\*([^*]+?)\*`)
)

func splitRow(s string) []string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(s), "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func mdInline(text string) string {
	escaped := html.EscapeString(text)
	var codes []string
	escaped = mdCode.ReplaceAllStringFunc(escaped, func(m string) string {
		inner := mdCode.FindStringSubmatch(m)[1]
		codes = append(codes, "<code>"+inner+"</code>")
		return "\x1A" + strconv.Itoa(len(codes)-1) + "\x1A"
	})
	escaped = mdStrong.ReplaceAllString(escaped, "<strong>$1</strong>")
	escaped = mdEm.ReplaceAllString(escaped, "<em>$1</em>")
	for idx, c := range codes {
		escaped = strings.ReplaceAll(escaped, "\x1A"+strconv.Itoa(idx)+"\x1A", c)
	}
	return escaped
}
