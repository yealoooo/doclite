package names

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFileAndFolderNames(t *testing.T) {
	for _, ok := range []string{
		"盒 WS-2025-001", "IMG_0231.jpg", ".hidden", "a", "a.b.c", "名字 (1).jpg", strings.Repeat("字", 255),
		"con (1).jpg", "icon.jpg", "COM10.jpg", "COM0", "CONSOLE", "LPT0.txt", ".con", "a.con", "盒 CON", "COM１",
	} {
		if err := ValidateFileName(ok); err != nil {
			t.Errorf("%q 应该合法：%v", ok, err)
		}
		if err := ValidateFolderName(ok); err != nil {
			t.Errorf("%q 应该合法：%v", ok, err)
		}
	}
	for _, bad := range []string{
		"", ".", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", "a<b", "a>b", "a|b", `a"b`,
		"a\nb", "a\tb", "a\x7fb", "a\u0085b", " a", "a ", "　盒", "a.", "a..", strings.Repeat("字", 256), "a\xffb",
	} {
		err := ValidateFileName(bad)
		if err == nil || !strings.HasPrefix(err.Error(), "文件名") {
			t.Errorf("%q 应该不合法，说明以\"文件名\"开头：%v", bad, err)
		}
		if err := ValidateFolderName(bad); err == nil || !strings.HasPrefix(err.Error(), "文件夹名称") {
			t.Errorf("%q 应该不合法，说明以\"文件夹名称\"开头：%v", bad, err)
		}
	}
}

// Windows 保留名的各种写法：取第一个点之前、去掉尾部空格，不分大小写。
func TestWindowsReserved(t *testing.T) {
	for _, bad := range []string{
		"CON", "con", "Con", "con.txt", "CON.jpg", "nul.tar.gz", "Com1.png", "LPT9", "con .jpg", "prn",
		"Aux.pdf", "lpt1.x", "COM9", "NUL  .a.b", "con.", "CON  ",
	} {
		if !IsWindowsReserved(bad) {
			t.Errorf("%q 应该是保留名", bad)
		}
	}
	for _, bad := range []string{"CON", "con.txt", "Com1.png", "con .jpg", "NUL  .a.b"} {
		err := ValidateFileName(bad)
		if err == nil || !strings.Contains(err.Error(), "Windows 保留名") {
			t.Errorf("%q 应该因为保留名不合法：%v", bad, err)
		}
	}
	for _, ok := range []string{
		"con (1).jpg", "icon.jpg", "COM10.jpg", "COM0", "CONSOLE", "LPT0.txt", "LPT10", "COM", "a.con", ".con",
		" con", "con_1.jpg", "盒 CON", "COM１", "ＣＯＮ", "",
	} {
		if IsWindowsReserved(ok) {
			t.Errorf("%q 不应该是保留名", ok)
		}
	}
}

func TestNumberedName(t *testing.T) {
	for name, want := range map[string]string{
		"IMG_0231.jpg": "IMG_0231 (1).jpg",
		"a.tar.gz":     "a.tar (1).gz",
		"盒 001":        "盒 001 (1)",
		".jpg":         ".jpg (1)",
		"a (1).jpg":    "a (1) (1).jpg",
		"con (1).jpg":  "con (1) (1).jpg",
	} {
		if got := NumberedName(name, 1); got != want {
			t.Errorf("NumberedName(%q, 1) = %q，应为 %q", name, got, want)
		}
	}
	// 超长时截短主干，扩展名保留；扩展名太长时整个名字当主干截短。结果都合法、和原名不同、不超过 255 个字符。
	long := strings.Repeat("字", 251) + ".jpg"
	longExt := "a." + strings.Repeat("x", 252)
	if got := NumberedName(long, 12); utf8.RuneCountInString(got) != 255 || !strings.HasSuffix(got, " (12).jpg") {
		t.Errorf("超长名字：%q", got)
	}
	if got := NumberedName(longExt, 1); utf8.RuneCountInString(got) != 255 || !strings.HasSuffix(got, " (1)") {
		t.Errorf("扩展名太长：%q", got)
	}
	for _, name := range []string{"IMG_0231.jpg", ".jpg", "a", long, longExt, strings.Repeat("字", 255)} {
		for _, n := range []int{1, 99, 100000} {
			got := NumberedName(name, n)
			if err := ValidateFileName(got); err != nil || strings.EqualFold(got, name) {
				t.Errorf("NumberedName(%q, %d) = %q：%v", name, n, got, err)
			}
		}
	}
}

func TestProjectCodeAndName(t *testing.T) {
	for in, want := range map[string]string{
		"WS2025": "WS2025", "a": "a", "0.b_c-D": "0.b_c-D", " DEMO-A ": "DEMO-A", strings.Repeat("x", 32): strings.Repeat("x", 32),
	} {
		got, err := NormalizeProjectCode(in)
		if err != nil || got != want {
			t.Errorf("NormalizeProjectCode(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", " ", ".", "..", ".a", "-a", "_a", "a b", "a/b", "项目", "a:b", strings.Repeat("x", 33)} {
		if _, err := NormalizeProjectCode(bad); err == nil {
			t.Errorf("项目编码 %q 应该不合法", bad)
		}
	}

	if got, err := NormalizeProjectName("  2025 文书档案 "); err != nil || got != "2025 文书档案" {
		t.Errorf("项目名称应去首尾空白：%q, %v", got, err)
	}
	if _, err := NormalizeProjectName(strings.Repeat("字", 100)); err != nil {
		t.Errorf("100 个字应该可以：%v", err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("字", 101), "a/b", "名称.", "CON", "aux.txt", "a\tb", ".."} {
		if _, err := NormalizeProjectName(bad); err == nil || !strings.HasPrefix(err.Error(), "项目名称") {
			t.Errorf("项目名称 %q 应该不合法：%v", bad, err)
		}
	}
}

func TestCategoryCode(t *testing.T) {
	seps := []rune{'-', '·'} // 默认模板的分隔符
	for _, ok := range []string{"Z109", "WS", "2025", "Y", "办公室", "a.b_c", strings.Repeat("x", 32)} {
		if err := ValidateCategoryCode(ok, seps); err != nil {
			t.Errorf("编码 %q 应该合法：%v", ok, err)
		}
	}
	for bad, hint := range map[string]string{
		"":                      "1–32",
		strings.Repeat("x", 33): "1–32",
		"a b":                   "空白",
		"a　b":                   "空白",
		"a\x01b":                "控制字符",
		"a{b":                   "花括号",
		"a}":                    "花括号",
		"a/b":                   "不能有 /",
		`a"b`:                   `不能有 "`,
		"a|b":                   "不能有 |",
		"WS-2025":               "分隔符",
		"WS·2025":               "分隔符",
		"\xff":                  "UTF-8",
	} {
		err := ValidateCategoryCode(bad, seps)
		if err == nil || !strings.Contains(err.Error(), hint) {
			t.Errorf("编码 %q 应该不合法（%s）：%v", bad, hint, err)
		}
	}
	// 分隔符换了，规则跟着换。
	if err := ValidateCategoryCode("WS-2025", []rune{'.'}); err != nil {
		t.Errorf("分隔符只有 . 时 - 可以用：%v", err)
	}

	got := CategoryCodeForbiddenChars([]rune{'·', '-', '-'})
	want := []string{`"`, "*", "-", "/", ":", "<", ">", "?", `\`, "{", "|", "}", "·"}
	if !slices.Equal(got, want) {
		t.Errorf("CategoryCodeForbiddenChars = %q，应为 %q", got, want)
	}
}
