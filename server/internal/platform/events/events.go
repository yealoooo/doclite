// Package events 是项目的实时事件（requirements 第 7 节）：进程内的事件总线 [Bus] 和 SSE 输出 [Serve]。
//
// 规则：
//   - 事件只带 id（`{type, project_id, folder_ids?, file_ids?, category_ids?, item_ids?, task_ids?}`），客户端收到后
//     自己重新取数据；
//   - **事务提交之后才发**：写操作用 [Bus.InTx]（或在 db.InTx 返回 nil 之后调 [Bus.Publish]），回滚时不发；
//   - 没有实际变化、改设置不发；
//   - 每个项目一组订阅者，每个订阅一个有缓冲的 channel；积压满了就丢掉积压的事件、换成一条 `resync`
//     （客户端整体刷新），发布永远不阻塞；
//   - 只在一个 server 进程内有效（硬性规则 16：单台服务器、单个进程）。
//
// 后面的模块这样发事件：
//
//	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, out *events.Pending) error {
//		…                                                // 写库
//		out.Add(events.FoldersChanged(projectID, parentID, folderID))
//		return nil
//	})                                                   // 提交成功后才发出去；返回错误（回滚）时不发
package events

import (
	"slices"

	"github.com/google/uuid"
)

// 事件类型（requirements 第 7 节的表）。
const (
	TypeFoldersChanged = "folders.changed"
	TypeFilesChanged   = "files.changed"
	TypeCatalogChanged = "catalog.changed"
	TypeExportChanged  = "export.changed"
	TypeResync         = "resync"
)

// MaxIDs：files.changed 的 file_ids、catalog.changed 的 item_ids 超过这么多个时不带（客户端按 folder_ids、
// category_ids 整体刷新）。
const MaxIDs = 500

// Event 是一条事件，序列化后就是 SSE 里 `data:` 的 JSON。用下面的构造函数建，不要直接填。
type Event struct {
	Type      string    `json:"type"`
	ProjectID uuid.UUID `json:"project_id"`
	// FolderIDs：folders.changed、files.changed。空时不输出。
	FolderIDs []uuid.UUID `json:"folder_ids,omitempty"`
	// FileIDs：files.changed；超过 MaxIDs 个时不带。
	FileIDs []uuid.UUID `json:"file_ids,omitempty"`
	// CategoryIDs：catalog.changed。空时不输出。
	CategoryIDs []uuid.UUID `json:"category_ids,omitempty"`
	// ItemIDs：catalog.changed 一律带（没有条目变化时是 `[]`），超过 MaxIDs 个时不带（nil）。
	// 用 omitzero：nil 不输出，空切片输出 `[]`。
	ItemIDs []uuid.UUID `json:"item_ids,omitzero"`
	// TaskIDs：export.changed。
	TaskIDs []uuid.UUID `json:"task_ids,omitempty"`
}

// FoldersChanged：文件夹新建、改名、改备注、移动、删除；项目改名时是根文件夹。
func FoldersChanged(projectID uuid.UUID, folderIDs ...uuid.UUID) Event {
	return Event{Type: TypeFoldersChanged, ProjectID: projectID, FolderIDs: uniq(folderIDs)}
}

// FilesChanged：上传、重扫、改名、移动、复制、删除、审核状态变、挂上移出。folderIDs 是文件所在（移入、移出）的
// 文件夹；fileIDs 超过 MaxIDs 个时只带 folder_ids。
func FilesChanged(projectID uuid.UUID, folderIDs, fileIDs []uuid.UUID) Event {
	files := uniq(fileIDs)
	if len(files) > MaxIDs {
		files = nil
	}
	return Event{Type: TypeFilesChanged, ProjectID: projectID, FolderIDs: uniq(folderIDs), FileIDs: files}
}

// CatalogChanged：归类节点或条目变化。itemIDs 是条目本身变了的（档号、著录、状态、页）：
// 为空表示只有节点变了（输出 `[]`）；超过 MaxIDs 个时不带，客户端把 categoryIDs 当作子树根整体刷新。
func CatalogChanged(projectID uuid.UUID, categoryIDs, itemIDs []uuid.UUID) Event {
	items := uniq(itemIDs)
	switch {
	case len(items) > MaxIDs:
		items = nil
	case items == nil:
		items = []uuid.UUID{}
	}
	return Event{Type: TypeCatalogChanged, ProjectID: projectID, CategoryIDs: uniq(categoryIDs), ItemIDs: items}
}

// ExportChanged：导出任务建立、取消、开始、进度（每个任务 ≤ 1 次/秒，由调用方节流）、完成、失败、重试、清理完。
func ExportChanged(projectID uuid.UUID, taskIDs ...uuid.UUID) Event {
	return Event{Type: TypeExportChanged, ProjectID: projectID, TaskIDs: uniq(taskIDs)}
}

// Resync：事件丢了（积压溢出），客户端整体刷新。
func Resync(projectID uuid.UUID) Event {
	return Event{Type: TypeResync, ProjectID: projectID}
}

// uniq 去重（保持第一次出现的顺序），去掉 uuid.Nil；空时返回 nil。
func uniq(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return slices.Clip(out)
}
