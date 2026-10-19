package project

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 读时用的项目设置：按 [EffectiveSettings] 补默认值，再宽松地解析（类型不对的项按默认值；著录项里 key、label、type
// 不合格的、重复的、叫 title 的跳过）。写时的严格校验见 validateSettings。

// 著录项的类型。
const (
	FieldText = "text"
	FieldDate = "date"
	FieldEnum = "enum"
)

// FieldDef 是一个著录项（补上了默认值，能原样写回 settings.item_fields）。
type FieldDef struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	// Options 是 enum 的选项；其他类型为空数组。
	Options []string `json:"options"`
	// MaxLength 是 text 的最大字符数（没设时 500）；其他类型为 null。
	MaxLength *int `json:"max_length"`
	// Default 是新建条目时的默认值；没有时为 null。
	Default *string `json:"default"`
}

// Settings 是解析好的项目设置。
type Settings struct {
	// RejectReasons 去重、去空；没有时是默认的 8 个。
	RejectReasons  []string
	RescanReassign string
	// ArchiveNoTemplate 是档号模板原文（不是字符串时为空串，ArchiveNoTemplateNotString 为 true）。
	ArchiveNoTemplate          string
	ArchiveNoTemplateNotString bool
	ItemFields                 []FieldDef
	LockRequiresApproved       bool
	RequireLockedItem          bool
	RetentionDays              int
}

// ParseSettings 解析库里存的 settings（nil 或不是对象时全按默认值）。
func ParseSettings(stored []byte) *Settings {
	eff := EffectiveSettings(stored)
	s := &Settings{RescanReassign: "same_reviewer", LockRequiresApproved: true, RequireLockedItem: true,
		RetentionDays: defaultRetentionDays, ItemFields: []FieldDef{}}
	if rv, ok := eff["review"].(map[string]any); ok {
		list, _ := rv["reject_reasons"].([]any)
		for _, x := range list {
			if r, ok := x.(string); ok && r != "" && !slices.Contains(s.RejectReasons, r) {
				s.RejectReasons = append(s.RejectReasons, r)
			}
		}
		if m, ok := rv["rescan_reassign"].(string); ok && m != "" {
			s.RescanReassign = m
		}
	}
	if len(s.RejectReasons) == 0 {
		s.RejectReasons = slices.Clone(DefaultRejectReasons)
	}
	an, _ := eff["archive_no"].(map[string]any)
	s.ArchiveNoTemplate, s.ArchiveNoTemplateNotString = "", true
	if t, ok := an["template"].(string); ok {
		s.ArchiveNoTemplate, s.ArchiveNoTemplateNotString = t, false
	}
	if c, ok := eff["catalog"].(map[string]any); ok {
		if v, ok := c["lock_requires_approved"].(bool); ok {
			s.LockRequiresApproved = v
		}
	}
	if e, ok := eff["export"].(map[string]any); ok {
		if v, ok := e["require_locked_item"].(bool); ok {
			s.RequireLockedItem = v
		}
		if n, ok := integer(e["retention_days"]); ok && n >= 1 && n <= maxRetentionDays {
			s.RetentionDays = int(n)
		}
	}
	s.ItemFields = parseItemFields(eff["item_fields"])
	return s
}

func parseItemFields(v any) []FieldDef {
	out := []FieldDef{}
	seen := map[string]bool{TitleKey: true}
	list, _ := v.([]any)
	for _, raw := range list {
		f, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		def := FieldDef{Options: []string{}}
		def.Key, _ = f["key"].(string)
		def.Label, _ = f["label"].(string)
		def.Type, _ = f["type"].(string)
		if def.Key == "" || def.Label == "" || seen[def.Key] || (def.Type != FieldText && def.Type != FieldDate && def.Type != FieldEnum) {
			continue
		}
		seen[def.Key] = true
		def.Required, _ = f["required"].(bool)
		if def.Type == FieldEnum {
			opts, _ := f["options"].([]any)
			for _, o := range opts {
				if str, ok := o.(string); ok {
					def.Options = append(def.Options, str)
				}
			}
		}
		if def.Type == FieldText {
			ml := DefaultTextMaxLength
			if n, ok := integer(f["max_length"]); ok && n >= 1 && n <= MaxTextMaxLength {
				ml = int(n)
			}
			def.MaxLength = &ml
		}
		if d, ok := f["default"].(string); ok {
			def.Default = &d
		}
		out = append(out, def)
	}
	return out
}

// ReadSettings 读项目的设置并解析（跨模块的只读查询）；项目不存在或已删除 404。
func ReadSettings(ctx context.Context, q db.Querier, projectID uuid.UUID) (*Settings, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT settings FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).Scan(&raw)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("项目")
	}
	if err != nil {
		return nil, err
	}
	return ParseSettings(raw), nil
}
