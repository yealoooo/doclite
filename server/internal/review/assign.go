package review

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/file"
	"doclite/server/internal/folder"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// 分配和改派（负责人，review_assign）。

// AssignInput 是分配的请求：folder_id（子树里全部待分配的文件）和 file_ids（1–500 个）二选一。
type AssignInput struct {
	ReviewerID *uuid.UUID  `json:"reviewer_id"`
	FolderID   *uuid.UUID  `json:"folder_id"`
	FileIDs    []uuid.UUID `json:"file_ids"`
}

// AssignResult 是分配的响应。
type AssignResult struct {
	Assigned int `json:"assigned"`
	// Skipped：按文件时是选中的里不是待分配的（重复的只算一个）；按文件夹时是子树里不是待分配的文件数。
	Skipped int `json:"skipped"`
	// FileIDs 是这次分配了的文件（按所在文件夹 path_code、sort_no 排）。
	FileIDs []uuid.UUID `json:"file_ids"`
}

// Assign 把待分配的文件分给审核人：新增 open、→ pending、记 assign_review。检查顺序：参数 400（含审核人不能审）→
// 文件夹 404 / 选中的文件有不在的 404（details.ids）；按文件夹超过 1 万个待分配的 400。不是待分配的跳过计数。
func (s *Service) Assign(ctx context.Context, userID, projectID uuid.UUID, in AssignInput) (*AssignResult, error) {
	if in.ReviewerID == nil {
		return nil, httpx.Invalid("缺少 reviewer_id")
	}
	byFolder := in.FolderID != nil
	if byFolder == (in.FileIDs != nil) {
		return nil, httpx.Invalid("folder_id 和 file_ids 要给一个，而且只能给一个")
	}
	if !byFolder && (len(in.FileIDs) == 0 || len(in.FileIDs) > MaxBatchFiles) {
		return nil, httpx.Invalid("按选中的文件分配一次 1–%d 个", MaxBatchFiles)
	}
	reviewer, err := s.eligibility(ctx, projectID, *in.ReviewerID)
	if err != nil {
		return nil, err
	}
	out := &AssignResult{FileIDs: []uuid.UUID{}}
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		var scope *scopeFolder
		var candidates []uuid.UUID
		if byFolder {
			var err error
			if scope, err = getScopeFolder(ctx, tx, projectID, in.FolderID); err != nil {
				return err
			}
			if scope == nil {
				return errFolderNotFound
			}
			if candidates, err = subtreeUnassigned(ctx, tx, projectID, scope, MaxFolderFiles+1); err != nil {
				return err
			}
			if len(candidates) > MaxFolderFiles {
				return httpx.Invalid("这个文件夹（含子文件夹）待分配的文件超过 %d 个，请选子文件夹分别分配", MaxFolderFiles)
			}
		} else {
			candidates = dedup(in.FileIDs)
		}
		locked, err := s.lockFilesHeld(ctx, tx, projectID, candidates)
		if err != nil {
			return err
		}
		if !byFolder {
			if missing := locked.missing(candidates); len(missing) > 0 {
				return notFoundIDs(missing)
			}
		}
		var picked []*fileState
		for _, f := range locked.list {
			if f.Status == folder.ReviewUnassigned && f.Open == nil && (scope == nil || scope.inSubtree(f.PathCode)) {
				picked = append(picked, f)
			}
		}
		if byFolder {
			n, err := subtreeNotUnassigned(ctx, tx, projectID, scope)
			if err != nil {
				return err
			}
			out.Skipped = n
		} else {
			out.Skipped = len(candidates) - len(picked)
		}
		if len(picked) == 0 {
			return nil
		}
		if err := s.withPaths(ctx, tx, picked); err != nil {
			return err
		}
		ids := newIDs(len(picked))
		rows := make([]newAssignment, len(picked))
		fileIDs := make([]uuid.UUID, len(picked))
		logs := make([]file.Log, len(picked))
		for i, f := range picked {
			rows[i] = newAssignment{ID: ids[i], FileID: f.FileID, ReviewerID: reviewer.ID, AssignedBy: userID}
			fileIDs[i] = f.FileID
			logs[i] = file.Log{ProjectID: projectID, FileID: f.FileID, Action: file.ActionAssignReview, OperatorID: userID,
				Detail: withFile(f, map[string]any{"reviewer": reviewer.DisplayName, "reviewer_id": reviewer.ID})}
		}
		if err := insertOpen(ctx, tx, projectID, rows); err != nil {
			return err
		}
		if err := s.folders.SetReviewStatus(ctx, tx, fileIDs, folder.ReviewPending); err != nil {
			return err
		}
		if err := file.WriteLogs(ctx, tx, logs...); err != nil {
			return err
		}
		out.Assigned, out.FileIDs = len(picked), fileIDs
		ev.Add(filesChanged(projectID, picked))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// withFile 给日志 detail 加上文件当时的名字、文件夹路径、版本号、轮次（审核动作都带）。
func withFile(f *fileState, d map[string]any) map[string]any {
	d["file_name"] = f.FileName
	d["folder_path"] = f.FolderPath
	d["version_no"] = f.VersionNo
	d["review_round"] = f.Round
	return d
}

// ReassignOneResult 是单个改派的响应。
type ReassignOneResult struct {
	// AssignmentID 是新的那一行（没有改时是原来那一行）。
	AssignmentID   uuid.UUID `json:"assignment_id"`
	FileID         uuid.UUID `json:"file_id"`
	FolderID       uuid.UUID `json:"folder_id"`
	ReviewerID     uuid.UUID `json:"reviewer_id"`
	FromReviewerID uuid.UUID `json:"from_reviewer_id"`
	// Changed：原来就分给这个人时为 false（不写库、不写日志、不发事件）。
	Changed bool `json:"changed"`
}

// ProjectOfAssignment 是分配所在的项目（路由的项目授权检查用）：分配不存在，或文件已删除（含所在文件夹、项目）404。
func (s *Service) ProjectOfAssignment(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	_, pid, ok, err := assignmentFile(ctx, s.pool, id)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, errAssignmentNotFound
	}
	return pid, nil
}

