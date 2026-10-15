package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"doclite/server/internal/platform/httpx"
	"doclite/shared/archiveno"
)

// projects.settings（requirements 3.4）：读时补默认；写时整体替换，只校验已知键，保留未知键，≤ 64 KiB。
//
// 已知键：review.reject_reasons、review.rescan_reassign、archive_no.template、item_fields、
// catalog.lock_requires_approved、export.require_locked_item、export.retention_days。
// 著录项（item_fields）和档号模板的校验按 3.4、5.9 写到的程度：模板用 archiveno.Parse；著录项的 key、label、type、
// required、options、max_length、default。
//
// 校验错误是 400 `invalid_input`，message 以"settings 不对："开头（前端据此把错误显示在著录设置里，
// `item_fields[i]` 指第 i 个著录项，含"档号模板"时标出模板输入框）。

const (
	maxSettingsBytes = 64 << 10
	// DefaultTextMaxLength 是 text 著录项不设 max_length 时的上限。
	DefaultTextMaxLength = 500
	// MaxTextMaxLength 是 text 著录项 max_length 的上限。
	MaxTextMaxLength = 10000
	maxRetentionDays = 365
	// TitleKey 是题名（条目的固定列）在著录校验里的 key，著录项不能叫这个。
	TitleKey = "title"
)

// DefaultRejectReasons 是内置的驳回原因（存代码，界面译中文）。
var DefaultRejectReasons = []string{"blur", "skew", "cut", "stain", "missing", "dup", "orient", "other"}

// DateFormats 是 date 著录项的三种格式。
var DateFormats = []string{"YYYY", "YYYY-MM", "YYYY-MM-DD"}

// defaultSettings 是 settings 的默认值（每次新建一份，调用方可以改）。
func defaultSettings() map[string]any {
	reasons := make([]any, len(DefaultRejectReasons))
	for i, r := range DefaultRejectReasons {
		reasons[i] = r
	}
	return map[string]any{
		"review":      map[string]any{"reject_reasons": reasons, "rescan_reassign": "same_reviewer"},
		"export":      map[string]any{"require_locked_item": true, "retention_days": json.Number("7")},
		"archive_no":  map[string]any{"template": archiveno.DefaultTemplate},
		"item_fields": []any{},
		"catalog":     map[string]any{"lock_requires_approved": true},
	}
}

// decodeJSON 解析 JSON（数字保留原样，json.Number）。
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("多余的内容")
	}
	return v, nil
}

// EffectiveSettings 是库里存的 settings 补上默认值：对象逐层合并（库里有的键优先），其他类型整体取库里的。
func EffectiveSettings(stored []byte) map[string]any {
	merged := defaultSettings()
	if v, err := decodeJSON(stored); err == nil {
		if m, ok := v.(map[string]any); ok {
			mergeInto(merged, m)
		}
	}
	return merged
}

func mergeInto(base, overlay map[string]any) {
	for k, v := range overlay {
		bm, bok := base[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			mergeInto(bm, om)
			continue
		}
		base[k] = v
	}
}

