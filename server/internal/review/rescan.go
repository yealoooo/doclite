package review

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/file"
	"doclite/server/internal/folder"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/imagehdr"
	"doclite/server/internal/storage"
)

// 重扫（requirements 5.6、5.8）：`POST /api/files/{fid}/rescan`，请求格式同上传（file.Service.Receive）。
//
//  1. 同一个文件的重扫串行：进程内按 file_id 的互斥（硬性规则 16），包住"定版本号 → 传 S3 → 写库"；
//  2. 预判（不加锁）：不是已驳回 409 invalid_state（客户端据此认为上一次已经成功）；和项目里别的文件或自己的当前版本
//     内容相同 409 duplicate_content（和自己更早的版本相同放行）；记下当前版本号 v；
//  3. 事务外把原图和封面传到默认存储，key 是 v+1；
//  4. 短事务：文件树共享锁 → files 行 → 重读：已删除 404、不是已驳回 409 invalid_state、版本号不是 v 409 conflict →
//     存储配置行 → 内容锁 → 复查重 → 追加 rescan 快照、轮次 +1、分回最近一次驳回它的人（不能再审时 → 待分配、
//     日志记 skipped_reviewer）→ 记 rescan；
//  5. 提交后发 files.changed。写库失败留下的对象只记警告（孤儿对象）。
//
// 断网重试不适用于重扫：成功之后文件已经不是已驳回，重试得到 409 invalid_state。

// RescanResponse 是重扫的响应（201）：字段同上传（file_name 不变，renamed 总是 false），另加所在文件夹、审核状态、
// 重扫后的轮次和分给的审核人（没分配时为 null）。
type RescanResponse struct {
	file.IngestResponse
	FolderID     uuid.UUID  `json:"folder_id"`
	ReviewStatus string     `json:"review_status"`
	ReviewRound  int        `json:"review_round"`
	ReviewerID   *uuid.UUID `json:"reviewer_id"`
}

