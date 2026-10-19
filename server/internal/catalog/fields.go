package catalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// 著录（requirements 5.9"条目"的著录，用户决定 Q1）：
//   - 题名是固定列（≤ 1000 字，不能有任何控制字符，含换行），校验错误里的 key 是 `title`；其他著录项存在
//     extra_fields（key → 字符串）；
//   - 提交的值去掉首尾空白，null 或空串是清空（删掉这个 key）；PATCH 的 extra_fields 按 key 合并；
//   - 草稿只查格式：text ≤ max_length、不能有控制字符（换行、回车、制表符可以），date 是 YYYY / YYYY-MM /
//     YYYY-MM-DD 的真实日期，enum 在选项里；值不是字符串的 400；
//   - 旧值规则：提交的值和库里存的一样（规整之后）就不校验、原样保留；设置里没有的 key 只能清空或原样提交；
//   - 必填（含题名）在锁定时查（[completionIssues]，2026-10-19 去掉"已完成"），现有的值也要合当前设置；
//   - 校验不过 400 `invalid_input`，`details.fields [{key, message}]` 一次列全（题名、设置里的顺序、设置里没有的
//     按 key 排在最后），message 是"著录内容不对："加前 3 条。

// MaxTitleChars 是题名的最大字符数。
const MaxTitleChars = 1000

// FieldIssue 是一个著录问题（`field_issues`、400 的 `details.fields`）。
type FieldIssue struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// fieldsError 是著录校验不过的 400。
func fieldsError(issues []FieldIssue) error {
	msgs := make([]string, 0, 3)
	for i, is := range issues {
		if i == 3 {
			break
		}
		msgs = append(msgs, is.Message)
	}
	more := ""
	if len(issues) > 3 {
		more = fmt.Sprintf("（共 %d 处）", len(issues))
	}
	return httpx.Invalid("著录内容不对：%s%s", strings.Join(msgs, "；"), more).
		WithDetails(map[string]any{"fields": issues})
}

// checkTitle：最多 1000 个字符，不能有控制字符（含换行）。合格时返回空串。
func checkTitle(v string) string {
	if utf8.RuneCountInString(v) > MaxTitleChars {
		return fmt.Sprintf("题名最长 %d 个字符", MaxTitleChars)
	}
	if strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return "题名不能有控制字符（含换行）"
	}
	return ""
}

// checkValue：值是否合著录项现在的定义。合格时返回空串，否则返回原因（不带著录项名称）。
func checkValue(def *project.FieldDef, v string) string {
	switch def.Type {
	case project.FieldText:
		ml := project.DefaultTextMaxLength
		if def.MaxLength != nil {
			ml = *def.MaxLength
		}
		return project.CheckTextValue(v, ml)
	case project.FieldDate:
		if !project.IsValidDateValue(v) {
			return fmt.Sprintf("不是合法的日期（只能是 %s，日期要真实存在）", strings.Join(project.DateFormats, "、"))
		}
	case project.FieldEnum:
		if !slices.Contains(def.Options, v) {
			shown := def.Options
			more := ""
			if len(shown) > 10 {
				shown, more = shown[:10], fmt.Sprintf(" 等 %d 项", len(def.Options))
			}
			return fmt.Sprintf("只能从选项里选（%s%s）", strings.Join(shown, "、"), more)
		}
	}
	return ""
}

// storedText 是库里存的值的显示形式（以前写进去的非字符串值按 JSON）。
func storedText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// hasValue：库里存的值算不算"有值"（非空字符串；以前写进去的其他 JSON 类型也算）。
func hasValue(v any, ok bool) bool {
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return s != ""
	}
	return true
}

// storedIssue：库里存的一个值和现在的设置对不上的原因；对得上时为空串。
func (s *settings) storedIssue(key string, v any) string {
	def := s.fieldByKey[key]
	if def == nil {
		return fmt.Sprintf("项目设置里已经没有著录项“%s”，原值“%s”保留", key, storedText(v))
	}
	str, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%s的原值“%s”和现在的设置不符：要是字符串", def.Label, storedText(v))
	}
	if why := checkValue(def, str); why != "" {
		return fmt.Sprintf("%s的原值“%s”和现在的设置不符：%s", def.Label, str, why)
	}
	return ""
}

