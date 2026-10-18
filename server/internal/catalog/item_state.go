package catalog

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// 条目的状态（requirements 5.9 的状态表，2026-10-19）：status 只有 draft（未锁定）/ locked（已锁定），
// source_status 是 sealed（封存）/ modifying（修改中，一定是 draft）：
//
//	动作 \ 状态                  draft · sealed      draft · modifying       locked（一定 sealed）
//	改著录、移动、改件号            可以                可以                    409 item_locked
//	解除封存（seal.go）            → modifying         409 invalid_state       409 item_locked
//	重新归档（seal.go）            409 invalid_state   → sealed                409 item_locked
//	锁定                          → locked（查前置）   409 invalid_state       409 invalid_state
//	解锁                          409 invalid_state   409 invalid_state       → draft
//	撤销归档（删除）                可以                可以                    409 item_locked
//
// 锁定的前置条件：封存（不在修改中）、著录合格（含必填，400 + details.fields）、≥ 1 页、档号不空、
// lock_requires_approved 时全部页审核通过（409 `invalid_state`，message 写明）。审核状态按锁定这一刻读，不锁文件
// （审核不等归类，归类也不等审核）。批量锁定 1–500 个，已锁定的跳过，有一个不满足就整体不做（409，details.ids 列全部，
// message 按原因计数）。审计 archive_item：lock（另带 archive_no、page_count、lock_requires_approved，批量带
// batch: true）、unlock（另带 archive_no）。"锁定后有新版本"用最近一条 lock 审计的时间，所以它不能省。
// 锁定、解锁改了来源文件夹显示的归档状态，另发 folders.changed（来源文件夹）。

const (
	statusDraft  = "draft"
	statusLocked = "locked"

	sourceSealed    = "sealed"
	sourceModifying = "modifying"

	// MaxLockBatch 是批量锁定一次最多的条目数。
	MaxLockBatch = 500
)

// 单个条目的状态动作（路径的最后一段）。
const (
	ActionLock   = "lock"
	ActionUnlock = "unlock"
)

func errLockedEdit(what string) error {
	return httpx.Conflict("item_locked", fmt.Sprintf("条目已锁定，不能%s；要先解锁", what))
}

func invalidState(msg string) error { return httpx.Conflict(httpx.CodeInvalidState, msg) }

const msgModifying = "条目修改中（来源文件夹解除了封存），不能锁定；改完先重新归档"

// transition 是状态表的锁定、解锁两行：动作在这个状态下允许时返回新状态。
func transition(action string, r *itemRow) (string, error) {
	switch action {
	case ActionLock:
		switch {
		case r.Status == statusLocked:
			return "", invalidState("条目已经锁定了")
		case r.SourceStatus == sourceModifying:
			return "", invalidState(msgModifying)
		}
		return statusLocked, nil
	case ActionUnlock:
		if r.Status == statusLocked {
			return statusDraft, nil
		}
		return "", invalidState("条目没有锁定，不用解锁")
	}
	return "", httpx.NotFound("动作")
}

// 锁定不满足的原因（批量锁定按它计数）。
const (
	blockModifying   = "修改中（要先重新归档）"
	blockFields      = "著录不全或和现在的设置不符"
	blockNoPages     = "没有页"
	blockNoArchiveNo = "没有档号"
	blockUnapproved  = "有页没审核通过"
)

var blockOrder = []string{blockModifying, blockFields, blockNoPages, blockNoArchiveNo, blockUnapproved}

// lockBlocker 是条目不能锁定的第一个原因（kind 为空表示可以锁定）。fields 是著录的问题（kind 为 blockFields 时）。
func lockBlocker(ctx context.Context, q db.Querier, st *settings, r *itemRow, ps pageStats) (kind, message string, fields []FieldIssue, err error) {
	if r.SourceStatus == sourceModifying {
		return blockModifying, msgModifying, nil, nil
	}
	if issues := st.completionIssues(r.Title, r.ExtraFields); len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			msgs = append(msgs, is.Message)
		}
		return blockFields, "著录不全或和现在的设置不符：" + strings.Join(msgs, "；"), issues, nil
	}
	if ps.Pages == 0 {
		return blockNoPages, "条目没有页，不能锁定", nil, nil
	}
	if r.ArchiveNo == nil {
		why := ""
		ch, err := categoryChain(ctx, q, r.ProjectID, r.CategoryPath)
		if err != nil {
			return "", "", nil, err
		}
		reason, err := emptyReason(ctx, q, st, r.ProjectID, r.ID, ch, r.ItemNo)
		if err != nil {
			return "", "", nil, err
		}
		if reason != nil {
			why = "：" + reason.Message
		}
		return blockNoArchiveNo, "条目没有档号，不能锁定" + why, nil, nil
	}
	if st.lockRequiresApproved && ps.NotApproved > 0 {
		return blockUnapproved, fmt.Sprintf("条目有 %d 页还没审核通过（例如第 %d 页），项目设置要求全部页审核通过才能锁定",
			ps.NotApproved, ps.FirstNotOK), nil, nil
	}
	return "", "", nil, nil
}

