// Package project 是项目和项目授权（requirements 5.3）、项目的两把树锁（locks.go）、项目的实时通道（handler 的 stream）。
//
// 规则：
//   - 管理项目和授权要 system.projects 菜单，能管所有未删除的项目，不要求自己能进；建项目的人不自动成为成员；
//   - project_code `^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`，区分大小写，不可改，未删除的项目里唯一（409 `conflict`），
//     删除后可再用；名称去首尾空白 1–100 字，其余按文件夹名规则；说明 ≤ 500；
//   - 建项目在同一个事务里建根文件夹（名 = 项目名）和归类根（root，编码 = 项目编码、名 = 项目名）；改名时一起改，
//     提交后发 folders.changed（根文件夹）；
//   - 版本检查：PATCH 可带 expected_updated_at，锁住项目行之后和库里的 updated_at 比较，不同 409 `conflict`、
//     什么都不改（没有实际变化的修改也检查）；不带不查。updated_at 写成 max(now, 旧值 + 1µs)，改授权也换版本；
//   - 删除是软删除；有未删除的文件 409 `project_not_empty`（在文件树排他锁下检查）；
//   - 授权 PUT access 整体替换（各 ≤ 1000，可以授权给停用的用户），只锁项目行；
//   - settings 整体替换，校验见 settings.go；读时补默认值；
//   - 审计 target_type project（create、update、delete、set_access）；没有实际变化不写库、不审计、不发事件；
//     改设置不发事件。
package project

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/platform/xslices"
)

const (
	maxDescriptionChars = 500
	maxKeywordChars     = 64
	maxAccessEntries    = 1000
	// 项目状态。
	StatusActive   = "active"
	StatusArchived = "archived"
)

var errNotFound = httpx.NotFound("项目")

// Service 是项目的业务规则。
type Service struct {
	pool   *pgxpool.Pool
	events *events.Bus
	auth   *auth.Service
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, bus *events.Bus, authSvc *auth.Service) *Service {
	return &Service{pool: pool, events: bus, auth: authSvc}
}

// ListMine 是用户能进的未删除项目（admin 是全部），含已归档的，按编码排序。
func (s *Service) ListMine(ctx context.Context, userID uuid.UUID) ([]MyProject, error) {
	return listMyProjects(ctx, s.pool, userID)
}

// List 是系统管理里的项目列表（未删除的，新建的在前）。
func (s *Service) List(ctx context.Context, keyword, status string, page httpx.PageRequest) (httpx.Page[Item], error) {
	keyword = strings.TrimSpace(keyword)
	if utf8.RuneCountInString(keyword) > maxKeywordChars {
		return httpx.Page[Item]{}, httpx.Invalid("搜索关键字最长 %d 个字符", maxKeywordChars)
	}
	status = strings.TrimSpace(status)
	if status != "" {
		if _, err := checkStatus(status); err != nil {
			return httpx.Page[Item]{}, err
		}
	}
	items, total, err := listProjects(ctx, s.pool, keyword, status, page)
	if err != nil {
		return httpx.Page[Item]{}, err
	}
	return httpx.NewPage(items, total, page), nil
}

// Detail 是 `/api/system/projects/{id}` 的项目详情。
type Detail struct {
	ID          uuid.UUID `json:"id"`
	ProjectCode string    `json:"project_code"`
	ProjectName string    `json:"project_name"`
	Description *string   `json:"description"`
	Status      string    `json:"status"`
	// Settings 是补上了默认值的 settings。
	Settings       map[string]any `json:"settings"`
	RootFolderID   uuid.UUID      `json:"root_folder_id"`
	CategoryRootID uuid.UUID      `json:"category_root_id"`
	CreatedBy      uuid.UUID      `json:"created_by"`
	CreatedAt      time.Time      `json:"created_at"`
	// UpdatedAt 是版本：PATCH 的 expected_updated_at 原样传回它。
	UpdatedAt time.Time `json:"updated_at"`
	Access
}

func detailOf(p *projectRow, a *Access) *Detail {
	return &Detail{ID: p.ID, ProjectCode: p.ProjectCode, ProjectName: p.ProjectName, Description: p.Description,
		Status: p.Status, Settings: EffectiveSettings(p.Settings), RootFolderID: p.RootFolderID, CategoryRootID: p.CategoryRootID,
		CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Access: *a}
}

// readDetail 在 q 上读详情（新建、修改、授权在提交前读，不会因为提交后马上被删就报 404）。
func readDetail(ctx context.Context, q db.Querier, id uuid.UUID) (*Detail, error) {
	p, err := getProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errNotFound
	}
	a, err := getAccess(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return detailOf(p, a), nil
}

// Get 读未删除的项目详情；不存在或已删除 404。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Detail, error) {
	return readDetail(ctx, s.pool, id)
}

