// Package names 是名称和编码的规则（纯函数，不碰数据库）：
//
//   - 文件名、文件夹名（requirements 5.5）：[ValidateFileName]、[ValidateFolderName]，含 Windows 保留名
//     （[IsWindowsReserved]）；重名时自动改的名字 [NumberedName]；
//   - 项目编码、项目名称（5.3）：[NormalizeProjectCode]、[NormalizeProjectName]；
//   - 归类节点编码（5.9）：[ValidateCategoryCode]、[CategoryCodeForbiddenChars]，分隔符来自档号模板
//     （archiveno.Template.Separators）。
//
// 不合规时返回的 error 的 Error() 是给用户看的中文说明；server 一律按 400 invalid_input 返回。
// 长度都按 Unicode 码点数算。
package names

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxNameChars         = 255 // 文件名、文件夹名
	MaxProjectCodeChars  = 32
	MaxProjectNameChars  = 100
	MaxCategoryCodeChars = 32
)

// ForbiddenChars 是 Windows 文件名不允许的字符。文件名、文件夹名、项目名称、归类编码、档号模板的文字部分都不能有
// （档号可能用作导出时的目录名、文件名）。另外控制字符也都不能有。
const ForbiddenChars = `\/:*?"<>|`

// WindowsReservedHint 是保留名的说明（不带"文件名"这类前缀）。
const WindowsReservedHint = "不能是 Windows 保留名（CON、PRN、AUX、NUL、COM1–COM9、LPT1–LPT9，不区分大小写，带扩展名也不行）"

// IsWindowsReserved：取第一个点之前的部分（没有点就是整个名字），去掉尾部空格后，不区分大小写等于
// CON、PRN、AUX、NUL、COM1–COM9、LPT1–LPT9（用户决定 2026-09-28）。
//
// `con.txt`、`CON.jpg`、`nul.tar.gz`、`con .jpg` 是；`con (1).jpg`、`icon.jpg`、`COM10.jpg`、`COM0`、
// `CONSOLE`、` con` 不是。
func IsWindowsReserved(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	stem = asciiUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	// COM1–COM9、LPT1–LPT9：3 个字母加 1 位 1–9 的 ASCII 数字。
	if len(stem) != 4 || stem[3] < '1' || stem[3] > '9' {
		return false
	}
	return stem[:3] == "COM" || stem[:3] == "LPT"
}

// asciiUpper 只把 a–z 变成大写（不按 Unicode 规则，免得别的字母变成 ASCII 字母）。
func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// ValidateFileName 校验文件名（5.5，用户决定 2026-09-27）：
//   - 1–255 个字符；
//   - 不能含 `\ / : * ? < > |`、双引号和控制字符；
//   - 首尾不能是空白（不自动去掉，原样拒绝）；
//   - 不能以点结尾，不能是 `.` 或 `..`；
//   - 不能是 Windows 保留名（[IsWindowsReserved]）。
func ValidateFileName(name string) error { return validateName(name, "文件名", MaxNameChars) }

// ValidateFolderName 校验文件夹名，规则同 [ValidateFileName]。
func ValidateFolderName(name string) error {
	return validateName(name, "文件夹名称", MaxNameChars)
}

func validateName(name, label string, maxChars int) error {
	invalid := func(format string, args ...any) error {
		return errors.New(label + fmt.Sprintf(format, args...))
	}
	if !utf8.ValidString(name) {
		return invalid("不是有效的 UTF-8 文本")
	}
	n := utf8.RuneCountInString(name)
	if n == 0 {
		return invalid("不能为空")
	}
	if n > maxChars {
		return invalid("最长 %d 个字符", maxChars)
	}
	if name == "." || name == ".." {
		return invalid("不能是 . 或 ..")
	}
	if i := strings.IndexAny(name, ForbiddenChars); i >= 0 {
		return invalid("不能包含 %c（不能用的字符：\\ / : * ? < > | 和双引号）", name[i])
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return invalid("不能包含控制字符（换行、制表符等）")
	}
	first, _ := utf8.DecodeRuneInString(name)
	last, _ := utf8.DecodeLastRuneInString(name)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return invalid("开头和结尾不能是空白")
	}
	if last == '.' {
		return invalid("不能以点结尾")
	}
	if IsWindowsReserved(name) {
		return invalid("%s", WindowsReservedHint)
	}
	return nil
}

// NumberedName 是同名时自动改的名字 `名字 (n).扩展名`（5.5），例如 `IMG_0231.jpg` 的第 1 个是 `IMG_0231 (1).jpg`。
//   - 扩展名是最后一个点及其后面的部分；点在开头（`.jpg`）或没有点时，整个名字当作主干，编号加在末尾。
//   - 结果超过 255 个字符时截短主干；扩展名太长、截短后主干为空时，整个名字当作主干截短。
//   - name 合法（[ValidateFileName]）时，结果也一定合法。
func NumberedName(name string, n int) string {
	suffix := fmt.Sprintf(" (%d)", n)
	suffixChars := utf8.RuneCountInString(suffix)
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		stem, ext := name[:i], name[i:]
		if budget := MaxNameChars - suffixChars - utf8.RuneCountInString(ext); budget >= 1 {
			return truncate(stem, budget) + suffix + ext
		}
	}
	return truncate(name, MaxNameChars-suffixChars) + suffix
}

