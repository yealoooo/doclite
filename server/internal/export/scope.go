package export

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/catalog"
	"doclite/server/internal/export/bundle"
	"doclite/server/internal/platform/httpx"
)

// 范围（requirements 5.11"范围"）：`category`（节点子树里未删除的条目，重叠算一次）或 `item`；scope_ids 1–5000 个，
// 都要是这个项目里未删除的（否则 404 details.ids，按请求里的顺序）；补交用 resubmit_of（和 scope_ids 二选一）。

const (
	scopeTypeCategory = "category"
	scopeTypeItem     = "item"
)

// RequestBody 是预检、建任务的请求体。
type RequestBody struct {
	ScopeType    *string     `json:"scope_type"`
	ScopeIDs     []uuid.UUID `json:"scope_ids"`
	ResubmitOf   *uuid.UUID  `json:"resubmit_of"`
	ExportFormat *string     `json:"export_format"`
	// OnlyApproved：只导出审核通过的（不传是 true）。
	OnlyApproved *bool `json:"only_approved"`
	// OnlyReady：只导出就绪的（建任务用，不传是 false）。
	OnlyReady *bool `json:"only_ready"`
}

// request 是检查过的请求。
type request struct {
	ScopeType    string
	ScopeIDs     []uuid.UUID // 去重后保持请求里的顺序；补交时由 server 算
	ResubmitOf   *uuid.UUID
	Format       bundle.Format
	OnlyApproved bool
	OnlyReady    bool
}

// parseRequest 检查参数（400）。
func parseRequest(b *RequestBody) (*request, error) {
	r := &request{OnlyApproved: true}
	if b.OnlyApproved != nil {
		r.OnlyApproved = *b.OnlyApproved
	}
	if b.OnlyReady != nil {
		r.OnlyReady = *b.OnlyReady
	}
	switch {
	case b.ScopeIDs != nil && b.ResubmitOf != nil:
		return nil, httpx.Invalid("scope_ids 和 resubmit_of 只能给一个")
	case b.ScopeIDs == nil && b.ResubmitOf == nil:
		return nil, httpx.Invalid("要给 scope_ids（导出范围）或 resubmit_of（补交哪个导出任务的变更）")
	case b.ResubmitOf != nil:
		if b.ScopeType != nil && *b.ScopeType != scopeTypeItem {
			return nil, httpx.Invalid("补交（resubmit_of）的范围是条目，scope_type 不给或者给 item")
		}
		r.ScopeType, r.ResubmitOf = scopeTypeItem, b.ResubmitOf
	default:
		if len(b.ScopeIDs) == 0 {
			return nil, httpx.Invalid("scope_ids 不能为空：至少选一个归类节点或条目")
		}
		if len(b.ScopeIDs) > MaxScopeIDs {
			return nil, httpx.Invalid("scope_ids 一次最多 %d 个，这次有 %d 个", MaxScopeIDs, len(b.ScopeIDs))
		}
		if b.ScopeType == nil {
			return nil, httpx.Invalid("缺少 scope_type（category 或 item）")
		}
		if *b.ScopeType != scopeTypeCategory && *b.ScopeType != scopeTypeItem {
			return nil, httpx.Invalid("scope_type 只能是 category（归类节点）或 item（条目），收到的是 %q", *b.ScopeType)
		}
		r.ScopeType = *b.ScopeType
		seen := make(map[uuid.UUID]bool, len(b.ScopeIDs))
		for _, id := range b.ScopeIDs {
			if !seen[id] {
				seen[id] = true
				r.ScopeIDs = append(r.ScopeIDs, id)
			}
		}
	}
	if b.ExportFormat == nil || *b.ExportFormat == "" {
		return nil, httpx.Invalid("缺少 export_format（pdf 或 images）")
	}
	switch f := *b.ExportFormat; f {
	case string(bundle.FormatPDF), string(bundle.FormatImages):
		r.Format = bundle.Format(f)
	case "pdf_ocr", "ofd":
		return nil, httpx.Invalid("导出格式 %s 以后的版本才支持，现在只能是 pdf 或 images", f)
	default:
		return nil, httpx.Invalid("export_format 只能是 pdf 或 images，收到的是 %q", f)
	}
	return r, nil
}