// canonicalJSON 是比较用的规范形式（对象的键排好序）。
func canonicalJSON(raw []byte) string {
	v, err := decodeJSON(raw)
	if err != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func settingsError(format string, args ...any) error {
	return httpx.Invalid("settings 不对："+format, args...)
}

// validateSettings 校验要写入的 settings（整体替换），返回原样的 JSON。
func validateSettings(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > maxSettingsBytes {
		return nil, settingsError("最大 %d KiB", maxSettingsBytes>>10)
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, settingsError("不是合法的 JSON")
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, settingsError("必须是 JSON 对象")
	}
	if review, ok := root["review"]; ok {
		m, err := object(review, "review")
		if err != nil {
			return nil, err
		}
		if reasons, ok := m["reject_reasons"]; ok {
			// 至少一个：列表空了就驳回不了。
			if _, err := stringArray(reasons, "review.reject_reasons"); err != nil {
				return nil, err
			}
		}
		if mode, ok := m["rescan_reassign"]; ok && mode != "same_reviewer" {
			return nil, settingsError(`review.rescan_reassign 目前只能是 "same_reviewer"`)
		}
	}
	if export, ok := root["export"]; ok {
		m, err := object(export, "export")
		if err != nil {
			return nil, err
		}
		if v, ok := m["require_locked_item"]; ok {
			if _, isBool := v.(bool); !isBool {
				return nil, settingsError("export.require_locked_item 必须是 true 或 false")
			}
		}
		if v, ok := m["retention_days"]; ok {
			if n, isInt := integer(v); !isInt || n < 1 || n > maxRetentionDays {
				return nil, settingsError("export.retention_days 必须是 1 到 %d 的整数（天）", maxRetentionDays)
			}
		}
	}
	if an, ok := root["archive_no"]; ok {
		m, err := object(an, "archive_no")
		if err != nil {
			return nil, err
		}
		if v, ok := m["template"]; ok {
			s, isStr := v.(string)
			if !isStr {
				return nil, settingsError("archive_no.template 必须是字符串")
			}
			if _, err := archiveno.Parse(s); err != nil {
				return nil, settingsError("%s", err.Error())
			}
		}
	}
	if c, ok := root["catalog"]; ok {
		m, err := object(c, "catalog")
		if err != nil {
			return nil, err
		}
		if v, ok := m["lock_requires_approved"]; ok {
			if _, isBool := v.(bool); !isBool {
				return nil, settingsError("catalog.lock_requires_approved 必须是 true 或 false")
			}
		}
	}
	if fields, ok := root["item_fields"]; ok {
		if err := validateItemFields(fields); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func validateItemFields(v any) error {
	list, ok := v.([]any)
	if !ok {
		return settingsError("item_fields 必须是数组")
	}
	keys := map[string]bool{}
	for i, item := range list {
		at := fmt.Sprintf("item_fields[%d]", i)
		f, err := object(item, at)
		if err != nil {
			return err
		}
		key, err := nonEmptyString(f["key"], at+".key")
		if err != nil {
			return err
		}
		if keys[key] {
			return settingsError("%s.key %q 重复", at, key)
		}
		keys[key] = true
		if key == TitleKey {
			return settingsError("%s.key 不能是 %q（题名是固定的著录项，不用在 item_fields 里定义）", at, key)
		}
		if _, err := nonEmptyString(f["label"], at+".label"); err != nil {
			return err
		}
		typ, err := nonEmptyString(f["type"], at+".type")
		if err != nil {
			return err
		}
		if typ != "text" && typ != "date" && typ != "enum" {
			return settingsError("%s.type 只能是 text、date 或 enum", at)
		}
		if r, ok := f["required"]; ok {
			if _, isBool := r.(bool); !isBool {
				return settingsError("%s.required 必须是 true 或 false", at)
			}
		}
		var options []string
		switch opts, ok := f["options"]; {
		case !ok || opts == nil:
			if typ == "enum" {
				return settingsError("%s 是 enum，要有 options", at)
			}
		default:
			arr, isArr := opts.([]any)
			if isArr && len(arr) == 0 && typ != "enum" {
				break // catalog-settings 给非 enum 的著录项返回 `options: []`，原样写回要能通过
			}
			if options, err = stringArray(opts, at+".options"); err != nil {
				return err
			}
			for j, o := range options {
				if strings.TrimSpace(o) != o {
					return settingsError("%s.options[%d] 首尾不能有空白（%q）", at, j, o)
				}
			}
		}
		maxLength := DefaultTextMaxLength
		if ml, ok := f["max_length"]; ok && ml != nil {
			if typ != "text" {
				return settingsError("%s.max_length 只有 text 类型能设", at)
			}
			n, isInt := integer(ml)
			if !isInt || n < 1 || n > MaxTextMaxLength {
				return settingsError("%s.max_length 要是 1–%d 的整数", at, MaxTextMaxLength)
			}
			maxLength = int(n)
		}
		switch d := f["default"].(type) {
		case nil:
		case string:
			switch typ {
			case "text":
				if why := CheckTextValue(d, maxLength); why != "" {
					return settingsError("%s.default %s", at, why)
				}
			case "date":
				if !IsValidDateValue(d) {
					return settingsError("%s.default 不是合法的日期（%s）", at, strings.Join(DateFormats, "、"))
				}
			case "enum":
				found := false
				for _, o := range options {
					found = found || o == d
				}
				if !found {
					return settingsError("%s.default 不在 options 里", at)
				}
			}
		default:
			return settingsError("%s.default 必须是字符串", at)
		}
	}
	return nil
}

func object(v any, at string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, settingsError("%s 必须是对象", at)
	}
	return m, nil
}

func nonEmptyString(v any, at string) (string, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", settingsError("%s 必须是非空字符串", at)
	}
	return s, nil
}

// stringArray：至少一个非空字符串的数组。
func stringArray(v any, at string) ([]string, error) {
	arr, ok := v.([]any)
	if ok && len(arr) > 0 {
		out := make([]string, 0, len(arr))
		for _, x := range arr {
			s, isStr := x.(string)
			if !isStr || strings.TrimSpace(s) == "" {
				ok = false
				break
			}
			out = append(out, s)
		}
		if ok {
			return out, nil
		}
	}
	return nil, settingsError("%s 必须是至少一个非空字符串的数组", at)
}

// integer：JSON 整数（json.Number，没有小数点和指数）。
func integer(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// IsValidDateValue：`YYYY`、`YYYY-MM`、`YYYY-MM-DD` 之一，年份 0001–9999，月、日要真实存在（含闰年）。
func IsValidDateValue(v string) bool {
	var layout string
	switch len(v) {
	case 4:
		layout = "2006"
	case 7:
		layout = "2006-01"
	case 10:
		layout = "2006-01-02"
	default:
		return false
	}
	for i, c := range v {
		if (i == 4 || i == 7) && len(v) > i {
			if c != '-' {
				return false
			}
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	t, err := time.Parse(layout, v)
	return err == nil && t.Year() >= 1
}

// CheckTextValue：text 的值不超过 maxLength 个字符，不能有控制字符（换行、回车、制表符可以）。合格时返回空串。
func CheckTextValue(v string, maxLength int) string {
	if utf8.RuneCountInString(v) > maxLength {
		return fmt.Sprintf("最长 %d 个字符", maxLength)
	}
	if strings.IndexFunc(v, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' }) >= 0 {
		return "不能有控制字符"
	}
	return ""
}