// CreateInput 是新建项目的输入。
type CreateInput struct {
	ProjectCode string  `json:"project_code"`
	ProjectName string  `json:"project_name"`
	Description *string `json:"description"`
}

// Create 新建项目，连同根文件夹和归类根。编码重复 409 `conflict`。
func (s *Service) Create(ctx context.Context, operatorID uuid.UUID, in CreateInput) (*Detail, error) {
	code, err := names.NormalizeProjectCode(in.ProjectCode)
	if err != nil {
		return nil, httpx.Invalid("%s", err.Error())
	}
	name, err := normalizeName(in.ProjectName)
	if err != nil {
		return nil, err
	}
	desc, err := names.OptionalText(in.Description, maxDescriptionChars, "项目说明")
	if err != nil {
		return nil, httpx.Invalid("%s", err)
	}
	p := &projectRow{ID: db.NewID(), ProjectCode: code, ProjectName: name, Description: desc, CreatedBy: operatorID,
		RootFolderID: db.NewID(), CategoryRootID: db.NewID()}
	var out *Detail
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertProject(ctx, tx, p); err != nil {
			if db.UniqueViolation(err, "ux_projects_code") {
				return httpx.Conflict("", "项目编码 "+code+" 已被使用")
			}
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &p.ID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetProject, TargetID: p.ID, Action: "create",
			Detail: map[string]any{"after": map[string]any{"project_code": code, "project_name": name, "description": desc}}}); err != nil {
			return err
		}
		out, err = readDetail(ctx, tx, p.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateInput 是修改项目的输入；nil 表示不改。Description 传空串表示清空。
type UpdateInput struct {
	ProjectName *string
	Description *string
	Status      *string
	// Settings 整体替换（必须是 JSON 对象）。
	Settings json.RawMessage
	// ExpectedUpdatedAt：版本检查，和库里的 updated_at 不同时 409 `conflict`；nil 不检查。
	ExpectedUpdatedAt *time.Time
}

// Update 改名称、说明、状态、settings。检查顺序：输入（400）→ 项目不存在或已删除（404）→ 版本（409）。
func (s *Service) Update(ctx context.Context, operatorID, id uuid.UUID, in UpdateInput) (*Detail, error) {
	var name, desc, status *string
	var err error
	if in.ProjectName != nil {
		n, err := normalizeName(*in.ProjectName)
		if err != nil {
			return nil, err
		}
		name = &n
	}
	if in.Description != nil {
		if desc, err = names.OptionalText(in.Description, maxDescriptionChars, "项目说明"); err != nil {
			return nil, httpx.Invalid("%s", err)
		}
	}
	if in.Status != nil {
		st, err := checkStatus(*in.Status)
		if err != nil {
			return nil, err
		}
		status = &st
	}
	var settings json.RawMessage
	if in.Settings != nil {
		if settings, err = validateSettings(in.Settings); err != nil {
			return nil, err
		}
	}

	var out *Detail
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		// 加锁顺序（requirements 3.6）：改名时 归类树锁（共享）→ 归类根 → 文件树锁（共享）→ 项目行。
		if name != nil {
			if err := LockCatalogTree(ctx, tx, id, false); err != nil {
				return err
			}
			if err := lockCategoryRoot(ctx, tx, id); err != nil {
				return err
			}
		}
		if err := LockFileTree(ctx, tx, id, false); err != nil {
			return err
		}
		before, err := lockProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if before == nil {
			return errNotFound
		}
		if in.ExpectedUpdatedAt != nil && !in.ExpectedUpdatedAt.Equal(before.UpdatedAt) {
			return httpx.Conflict("", "项目在你打开之后已被修改过，请重新载入后再保存")
		}
		after := *before
		b, a := map[string]any{}, map[string]any{}
		if name != nil && *name != before.ProjectName {
			after.ProjectName = *name
			b["project_name"], a["project_name"] = before.ProjectName, *name
		}
		if in.Description != nil && !names.SameOptional(desc, before.Description) {
			after.Description = desc
			b["description"], a["description"] = before.Description, desc
		}
		if status != nil && *status != before.Status {
			after.Status = *status
			b["status"], a["status"] = before.Status, *status
		}
		if settings != nil && canonicalJSON(settings) != canonicalJSON(before.Settings) {
			after.Settings = settings
			b["settings"], a["settings"] = json.RawMessage(before.Settings), settings
		}
		if len(a) > 0 {
			if _, err := updateProject(ctx, tx, &after); err != nil {
				return err
			}
			if after.ProjectName != before.ProjectName {
				if err := renameRoots(ctx, tx, id, after.ProjectName); err != nil {
					return err
				}
				ev.Add(events.FoldersChanged(id, before.RootFolderID))
			}
			if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &id, OperatorID: audit.Operator(operatorID),
				TargetType: audit.TargetProject, TargetID: id, Action: "update",
				Detail: map[string]any{"before": b, "after": a}}); err != nil {
				return err
			}
		}
		out, err = readDetail(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete 软删除项目。项目里还有未删除的文件 409 `project_not_empty`。
func (s *Service) Delete(ctx context.Context, operatorID, id uuid.UUID) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := LockFileTree(ctx, tx, id, true); err != nil {
			return err
		}
		p, err := lockProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if p == nil {
			return errNotFound
		}
		has, err := hasLiveFiles(ctx, tx, id)
		if err != nil {
			return err
		}
		if has {
			return httpx.Conflict("project_not_empty", "项目里还有文件，不能删除：先删掉项目里的全部文件")
		}
		if err := softDeleteProject(ctx, tx, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ProjectID: &id, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetProject, TargetID: id, Action: "delete",
			Detail: map[string]any{"before": map[string]any{"project_code": p.ProjectCode, "project_name": p.ProjectName}}})
	})
}

