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

// 条目的状态（requirements 5.9 的状态表，用户决定 Q5）：
//
//	动作 \ 状态              draft                 done                 locked
//	改著录、移动、改件号、删除  可以                  可以，保持 done       409 item_locked
//	标记完成 / 退回草稿        → done（查必填）/ 409   409 / → draft        409 item_locked
//	定稿 / 解锁               409 invalid_state     → locked / 409       409 / → done
//
// 定稿的前置条件：已完成、著录合格（400 + details.fields）、≥ 1 页、档号不空、lock_requires_approved 时全部页审核
// 通过（后三条 409 `invalid_state`，message 写明）。审核状态按定稿这一刻读，不锁文件（审核不等归类，归类也不等审核）。
// 批量定稿 1–500 个，已定稿的跳过，有一个不满足就整体不做（409，details.ids 列全部，message 按原因计数）。
// 审计 archive_item：mark_done、back_to_draft、lock（另带 archive_no、page_count、lock_requires_approved，批量带
// batch: true）、unlock（另带 archive_no）。"定稿后有新版本"用最近一条 lock 审计的时间，所以它不能省。

const (
	statusDraft  = "draft"
	statusDone   = "done"
	statusLocked = "locked"

	// MaxLockBatch 是批量定稿一次最多的条目数。
	MaxLockBatch = 500
)

// 单个条目的状态动作（路径的最后一段）。
const (
	ActionMarkDone    = "mark-done"
	ActionBackToDraft = "back-to-draft"
	ActionLock        = "lock"
	ActionUnlock      = "unlock"
)

func statusLabel(s string) string {
	switch s {
	case statusDraft:
		return "草稿"
	case statusDone:
		return "已完成"
	case statusLocked:
		return "已定稿"
	}
	return s
}

func errLockedEdit(what string) error {
	return httpx.Conflict("item_locked", fmt.Sprintf("条目已定稿，不能%s；要先解锁（需要项目负责人）", what))
}

func invalidState(msg string) error { return httpx.Conflict(httpx.CodeInvalidState, msg) }

// transition 是状态表：动作在这个状态下允许时返回新状态。
func transition(action, status string) (string, error) {
	switch action {
	case ActionMarkDone:
		switch status {
		case statusDraft:
			return statusDone, nil
		case statusDone:
			return "", invalidState("条目已经是“已完成”")
		}
		return "", errLockedEdit("标记完成")
	case ActionBackToDraft:
		switch status {
		case statusDone:
			return statusDraft, nil
		case statusDraft:
			return "", invalidState("条目已经是草稿")
		}
		return "", errLockedEdit("退回草稿")
	case ActionLock:
		switch status {
		case statusDone:
			return statusLocked, nil
		case statusDraft:
			return "", invalidState("条目还是草稿，要先“标记完成”才能定稿")
		}
		return "", invalidState("条目已经定稿了")
	case ActionUnlock:
		if status == statusLocked {
			return statusDone, nil
		}
		return "", invalidState("条目没有定稿，不用解锁")
	}
	return "", httpx.NotFound("动作")
}

// 定稿不满足的原因（批量定稿按它计数）。
const (
	blockNotDone     = "不是“已完成”"
	blockFields      = "著录不全或和现在的设置不符"
	blockNoPages     = "没有页"
	blockNoArchiveNo = "没有档号"
	blockUnapproved  = "有页没审核通过"
)

var blockOrder = []string{blockNotDone, blockFields, blockNoPages, blockNoArchiveNo, blockUnapproved}

// lockBlocker 是条目不能定稿的第一个原因（kind 为空表示可以定稿）。fields 是著录的问题（kind 为 blockFields 时）。
func lockBlocker(ctx context.Context, q db.Querier, st *settings, r *itemRow, ps pageStats) (kind, message string, fields []FieldIssue, err error) {
	if r.Status != statusDone {
		return blockNotDone, fmt.Sprintf("条目是“%s”，要先“标记完成”才能定稿", statusLabel(r.Status)), nil, nil
	}
	if issues := st.completionIssues(r.Title, r.ExtraFields); len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			msgs = append(msgs, is.Message)
		}
		return blockFields, "著录不全或和现在的设置不符：" + strings.Join(msgs, "；"), issues, nil
	}
	if ps.Pages == 0 {
		return blockNoPages, "条目还没有挂接任何页，不能定稿", nil, nil
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
		return blockNoArchiveNo, "条目没有档号，不能定稿" + why, nil, nil
	}
	if st.lockRequiresApproved && ps.NotApproved > 0 {
		return blockUnapproved, fmt.Sprintf("条目有 %d 页还没审核通过（例如第 %d 页），项目设置要求全部页审核通过才能定稿",
			ps.NotApproved, ps.FirstNotOK), nil, nil
	}
	return "", "", nil, nil
}

// ItemAction 是单个条目的状态动作：标记完成、退回草稿、定稿、解锁。
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
		next, err := transition(action, r.Status)
		if err != nil {
			return err
		}
		if r.RowVersion != rowVersion {
			return staleVersion(r.RowVersion)
		}
		detail := map[string]any{"before": map[string]any{"status": r.Status}, "after": map[string]any{"status": next}}
		switch action {
		case ActionMarkDone:
			if issues := st.completionIssues(r.Title, r.ExtraFields); len(issues) > 0 {
				return fieldsError(issues)
			}
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
			TargetType: audit.TargetArchiveItem, TargetID: id, Action: strings.ReplaceAll(action, "-", "_"),
			Detail: detail}); err != nil {
			return err
		}
		if out, err = readItem(ctx, tx, st, id); err != nil {
			return err
		}
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{r.CategoryID}, []uuid.UUID{id}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LockResult 是批量定稿的结果。
type LockResult struct {
	Locked int `json:"locked"`
	// Skipped 是原来就定稿了、跳过的个数。
	Skipped int `json:"skipped"`
	// ItemIDs 是这次定稿的条目（按 id 排）。
	ItemIDs []uuid.UUID `json:"item_ids"`
}

func dedupSorted(ids []uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(out)
}

// LockItems 批量定稿（一个事务）。
func (s *Service) LockItems(ctx context.Context, operatorID, projectID uuid.UUID, ids []uuid.UUID) (*LockResult, error) {
	if len(ids) == 0 {
		return nil, httpx.Invalid("至少要选一个条目")
	}
	if len(ids) > MaxLockBatch {
		return nil, httpx.Invalid("一次最多定稿 %d 个条目，这次有 %d 个", MaxLockBatch, len(ids))
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
			return httpx.Conflict(httpx.CodeInvalidState, fmt.Sprintf("有 %d 个条目不能定稿（%s），这次一个都没有定稿%s",
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

// Renumber 按项目现在的模板重新生成 categoryID 子树（nil 是整个项目）里未定稿条目的档号（归类树排他锁）。
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
