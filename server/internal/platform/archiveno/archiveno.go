// Package archiveno 是档号模板的解析和渲染（requirements 5.9"档号和件号"，用户决定 2026-10-14 Q2），纯函数。
//
// 模板（项目设置 `archive_no.template`，默认 [DefaultTemplate]）由文字和占位符组成：
//   - 节点占位符 `{fonds}` `{category}` `{year}` `{retention}` `{org}` `{custom}`：取祖先链上离条目最近的
//     同类型节点的编码（category_code）。没有 `{root}`：根节点的编码是项目编码，不进档号。
//   - 件号 `{item_no}`（不补零）或 `{item_no:0N}`（左边补 0 到 N 位，N 是 1–10、不带前导 0，即 `:01`–`:09`、
//     `:010`；位数比 N 多时照常全部输出）。
//
// [Parse] 的规则：`{item_no}` 正好一次；节点占位符各最多一次；相邻两个占位符之间要有分隔符（中间的文字里至少一个
// 不是字母、数字的字符）；文字部分不能有空白、控制字符、`\ / : * ? " < > |`、单独的花括号（不支持转义）；
// 不认识的占位符、给节点占位符带格式都不行；整个模板 1–200 个字符。
//
// 分隔符（[Template.Separators]）是文字部分里所有不是字母、数字的字符，归类节点的编码不能含它们
// （names.ValidateCategoryCode）。
package archiveno

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultTemplate 是默认的档号模板。
	DefaultTemplate = "{fonds}-{category}·{year}-{retention}-{org}-{item_no:04}"
	// MaxTemplateChars 是模板的最大字符数。
	MaxTemplateChars = 200
	// MaxItemNoWidth 是 `{item_no:0N}` 的 N 的上限（件号最大 999999999）。
	MaxItemNoWidth = 10
)

// forbiddenChars 是 Windows 文件名不允许的字符，档号模板的文字部分不能有（档号可能用作导出时的目录名、文件名）。
// 和 names.ForbiddenChars 是同一组字符；这个包只用标准库，所以自己写一份。
const forbiddenChars = `\/:*?"<>|`

// FallbackSeparators：当前模板解析不了时，归类编码按这些字符当分隔符（不能含）。
var FallbackSeparators = []rune{'-', '.', '_', '·'}

// CategoryType 是归类节点的类型（archive_categories.category_type）。
type CategoryType string

const (
	Root      CategoryType = "root" // 根节点：建项目时建，编码是项目编码，不进档号
	Fonds     CategoryType = "fonds"
	Category  CategoryType = "category"
	Year      CategoryType = "year"
	Retention CategoryType = "retention"
	Org       CategoryType = "org"
	Custom    CategoryType = "custom"
)

// AssignableTypes 是能新建、能改成的类型（除 root 以外的全部），也是模板里能用的节点占位符。
var AssignableTypes = []CategoryType{Fonds, Category, Year, Retention, Org, Custom}

// Label 是类型的中文名（写进给人看的说明）。
func (t CategoryType) Label() string {
	switch t {
	case Root:
		return "根节点"
	case Fonds:
		return "全宗"
	case Category:
		return "门类"
	case Year:
		return "年度"
	case Retention:
		return "保管期限"
	case Org:
		return "机构"
	case Custom:
		return "自定义"
	}
	return string(t)
}

// Node 是祖先链上的一个节点。
type Node struct {
	Type CategoryType
	Code string
}

// segment 是模板的一段：文字、节点占位符或件号。
type segment struct {
	literal string       // 文字（typ 为空、itemNo 为 false 时）
	typ     CategoryType // 节点占位符的类型
	itemNo  bool         // 是件号占位符
	width   int          // 件号补零的位数，0 表示不补零
}

func (s segment) isPlaceholder() bool { return s.typ != "" || s.itemNo }

func (s segment) String() string {
	switch {
	case s.typ != "":
		return "{" + string(s.typ) + "}"
	case s.itemNo && s.width > 0:
		return fmt.Sprintf("{item_no:0%d}", s.width)
	case s.itemNo:
		return "{item_no}"
	}
	return s.literal
}

// Template 是解析好的档号模板。
type Template struct {
	source   string
	segments []segment
}

func isSeparator(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsNumber(c) }

func invalidf(format string, args ...any) error {
	return errors.New("档号模板不对：" + fmt.Sprintf(format, args...))
}