// GetAccess 是项目授权；项目不存在或已删除 404。
func (s *Service) GetAccess(ctx context.Context, id uuid.UUID) (*Access, error) {
	p, err := getProject(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errNotFound
	}
	return getAccess(ctx, s.pool, id)
}

// SetAccess 整体替换项目授权。重复的 id 去掉；有不存在的用户或角色 400。只锁项目行，不拿树锁。
func (s *Service) SetAccess(ctx context.Context, operatorID, id uuid.UUID, userIDs, roleIDs []uuid.UUID) (*Access, error) {
	userIDs, roleIDs = xslices.SortedIDs(userIDs), xslices.SortedIDs(roleIDs)
	if len(userIDs) > maxAccessEntries || len(roleIDs) > maxAccessEntries {
		return nil, httpx.Invalid("一次最多授权 %d 个人、%d 个角色", maxAccessEntries, maxAccessEntries)
	}
	var out *Access
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lockProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if p == nil {
			return errNotFound
		}
		if err := checkExisting(ctx, tx, "users", "用户", userIDs); err != nil {
			return err
		}
		if err := checkExisting(ctx, tx, "roles", "角色", roleIDs); err != nil {
			return err
		}
		before, err := getAccess(ctx, tx, id)
		if err != nil {
			return err
		}
		if slices.Equal(userIDsOf(before), userIDs) && slices.Equal(roleIDsOf(before), roleIDs) {
			out = before
			return nil // 没有变化
		}
		if err := replaceAccess(ctx, tx, id, userIDs, roleIDs); err != nil {
			return err
		}
		if err := touchProject(ctx, tx, id); err != nil {
			return err
		}
		if out, err = getAccess(ctx, tx, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ProjectID: &id, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetProject, TargetID: id, Action: "set_access",
			Detail: map[string]any{"before": accessSummary(before), "after": accessSummary(out)}})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AccessCandidates 是授权时的选人候选（不为选人放宽 /api/system/users、/api/system/roles 的菜单权限）。
func (s *Service) AccessCandidates(ctx context.Context) (*Candidates, error) {
	return accessCandidates(ctx, s.pool)
}

func checkExisting(ctx context.Context, tx pgx.Tx, table, what string, ids []uuid.UUID) error {
	found, err := existingIDs(ctx, tx, table, ids)
	if err != nil {
		return err
	}
	var missing []uuid.UUID
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return httpx.Invalid("有不存在的%s：%s", what, missing[0]).WithDetails(map[string]any{"ids": missing})
	}
	return nil
}

func accessSummary(a *Access) map[string]any {
	users := make([]string, 0, len(a.Users))
	for _, u := range a.Users {
		users = append(users, u.Username)
	}
	roles := make([]string, 0, len(a.Roles))
	for _, r := range a.Roles {
		roles = append(roles, r.RoleCode)
	}
	return map[string]any{"users": users, "roles": roles}
}

func userIDsOf(a *Access) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(a.Users))
	for _, u := range a.Users {
		out = append(out, u.ID)
	}
	return xslices.SortedIDs(out)
}

func roleIDsOf(a *Access) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(a.Roles))
	for _, r := range a.Roles {
		out = append(out, r.ID)
	}
	return xslices.SortedIDs(out)
}

func normalizeName(v string) (string, error) {
	name, err := names.NormalizeProjectName(v)
	if err != nil {
		return "", httpx.Invalid("%s", err.Error())
	}
	return name, nil
}

func checkStatus(v string) (string, error) {
	if v != StatusActive && v != StatusArchived {
		return "", httpx.Invalid("项目状态只能是 active 或 archived")
	}
	return v, nil
}