// Rescan 把收好的重扫件追加为文件的新版本（项目授权已经查过）。ctx 要已经脱离请求的取消（context.WithoutCancel）。
func (s *Service) Rescan(ctx context.Context, userID, projectID, fileID uuid.UUID, u *file.Upload) (*RescanResponse, error) {
	unlock := s.rescans.Lock(fileID)
	defer unlock()

	pre, err := rereadFiles(ctx, s.pool, projectID, []uuid.UUID{fileID})
	if err != nil {
		return nil, err
	}
	if len(pre) == 0 {
		return nil, errFileNotFound
	}
	if err := checkRescannable(pre[0], pre[0].VersionNo); err != nil {
		return nil, err
	}
	if err := s.files.FindDuplicate(ctx, s.pool, projectID, u.SHA256, &fileID); err != nil {
		return nil, err
	}
	version := pre[0].VersionNo + 1

	target, err := s.storage.DefaultTarget(ctx)
	if err != nil {
		return nil, err
	}
	code, err := file.ProjectCode(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	keys := file.KeysFor(target.PathPrefix, code, fileID, version, u.Info.Format)
	if err := s.files.PutObjects(ctx, target, keys, u); err != nil {
		return nil, err
	}
	written := []string{keys.Original}
	var coverKey *string
	if u.Cover != nil {
		coverKey = &keys.Cover
		written = append(written, keys.Cover)
	}

	var out *RescanResponse
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		locked, err := s.lockFiles(ctx, tx, projectID, []uuid.UUID{fileID})
		if err != nil {
			return err
		}
		f := locked.byID[fileID]
		if f == nil {
			return errFileNotFound
		}
		if err := checkRescannable(f, version-1); err != nil {
			return err
		}
		if err := s.storage.LockForSnapshot(ctx, tx, target.ProfileID); err != nil {
			return err
		}
		if err := file.LockContent(ctx, tx, projectID, u.SHA256); err != nil {
			return err
		}
		if err := s.files.FindDuplicate(ctx, tx, projectID, u.SHA256, &fileID); err != nil {
			return err
		}
		snap := &file.NewSnapshot{ID: db.NewID(), FileID: fileID, VersionNo: version, SnapshotType: file.SnapshotRescan,
			StorageProfileID: target.ProfileID, StorageKey: keys.Original, CoverKey: coverKey, SHA256: u.SHA256,
			FileSize: u.Size, MimeType: u.Info.Format.MIME(), UploadName: u.FileName, Width: u.Info.Width,
			Height: u.Info.Height, DPI: dpiOf(u.Info), ColorMode: colorOf(u.Info), CreatedBy: userID}
		if err := file.InsertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		last, err := latestResult(ctx, tx, fileID, resultRejected)
		if err != nil {
			return err
		}
		next, err := s.reassignBack(ctx, tx, projectID, f, last)
		if err != nil {
			return err
		}
		round := f.Round + 1
		if err := s.folders.SetReviewState(ctx, tx, fileID, next.status, round); err != nil {
			return err
		}
		if err := s.withPaths(ctx, tx, []*fileState{f}); err != nil {
			return err
		}
		folderName, err := folderNameOf(ctx, tx, f.FolderID)
		if err != nil {
			return err
		}
		d := map[string]any{"folder": folderName, "folder_path": f.FolderPath, "file_name": f.FileName,
			"upload_name": u.FileName, "version_no": version, "review_round": round, "review_status": next.status}
		next.describe(d)
		if err := file.WriteLogs(ctx, tx, file.Log{ProjectID: projectID, FileID: fileID, Action: file.ActionRescan,
			OperatorID: userID, Detail: d}); err != nil {
			return err
		}
		w, h := u.Info.Width, u.Info.Height
		out = &RescanResponse{
			IngestResponse: file.IngestResponse{FileID: fileID, SnapshotID: snap.ID, FolderFileID: f.FolderFileID,
				FileName: f.FileName, SortNo: f.SortNo, VersionNo: version, WidthPx: &w, HeightPx: &h, DPI: snap.DPI,
				ColorMode: snap.ColorMode, FileSize: u.Size, HasCover: u.Cover != nil, Warnings: u.Warnings()},
			FolderID: f.FolderID, ReviewStatus: next.status, ReviewRound: round, ReviewerID: next.reviewerID}
		ev.Add(filesChanged(projectID, []*fileState{f}))
		return nil
	})
	if err != nil {
		orphans(ctx, target, written...)
		return nil, err
	}
	return out, nil
}

// checkRescannable：不是已驳回 409 invalid_state；当前版本号不是 version 409 conflict。
func checkRescannable(f *fileState, version int) error {
	if f.Status != folder.ReviewRejected {
		return invalidState("文件不是已驳回的，不需要重扫（上一次重扫可能已经成功），请刷新")
	}
	if f.VersionNo != version {
		return httpx.Conflict("", "文件的版本刚刚变了（现在是 v"+itoa(f.VersionNo)+"），请刷新后重试")
	}
	return nil
}

// orphans 记下传上去但没有入库的对象（requirements 5.6：只记警告）。
func orphans(ctx context.Context, target *storage.Target, keys ...string) {
	httpx.LoggerFrom(ctx).Warn("上传的对象没有入库，成了孤儿对象", "storage_profile_id", target.ProfileID, "keys", keys)
}

func dpiOf(info imagehdr.Info) *int {
	if info.DPI <= 0 {
		return nil
	}
	d := info.DPI
	return &d
}

func colorOf(info imagehdr.Info) *string {
	if info.ColorMode == "" {
		return nil
	}
	c := string(info.ColorMode)
	return &c
}

// keyedMutex 是按 file_id 的进程内互斥：没人持有、也没人在等的 id 不占内存。零值可用。
type keyedMutex struct {
	mu sync.Mutex
	m  map[uuid.UUID]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// Lock 拿 id 的锁，返回解锁函数。
func (k *keyedMutex) Lock(id uuid.UUID) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[uuid.UUID]*keyedEntry{}
	}
	e := k.m[id]
	if e == nil {
		e = &keyedEntry{}
		k.m[id] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, id)
		}
		k.mu.Unlock()
	}
}