// ItemAction 是单个条目的状态动作：锁定、解锁。
func (s *Service) ItemAction(ctx context.Context, operatorID, id uuid.UUID, action string, rowVersion int) (*Item, error) {
	if err := checkRowVersion(rowVersion); err != nil {
		return nil, err
	}
	cur, err := getItem(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errItemNotFound
	}
	projectID := cur.ProjectID
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *Item
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		r, err := lockItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if r == nil || r.Deleted {
			return errItemNotFound
		}
		next, err := transition(action, r)
		if err != nil {
			return err
		}
		if r.RowVersion != rowVersion {
			return staleVersion(r.RowVersion)
		}
		detail := map[string]any{"before": map[string]any{"status": r.Status}, "after": map[string]any{"status": next}}
		switch action {
		case ActionLock:
			if issues := st.completionIssues(r.Title, r.ExtraFields); len(issues) > 0 {
				return fieldsError(issues)
			}
			stats, err := itemPageStats(ctx, tx, []uuid.UUID{id})
			if err != nil {
				return err
			}
			kind, msg, _, err := lockBlocker(ctx, tx, st, r, stats[id])
			if err != nil {
				return err
			}
			if kind != "" {
				return invalidState(msg)
			}
			detail["archive_no"], detail["page_count"] = r.ArchiveNo, stats[id].Pages
			detail["lock_requires_approved"] = st.lockRequiresApproved
		case ActionUnlock:
			detail["archive_no"] = r.ArchiveNo
		}
		if err := setItemsStatus(ctx, tx, []uuid.UUID{id}, next, operatorID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveItem, TargetID: id, Action: action,
			Detail: detail}); err != nil {
			return err
		}
		if out, err = readItem(ctx, tx, st, id); err != nil {
			return err
		}
		ev.Add(events.FoldersChanged(projectID, sourceIDs(r)...))
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{r.CategoryID}, []uuid.UUID{id}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LockResult 是批量锁定的结果。
type LockResult struct {
	Locked int `json:"locked"`
	// Skipped 是原来就锁定了、跳过的个数。
	Skipped int `json:"skipped"`
	// ItemIDs 是这次锁定的条目（按 id 排）。
	ItemIDs []uuid.UUID `json:"item_ids"`
}

func dedupSorted(ids []uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(out)
}

