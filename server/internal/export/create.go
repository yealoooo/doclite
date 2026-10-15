package export

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"doclite/server/internal/audit"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
)

// 建任务（requirements 5.11"冻结"，用户决定 Q4）：一个 REPEATABLE READ 事务，第一条语句（读项目设置）定下快照，
// 之后的范围、条目、页、就绪的判断、每页当时的最新快照都来自这个快照；写 export_tasks（queued）、export_task_files
// （就绪条目的每页，页号 1..n）、background_jobs（queued，payload 是条目当时的状态，progress_total = 页数）、审计；
// 提交后发 export.changed 并叫醒执行器。
//
// 不拿树锁、不锁归类的行和 files 行：只插新行，外键检查对 archive_items、file_snapshots、projects、users 的行拿
// FOR KEY SHARE，和归类、审核的 FOR NO KEY UPDATE 不冲突。万一串行化失败（40001）整个重来，最多 3 次。

const createAttempts = 3

// Create 建导出任务，返回 201 的任务。
func (s *Service) Create(ctx context.Context, userID, projectID uuid.UUID, body *RequestBody) (*Task, error) {
	req, err := parseRequest(body)
	if err != nil {
		return nil, err
	}
	var taskID uuid.UUID
	for attempt := 1; ; attempt++ {
		r := *req
		taskID, err = s.create(ctx, userID, projectID, &r)
		var pgErr *pgconn.PgError
		if err != nil && errors.As(err, &pgErr) && pgErr.Code == "40001" && attempt < createAttempts {
			continue
		}
		break
	}
	if err != nil {
		return nil, err
	}
	s.events.Publish(events.ExportChanged(projectID, taskID))
	s.jobs.Wake()
	return s.Get(ctx, taskID)
}

func (s *Service) create(ctx context.Context, userID, projectID uuid.UUID, req *request) (uuid.UUID, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	sc, err := s.readScope(ctx, tx, projectID, req)
	if err != nil {
		return uuid.Nil, err
	}
	p := sc.precheck
	if err := checkCreatable(p, req.OnlyReady); err != nil {
		return uuid.Nil, err
	}

	// 就绪的条目（按导出顺序）和它们的页。
	var readyItems []*scopeItem
	for i, it := range sc.items {
		if sc.ready[i] {
			readyItems = append(readyItems, it)
		}
	}
	ids := make([]uuid.UUID, len(readyItems))
	for i, it := range readyItems {
		ids[i] = it.ID
	}
	pages, err := itemPageRows(ctx, tx, ids)
	if err != nil {
		return uuid.Nil, err
	}
	if len(pages) != p.ReadyPageCount {
		return uuid.Nil, errInternal("冻结的页数 %d 和预检的 %d 对不上", len(pages), p.ReadyPageCount)
	}
	fileIDs := make([]uuid.UUID, len(pages))
	for i, pg := range pages {
		fileIDs[i] = pg.FileID
	}
	snaps, err := file.CurrentOf(ctx, tx, fileIDs)
	if err != nil {
		return uuid.Nil, err
	}
	rows := make([]taskFileRow, len(pages))
	nextNo := map[uuid.UUID]int{}
	for i, pg := range pages {
		snap := snaps[pg.FileID]
		if snap == nil {
			return uuid.Nil, errInternal("文件 %s 没有快照", pg.FileID)
		}
		nextNo[pg.ItemID]++ // itemPageRows 按条目、页号排好了：页号重排成 1..n
		rows[i] = taskFileRow{ItemID: pg.ItemID, FileID: pg.FileID, SnapshotID: snap.ID, PageNo: nextNo[pg.ItemID]}
	}

	taskID, jobID := db.NewID(), db.NewID()
	pl := &payload{
		ExportTaskID: taskID, PayloadVersion: PayloadVersion, ResubmitOf: req.ResubmitOf, OnlyReady: req.OnlyReady,
		RequireLockedItem: sc.settings.requireLockedItem, ItemCount: len(readyItems), PageCount: len(rows),
		SkippedItemCount: p.NotReadyCount, ScopeCategories: sc.scope.scopeCategories, ItemFields: sc.settings.fields,
		Items: make([]frozenItem, len(readyItems)),
	}
	for i, it := range readyItems {
		pl.Items[i] = frozenItem{ID: it.ID, CategoryID: it.CategoryID, CategoryNames: it.CategoryNames, ItemNo: it.ItemNo,
			ArchiveNo: it.ArchiveNo, Title: it.Title, ExtraFields: it.ExtraFields, Status: it.Status, RowVersion: it.RowVersion,
			PageCount: it.PageCount}
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return uuid.Nil, err
	}
	if err := insertTask(ctx, tx, &taskInsert{ID: taskID, ProjectID: projectID, JobID: jobID, CreatedBy: userID,
		ScopeType: req.ScopeType, Format: string(req.Format), ScopeIDs: req.ScopeIDs, OnlyApproved: req.OnlyApproved}); err != nil {
		return uuid.Nil, err
	}
	if err := insertJob(ctx, tx, jobID, projectID, userID, raw, len(rows)); err != nil {
		return uuid.Nil, err
	}
	if err := insertTaskFiles(ctx, tx, taskID, rows); err != nil {
		return uuid.Nil, err
	}
	after := map[string]any{
		"scope_type": req.ScopeType, "scope_ids": req.ScopeIDs, "export_format": req.Format, "only_approved": req.OnlyApproved,
		"only_ready": req.OnlyReady, "require_locked_item": sc.settings.requireLockedItem, "item_count": len(readyItems),
		"page_count": len(rows), "skipped_item_count": p.NotReadyCount, "job_id": jobID, "status": StatusQueued,
	}
	if req.ResubmitOf != nil {
		after["resubmit_of"] = *req.ResubmitOf
	}
	if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(userID),
		TargetType: audit.TargetExportTask, TargetID: taskID, Action: "create", Detail: map[string]any{"after": after}}); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return taskID, nil
}
