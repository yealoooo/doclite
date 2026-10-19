package folder

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/catalog"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
)

// 文件槽位：文件进文件夹（上传、移动、复制）时，在锁住目标文件夹行之后定下不重名的名字和 sort_no。

// ClaimSlot 在事务 tx 里锁住目标文件夹行（不存在、已删除、不在这个项目 404；封存 409 folder_archived），定下名字
// （撞名自动改为"名字 (n).扩展名"）和 sort_no（文件夹里未删除文件的 max + 1）。调用方要已经拿了文件树锁，并在同一个事务里
// 用 AddFile 插入。name 由调用方校验（上传校验，移动、复制的老名字不重校验）。
func (s *Service) ClaimSlot(ctx context.Context, tx pgx.Tx, projectID, folderID uuid.UUID, name string) (file.Slot, error) {
	locked, err := lockFolders(ctx, tx, folderID)
	if err != nil {
		return file.Slot{}, err
	}
	f := locked[folderID]
	if f == nil || f.Deleted || f.ProjectID != projectID {
		return file.Slot{}, errFolderNotFound
	}
	if err := catalog.SealedCheck(ctx, tx, "上传文件", f.ID); err != nil {
		return file.Slot{}, err
	}
	return s.claimIn(ctx, tx, f, name)
}

// CheckUpload 是上传之前（传 S3 之前，不加锁）的封存检查：文件夹封存时 409 folder_archived。入库时 ClaimSlot 锁住
// 文件夹行之后还会再查一次。
func (s *Service) CheckUpload(ctx context.Context, folderID uuid.UUID) error {
	return catalog.SealedCheck(ctx, s.pool, "上传文件", folderID)
}

// claimIn 在已经锁住的文件夹 f 里领位置。
func (s *Service) claimIn(ctx context.Context, q db.Querier, f *folderRow, name string) (file.Slot, error) {
	final, err := freeName(ctx, q, f.ID, name)
	if err != nil {
		return file.Slot{}, err
	}
	sortNo, err := nextSortNo(ctx, q, f.ID)
	if err != nil {
		return file.Slot{}, err
	}
	path, err := file.FolderPath(ctx, q, f.ID)
	if err != nil {
		return file.Slot{}, err
	}
	return file.Slot{ProjectID: f.ProjectID, FolderID: f.ID, FolderName: f.FolderName, FolderPath: path, FileName: final,
		Renamed: final != name, SortNo: sortNo}, nil
}

// AddFile 按 slot 插入 folder_files（待分配、第 1 轮），返回它的 id。
func (s *Service) AddFile(ctx context.Context, tx pgx.Tx, slot file.Slot, fileID, createdBy uuid.UUID) (uuid.UUID, error) {
	id := db.NewID()
	if err := insertFolderFile(ctx, tx, id, slot.ProjectID, slot.FolderID, fileID, slot.FileName, slot.SortNo, createdBy); err != nil {
		if db.UniqueViolation(err, "ux_folder_files_name") {
			return uuid.Nil, fileNameConflict(slot.FileName)
		}
		return uuid.Nil, err
	}
	return id, nil
}

// PreviewName 是现在放进这个文件夹会得到的名字（不加锁，预检用）。
func (s *Service) PreviewName(ctx context.Context, folderID uuid.UUID, name string) (string, error) {
	return freeName(ctx, s.pool, folderID, name)
}

// freeName 是文件夹里不重名的名字：name 没被占用就是它，否则依次试"名字 (1).扩展名"、"名字 (2).扩展名"……
// （不区分大小写，只看未删除的；每次查 100 个候选）。
func freeName(ctx context.Context, q db.Querier, folderID uuid.UUID, name string) (string, error) {
	const batch = 100
	for start := 0; start < 1_000_000; start += batch {
		cands := make([]string, 0, batch)
		for n := start; n < start+batch; n++ {
			if n == 0 {
				cands = append(cands, name)
			} else {
				cands = append(cands, names.NumberedName(name, n))
			}
		}
		got, err := firstFreeName(ctx, q, folderID, cands)
		if err != nil || got != "" {
			return got, err
		}
	}
	return "", fmt.Errorf("文件夹 %s 里找不到不重名的名字：%s", folderID, name)
}

func fileNameConflict(name string) error {
	return httpx.Conflict("", "同一个文件夹里已有名为「"+name+"」的文件（不区分大小写），请换一个名字")
}

func fileNameOf(ctx context.Context, q db.Querier, fileID uuid.UUID) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT file_name FROM folder_files WHERE file_id = $1`, fileID).Scan(&name)
	return name, err
}
