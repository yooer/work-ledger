package xlsx

import "testing"

func TestRoundTrip(t *testing.T) {
	data, err := Write([]string{"工作分类", "工作内容", "次数"}, [][]any{
		{"集客业务", "开通专线 <A&B>\n第二行", int64(2)},
		{"日常维护", "", 1},
	}, "工作记录")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1][1] != "开通专线 <A&B>\n第二行" || rows[1][2] != "2" || rows[2][0] != "日常维护" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestColumns(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 701: "ZZ", 702: "AAA"} {
		if ColumnName(i) != want || ColumnIndex(want+"12") != i {
			t.Errorf("column %d", i)
		}
	}
}
