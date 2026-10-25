package review

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/auth"
	"doclite/server/internal/file"
	"doclite/server/internal/folder"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/platform/xslices"
	"doclite/server/internal/project"
)

// 审核（审核员，review）、批量通过、撤回通过、审核设置。

// Settings 是 `GET /api/projects/{pid}/review-settings` 的响应。
type Settings struct {
	// RejectReasons：内置的是代码（blur……），自定义的是中文；库里存的是空列表或不对时按默认的 8 个。
	RejectReasons   []string `json:"reject_reasons"`
	RescanReassign  string   `json:"rescan_reassign"`
	CommentMaxChars int      `json:"comment_max_chars"`
	MaxBatchFiles   int      `json:"max_batch_files"`
	MaxFolderFiles  int      `json:"max_folder_files"`
}

// Settings 读项目的审核设置（项目授权已经查过）。
func (s *Service) Settings(ctx context.Context, projectID uuid.UUID) (*Settings, error) {
	p, err := project.ReadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	return &Settings{RejectReasons: p.RejectReasons, RescanReassign: p.RescanReassign, CommentMaxChars: MaxCommentChars,
		MaxBatchFiles: httpx.MaxBatchIDs, MaxFolderFiles: MaxFolderFiles}, nil
}

// ReviewInput 是 `POST /api/files/{fid}/review` 的请求。
type ReviewInput struct {
	Result        string   `json:"result"`
	RejectReasons []string `json:"reject_reasons"`
	Comment       *string  `json:"comment"`
	// SnapshotID 是看到的快照；带了而且不是当前版本 409 conflict。
	SnapshotID *uuid.UUID `json:"snapshot_id"`
}

// ReviewedFile 是审核的响应（批量通过的每一项也是它）。
type ReviewedFile struct {
	FileID       uuid.UUID `json:"file_id"`
	FolderID     uuid.UUID `json:"folder_id"`
	AssignmentID uuid.UUID `json:"assignment_id"`
	Result       string    `json:"result"`
	ReviewStatus string    `json:"review_status"`
	SnapshotID   uuid.UUID `json:"snapshot_id"`
	VersionNo    int       `json:"version_no"`
	ReviewRound  int       `json:"review_round"`
	ReviewedAt   time.Time `json:"reviewed_at"`
}

// checkInput 校验审核的请求（400）：驳回至少一个原因、只能从项目列表里选（区分大小写，重复的算一个），个数不超过列表
// 长度和 64；选了 other 时说明必填；通过不能带原因。返回去重后的原因和规整后的说明。
func checkInput(in *ReviewInput, settings *Settings) ([]string, *string, error) {
	comment, err := names.OptionalText(in.Comment, MaxCommentChars, "说明")
	if err != nil {
		return nil, nil, httpx.Invalid("%s", err)
	}
	switch in.Result {
	case resultApproved:
		if len(in.RejectReasons) > 0 {
			return nil, nil, httpx.Invalid("通过时不能带驳回原因")
		}
		return []string{}, comment, nil
	case resultRejected:
	default:
		return nil, nil, httpx.Invalid(`result 只能是 "approved" 或 "rejected"`)
	}
	if len(in.RejectReasons) == 0 {
		return nil, nil, httpx.Invalid("驳回至少要选一个原因")
	}
	if len(in.RejectReasons) > len(settings.RejectReasons) || len(in.RejectReasons) > maxReasons {
		return nil, nil, httpx.Invalid("驳回原因太多了")
	}
	var reasons []string
	for _, r := range in.RejectReasons {
		if !slices.Contains(settings.RejectReasons, r) {
			return nil, nil, httpx.Invalid("驳回原因「%s」不在项目的原因列表里", r)
		}
		if !slices.Contains(reasons, r) {
			reasons = append(reasons, r)
		}
	}
	if slices.Contains(reasons, "other") && comment == nil {
		return nil, nil, httpx.Invalid("选了「其他」时要写说明")
	}
	return reasons, comment, nil
}

