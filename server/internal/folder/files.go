package folder

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/catalog"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/platform/xslices"
	"doclite/server/internal/project"
)

// 文件的整理（id 是 folder_files 的 id）：改名、移动、复制、删除、批量移动、批量删除（requirements 5.5）。
// 加锁：文件树共享锁 → files 行（id 升序）→ 锁住之后重读 folder_files → folders 行（id 升序）→（复制时）源存储配置行。
// 锁住 folders 行之后做封存检查（catalog.SealedCheck：改名查所在的文件夹，移动查源和目标，复制查目标，删除查所在的
// 文件夹），再查已归类。
// 只改 folder_files（硬性规则 3）；文件的操作写 file_logs（detail 存当时的名字和文件夹路径）；提交后发 files.changed。

// FolderFileUpdated 是 `PATCH /api/folder-files/{id}` 的响应。
type FolderFileUpdated struct {
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileID       uuid.UUID `json:"file_id"`
	FolderID     uuid.UUID `json:"folder_id"`
	FileName     string    `json:"file_name"`
	SortNo       int       `json:"sort_no"`
	// Renamed：移动时目标文件夹里有同名文件、自动改了名。
	Renamed bool `json:"renamed"`
}

// FolderFileInput 是修改文件的输入；nil 表示不改。
type FolderFileInput struct {
	FileName *string    `json:"file_name"`
	FolderID *uuid.UUID `json:"folder_id"`
}