// errScopeMissing 是范围里有不存在、已删除、不在这个项目里的节点或条目：404，details.ids 按请求里的顺序。
func errScopeMissing(scopeType string, ids []uuid.UUID) error {
	what := "归类节点"
	if scopeType == scopeTypeItem {
		what = "条目"
	}
	return httpx.NewError(http.StatusNotFound, httpx.CodeNotFound,
		fmt.Sprintf("导出范围里有 %d 个%s不存在、已删除或不在这个项目里", len(ids), what)).WithDetails(map[string]any{"ids": ids})
}

// resolved 是解析好的范围：条目 id（未排序）、范围里节点的名称链（category 时）、补交时算出的条目。
type resolved struct {
	itemIDs         []uuid.UUID
	scopeCategories []ScopeCategory
}

// resolveScope 在快照 tx 里把范围变成条目 id。补交时 req.ScopeIDs 改成算出来的条目（按源任务的导出顺序）。
func (s *Service) resolveScope(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, req *request) (*resolved, error) {
	if req.ResubmitOf != nil {
		ids, err := s.resubmitItems(ctx, tx, projectID, *req.ResubmitOf)
		if err != nil {
			return nil, err
		}
		req.ScopeIDs = ids
		return &resolved{itemIDs: ids, scopeCategories: []ScopeCategory{}}, nil
	}
	if req.ScopeType == scopeTypeItem {
		live, err := liveItemIDs(ctx, tx, projectID, req.ScopeIDs)
		if err != nil {
			return nil, err
		}
		var missing []uuid.UUID
		for _, id := range req.ScopeIDs {
			if !live[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return nil, errScopeMissing(scopeTypeItem, missing)
		}
		return &resolved{itemIDs: req.ScopeIDs, scopeCategories: []ScopeCategory{}}, nil
	}
	paths, err := liveCategories(ctx, tx, projectID, req.ScopeIDs)
	if err != nil {
		return nil, err
	}
	var missing []uuid.UUID
	list := make([]string, 0, len(paths))
	for _, id := range req.ScopeIDs {
		p, ok := paths[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		list = append(list, p)
	}
	if len(missing) > 0 {
		return nil, errScopeMissing(scopeTypeCategory, missing)
	}
	ids, err := subtreeItemIDs(ctx, tx, projectID, list)
	if err != nil {
		return nil, err
	}
	names, err := catalog.CategoryNames(ctx, tx, req.ScopeIDs)
	if err != nil {
		return nil, err
	}
	cats := make([]ScopeCategory, len(req.ScopeIDs))
	for i, id := range req.ScopeIDs {
		cats[i] = ScopeCategory{ID: id, CategoryNames: names[id]}
	}
	return &resolved{itemIDs: ids, scopeCategories: cats}, nil
}

// resubmitItems 是补交的条目：源任务（要在这个项目里，否则 404；要是 done，否则 409）的交付后变更里有变更、没删除的
// 条目，按源任务的导出顺序；没有 409。
func (s *Service) resubmitItems(ctx context.Context, tx pgx.Tx, projectID, sourceID uuid.UUID) ([]uuid.UUID, error) {
	src, err := getTask(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	if src == nil || src.ProjectID != projectID {
		return nil, httpx.NotFound("要补交的导出任务")
	}
	if src.Status != StatusDone {
		return nil, httpx.InvalidState(fmt.Sprintf("只能补交已完成的导出任务的变更，这个任务现在是“%s”", statusLabel(src.Status)))
	}
	ch, err := s.changesIn(ctx, tx, src)
	if err != nil {
		return nil, err
	}
	if len(ch.ResubmitItemIDs) == 0 {
		return nil, httpx.InvalidState(fmt.Sprintf("导出任务 %s 的交付后变更里没有要补交的条目（有变更、没删除的）", sourceID))
	}
	return ch.ResubmitItemIDs, nil
}
