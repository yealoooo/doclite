package folder

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/catalog"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/xslices"
	"doclite/server/internal/project"
)

// 文件夹内调序（requirements 5.5，用户 2026-10-25）：sort_no 就是纸张顺序，归档按它排页。
// 请求带文件夹里全部未删除文件的新顺序（folder_files 的 id），一条 UPDATE 重排成 1..n。
// 加锁：文件树共享锁 → 文件夹行（FOR NO KEY UPDATE），锁住之后查封存、读清单（和上传、移入移出、删除、归档读清单互斥；
// 不锁 files 行：只改 sort_no，不碰审核、归类）。
// 记一条 folder / reorder 审计（只记挪动过的文件，算法同重新归档的"调序"：最长递增子序列之外的）；提交后发 files.changed。

// MaxReorderFiles 是一次调序最多的文件数（和删除文件夹的上限相同）。
const MaxReorderFiles = MaxDeleteFiles

// ReorderFiles 把文件夹 folderID 里未删除的文件按 ids 的顺序排（sort_no 1..n）。ids 要正好是文件夹里现在的文件
// （否则 409 conflict，界面刷新后再调）；空、重复、太多 400；文件夹封存 409 folder_archived；顺序没变不写库、不审计、
// 不发事件。
func (s *Service) ReorderFiles(ctx context.Context, userID, projectID, folderID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return httpx.Invalid("folder_file_ids 不能为空")
	}
	if len(ids) > MaxReorderFiles {
		return httpx.Invalid("文件夹里的文件超过 %d 个，不能调顺序", MaxReorderFiles)
	}
	if len(xslices.Unique(ids)) != len(ids) {
		return httpx.Invalid("folder_file_ids 里有重复的")
	}
	return s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		locked, err := lockFolders(ctx, tx, folderID)
		if err != nil {
			return err
		}
		f := locked[folderID]
		if f == nil || f.Deleted || f.ProjectID != projectID {
			return errFolderNotFound
		}
		if err := catalog.SealedCheck(ctx, tx, "调顺序", folderID); err != nil {
			return err
		}
		cur, err := folderOrder(ctx, tx, folderID)
		if err != nil {
			return err
		}
		changed := httpx.Conflict("", "文件夹里的文件有变化（可能别人刚上传、移动或删除了文件），请刷新后再调")
		if len(cur) != len(ids) {
			return changed
		}
		newPos := make(map[uuid.UUID]int, len(ids))
		for i, id := range ids {
			newPos[id] = i + 1
		}
		to := make([]int, len(cur)) // 现在的顺序里，每个文件的新位置
		for i, e := range cur {
			p, ok := newPos[e.ID]
			if !ok {
				return changed
			}
			to[i] = p
		}
		inLIS := xslices.LongestIncreasing(to)
		var moved []map[string]any
		var movedFiles []uuid.UUID
		for i, e := range cur {
			if !inLIS[i] {
				moved = append(moved, map[string]any{"file_id": e.FileID, "file_name": e.FileName, "from_page": i + 1, "to_page": to[i]})
				movedFiles = append(movedFiles, e.FileID)
			}
		}
		if len(moved) == 0 {
			return nil
		}
		if err := renumberFiles(ctx, tx, ids); err != nil {
			return err
		}
		path, err := file.FolderPath(ctx, tx, folderID)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(userID),
			TargetType: audit.TargetFolder, TargetID: folderID, Action: "reorder",
			Detail: map[string]any{"folder_name": f.FolderName, "folder_path": path, "file_count": len(cur),
				"moved": moved}}); err != nil {
			return err
		}
		ev.Add(events.FilesChanged(projectID, []uuid.UUID{folderID}, movedFiles))
		return nil
	})
}
