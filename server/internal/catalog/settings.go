package catalog

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"doclite/server/internal/platform/archiveno"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/project"
)

// 归类要用的项目设置（requirements 3.4、5.9"归类信息"）：读时按 project.EffectiveSettings 补默认值，再宽松地解析成
// 下面的结构（著录项里解析不了的项跳过；模板解析不了时记下原因，需要生成档号的操作 409 `invalid_state`，归类编码
// 按 archiveno.FallbackSeparators 校验）。

// MaxCategoryNameChars 是归类节点名称的最大字符数（去掉首尾空白后）。
const MaxCategoryNameChars = 100

// 著录项的类型。
const (
	fieldText = "text"
	fieldDate = "date"
	fieldEnum = "enum"
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

// settings 是解析好的归类设置。
type settings struct {
	template    string
	tmpl        *archiveno.Template // 模板解析不了时为 nil
	templateErr string
	fields      []FieldDef
	fieldByKey  map[string]*FieldDef
	// lockRequiresApproved：定稿要求全部页审核通过。
	lockRequiresApproved bool
	requireLockedItem    bool
}

func parseSettings(stored []byte) *settings {
	eff := project.EffectiveSettings(stored)
	s := &settings{fieldByKey: map[string]*FieldDef{}, lockRequiresApproved: true, requireLockedItem: true}
	if an, ok := eff["archive_no"].(map[string]any); ok {
		if t, ok := an["template"].(string); ok {
			s.template = t
			if tmpl, err := archiveno.Parse(t); err == nil {
				s.tmpl = tmpl
			} else {
				s.templateErr = err.Error()
			}
		} else {
			s.templateErr = "档号模板不对：archive_no.template 不是字符串"
		}
	}
	if c, ok := eff["catalog"].(map[string]any); ok {
		if v, ok := c["lock_requires_approved"].(bool); ok {
			s.lockRequiresApproved = v
		}
	}
	if e, ok := eff["export"].(map[string]any); ok {
		if v, ok := e["require_locked_item"].(bool); ok {
			s.requireLockedItem = v
		}
	}
	list, _ := eff["item_fields"].([]any)
	for _, raw := range list {
		f, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		def := FieldDef{Options: []string{}}
		def.Key, _ = f["key"].(string)
		def.Label, _ = f["label"].(string)
		def.Type, _ = f["type"].(string)
		if def.Key == "" || def.Label == "" || (def.Type != fieldText && def.Type != fieldDate && def.Type != fieldEnum) {
			continue
		}
		if _, dup := s.fieldByKey[def.Key]; dup || def.Key == project.TitleKey {
			continue
		}
		def.Required, _ = f["required"].(bool)
		if def.Type == fieldEnum {
			opts, _ := f["options"].([]any)
			for _, o := range opts {
				if str, ok := o.(string); ok {
					def.Options = append(def.Options, str)
				}
			}
		}
		if def.Type == fieldText {
			ml := project.DefaultTextMaxLength
			if n, ok := f["max_length"].(json.Number); ok {
				if v, err := n.Int64(); err == nil && v >= 1 && v <= project.MaxTextMaxLength {
					ml = int(v)
				}
			}
			def.MaxLength = &ml
		}
		if d, ok := f["default"].(string); ok {
			def.Default = &d
		}
		s.fields = append(s.fields, def)
		s.fieldByKey[def.Key] = &s.fields[len(s.fields)-1]
	}
	// fieldByKey 指向 fields 的元素：append 可能搬家，最后重建一次。
	for i := range s.fields {
		s.fieldByKey[s.fields[i].Key] = &s.fields[i]
	}
	return s
}

// separators 是归类编码不能有的模板分隔符（模板解析不了时用兜底的）。
func (s *settings) separators() []rune {
	if s.tmpl == nil {
		return archiveno.FallbackSeparators
	}
	return s.tmpl.Separators()
}

// templateInvalid 是模板解析不了、又要生成档号时的 409 `invalid_state`。
func (s *settings) templateInvalid(what string) error {
	return httpx.Conflict(httpx.CodeInvalidState, fmt.Sprintf("项目的档号模板不对（%s），不能%s；先在项目设置里改好档号模板", s.templateErr, what))
}

// SettingsResponse 是 `GET /api/projects/{pid}/catalog-settings`。
type SettingsResponse struct {
	ArchiveNoTemplate      string     `json:"archive_no_template"`
	ArchiveNoTemplateError *string    `json:"archive_no_template_error"`
	ArchiveNoLevels        []string   `json:"archive_no_levels"`
	ItemFields             []FieldDef `json:"item_fields"`
	LockRequiresApproved   bool       `json:"lock_requires_approved"`
	RequireLockedItem      bool       `json:"require_locked_item"`
	// CategoryTypes 是能新建、能改成的节点类型（不含 root）。
	CategoryTypes        []string `json:"category_types"`
	CategoryCodeMaxChars int      `json:"category_code_max_chars"`
	// CategoryCodeForbiddenChars 是编码里不能有的可见字符（当前模板的分隔符、花括号、`\ / : * ? " < > |`）。
	CategoryCodeForbiddenChars []string `json:"category_code_forbidden_chars"`
	CategoryNameMaxChars       int      `json:"category_name_max_chars"`
	DateFormats                []string `json:"date_formats"`
}

func (s *settings) response() *SettingsResponse {
	out := &SettingsResponse{
		ArchiveNoTemplate:          s.template,
		ArchiveNoLevels:            []string{},
		ItemFields:                 s.fields,
		LockRequiresApproved:       s.lockRequiresApproved,
		RequireLockedItem:          s.requireLockedItem,
		CategoryTypes:              []string{},
		CategoryCodeMaxChars:       names.MaxCategoryCodeChars,
		CategoryCodeForbiddenChars: names.CategoryCodeForbiddenChars(s.separators()),
		CategoryNameMaxChars:       MaxCategoryNameChars,
		DateFormats:                project.DateFormats,
	}
	if out.ItemFields == nil {
		out.ItemFields = []FieldDef{}
	}
	if s.tmpl != nil {
		for _, t := range s.tmpl.Levels() {
			out.ArchiveNoLevels = append(out.ArchiveNoLevels, string(t))
		}
	} else {
		msg := s.templateErr
		out.ArchiveNoTemplateError = &msg
	}
	for _, t := range archiveno.AssignableTypes {
		out.CategoryTypes = append(out.CategoryTypes, string(t))
	}
	return out
}

// loadSettings 读项目的归类设置；项目不存在或已删除 404。
func loadSettings(ctx context.Context, q db.Querier, projectID uuid.UUID) (*settings, error) {
	raw, err := projectSettings(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, httpx.NotFound("项目")
	}
	return parseSettings(raw), nil
}

// Settings 是 `GET /api/projects/{pid}/catalog-settings`。
func (s *Service) Settings(ctx context.Context, projectID uuid.UUID) (*SettingsResponse, error) {
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	return st.response(), nil
}