// lockFolderFiles 在事务里锁住这些文件的 files 行（id 升序），再重读 folder_files（未删除，所在文件夹、项目也未删除）。
// 不在这个项目里的当作不存在。
func lockFolderFiles(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*folderFileRow, error) {
	pre, err := liveFolderFiles(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	fileIDs := make([]uuid.UUID, 0, len(pre))
	for _, e := range pre {
		fileIDs = append(fileIDs, e.FileID)
	}
	if err := file.LockFiles(ctx, tx, fileIDs); err != nil {
		return nil, err
	}
	cur, err := liveFolderFiles(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for id, e := range cur {
		if e.ProjectID != projectID {
			delete(cur, id)
		}
	}
	return cur, nil
}

// UpdateFolderFile 改名（撞名 409 conflict）或移动到本项目的另一个文件夹（撞名自动改名，排到末尾）；同时改名和移动时新名字
// 在目标里撞名 409，日志先 rename 后 move。没有变化时不写库、不发事件。
func (s *Service) UpdateFolderFile(ctx context.Context, userID, projectID, id uuid.UUID, in FolderFileInput) (*FolderFileUpdated, error) {
	if in.FileName != nil {
		if err := names.ValidateFileName(*in.FileName); err != nil {
			return nil, httpx.Invalid("%s", err.Error())
		}
	}
	var out *FolderFileUpdated
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		es, err := lockFolderFiles(ctx, tx, projectID, []uuid.UUID{id})
		if err != nil {
			return err
		}
		e := es[id]
		if e == nil {
			return errFileNotFound
		}
		out = &FolderFileUpdated{FolderFileID: e.ID, FileID: e.FileID, FolderID: e.FolderID, FileName: e.FileName, SortNo: e.SortNo}
		rename := in.FileName != nil && *in.FileName != e.FileName
		move := in.FolderID != nil && *in.FolderID != e.FolderID
		if !rename && !move {
			return nil
		}
		lockIDs := []uuid.UUID{e.FolderID}
		if move {
			lockIDs = append(lockIDs, *in.FolderID)
		}
		locked, err := lockFolders(ctx, tx, lockIDs...)
		if err != nil {
			return err
		}
		src := locked[e.FolderID]
		dest := src
		if move {
			dest = locked[*in.FolderID]
			if dest == nil || dest.Deleted || dest.ProjectID != projectID {
				return errTargetNotFound
			}
			if err := catalog.SealedCheck(ctx, tx, "移入移出文件", src.ID, dest.ID); err != nil {
				return err
			}
		}
		if rename {
			if err := catalog.SealedCheck(ctx, tx, "给里面的文件改名", src.ID); err != nil {
				return err
			}
		}
		paths, err := file.FolderPaths(ctx, tx, lockIDs)
		if err != nil {
			return err
		}
		var logs []file.Log
		name, sortNo := e.FileName, e.SortNo
		if rename {
			taken, err := nameTaken(ctx, tx, dest.ID, *in.FileName, e.ID)
			if err != nil {
				return err
			}
			if taken {
				return fileNameConflict(*in.FileName)
			}
			logs = append(logs, file.Log{ProjectID: projectID, FileID: e.FileID, Action: file.ActionRename, OperatorID: userID,
				Detail: map[string]any{"from": e.FileName, "to": *in.FileName, "folder": src.FolderName, "folder_path": paths[src.ID]}})
			name = *in.FileName
		}
		if move {
			if !rename {
				if name, err = freeName(ctx, tx, dest.ID, e.FileName); err != nil {
					return err
				}
				out.Renamed = name != e.FileName
			}
			if sortNo, err = nextSortNo(ctx, tx, dest.ID); err != nil {
				return err
			}
			logs = append(logs, moveLog(projectID, userID, e, src, dest, paths, name, out.Renamed))
		}
		if err := updateFolderFile(ctx, tx, e.ID, dest.ID, name, sortNo); err != nil {
			if db.UniqueViolation(err, "ux_folder_files_name") {
				return fileNameConflict(name)
			}
			return err
		}
		if err := file.WriteLogs(ctx, tx, logs...); err != nil {
			return err
		}
		out.FolderID, out.FileName, out.SortNo = dest.ID, name, sortNo
		ev.Add(events.FilesChanged(projectID, []uuid.UUID{src.ID, dest.ID}, []uuid.UUID{e.FileID}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func moveLog(projectID, userID uuid.UUID, e *folderFileRow, src, dest *folderRow, paths map[uuid.UUID]string, name string,
	renamed bool) file.Log {
	d := map[string]any{"from": src.FolderName, "to": dest.FolderName, "from_path": paths[src.ID], "to_path": paths[dest.ID],
		"file_name": name}
	if renamed {
		d["renamed_from"] = e.FileName
	}
	return file.Log{ProjectID: projectID, FileID: e.FileID, Action: file.ActionMove, OperatorID: userID, Detail: d}
}

// FolderFileCopied 是复制的响应（201）。
type FolderFileCopied struct {
	FolderFileID uuid.UUID `json:"folder_file_id"`
	// FileID 是新文件的 file_id。
	FileID     uuid.UUID `json:"file_id"`
	SnapshotID uuid.UUID `json:"snapshot_id"`
	FolderID   uuid.UUID `json:"folder_id"`
	FileName   string    `json:"file_name"`
	SortNo     int       `json:"sort_no"`
	Renamed    bool      `json:"renamed"`
}

// CopyFolderFile 复制文件到 folderID（nil 表示原文件夹，只在本项目内）：新 file_id，v1 类型 copy，复用源文件当前快照的对象
// （不复制字节）；新文件待分配、自动改名、排末尾；只在新文件上记 copy 日志。源存储已停用 409 storage_unavailable。
func (s *Service) CopyFolderFile(ctx context.Context, userID, projectID, id uuid.UUID, folderID *uuid.UUID) (*FolderFileCopied, error) {
	var out *FolderFileCopied
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		es, err := lockFolderFiles(ctx, tx, projectID, []uuid.UUID{id})
		if err != nil {
			return err
		}
		e := es[id]
		if e == nil {
			return errFileNotFound
		}
		targetID := e.FolderID
		if folderID != nil {
			targetID = *folderID
		}
		locked, err := lockFolders(ctx, tx, targetID)
		if err != nil {
			return err
		}
		dest := locked[targetID]
		if dest == nil || dest.Deleted || dest.ProjectID != projectID {
			return errTargetNotFound
		}
		if err := catalog.SealedCheck(ctx, tx, "复制文件进去", dest.ID); err != nil {
			return err
		}
		src, err := file.Current(ctx, tx, e.FileID)
		if err != nil {
			return err
		}
		if src == nil {
			return errFileNotFound
		}
		slot, err := s.claimIn(ctx, tx, dest, e.FileName)
		if err != nil {
			return err
		}
		if err := s.storage.LockForReuse(ctx, tx, src.StorageProfileID); err != nil {
			return err
		}
		// 存储迁移（M7-2）改快照位置时拿同一把内容锁：锁内重读源快照，要么拿到迁移之后的位置，要么迁移等这次复制提交后
		// 把新快照一起改掉，不会留下一个指向已经迁走的位置的新快照。重读到的存储变了（刚迁到可读写的目标存储）再查一次它
		// 没有停用（FOR KEY SHARE，和插入快照时外键检查拿的锁一样，不挡别的事务）。
		if err := project.LockContent(ctx, tx, projectID, src.ContentSHA256); err != nil {
			return err
		}
		moved, err := file.Current(ctx, tx, e.FileID)
		if err != nil {
			return err
		}
		if moved == nil {
			return errFileNotFound
		}
		if moved.StorageProfileID != src.StorageProfileID {
			if err := s.storage.LockForReuse(ctx, tx, moved.StorageProfileID); err != nil {
				return err
			}
		}
		src = moved
		srcFolder, err := getFolder(ctx, tx, e.FolderID)
		if err != nil {
			return err
		}
		srcPath, err := file.FolderPath(ctx, tx, e.FolderID)
		if err != nil {
			return err
		}
		newFileID := db.NewID()
		snapID, err := file.InsertCopy(ctx, tx, newFileID, src, userID)
		if err != nil {
			return err
		}
		ffID, err := s.AddFile(ctx, tx, slot, newFileID, userID)
		if err != nil {
			return err
		}
		if err := file.WriteLogs(ctx, tx, file.Log{ProjectID: projectID, FileID: newFileID, Action: file.ActionCopy,
			OperatorID: userID, Detail: map[string]any{"source_file_id": e.FileID, "source_snapshot_id": src.ID,
				"source_file_name": e.FileName, "source_folder": srcFolder.FolderName, "source_folder_path": srcPath,
				"file_name": slot.FileName, "folder": slot.FolderName, "folder_path": slot.FolderPath}}); err != nil {
			return err
		}
		out = &FolderFileCopied{FolderFileID: ffID, FileID: newFileID, SnapshotID: snapID, FolderID: dest.ID, FileName: slot.FileName,
			SortNo: slot.SortNo, Renamed: slot.Renamed}
		ev.Add(events.FilesChanged(projectID, []uuid.UUID{dest.ID}, []uuid.UUID{newFileID}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteFolderFile 软删除一个文件：所在文件夹封存 409 folder_archived；已归类的 409 file_cataloged（修改中的条目的页
// 除外）；取消进行中的分配；记 delete。
func (s *Service) DeleteFolderFile(ctx context.Context, userID, projectID, id uuid.UUID) error {
	return s.deleteFolderFiles(ctx, userID, projectID, []uuid.UUID{id}, false)
}

// deleteFolderFiles 删除这些文件（一个事务）：有不在的整体 404（bulk 时 details.ids），有已归类的整体 409 file_cataloged。
func (s *Service) deleteFolderFiles(ctx context.Context, userID, projectID uuid.UUID, ids []uuid.UUID, bulk bool) error {
	return s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		es, err := lockFolderFiles(ctx, tx, projectID, ids)
		if err != nil {
			return err
		}
		if missing := missingOf(ids, es); len(missing) > 0 {
			if !bulk {
				return errFileNotFound
			}
			return httpx.NotFoundIDs("有 "+strconv.Itoa(len(missing))+" 个文件不存在或已删除（可能刚被别人删除），请刷新后重试", missing)
		}
		entries := ordered(ids, es)
		fileIDs := make([]uuid.UUID, len(entries))
		for i, e := range entries {
			fileIDs[i] = e.FileID
		}
		folderIDs := uniqueFolders(entries)
		locked, err := lockFolders(ctx, tx, folderIDs...)
		if err != nil {
			return err
		}
		if err := catalog.SealedCheck(ctx, tx, "删除里面的文件", folderIDs...); err != nil {
			return err
		}
		cataloged, err := catalogedAmong(ctx, tx, fileIDs)
		if err != nil {
			return err
		}
		if len(cataloged) > 0 {
			return catalogedError(entries, cataloged, bulk)
		}
		paths, err := file.FolderPaths(ctx, tx, folderIDs)
		if err != nil {
			return err
		}
		if _, err := softDeleteFolderFiles(ctx, tx, ids, userID); err != nil {
			return err
		}
		if err := cancelOpenReviews(ctx, tx, fileIDs); err != nil {
			return err
		}
		logs := make([]file.Log, len(entries))
		for i, e := range entries {
			logs[i] = file.Log{ProjectID: projectID, FileID: e.FileID, Action: file.ActionDelete, OperatorID: userID,
				Detail: map[string]any{"file_name": e.FileName, "folder": locked[e.FolderID].FolderName, "folder_path": paths[e.FolderID]}}
		}
		if err := file.WriteLogs(ctx, tx, logs...); err != nil {
			return err
		}
		ev.Add(events.FilesChanged(projectID, folderIDs, fileIDs))
		return nil
	})
}

func catalogedError(entries []*folderFileRow, cataloged []uuid.UUID, bulk bool) error {
	set := map[uuid.UUID]bool{}
	for _, id := range cataloged {
		set[id] = true
	}
	var ids []uuid.UUID
	var names []string
	for _, e := range entries {
		if set[e.FileID] {
			ids = append(ids, e.ID)
			names = append(names, "「"+e.FileName+"」")
		}
	}
	if !bulk {
		return httpx.Conflict(httpx.CodeFileCataloged, "文件"+names[0]+"已归类，不能删除")
	}
	example := strings.Join(names[:min(len(names), 3)], "、")
	return httpx.Conflict(httpx.CodeFileCataloged, "有 "+strconv.Itoa(len(ids))+" 个文件已归类（"+example+"），不能删除").
		WithDetails(map[string]any{"ids": ids})
}

// BulkScope 是批量操作的文件：ids 去重后都在同一个项目里，用户能进这个项目。
type BulkScope struct {
	ProjectID uuid.UUID
	IDs       []uuid.UUID
}

// ResolveBulk 检查批量操作的 ids（folder_files 的 id）：空或超过 500 个 400；不在同一项目 400；进不了项目 403；
// 有不存在、已删除的 404（details.ids）。
func (s *Service) ResolveBulk(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (*BulkScope, error) {
	if len(ids) == 0 {
		return nil, httpx.Invalid("ids 不能为空")
	}
	if len(ids) > httpx.MaxBatchIDs {
		return nil, httpx.Invalid("一次最多 %d 个文件", httpx.MaxBatchIDs)
	}
	ids = xslices.Unique(ids)
	es, err := liveFolderFiles(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	projects := map[uuid.UUID]bool{}
	var pid uuid.UUID
	for _, e := range es {
		projects[e.ProjectID] = true
		pid = e.ProjectID
	}
	if len(projects) > 1 {
		return nil, httpx.Invalid("批量操作的文件要在同一个项目里")
	}
	if len(projects) == 1 {
		if err := s.auth.CheckProjectAccess(ctx, userID, pid); err != nil {
			return nil, err
		}
	}
	if missing := missingOf(ids, es); len(missing) > 0 {
		return nil, httpx.NotFoundIDs("有 "+strconv.Itoa(len(missing))+" 个文件不存在或已删除（可能刚被别人删除），请刷新后重试", missing)
	}
	return &BulkScope{ProjectID: pid, IDs: ids}, nil
}

// DeleteFolderFiles 批量删除（≤ 500 个，一个事务）：有已归类的整体不做，409 file_cataloged（details.ids）。
func (s *Service) DeleteFolderFiles(ctx context.Context, userID uuid.UUID, scope *BulkScope) error {
	return s.deleteFolderFiles(ctx, userID, scope.ProjectID, scope.IDs, true)
}

// FolderFileMoved 是批量移动的一项。
type FolderFileMoved struct {
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileID       uuid.UUID `json:"file_id"`
	FolderID     uuid.UUID `json:"folder_id"`
	FileName     string    `json:"file_name"`
	SortNo       int       `json:"sort_no"`
	// Moved：本来就在目标文件夹里的为 false。
	Moved   bool `json:"moved"`
	Renamed bool `json:"renamed"`
}

// MoveResult 是批量移动的响应。
type MoveResult struct {
	Items []FolderFileMoved `json:"items"`
}

// MoveFolderFiles 批量移动到 folderID（≤ 500 个，一个事务）：按原文件夹 path_code、sort_no、名字依次排到目标末尾，
// 撞名自动改名；目标不在这个项目或已删除 404。
func (s *Service) MoveFolderFiles(ctx context.Context, userID uuid.UUID, scope *BulkScope, folderID uuid.UUID) (*MoveResult, error) {
	projectID := scope.ProjectID
	out := &MoveResult{Items: []FolderFileMoved{}}
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		es, err := lockFolderFiles(ctx, tx, projectID, scope.IDs)
		if err != nil {
			return err
		}
		if missing := missingOf(scope.IDs, es); len(missing) > 0 {
			return httpx.NotFoundIDs("有 "+strconv.Itoa(len(missing))+" 个文件不存在或已删除（可能刚被别人删除），请刷新后重试", missing)
		}
		entries := ordered(scope.IDs, es)
		slices.SortStableFunc(entries, func(a, b *folderFileRow) int {
			if c := strings.Compare(a.FolderPathCode, b.FolderPathCode); c != 0 {
				return c
			}
			if a.SortNo != b.SortNo {
				return a.SortNo - b.SortNo
			}
			if c := strings.Compare(a.FileName, b.FileName); c != 0 {
				return c
			}
			return strings.Compare(a.ID.String(), b.ID.String())
		})
		folderIDs := append(uniqueFolders(entries), folderID)
		locked, err := lockFolders(ctx, tx, folderIDs...)
		if err != nil {
			return err
		}
		dest := locked[folderID]
		if dest == nil || dest.Deleted || dest.ProjectID != projectID {
			return errTargetNotFound
		}
		var touched []uuid.UUID
		for _, e := range entries {
			if e.FolderID != dest.ID {
				touched = append(touched, e.FolderID)
			}
		}
		if len(touched) > 0 {
			if err := catalog.SealedCheck(ctx, tx, "移入移出文件", append(touched, dest.ID)...); err != nil {
				return err
			}
		}
		paths, err := file.FolderPaths(ctx, tx, folderIDs)
		if err != nil {
			return err
		}
		sortNo, err := nextSortNo(ctx, tx, dest.ID)
		if err != nil {
			return err
		}
		var logs []file.Log
		var movedFiles []uuid.UUID
		changedFolders := []uuid.UUID{dest.ID}
		for _, e := range entries {
			if e.FolderID == dest.ID {
				out.Items = append(out.Items, FolderFileMoved{FolderFileID: e.ID, FileID: e.FileID, FolderID: e.FolderID,
					FileName: e.FileName, SortNo: e.SortNo})
				continue
			}
			name, err := freeName(ctx, tx, dest.ID, e.FileName)
			if err != nil {
				return err
			}
			renamed := name != e.FileName
			if err := updateFolderFile(ctx, tx, e.ID, dest.ID, name, sortNo); err != nil {
				return err
			}
			logs = append(logs, moveLog(projectID, userID, e, locked[e.FolderID], dest, paths, name, renamed))
			out.Items = append(out.Items, FolderFileMoved{FolderFileID: e.ID, FileID: e.FileID, FolderID: dest.ID, FileName: name,
				SortNo: sortNo, Moved: true, Renamed: renamed})
			movedFiles = append(movedFiles, e.FileID)
			changedFolders = append(changedFolders, e.FolderID)
			sortNo++
		}
		if err := file.WriteLogs(ctx, tx, logs...); err != nil {
			return err
		}
		if len(movedFiles) > 0 {
			ev.Add(events.FilesChanged(projectID, changedFolders, movedFiles))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func missingOf(ids []uuid.UUID, es map[uuid.UUID]*folderFileRow) []uuid.UUID {
	var out []uuid.UUID
	for _, id := range ids {
		if es[id] == nil {
			out = append(out, id)
		}
	}
	return out
}

func ordered(ids []uuid.UUID, es map[uuid.UUID]*folderFileRow) []*folderFileRow {
	out := make([]*folderFileRow, 0, len(ids))
	for _, id := range ids {
		if e := es[id]; e != nil {
			out = append(out, e)
		}
	}
	return out
}

func uniqueFolders(entries []*folderFileRow) []uuid.UUID {
	var out []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, e := range entries {
		if !seen[e.FolderID] {
			seen[e.FolderID] = true
			out = append(out, e.FolderID)
		}
	}
	return out
}