// Review 审核一个文件：通过或驳回。检查顺序：参数 400 → 不是待审核 409 invalid_state → open 的那一行不是自己的
// 403 not_assignee（负责人、admin 也一样）→ 带的快照不是当前版本 409 conflict。
func (s *Service) Review(ctx context.Context, userID, projectID, fileID uuid.UUID, in ReviewInput) (*ReviewedFile, error) {
	settings, err := s.Settings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	reasons, comment, err := checkInput(&in, settings)
	if err != nil {
		return nil, err
	}
	var out *ReviewedFile
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		locked, err := s.lockFiles(ctx, tx, projectID, []uuid.UUID{fileID})
		if err != nil {
			return err
		}
		f := locked.byID[fileID]
		if f == nil {
			return errFileNotFound
		}
		if f.Status != folder.ReviewPending {
			return httpx.InvalidState("文件不是待审核的（可能已经审过、被撤回或改派），请刷新")
		}
		if f.Open == nil || f.Open.ReviewerID != userID {
			return notAssignee("这个文件没有分给你（可能刚被改派给别人），请刷新待办")
		}
		if in.SnapshotID != nil && *in.SnapshotID != f.SnapshotID {
			return httpx.Conflict("", "你看到的不是这个文件现在的版本（已经有 v"+strconv.Itoa(f.VersionNo)+"），请重新打开再审")
		}
		items, err := s.finish(ctx, tx, projectID, userID, []*fileState{f}, in.Result, reasons, comment, false)
		if err != nil {
			return err
		}
		out = &items[0]
		ev.Add(filesChanged(projectID, []*fileState{f}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// finish 写审核结果：open 的那一行 done（结果、当前快照、原因、说明、reviewed_at），文件 → approved / rejected，
// 记 approve / reject。files 已锁。
func (s *Service) finish(ctx context.Context, tx pgx.Tx, projectID, userID uuid.UUID, files []*fileState, result string,
	reasons []string, comment *string, batch bool) ([]ReviewedFile, error) {
	if err := s.withPaths(ctx, tx, files); err != nil {
		return nil, err
	}
	rows := make([]finished, len(files))
	ids := make([]uuid.UUID, len(files))
	for i, f := range files {
		rows[i] = finished{ID: f.Open.ID, SnapshotID: f.SnapshotID}
		ids[i] = f.FileID
	}
	reviewer := files[0].Open.ReviewerName
	logs := make([]file.Log, len(files))
	out := make([]ReviewedFile, len(files))
	for i, f := range files {
		d := withFile(f, map[string]any{"reviewer": reviewer})
		action := file.ActionApprove
		if result == resultRejected {
			action = file.ActionReject
			d["reasons"] = reasons
			d["comment"] = comment
		} else if comment != nil {
			d["comment"] = *comment
		}
		if batch {
			d["batch"] = true
		}
		logs[i] = file.Log{ProjectID: projectID, FileID: f.FileID, Action: action, OperatorID: userID, Detail: d}
		out[i] = ReviewedFile{FileID: f.FileID, FolderID: f.FolderID, AssignmentID: f.Open.ID, Result: result,
			ReviewStatus: result, SnapshotID: f.SnapshotID, VersionNo: f.VersionNo, ReviewRound: f.Round}
	}
	// 分配行、文件的审核状态、日志一次往返写。
	b := &pgx.Batch{}
	var at time.Time
	queueFinishReviews(b, rows, result, reasons, comment, &at)
	s.folders.QueueReviewStatus(b, ids, result)
	if err := file.QueueLogs(b, logs...); err != nil {
		return nil, err
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ReviewedAt = at
	}
	return out, nil
}

// ApproveManyResult 是批量通过的响应。
type ApproveManyResult struct {
	Approved int `json:"approved"`
	// Items 按所在文件夹 path_code、sort_no 排。
	Items []ReviewedFile `json:"items"`
}

// ApproveMany 批量通过（1–500 个，一个事务）：依次查 不存在、已删除、不在这个项目里 404 → 不是待审核 409 invalid_state
// → 不是分给自己的 403 not_assignee，有一类就整体不做（details.ids 是这一类的全部 file_id）。
func (s *Service) ApproveMany(ctx context.Context, userID, projectID uuid.UUID, fileIDs []uuid.UUID) (*ApproveManyResult, error) {
	if len(fileIDs) == 0 || len(fileIDs) > httpx.MaxBatchIDs {
		return nil, httpx.Invalid("批量通过一次 1–%d 个", httpx.MaxBatchIDs)
	}
	ids := xslices.Unique(fileIDs)
	var out *ApproveManyResult
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		locked, err := s.lockFiles(ctx, tx, projectID, ids)
		if err != nil {
			return err
		}
		if missing := locked.missing(ids); len(missing) > 0 {
			return notFoundFiles(missing)
		}
		var notPending, notMine []uuid.UUID
		for _, f := range locked.list {
			switch {
			case f.Status != folder.ReviewPending:
				notPending = append(notPending, f.FileID)
			case f.Open == nil || f.Open.ReviewerID != userID:
				notMine = append(notMine, f.FileID)
			}
		}
		if len(notPending) > 0 {
			return httpx.InvalidState("有 " + strconv.Itoa(len(notPending)) + " 个文件不是待审核的（可能已经审过或被改派），请刷新后重试").
				WithDetails(map[string]any{"ids": notPending})
		}
		if len(notMine) > 0 {
			return notAssignee("有 " + strconv.Itoa(len(notMine)) + " 个文件没有分给你（可能刚被改派给别人），请刷新待办").
				WithDetails(map[string]any{"ids": notMine})
		}
		items, err := s.finish(ctx, tx, projectID, userID, locked.list, resultApproved, nil, nil, true)
		if err != nil {
			return err
		}
		out = &ApproveManyResult{Approved: len(items), Items: items}
		ev.Add(filesChanged(projectID, locked.list))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReopenResult 是撤回通过的响应：重新分给的审核人；他已不能再审时为 null（改为待分配）。
type ReopenResult struct {
	FileID       uuid.UUID  `json:"file_id"`
	FolderID     uuid.UUID  `json:"folder_id"`
	ReviewStatus string     `json:"review_status"`
	ReviewRound  int        `json:"review_round"`
	AssignmentID *uuid.UUID `json:"assignment_id"`
	ReviewerID   *uuid.UUID `json:"reviewer_id"`
}

// Reopen 撤回通过：不是已通过 409 invalid_state；只有 review 菜单的人只能撤回自己通过的（最近一次通过的那一行），
// 否则 403 not_assignee；有 review_assign 的能撤回任何人通过的。approved → pending，给最近一次通过它的人新增一行 open
// （assigned_by 沿用那一行）；那人不能再审（停用、没有 review 菜单、失去授权）时不分配、→ unassigned，日志带
// skipped_reviewer。轮次不变。
func (s *Service) Reopen(ctx context.Context, p *auth.Principal, projectID, fileID uuid.UUID) (*ReopenResult, error) {
	canAny := p.HasMenu(auth.MenuReviewAssign)
	var out *ReopenResult
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		locked, err := s.lockFiles(ctx, tx, projectID, []uuid.UUID{fileID})
		if err != nil {
			return err
		}
		f := locked.byID[fileID]
		if f == nil {
			return errFileNotFound
		}
		if f.Status != folder.ReviewApproved {
			return httpx.InvalidState("文件不是已通过的（可能刚被撤回），请刷新")
		}
		last, err := latestResult(ctx, tx, fileID, resultApproved)
		if err != nil {
			return err
		}
		if !canAny && (last == nil || last.ReviewerID != p.UserID) {
			return notAssignee("只能撤回自己通过的文件")
		}
		if err := s.withPaths(ctx, tx, []*fileState{f}); err != nil {
			return err
		}
		next, err := s.reassignBack(ctx, tx, projectID, f, last)
		if err != nil {
			return err
		}
		if err := s.folders.SetReviewStatus(ctx, tx, []uuid.UUID{fileID}, next.status); err != nil {
			return err
		}
		d := withFile(f, map[string]any{"by": p.DisplayName})
		next.describe(d)
		if err := file.WriteLogs(ctx, tx, file.Log{ProjectID: projectID, FileID: fileID, Action: file.ActionReopen,
			OperatorID: p.UserID, Detail: d}); err != nil {
			return err
		}
		out = &ReopenResult{FileID: fileID, FolderID: f.FolderID, ReviewStatus: next.status, ReviewRound: f.Round,
			AssignmentID: next.assignmentID, ReviewerID: next.reviewerID}
		ev.Add(filesChanged(projectID, []*fileState{f}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// backTo 是撤回通过、重扫之后文件分给谁。
type backTo struct {
	status       string
	assignmentID *uuid.UUID
	reviewerID   *uuid.UUID
	reviewerName *string
	// skipped 是原审核人（不能再审，没有分给他）的显示名。
	skipped *string
}

// describe 往日志 detail 里写 reviewer、reviewer_id，没分出去时另写 skipped_reviewer。
func (b *backTo) describe(d map[string]any) {
	d["reviewer"] = b.reviewerName
	d["reviewer_id"] = b.reviewerID
	if b.skipped != nil {
		d["skipped_reviewer"] = *b.skipped
	}
}

// reassignBack 把文件分回给 last 那一行的审核人（撤回通过、重扫，requirements 5.8）：
//   - 已经有 open 的分配（数据异常）：不新增，待审核，审核人是那一行的；
//   - last 为空（找不到通过、驳回记录）：不分配，待分配；
//   - 那人仍能审（启用、有 review 菜单、能进项目）：新增 open（assigned_by 沿用 last），待审核；
//   - 否则不分配、待分配，记下 skipped。
func (s *Service) reassignBack(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, f *fileState, last *resultRow) (*backTo, error) {
	if f.Open != nil {
		return &backTo{status: folder.ReviewPending, assignmentID: &f.Open.ID, reviewerID: &f.Open.ReviewerID,
			reviewerName: &f.Open.ReviewerName}, nil
	}
	if last == nil {
		return &backTo{status: folder.ReviewUnassigned}, nil
	}
	facts, err := reviewerFactsOf(ctx, tx, projectID, []uuid.UUID{last.ReviewerID})
	if err != nil {
		return nil, err
	}
	if fa := facts[last.ReviewerID]; fa == nil || !fa.canReview() {
		return &backTo{status: folder.ReviewUnassigned, skipped: &last.ReviewerName}, nil
	}
	id := newIDs(1)[0]
	if err := insertOpen(ctx, tx, projectID, []newAssignment{{ID: id, FileID: f.FileID, ReviewerID: last.ReviewerID,
		AssignedBy: last.AssignedBy}}); err != nil {
		return nil, err
	}
	return &backTo{status: folder.ReviewPending, assignmentID: &id, reviewerID: &last.ReviewerID,
		reviewerName: &last.ReviewerName}, nil
}
