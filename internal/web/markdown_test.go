package web

import (
	"strings"
	"testing"
)

func TestMarkdownEscapesAndRenders(t *testing.T) {
	html := Markdown("## 标题\n\n- **重点** `code`\n- <script>\n\n| 指标 | 值 |\n|---|---|\n| 记录 | 6 |\n\n普通段落")
	for _, want := range []string{"<h2>标题</h2>", "<strong>重点</strong>", "<code>code</code>", "&lt;script&gt;", "<td>6</td>", "<p>普通段落</p>"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %s", want, html)
		}
	}
	if strings.Contains(html, "<script>") {
		t.Fatal("unescaped script")
	}
}
