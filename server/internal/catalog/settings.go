package catalog

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"doclite/server/internal/platform/archiveno"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/project"
)

// 归类要用的项目设置（requirements 3.4、5.9"归类信息"）：project.ReadSettings 读出补了默认值、宽松解析的设置，
// 这里再解析档号模板（解析不了时记下原因，需要生成档号的操作 409 `invalid_state`，归类编码按
// archiveno.FallbackSeparators 校验）。

// MaxCategoryNameChars 是归类节点名称的最大字符数（去掉首尾空白后）。
const MaxCategoryNameChars = 100

// settings 是解析好的归类设置。
type settings struct {
	*project.Settings
	tmpl        *archiveno.Template // 模板解析不了时为 nil
	templateErr string
	fieldByKey  map[string]*project.FieldDef
}

func newSettings(p *project.Settings) *settings {
	s := &settings{Settings: p, fieldByKey: map[string]*project.FieldDef{}}
	if p.ArchiveNoTemplateNotString {
		s.templateErr = "档号模板不对：archive_no.template 不是字符串"
	} else if tmpl, err := archiveno.Parse(p.ArchiveNoTemplate); err == nil {
		s.tmpl = tmpl
	} else {
		s.templateErr = err.Error()
	}
	for i := range p.ItemFields {
		s.fieldByKey[p.ItemFields[i].Key] = &p.ItemFields[i]
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
	return httpx.InvalidState(fmt.Sprintf("项目的档号模板不对（%s），不能%s；先在项目设置里改好档号模板", s.templateErr, what))
}

// SettingsResponse 是 `GET /api/projects/{pid}/catalog-settings`。
type SettingsResponse struct {
	ArchiveNoTemplate      string             `json:"archive_no_template"`
	ArchiveNoTemplateError *string            `json:"archive_no_template_error"`
	ArchiveNoLevels        []string           `json:"archive_no_levels"`
	ItemFields             []project.FieldDef `json:"item_fields"`
	LockRequiresApproved   bool               `json:"lock_requires_approved"`
	RequireLockedItem      bool               `json:"require_locked_item"`
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
		ArchiveNoTemplate:          s.ArchiveNoTemplate,
		ArchiveNoLevels:            []string{},
		ItemFields:                 s.ItemFields,
		LockRequiresApproved:       s.LockRequiresApproved,
		RequireLockedItem:          s.RequireLockedItem,
		CategoryTypes:              []string{},
		CategoryCodeMaxChars:       names.MaxCategoryCodeChars,
		CategoryCodeForbiddenChars: names.CategoryCodeForbiddenChars(s.separators()),
		CategoryNameMaxChars:       MaxCategoryNameChars,
		DateFormats:                project.DateFormats,
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
	p, err := project.ReadSettings(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	return newSettings(p), nil
}

// Settings 是 `GET /api/projects/{pid}/catalog-settings`。
func (s *Service) Settings(ctx context.Context, projectID uuid.UUID) (*SettingsResponse, error) {
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	return st.response(), nil
}
