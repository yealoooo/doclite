package archiveno

import (
	"slices"
	"strings"
	"testing"
)

// 根 → 全宗 → 门类 → 年度 → 保管期限 → 机构。
var fullChain = []Node{
	{Root, "WS2025"}, {Fonds, "Z109"}, {Category, "WS"}, {Year, "2025"}, {Retention, "Y"}, {Org, "BGS"},
}

func mustParse(t *testing.T, s string) *Template {
	t.Helper()
	tmpl, err := Parse(s)
	if err != nil {
		t.Fatalf("%q 应该合法：%v", s, err)
	}
	return tmpl
}

func TestDefaultTemplate(t *testing.T) {
	tmpl := mustParse(t, DefaultTemplate)
	if tmpl.Source() != DefaultTemplate {
		t.Errorf("Source = %q", tmpl.Source())
	}
	if got := tmpl.Levels(); !slices.Equal(got, []CategoryType{Fonds, Category, Year, Retention, Org}) {
		t.Errorf("Levels = %v", got)
	}
	if tmpl.ItemNoWidth() != 4 {
		t.Errorf("ItemNoWidth = %d", tmpl.ItemNoWidth())
	}
	if got := tmpl.Separators(); !slices.Equal(got, []rune{'-', '·'}) {
		t.Errorf("Separators = %q", got)
	}
	if no, missing := tmpl.Render(fullChain, 12); no != "Z109-WS·2025-Y-BGS-0012" || missing != nil {
		t.Errorf("Render = %q, %v", no, missing)
	}
}

func TestRender(t *testing.T) {
	chainWithCustom := append(slices.Clone(fullChain), Node{Custom, "J01"})
	// 全宗 → 机构 A → 全宗 F2 → 自定义 → 机构 B：取离条目最近的同类型节点。
	nested := []Node{{Root, "P"}, {Fonds, "F1"}, {Org, "A"}, {Fonds, "F2"}, {Custom, "X"}, {Org, "B"}}
	cases := []struct {
		name     string
		template string
		chain    []Node
		itemNo   int
		want     string
		missing  []CategoryType
	}{
		{"全部占位符", "DA{fonds}.{category}.{year}.{retention}.{org}.{custom}#{item_no}号", chainWithCustom, 7, "DAZ109.WS.2025.Y.BGS.J01#7号", nil},
		{"不补零", "{org}-{item_no}", fullChain, 7, "BGS-7", nil},
		{"补 1 位", "{org}-{item_no:01}", fullChain, 7, "BGS-7", nil},
		{"补 4 位", "{org}-{item_no:04}", fullChain, 7, "BGS-0007", nil},
		{"位数超过 N 不截断", "{org}-{item_no:04}", fullChain, 123456, "BGS-123456", nil},
		{"补 10 位", "{org}-{item_no:010}", fullChain, 42, "BGS-0000000042", nil},
		{"只有件号", "{item_no:03}", nil, 5, "005", nil},
		{"最近的同类型节点", "{fonds}-{org}-{item_no:02}", nested, 1, "F2-B-01", nil},
		{"条目在中间那层", "{fonds}-{org}-{item_no:02}", nested[:3], 1, "F1-A-01", nil},
		{"链的顺序只看最近", "{custom}-{item_no}", []Node{{Custom, "C"}}, 1, "C-1", nil},
		{"模板没用到的类型缺了没关系", "{fonds}-{item_no}", fullChain[:2], 3, "Z109-3", nil},
		// 缺级：档号留空，列出缺的类型（模板顺序）。
		{"缺保管期限", DefaultTemplate, []Node{{Root, "P"}, {Fonds, "Z109"}, {Category, "WS"}, {Year, "2025"}, {Org, "BGS"}}, 1, "", []CategoryType{Retention}},
		{"只有根", DefaultTemplate, []Node{{Root, "P"}}, 1, "", []CategoryType{Fonds, Category, Year, Retention, Org}},
		{"根的编码不进档号", "{fonds}-{item_no}", []Node{{Root, "P"}}, 1, "", []CategoryType{Fonds}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			no, missing := mustParse(t, c.template).Render(c.chain, c.itemNo)
			if no != c.want || !slices.Equal(missing, c.missing) {
				t.Errorf("Render = %q, %v；应为 %q, %v", no, missing, c.want, c.missing)
			}
		})
	}
}