// ReassignOne 改派一行 open 的分配：那一行已经不是 open、或文件不是待审核 409 invalid_state；原来就分给这个人时
// 200 changed:false。旧行 cancelled、新增 open（assigned_by 是操作人），记 reassign_review。
func (s *Service) ReassignOne(ctx context.Context, userID, projectID, assignmentID uuid.UUID, reviewerID *uuid.UUID) (*ReassignOneResult, error) {
	if reviewerID == nil {
		return nil, httpx.Invalid("缺少 reviewer_id")
	}
	to, err := s.eligibility(ctx, projectID, *reviewerID)
	if err != nil {
		return nil, err
	}
	fileID, _, ok, err := assignmentFile(ctx, s.pool, assignmentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errAssignmentNotFound
	}
	var out *ReassignOneResult
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		locked, err := s.lockFiles(ctx, tx, projectID, []uuid.UUID{fileID})
		if err != nil {
			return err
		}
		f := locked.byID[fileID]
		if f == nil {
			return errFileNotFound
		}
		from, status, err := assignmentRow(ctx, tx, assignmentID)
		if err != nil {
			return err
		}
		if status != statusOpen || f.Status != folder.ReviewPending || f.Open == nil || f.Open.ID != assignmentID {
			return invalidState("这一行分配已经不是待审核的了（可能刚被审完、改派或撤回），请刷新")
		}
		out = &ReassignOneResult{AssignmentID: assignmentID, FileID: fileID, FolderID: f.FolderID, ReviewerID: to.ID,
			FromReviewerID: from}
		if from == to.ID {
			return nil
		}
		if err := s.withPaths(ctx, tx, []*fileState{f}); err != nil {
			return err
		}
		newID := newIDs(1)[0]
		if err := s.reassign(ctx, tx, projectID, userID, []*fileState{f}, []uuid.UUID{newID}, to); err != nil {
			return err
		}
		out.AssignmentID, out.Changed = newID, true
		ev.Add(filesChanged(projectID, []*fileState{f}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reassign 把这些文件 open 的那一行取消、给 to 新增一行 open，记 reassign_review。files 已锁、路径已填。
func (s *Service) reassign(ctx context.Context, tx pgx.Tx, projectID, userID uuid.UUID, files []*fileState, newIDs []uuid.UUID,
	to *reviewerFacts) error {
	old := make([]uuid.UUID, len(files))
	rows := make([]newAssignment, len(files))
	logs := make([]file.Log, len(files))
	for i, f := range files {
		old[i] = f.Open.ID
		rows[i] = newAssignment{ID: newIDs[i], FileID: f.FileID, ReviewerID: to.ID, AssignedBy: userID}
		logs[i] = file.Log{ProjectID: projectID, FileID: f.FileID, Action: file.ActionReassignReview, OperatorID: userID,
			Detail: withFile(f, map[string]any{"from": f.Open.ReviewerName, "from_id": f.Open.ReviewerID,
				"to": to.DisplayName, "to_id": to.ID})}
	}
	if err := cancelOpen(ctx, tx, old); err != nil {
		return err
	}
	if err := insertOpen(ctx, tx, projectID, rows); err != nil {
		return err
	}
	return file.WriteLogs(ctx, tx, logs...)
}

// ReassignManyInput 是批量改派的请求：file_ids（1–500 个），或 from_reviewer_id + folder_id（某人在某子树下的全部待审，
// ≤ 1 万个）。
type ReassignManyInput struct {
	ToReviewerID   *uuid.UUID  `json:"to_reviewer_id"`
	FileIDs        []uuid.UUID `json:"file_ids"`
	FromReviewerID *uuid.UUID  `json:"from_reviewer_id"`
	FolderID       *uuid.UUID  `json:"folder_id"`
}

// ReassignResult 是批量改派的响应。
type ReassignResult struct {
	Reassigned int         `json:"reassigned"`
	Skipped    int         `json:"skipped"`
	FileIDs    []uuid.UUID `json:"file_ids"`
}

// ReassignMany 批量改派：只改派待审核的 open 分配；不是待审核、没有 open、已经分给目标的（按子树时还有不是原审核人的、
// 已经不在子树里的）跳过计数。选中的文件有不在的整体 404（details.ids）。
func (s *Service) ReassignMany(ctx context.Context, userID, projectID uuid.UUID, in ReassignManyInput) (*ReassignResult, error) {
	if in.ToReviewerID == nil {
		return nil, httpx.Invalid("缺少 to_reviewer_id")
	}
	bySubtree := in.FromReviewerID != nil || in.FolderID != nil
	switch {
	case bySubtree && in.FileIDs != nil:
		return nil, httpx.Invalid("file_ids 和 from_reviewer_id + folder_id 只能给一种")
	case bySubtree && (in.FromReviewerID == nil || in.FolderID == nil):
		return nil, httpx.Invalid("按子树改派要同时给 from_reviewer_id 和 folder_id")
	case !bySubtree && (len(in.FileIDs) == 0 || len(in.FileIDs) > MaxBatchFiles):
		return nil, httpx.Invalid("按选中的文件改派一次 1–%d 个", MaxBatchFiles)
	case bySubtree && *in.FromReviewerID == *in.ToReviewerID:
		return nil, httpx.Invalid("原审核人和新审核人是同一个人")
	}
	to, err := s.eligibility(ctx, projectID, *in.ToReviewerID)
	if err != nil {
		return nil, err
	}
	out := &ReassignResult{FileIDs: []uuid.UUID{}}
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		var scope *scopeFolder
		var candidates []uuid.UUID
		if bySubtree {
			var err error
			if scope, err = getScopeFolder(ctx, tx, projectID, in.FolderID); err != nil {
				return err
			}
			if scope == nil {
				return errFolderNotFound
			}
			if candidates, err = subtreeOpenOf(ctx, tx, projectID, *in.FromReviewerID, scope, MaxFolderFiles+1); err != nil {
				return err
			}
			if len(candidates) > MaxFolderFiles {
				return httpx.Invalid("这个人在这个文件夹（含子文件夹）下的待审超过 %d 个，请选子文件夹分别改派", MaxFolderFiles)
			}
		} else {
			candidates = dedup(in.FileIDs)
		}
		locked, err := s.lockFilesHeld(ctx, tx, projectID, candidates)
		if err != nil {
			return err
		}
		if !bySubtree {
			if missing := locked.missing(candidates); len(missing) > 0 {
				return notFoundIDs(missing)
			}
		}
		var picked []*fileState
		for _, f := range locked.list {
			if f.Status != folder.ReviewPending || f.Open == nil || f.Open.ReviewerID == to.ID {
				continue
			}
			if scope != nil && (f.Open.ReviewerID != *in.FromReviewerID || !scope.inSubtree(f.PathCode)) {
				continue
			}
			picked = append(picked, f)
		}
		out.Skipped = len(candidates) - len(picked)
		if len(picked) == 0 {
			return nil
		}
		if err := s.withPaths(ctx, tx, picked); err != nil {
			return err
		}
		if err := s.reassign(ctx, tx, projectID, userID, picked, newIDs(len(picked)), to); err != nil {
			return err
		}
		out.Reassigned = len(picked)
		for _, f := range picked {
			out.FileIDs = append(out.FileIDs, f.FileID)
		}
		ev.Add(filesChanged(projectID, picked))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