// truncate 截到前 n 个字符。
func truncate(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// NormalizeProjectCode 校验项目编码（5.3，用户决定 2026-09-27），返回去掉首尾空白后的编码：
// `^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`（所以也不会是 `.`、`..`），区分大小写。编码进 S3 key，建好后不能改。
func NormalizeProjectCode(code string) (string, error) {
	code = strings.TrimSpace(code)
	ok := len(code) >= 1 && len(code) <= MaxProjectCodeChars && isASCIIAlnum(code[0])
	for i := 1; ok && i < len(code); i++ {
		c := code[i]
		ok = isASCIIAlnum(c) || c == '.' || c == '_' || c == '-'
	}
	if !ok {
		return "", fmt.Errorf("项目编码要以英文字母或数字开头，只能包含英文字母、数字和 . _ -，最长 %d 个字符", MaxProjectCodeChars)
	}
	return code, nil
}

func isASCIIAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// NormalizeProjectName 校验项目名称（5.3，用户决定 2026-10-07），返回去掉首尾空白后的名称：1–100 个字符，
// 其余按文件夹名的规则（它也是根文件夹的名称）。
func NormalizeProjectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxProjectNameChars {
		return "", fmt.Errorf("项目名称不能为空，最长 %d 个字符", MaxProjectNameChars)
	}
	if err := validateName(name, "项目名称", MaxProjectNameChars); err != nil {
		return "", err
	}
	return name, nil
}

// ValidateCategoryCode 校验归类节点的编码（5.9，用户决定 2026-10-14）：1–32 个字符，不能有空白、控制字符、
// 花括号、`\ / : * ? " < > |`，也不能有当前档号模板的分隔符 separators（模板解析不了时用
// archiveno.FallbackSeparators）。不去空白：有空白就拒绝。只在新建和改编码时校验。
func ValidateCategoryCode(code string, separators []rune) error {
	if !utf8.ValidString(code) {
		return errors.New("归类编码不是有效的 UTF-8 文本")
	}
	if n := utf8.RuneCountInString(code); n == 0 || n > MaxCategoryCodeChars {
		return fmt.Errorf("归类编码要有 1–%d 个字符", MaxCategoryCodeChars)
	}
	for _, c := range code {
		switch {
		case unicode.IsSpace(c) || unicode.IsControl(c):
			return errors.New("归类编码不能有空白或控制字符")
		case c == '{' || c == '}' || strings.ContainsRune(ForbiddenChars, c):
			return fmt.Errorf("归类编码不能有 %c（不能有花括号和 \\ / : * ? \" < > |，档号可能用作导出时的目录名、文件名）", c)
		case slices.Contains(separators, c):
			return fmt.Errorf("归类编码不能有 %c：它是档号模板里的分隔符（现在的分隔符：%s）", c, joinRunes(sortedUnique(separators), " "))
		}
	}
	return nil
}

// CategoryCodeForbiddenChars 是归类编码里不能有的可见字符（空白和控制字符另外不能有），按码点排序、去重：
// 模板的分隔符、`{` `}`、`\ / : * ? " < > |`。给界面提示用（catalog-settings 的 category_code_forbidden_chars）。
func CategoryCodeForbiddenChars(separators []rune) []string {
	all := append(slices.Clone(separators), '{', '}')
	all = append(all, []rune(ForbiddenChars)...)
	out := []string{}
	for _, c := range sortedUnique(all) {
		out = append(out, string(c))
	}
	return out
}

func sortedUnique(rs []rune) []rune {
	rs = slices.Clone(rs)
	slices.Sort(rs)
	return slices.Compact(rs)
}

func joinRunes(rs []rune, sep string) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = string(r)
	}
	return strings.Join(parts, sep)
}

// HasControlChars：有控制字符（换行、回车、制表符不算）。
func HasControlChars(s string) bool {
	return strings.IndexFunc(s, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' }) >= 0
}

// OptionalText 规整可选的文本（备注、说明）：nil 是没传；去掉首尾空白后为空当作没有（nil）；最长 max 个字符；不能有
// 控制字符（换行、回车、制表符可以）。label 是给用户看的名称（"备注"）。
func OptionalText(v *string, max int, label string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*v)
	switch {
	case t == "":
		return nil, nil
	case utf8.RuneCountInString(t) > max:
		return nil, fmt.Errorf("%s最长 %d 个字符", label, max)
	case HasControlChars(t):
		return nil, fmt.Errorf("%s不能包含控制字符", label)
	}
	return &t, nil
}

// SameOptional 判断两个可选的文本是否相同（都没有也算相同）。
func SameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