// LockItems 批量锁定（一个事务）。
func (s *Service) LockItems(ctx context.Context, operatorID, projectID uuid.UUID, ids []uuid.UUID) (*LockResult, error) {
	if len(ids) == 0 {
		return nil, httpx.Invalid("至少要选一个条目")
	}
	if len(ids) > MaxLockBatch {
		return nil, httpx.Invalid("一次最多锁定 %d 个条目，这次有 %d 个", MaxLockBatch, len(ids))
	}
	uniq := dedupSorted(ids)
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	out := &LockResult{ItemIDs: []uuid.UUID{}}
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		rows, err := lockItems(ctx, tx, projectID, uniq)
		if err != nil {
			return err
		}
		found := map[uuid.UUID]bool{}
		for _, r := range rows {
			found[r.ID] = true
		}
		var missing []uuid.UUID
		for _, id := range ids {
			if !found[id] && !slices.Contains(missing, id) {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return httpx.NewError(404, httpx.CodeNotFound, fmt.Sprintf("有 %d 个条目不存在或已删除", len(missing))).
				WithDetails(map[string]any{"ids": missing})
		}
		var todo []*itemRow
		for _, r := range rows {
			if r.Status == statusLocked {
				out.Skipped++
			} else {
				todo = append(todo, r)
			}
		}
		if len(todo) == 0 {
			return nil
		}
		todoIDs := make([]uuid.UUID, len(todo))
		for i, r := range todo {
			todoIDs[i] = r.ID
		}
		stats, err := itemPageStats(ctx, tx, todoIDs)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		var failed []uuid.UUID
		example := ""
		for _, r := range todo {
			kind, msg, _, err := lockBlocker(ctx, tx, st, r, stats[r.ID])
			if err != nil {
				return err
			}
			if kind == "" {
				continue
			}
			counts[kind]++
			failed = append(failed, r.ID)
			if example == "" {
				example = fmt.Sprintf("；例如第 %d 件（%s）：%s", r.ItemNo, titleOrPlaceholder(r.Title), msg)
			}
		}
		if len(failed) > 0 {
			var parts []string
			for _, k := range blockOrder {
				if counts[k] > 0 {
					parts = append(parts, fmt.Sprintf("%d 个%s", counts[k], k))
				}
			}
			return httpx.Conflict(httpx.CodeInvalidState, fmt.Sprintf("有 %d 个条目不能锁定（%s），这次一个都没有锁定%s",
				len(failed), strings.Join(parts, "，"), example)).WithDetails(map[string]any{"ids": failed})
		}
		if err := setItemsStatus(ctx, tx, todoIDs, statusLocked, operatorID); err != nil {
			return err
		}
		entries := make([]auditEntry, len(todo))
		catIDs := make([]uuid.UUID, 0, len(todo))
		for i, r := range todo {
			entries[i] = auditEntry{TargetType: audit.TargetArchiveItem, TargetID: r.ID, Action: "lock",
				Detail: map[string]any{"before": map[string]any{"status": r.Status}, "after": map[string]any{"status": statusLocked},
					"archive_no": r.ArchiveNo, "page_count": stats[r.ID].Pages,
					"lock_requires_approved": st.lockRequiresApproved, "batch": true}}
			catIDs = append(catIDs, r.CategoryID)
		}
		if err := insertAudits(ctx, tx, projectID, operatorID, entries); err != nil {
			return err
		}
		out.Locked, out.ItemIDs = len(todo), todoIDs
		ev.Add(events.FoldersChanged(projectID, sourceIDs(todo...)...))
		ev.Add(events.CatalogChanged(projectID, catIDs, todoIDs))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RenumberResult 是按新模板重新生成档号的结果。
type RenumberResult struct {
	// CategoryID 是范围：这个节点的子树（整个项目时是根节点）。
	CategoryID         uuid.UUID `json:"category_id"`
	Renumbered         int       `json:"renumbered"`
	SkippedLocked      int       `json:"skipped_locked"`
	ArchiveNoConflicts int       `json:"archive_no_conflicts"`
}

// Renumber 按项目现在的模板重新生成 categoryID 子树（nil 是整个项目）里未锁定条目的档号（归类树排他锁）。
func (s *Service) Renumber(ctx context.Context, operatorID, projectID uuid.UUID, categoryID *uuid.UUID) (*RenumberResult, error) {
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *RenumberResult
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, true); err != nil {
			return err
		}
		var cat *catRow
		var err error
		if categoryID == nil {
			cat, err = rootCategory(ctx, tx, projectID)
		} else {
			cat, err = getCategory(ctx, tx, projectID, *categoryID)
		}
		if err != nil {
			return err
		}
		if cat == nil {
			return errCategoryNotFound
		}
		rr, err := renumberSubtree(ctx, tx, projectID, operatorID, cat.PathCode, st,
			map[string]any{"category_id": cat.ID, "changes": []string{"template"}}, "重新生成档号")
		if err != nil {
			return err
		}
		out = &RenumberResult{CategoryID: cat.ID, Renumbered: len(rr.Changed), SkippedLocked: rr.SkippedLocked,
			ArchiveNoConflicts: rr.Conflicts}
		if len(rr.Changed) == 0 {
			return nil
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveCategory, TargetID: cat.ID, Action: "renumber",
			Detail: map[string]any{"after": map[string]any{"template": st.template, "renumbered": out.Renumbered,
				"skipped_locked": out.SkippedLocked, "archive_no_conflicts": out.ArchiveNoConflicts}}}); err != nil {
			return err
		}
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{cat.ID}, rr.Changed))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