// orderedKeys 是 extra 的 key：设置里的顺序在前，设置里没有的按 key 排在后面。
func (s *settings) orderedKeys(extra map[string]any) []string {
	out := make([]string, 0, len(extra))
	for _, d := range s.ItemFields {
		if _, ok := extra[d.Key]; ok {
			out = append(out, d.Key)
		}
	}
	var unknown []string
	for k := range extra {
		if s.fieldByKey[k] == nil {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	return append(out, unknown...)
}

// fieldIssues 是详情的 `field_issues`：库里存的值里和现在的设置对不上的（含设置里已经没有的著录项）。
func (s *settings) fieldIssues(extra map[string]any) []FieldIssue {
	out := []FieldIssue{}
	for _, k := range s.orderedKeys(extra) {
		if msg := s.storedIssue(k, extra[k]); msg != "" {
			out = append(out, FieldIssue{Key: k, Message: msg})
		}
	}
	return out
}

// edits 是一次提交的著录：Title 为 nil 表示不改；Fields 里 key → 提交的原值（JSON 解出来的 string、nil 或别的类型）。
type edits struct {
	Title  *string
	Fields map[string]any
}

// applyEdits 按草稿的规则校验并合并著录，返回新的题名、合并后的 extra_fields（新的 map）和问题（为空表示通过）。
func (s *settings) applyEdits(storedTitle string, stored map[string]any, e edits) (string, map[string]any, []FieldIssue) {
	var issues []FieldIssue
	title := storedTitle
	if e.Title != nil {
		t := strings.TrimSpace(*e.Title)
		if t != storedTitle {
			if why := checkTitle(t); why != "" {
				issues = append(issues, FieldIssue{Key: project.TitleKey, Message: why})
			}
		}
		title = t
	}
	merged := make(map[string]any, len(stored)+len(e.Fields))
	for k, v := range stored {
		merged[k] = v
	}
	for _, k := range s.orderedKeys(e.Fields) {
		raw := e.Fields[k]
		def := s.fieldByKey[k]
		label := k
		if def != nil {
			label = def.Label
		}
		var v string
		switch x := raw.(type) {
		case nil:
		case string:
			v = strings.TrimSpace(x)
		default:
			issues = append(issues, FieldIssue{Key: k, Message: label + "要是字符串（清空用 null 或空串）"})
			continue
		}
		if v == "" {
			delete(merged, k)
			continue
		}
		if old, ok := stored[k].(string); ok && old == v {
			merged[k] = v // 旧值原样保留，不校验
			continue
		}
		if def == nil {
			issues = append(issues, FieldIssue{Key: k, Message: fmt.Sprintf("项目设置里没有著录项“%s”（只能清空或保留原值）", k)})
			continue
		}
		if why := checkValue(def, v); why != "" {
			issues = append(issues, FieldIssue{Key: k, Message: label + why})
			continue
		}
		merged[k] = v
	}
	return title, merged, issues
}

// completionIssues：锁定时的检查——题名和必填的不能为空，现有的值要合现在的设置（设置里已经没有的不管）。
func (s *settings) completionIssues(title string, extra map[string]any) []FieldIssue {
	var out []FieldIssue
	if title == "" {
		out = append(out, FieldIssue{Key: project.TitleKey, Message: "题名不能为空"})
	}
	for _, d := range s.ItemFields {
		v, ok := extra[d.Key]
		if !hasValue(v, ok) {
			if d.Required {
				out = append(out, FieldIssue{Key: d.Key, Message: d.Label + "不能为空"})
			}
			continue
		}
		if msg := s.storedIssue(d.Key, v); msg != "" {
			out = append(out, FieldIssue{Key: d.Key, Message: msg})
		}
	}
	return out
}

// sameFields：两份 extra_fields 是否相同（值按 JSON 比较）。
func sameFields(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || storedText(va) != storedText(vb) {
			return false
		}
	}
	return true
}

// fieldDiff 是审计用的著录差异：变了的 key（不存在的是 null）。
func fieldDiff(before, after map[string]any) (map[string]any, map[string]any) {
	b, a := map[string]any{}, map[string]any{}
	for k, va := range after {
		if vb, ok := before[k]; !ok || storedText(vb) != storedText(va) {
			b[k], a[k] = before[k], va
		}
	}
	for k, vb := range before {
		if _, ok := after[k]; !ok {
			b[k], a[k] = vb, nil
		}
	}
	return b, a
}