// debugQuote 引用模板片段：和 Rust 的 `format!("{s:?}")`（str 的 Debug）一样加双引号、转义 `\0 \t \r \n \\ \"`，
// 其他控制字符写成 `\u{hex}`。前端（web/src/lib/catalog.ts 的 rustDebugStr）照这个写法在本地预览同样的说明，
// 两边要一字不差。
func debugQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case 0:
			b.WriteString(`\0`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\n':
			b.WriteString(`\n`)
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			if unicode.IsControl(r) {
				b.WriteString(`\u{` + strconv.FormatInt(int64(r), 16) + `}`)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Parse 解析并校验模板，规则见包说明。不合规则返回的 error 写明是哪一条（server 按 400 invalid_input）。
func Parse(source string) (*Template, error) {
	if !utf8.ValidString(source) {
		return nil, invalidf("不是有效的 UTF-8 文本")
	}
	if n := utf8.RuneCountInString(source); n == 0 || n > MaxTemplateChars {
		return nil, invalidf("不能为空，最长 %d 个字符", MaxTemplateChars)
	}
	var segs []segment
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			segs = append(segs, segment{literal: lit.String()})
			lit.Reset()
		}
	}
	for rest := source; rest != ""; {
		c, size := utf8.DecodeRuneInString(rest)
		switch {
		case c == '{':
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return nil, invalidf("%s 里的 { 没有对应的 }", debugQuote(rest))
			}
			if strings.ContainsRune(rest[1:end], '{') {
				return nil, invalidf("%s 里的 { 没有对应的 }", debugQuote(rest[:end+1]))
			}
			seg, err := parsePlaceholder(rest[1:end])
			if err != nil {
				return nil, err
			}
			flush()
			segs = append(segs, seg)
			rest = rest[end+1:]
			continue
		case c == '}':
			return nil, invalidf("有多出来的 }（不支持转义，文字里不能有花括号）")
		case unicode.IsSpace(c) || unicode.IsControl(c):
			return nil, invalidf("文字部分不能有空白或控制字符")
		case strings.ContainsRune(forbiddenChars, c):
			return nil, invalidf("文字部分不能有 %c（档号可能用作导出时的目录名、文件名，不能有 \\ / : * ? \" < > |）", c)
		}
		lit.WriteRune(c)
		rest = rest[size:]
	}
	flush()

	itemNos := 0
	seen := map[CategoryType]bool{}
	for _, s := range segs {
		if s.itemNo {
			itemNos++
		}
		if s.typ != "" {
			if seen[s.typ] {
				return nil, invalidf("%s 出现了不止一次", s)
			}
			seen[s.typ] = true
		}
	}
	if itemNos != 1 {
		return nil, invalidf("{item_no} 要正好出现一次（现在 %d 次），否则同一个节点里的条目档号都一样", itemNos)
	}
	// 相邻两个占位符之间要有分隔符（连续的文字已经并成一段，两个占位符之间最多一段文字）。
	prev := -1
	for i, s := range segs {
		if !s.isPlaceholder() {
			continue
		}
		if prev >= 0 {
			ok := i == prev+2 && strings.IndexFunc(segs[prev+1].literal, isSeparator) >= 0
			if !ok {
				return nil, invalidf("%s 和 %s 之间要有分隔符（至少一个不是字母、数字的字符，例如 - 或 ·）", segs[prev], s)
			}
		}
		prev = i
	}
	return &Template{source: source, segments: segs}, nil
}

// parsePlaceholder 解析花括号里的内容（不含花括号）。
func parsePlaceholder(inner string) (segment, error) {
	name, spec, hasSpec := strings.Cut(inner, ":")
	if name == "item_no" {
		if !hasSpec {
			return segment{itemNo: true}, nil
		}
		// 只接受 0 后面跟 N（1–10，不带前导 0）：04、010；004、0010、00、4 都不行。
		digits, ok := strings.CutPrefix(spec, "0")
		width, err := strconv.Atoi(digits)
		if !ok || err != nil || digits == "" || digits[0] < '1' || digits[0] > '9' || width > MaxItemNoWidth {
			return segment{}, invalidf("{%s} 的格式不对：补零写成 :0 加位数（1–%d），例如 {item_no:04}", inner, MaxItemNoWidth)
		}
		return segment{itemNo: true, width: width}, nil
	}
	for _, t := range AssignableTypes {
		if string(t) == name {
			if hasSpec {
				return segment{}, invalidf("{%s}：只有 {item_no} 能带格式，节点占位符直接写 {%s}", inner, name)
			}
			return segment{typ: t}, nil
		}
	}
	return segment{}, invalidf("不认识的占位符 {%s}，能用的有 {fonds}、{category}、{year}、{retention}、{org}、{custom}、{item_no}、{item_no:04}", inner)
}

// Source 是模板原文。
func (t *Template) Source() string { return t.source }

// Levels 是模板用到的节点类型，按模板里的顺序（catalog-settings 的 archive_no_levels）。
func (t *Template) Levels() []CategoryType {
	var out []CategoryType
	for _, s := range t.segments {
		if s.typ != "" {
			out = append(out, s.typ)
		}
	}
	return out
}

// ItemNoWidth 是件号补零的位数，0 表示不补零。
func (t *Template) ItemNoWidth() int {
	for _, s := range t.segments {
		if s.itemNo {
			return s.width
		}
	}
	return 0
}

// Separators 是文字部分里所有不是字母、数字的字符，按码点排序、去重。归类节点的编码不能含它们。
func (t *Template) Separators() []rune {
	var out []rune
	for _, s := range t.segments {
		for _, c := range s.literal {
			if isSeparator(c) {
				out = append(out, c)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Render 按祖先链和件号生成档号。chain 是从根到条目所在节点（含它自己）的节点，顺序从根往下。
//
// 每个节点占位符取链上离条目最近（最靠后）的同类型节点的编码。链上缺模板用到的类型时，archiveNo 为空，
// missing 列出缺的类型（按模板里的顺序）：档号留空，条目能建不能锁定。
func (t *Template) Render(chain []Node, itemNo int) (archiveNo string, missing []CategoryType) {
	var b strings.Builder
	for _, s := range t.segments {
		switch {
		case s.typ != "":
			code, ok := NearestCode(chain, s.typ)
			if !ok {
				missing = append(missing, s.typ)
			}
			b.WriteString(code)
		case s.itemNo:
			fmt.Fprintf(&b, "%0*d", s.width, itemNo)
		default:
			b.WriteString(s.literal)
		}
	}
	if len(missing) > 0 {
		return "", missing
	}
	return b.String(), nil
}

// NearestCode 是链上离条目最近（最靠后）的、类型为 typ 的节点的编码。
func NearestCode(chain []Node, typ CategoryType) (string, bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Type == typ {
			return chain[i].Code, true
		}
	}
	return "", false
}
