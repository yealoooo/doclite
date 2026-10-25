package folder

import (
	"context"
	"fmt"
	"strings"

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

// claimIn 在已经锁住的文件夹 f 里领位置（名字、sort_no、文件夹路径一次查出；前 100 个候选名都被占时再接着找）。
func (s *Service) claimIn(ctx context.Context, q db.Querier, f *folderRow, name string) (file.Slot, error) {
	final, sortNo, path, err := slotOf(ctx, q, f.ID, candidates(name, 0))
	if err != nil {
		return file.Slot{}, err
	}
	if final == "" {
		if final, err = freeName(ctx, q, f.ID, name); err != nil {
			return file.Slot{}, err
		}
	}
	return file.Slot{ProjectID: f.ProjectID, FolderID: f.ID, FolderName: f.FolderName, FolderPath: path, FileName: final,
		Renamed: final != name, SortNo: sortNo}, nil
}

// AddFile 按 slot 插入 folder_files（待分配、第 1 轮），和文件日志 logs 一次往返写入，返回 folder_files 的 id。
func (s *Service) AddFile(ctx context.Context, tx pgx.Tx, slot file.Slot, fileID, createdBy uuid.UUID, logs ...file.Log) (uuid.UUID, error) {
	id := db.NewID()
	b := &pgx.Batch{}
	b.Queue(insertFolderFileSQL, id, slot.ProjectID, slot.FolderID, fileID, slot.FileName, slot.SortNo, createdBy)
	if err := file.QueueLogs(b, logs...); err != nil {
		return uuid.Nil, err
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
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
	for start := 0; start < 1_000_000; start += nameBatch {
		got, err := firstFreeName(ctx, q, folderID, candidates(name, start))
		if err != nil || got != "" {
			return got, err
		}
	}
	return "", fmt.Errorf("文件夹 %s 里找不到不重名的名字：%s", folderID, name)
}

// nameBatch 是每次查的候选名个数。
const nameBatch = 100

// candidates 是第 start 个起的 nameBatch 个候选名（第 0 个是 name 本身）。
func candidates(name string, start int) []string {
	out := make([]string, 0, nameBatch)
	for n := start; n < start+nameBatch; n++ {
		if n == 0 {
			out = append(out, name)
		} else {
			out = append(out, names.NumberedName(name, n))
		}
	}
	return out
}

// pickFreeName 和 freeName 规则一样，但在内存里挑：used 是文件夹里已占用的名字（小写），挑中的名字也记进 used。批量移动
// 用（一次读出目标的名字，不逐个查）。
func pickFreeName(used map[string]bool, name string) string {
	final := name
	for n := 1; used[strings.ToLower(final)]; n++ {
		final = names.NumberedName(name, n)
	}
	used[strings.ToLower(final)] = true
	return final
}

func fileNameConflict(name string) error {
	return httpx.Conflict("", "同一个文件夹里已有名为「"+name+"」的文件（不区分大小写），请换一个名字")
}

func fileNameOf(ctx context.Context, q db.Querier, fileID uuid.UUID) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT file_name FROM folder_files WHERE file_id = $1`, fileID).Scan(&name)
	return name, err
}
