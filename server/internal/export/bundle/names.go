package bundle

import (
	"fmt"
	"strings"
	"unicode"

	"doclite/server/internal/platform/names"
)

// SafeName 把档号变成 Windows 上能用的文件名、文件夹名（requirements 5.11）：
//   - Windows 不允许的字符（`\ / : * ? " < > |`）和控制字符换成 `_`（兜底，档号模板本来就不允许）；
//   - Windows 保留名（按第一个点之前的部分判断，见 names.IsWindowsReserved）在这一部分后面加 `_`：
//     `CON` → `CON_`、`con.2025` → `con_.2025`（只加在末尾的话 `con.2025_` 仍是保留名）；
//   - 结尾是点或空格时末尾加 `_`。
//
// 不处理重名，重名见 [ItemNames]。
func SafeName(archiveNo string) string {
	var b strings.Builder
	for _, r := range archiveNo {
		if strings.ContainsRune(names.ForbiddenChars, r) || unicode.IsControl(r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	s := b.String()
	if names.IsWindowsReserved(s) {
		stem, rest, hasDot := strings.Cut(s, ".")
		s = stem + "_"
		if hasDot {
			s += "." + rest
		}
	}
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		s += "_"
	}
	return s
}

// ItemNames 是每件在 zip 里的名字（PDF 再加 `.pdf`，原图包是文件夹名）：[SafeName]，和前面的件重名（不区分大小写）
// 或和 `目录.xlsx`、`清单.csv` 重名时加 ` (2)`、` (3)`……（加完再查一遍，直到不重）。
func ItemNames(items []Item, format Format) []string {
	ext := ""
	if format == FormatPDF {
		ext = ".pdf"
	}
	taken := map[string]bool{foldName(catalogName): true, foldName(manifestName): true}
	out := make([]string, len(items))
	for i := range items {
		base := SafeName(items[i].ArchiveNo)
		name := base
		for n := 2; taken[foldName(name+ext)]; n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		taken[foldName(name+ext)] = true
		out[i] = name
	}
	return out
}

// foldName 是不区分大小写比较用的键（Windows 按 Unicode 大写比较）。
func foldName(s string) string { return strings.ToUpper(s) }
