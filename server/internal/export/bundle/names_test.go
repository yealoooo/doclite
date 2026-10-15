package bundle

import (
	"slices"
	"testing"
)

func TestNames(t *testing.T) {
	safe := []struct{ in, want string }{
		{"A-2025-0001", "A-2025-0001"},
		{"档号·2025-永久-0001", "档号·2025-永久-0001"},
		{`A/B\C:D*E?F"G<H>I|J`, "A_B_C_D_E_F_G_H_I_J"},
		{"A\tB\x01", "A_B_"},
		{"CON", "CON_"},
		{"con.2025", "con_.2025"},
		{"Com1.a.b", "Com1_.a.b"},
		{"LPT9", "LPT9_"},
		{"COM10", "COM10"},
		{"CONSOLE", "CONSOLE"},
		{"abc.", "abc._"},
		{"abc ", "abc _"},
		{"NUL.", "NUL_._"},
	}
	for _, c := range safe {
		if got := SafeName(c.in); got != c.want {
			t.Errorf("SafeName(%q) = %q，应该是 %q", c.in, got, c.want)
		}
	}

	dup := []struct {
		format Format
		in     []string
		want   []string
	}{
		// 不区分大小写重名加 (2)、(3)；和目录、清单撞名也加。
		{FormatImages, []string{"A", "a", "A", "目录.xlsx", "清单.CSV"}, []string{"A", "a (2)", "A (3)", "目录.xlsx (2)", "清单.CSV (2)"}},
		// 加完编号再查一遍。
		{FormatImages, []string{"A", "A (2)", "A"}, []string{"A", "A (2)", "A (3)"}},
		// PDF 的名字带 .pdf，`目录.xlsx.pdf` 不和 `目录.xlsx` 撞。
		{FormatPDF, []string{"目录.xlsx", "x", "X"}, []string{"目录.xlsx", "x", "X (2)"}},
		// 换过字符之后才重名的也算。
		{FormatPDF, []string{"A/1", "A:1"}, []string{"A_1", "A_1 (2)"}},
	}
	for _, c := range dup {
		var items []Item
		for _, n := range c.in {
			items = append(items, Item{ArchiveNo: n})
		}
		if got := ItemNames(items, c.format); !slices.Equal(got, c.want) {
			t.Errorf("ItemNames(%q, %s) = %q，应该是 %q", c.in, c.format, got, c.want)
		}
	}
}