func TestParseValid(t *testing.T) {
	for _, ok := range []string{
		"{item_no}", "DA{fonds}-{item_no}A", "{fonds}-X-{item_no}", "{fonds}·{item_no:09}",
		"全宗{fonds}（{year}）第{item_no}件",
	} {
		mustParse(t, ok)
	}
	tmpl := mustParse(t, "DA{fonds}.{custom}#{item_no}号")
	// 文字里的字母、数字和汉字不算分隔符。
	if got := tmpl.Separators(); !slices.Equal(got, []rune{'#', '.'}) {
		t.Errorf("Separators = %q", got)
	}
	if got := mustParse(t, "全宗{fonds}（{year}）第{item_no}件").Separators(); !slices.Equal(got, []rune{'（', '）'}) {
		t.Errorf("全角括号是分隔符：%q", got)
	}
}

func TestParseInvalid(t *testing.T) {
	for bad, hint := range map[string]string{
		"{fonds}-{foo}-{item_no}":        "不认识的占位符 {foo}",
		"{root}-{item_no}":               "不认识的占位符 {root}",
		"{Fonds}-{item_no}":              "不认识的占位符 {Fonds}",
		"{ fonds}-{item_no}":             "不认识的占位符",
		"{}-{item_no}":                   "不认识的占位符 {}",
		"{year:04}-{item_no}":            "只有 {item_no} 能带格式",
		"{fonds}-{item_no:4}":            "补零写成 :0 加位数",
		"{fonds}-{item_no:00}":           "补零写成",
		"{fonds}-{item_no:011}":          "补零写成",
		"{fonds}-{item_no:001}":          "补零写成",
		"{fonds}-{item_no:0010}":         "补零写成",
		"{fonds}-{item_no:0x}":           "补零写成",
		"{fonds}-{item_no:}":             "补零写成",
		"{fonds}-{item_no:0+4}":          "补零写成",
		"{fonds-{item_no}":               "没有对应的 }",
		"{fonds}-{item_no":               "没有对应的 }",
		"{fonds}}-{item_no}":             "多出来的 }",
		"{fonds}-0001":                   "{item_no} 要正好出现一次（现在 0 次）",
		"{fonds}-{item_no}-{item_no:02}": "{item_no} 要正好出现一次（现在 2 次）",
		"{fonds}-{fonds}-{item_no}":      "{fonds} 出现了不止一次",
		"{fonds}{item_no}":               "之间要有分隔符",
		"{fonds}X{item_no}":              "之间要有分隔符",
		"{fonds}文{item_no}":              "之间要有分隔符",
		"{fonds} {item_no}":              "空白",
		"{fonds}-{item_no}\n":            "空白",
		"{fonds}\x01{item_no}":           "控制字符",
		"{fonds}/{item_no}":              "不能有 /",
		"{fonds}:{item_no}":              "不能有 :",
		`{fonds}"{item_no}`:              `不能有 "`,
		"":                               "不能为空",
		"{fonds}-{item_no}" + strings.Repeat("-", MaxTemplateChars): "最长 200",
		"\xff{item_no}": "UTF-8",
	} {
		_, err := Parse(bad)
		if err == nil || !strings.Contains(err.Error(), hint) || !strings.HasPrefix(err.Error(), "档号模板不对：") {
			t.Errorf("%q 应该不合法（%s）：%v", bad, hint, err)
		}
	}
}

// TestUnclosedBraceQuote：没闭合的 { 引用片段时加引号、转义（和前端 rustDebugStr 一字不差）。
func TestUnclosedBraceQuote(t *testing.T) {
	for tpl, want := range map[string]string{
		"{fonds-{item_no}":   `"{fonds-{item_no}" 里的 { 没有对应的 }`,
		"{fonds\t-{item_no":  `"{fonds\t-{item_no" 里的 { 没有对应的 }`,
		`{fo"n\ds-{item_no}`: `"{fo\"n\\ds-{item_no}" 里的 { 没有对应的 }`,
		"{a\x01b":            `"{a\u{1}b" 里的 { 没有对应的 }`,
	} {
		_, err := Parse(tpl)
		if err == nil || err.Error() != "档号模板不对："+want {
			t.Errorf("%q：%v，期望 %s", tpl, err, want)
		}
	}
}

func TestCategoryTypes(t *testing.T) {
	if slices.Contains(AssignableTypes, Root) || len(AssignableTypes) != 6 {
		t.Errorf("AssignableTypes = %v", AssignableTypes)
	}
	for _, typ := range append([]CategoryType{Root}, AssignableTypes...) {
		if typ.Label() == string(typ) {
			t.Errorf("%s 没有中文名", typ)
		}
	}
	if !slices.IsSorted(FallbackSeparators) {
		t.Errorf("FallbackSeparators 应按码点排序：%q", FallbackSeparators)
	}
}
