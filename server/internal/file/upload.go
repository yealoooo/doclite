package file

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/s3"
	"doclite/server/internal/project"
	"doclite/server/internal/storage"
	"doclite/shared/imagehdr"
	"doclite/shared/names"
)

// 上传入库（requirements 5.6）：
//
//  1. [Service.Receive]：原图流式写进临时文件同时算 sha（超限 413、sha 不符 400）→ 读文件头 → 校验封面；
//  2. 预判重复：同一文件夹里 upload_name 和 sha 都相同 → 200 返回已有文件（断网重试）；同项目里当前版本 sha 相同 →
//     409 duplicate_content；
//  3. 事务外把原图和封面传到默认存储（key 按硬性规则 7）；
//  4. 短事务：文件树共享锁 → 目标文件夹行（领名字和 sort_no）→ 存储配置行（仍是 active 的默认）→ 内容锁 →
//     锁内复查重 → 写 files、快照 v1 original、folder_files（待分配）、upload 日志；
//  5. 删临时文件；提交后发 files.changed。
//
// 第 2 步之后不受客户端断开影响（调用方传 context.WithoutCancel 的 ctx）。传上去之后写库失败（storage_changed、
// 锁内发现重复、文件夹被删……）留下的对象只记警告（孤儿对象，留给以后清理）。

// IngestResponse 是上传的响应：201 新建；200 之前已上传过（字段相同，是已有文件现在的信息）。
type IngestResponse struct {
	FileID       uuid.UUID `json:"file_id"`
	SnapshotID   uuid.UUID `json:"snapshot_id"`
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileName     string    `json:"file_name"`
	// Renamed：入库后的名字和这次的原始文件名不同（重名自动改名，或之前上传后被人改过名）。
	Renamed   bool     `json:"renamed"`
	SortNo    int      `json:"sort_no"`
	VersionNo int      `json:"version_no"`
	WidthPx   *int     `json:"width_px"`
	HeightPx  *int     `json:"height_px"`
	DPI       *int     `json:"dpi"`
	ColorMode *string  `json:"color_mode"`
	FileSize  int64    `json:"file_size"`
	HasCover  bool     `json:"has_cover"`
	Warnings  []string `json:"warnings"`
}

// DuplicateDetails 是 409 duplicate_content 的 details：项目里已有的相同内容的文件。
type DuplicateDetails struct {
	FileID uuid.UUID `json:"file_id"`
	// FolderPath 从根文件夹起用 / 连接。
	FolderPath string `json:"folder_path"`
	FileName   string `json:"file_name"`
}

// ObjectKeys 是一个版本的对象 key（硬性规则 7）：`<path_prefix><project_code>/<file_id>/v<no>.<jpg|png|tif>`，
// 封面 `v<no>.cover.jpg`。
type ObjectKeys struct {
	Original string
	Cover    string
}

// KeysFor 生成一个版本的对象 key。扩展名按文件头识别出的格式，不用原文件的扩展名。
func KeysFor(pathPrefix, projectCode string, fileID uuid.UUID, versionNo int, format imagehdr.Format) ObjectKeys {
	base := pathPrefix + projectCode + "/" + fileID.String() + "/v" + strconv.Itoa(versionNo)
	return ObjectKeys{Original: base + "." + format.Ext(), Cover: base + ".cover.jpg"}
}

// metaOriginalName 是原图对象的自定义元数据（x-amz-meta-original-name）：原始文件名，URL 编码。
const metaOriginalName = "original-name"

// PutObjects 把原图和封面（有的话）传到 target（事务外）。对象带 `Cache-Control: private, max-age=<预签名有效期>,
// immutable`，原图的元数据里存原始文件名。封面传失败时，已经传上去的原图记成孤儿对象。
func (s *Service) PutObjects(ctx context.Context, target *storage.Target, keys ObjectKeys, u *Upload) error {
	cc := fmt.Sprintf("private, max-age=%d, immutable", int64(s3.PresignExpiry(target.URLExpireHours).Seconds()))
	err := target.Client.Put(ctx, keys.Original, u.Original(), s3.PutOptions{
		ContentType: u.Info.Format.MIME(), CacheControl: cc,
		Metadata: map[string]string{metaOriginalName: url.PathEscape(u.FileName)},
	})
	if err != nil {
		return fmt.Errorf("原图传到存储失败：%w", err)
	}
	if u.Cover != nil {
		err := target.Client.Put(ctx, keys.Cover, bytes.NewReader(u.Cover), s3.PutOptions{ContentType: "image/jpeg", CacheControl: cc})
		if err != nil {
			orphans(ctx, target, keys.Original)
			return fmt.Errorf("封面传到存储失败：%w", err)
		}
	}
	return nil
}

// orphans 记下传上去但没有入库的对象（requirements 5.6：只记警告）。
func orphans(ctx context.Context, target *storage.Target, keys ...string) {
	httpx.LoggerFrom(ctx).Warn("上传的对象没有入库，成了孤儿对象", "storage_profile_id", target.ProfileID, "keys", keys)
}

// Ingest 把收好的上传入库到文件夹 folderID（项目 projectID 的授权已经查过）。created 为 false 表示之前已上传过（200）。
// ctx 要已经脱离请求的取消（context.WithoutCancel）。
func (s *Service) Ingest(ctx context.Context, projectID, folderID, userID uuid.UUID, u *Upload) (*IngestResponse, bool, error) {
	if r, err := s.precheck(ctx, s.pool, projectID, folderID, u); r != nil || err != nil {
		return r, false, err
	}
	target, err := s.storage.DefaultTarget(ctx)
	if err != nil {
		return nil, false, err
	}
	code, err := ProjectCode(ctx, s.pool, projectID)
	if err != nil {
		return nil, false, err
	}
	fileID := db.NewID()
	keys := KeysFor(target.PathPrefix, code, fileID, 1, u.Info.Format)
	if err := s.PutObjects(ctx, target, keys, u); err != nil {
		return nil, false, err
	}
	written := []string{keys.Original}
	var coverKey *string
	if u.Cover != nil {
		coverKey = &keys.Cover
		written = append(written, keys.Cover)
	}

	var out *IngestResponse
	created := false
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		slot, err := s.folders.ClaimSlot(ctx, tx, projectID, folderID, u.FileName)
		if err != nil {
			return err
		}
		if err := s.storage.LockForSnapshot(ctx, tx, target.ProfileID); err != nil {
			return err
		}
		if err := LockContent(ctx, tx, projectID, u.SHA256); err != nil {
			return err
		}
		if r, err := s.precheck(ctx, tx, projectID, folderID, u); r != nil || err != nil {
			out = r
			return err
		}
		snap := &NewSnapshot{ID: db.NewID(), FileID: fileID, VersionNo: 1, SnapshotType: SnapshotOriginal,
			StorageProfileID: target.ProfileID, StorageKey: keys.Original, CoverKey: coverKey, SHA256: u.SHA256,
			FileSize: u.Size, MimeType: u.Info.Format.MIME(), UploadName: u.FileName, Width: u.Info.Width,
			Height: u.Info.Height, DPI: dpiOf(u.Info), ColorMode: colorOf(u.Info), CreatedBy: userID}
		if err := insertFile(ctx, tx, fileID); err != nil {
			return err
		}
		if err := InsertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		ffID, err := s.folders.AddFile(ctx, tx, slot, fileID, userID)
		if err != nil {
			return err
		}
		if err := WriteLogs(ctx, tx, Log{ProjectID: projectID, FileID: fileID, Action: ActionUpload, OperatorID: userID,
			Detail: map[string]any{"folder": slot.FolderName, "folder_path": slot.FolderPath, "file_name": slot.FileName,
				"upload_name": u.FileName}}); err != nil {
			return err
		}
		w, h := u.Info.Width, u.Info.Height
		out = &IngestResponse{FileID: fileID, SnapshotID: snap.ID, FolderFileID: ffID, FileName: slot.FileName,
			Renamed: slot.Renamed, SortNo: slot.SortNo, VersionNo: 1, WidthPx: &w, HeightPx: &h, DPI: snap.DPI,
			ColorMode: snap.ColorMode, FileSize: u.Size, HasCover: u.Cover != nil, Warnings: u.Warnings()}
		created = true
		ev.Add(events.FilesChanged(projectID, []uuid.UUID{folderID}, []uuid.UUID{fileID}))
		return nil
	})
	if !created {
		orphans(ctx, target, written...)
	}
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
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

// precheck 按顺序查重（预判和锁内复查都用它）：先看断网重试（同一文件夹里 upload_name 和 sha 都相同 → 返回已有文件），
// 再看项目范围（当前版本 sha 相同 → 409 duplicate_content）。都没有时返回 nil, nil。
func (s *Service) precheck(ctx context.Context, q db.Querier, projectID, folderID uuid.UUID, u *Upload) (*IngestResponse, error) {
	ffID, err := findRetry(ctx, q, folderID, u.FileName, u.SHA256)
	if err != nil {
		return nil, err
	}
	if ffID != nil {
		r, err := ingested(ctx, q, *ffID)
		if err != nil {
			return nil, err
		}
		r.Renamed = r.FileName != u.FileName
		return r, nil
	}
	if err := s.FindDuplicate(ctx, q, projectID, u.SHA256, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// FindDuplicate：项目里未删除的文件当前版本的 sha 和 sha 相同时返回 409 duplicate_content（details 是那个文件的位置），
// 没有时返回 nil。self 不为 nil 时（重扫）优先报它自己；别的项目里有相同内容不算。
func (s *Service) FindDuplicate(ctx context.Context, q db.Querier, projectID uuid.UUID, sha []byte, self *uuid.UUID) error {
	c, err := findSameContent(ctx, q, projectID, sha, self)
	if err != nil || c == nil {
		return err
	}
	path, err := s.folders.Path(ctx, q, c.FolderID)
	if err != nil {
		return err
	}
	where := path + "/" + c.FileName
	msg := "项目里已有内容相同的文件：" + where
	if self != nil && c.FileID == *self {
		msg = "和这个文件现在的版本内容相同，等于没有重扫：" + where
	}
	return httpx.Conflict(CodeDuplicateContent, msg).
		WithDetails(DuplicateDetails{FileID: c.FileID, FolderPath: path, FileName: c.FileName})
}

// CheckRequest 是预检 `POST /api/folders/{id}/files:check` 的请求（壳在本机算好 sha）。
type CheckRequest struct {
	FileName string `json:"file_name"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// CheckResponse 是预检的结果：判断顺序和实际上传相同，没有并发改动时结论一致。
type CheckResponse struct {
	// Duplicate：项目里内容相同的文件（会 409 duplicate_content）；already_uploaded 为 true 时为 null。
	Duplicate *DuplicateDetails `json:"duplicate"`
	// AlreadyUploaded：之前已上传过（同一文件夹里 upload_name 和 sha 都相同，上传会得到 200）。
	AlreadyUploaded bool `json:"already_uploaded"`
	// FinalName：命中断网重试时是已有文件现在的名字，其余（包括内容重复）是现在上传会得到的名字。
	FinalName string `json:"final_name"`
}

// Check 是预检：文件名不合法、sha 不是 64 位小写 hex、size 为负 400；size 超过上限 413（实际上传也会 413）。
func (s *Service) Check(ctx context.Context, projectID, folderID uuid.UUID, req CheckRequest) (*CheckResponse, error) {
	if err := names.ValidateFileName(req.FileName); err != nil {
		return nil, httpx.Invalid("%s", err.Error())
	}
	if !shaPattern.MatchString(req.SHA256) {
		return nil, httpx.Invalid("sha256 要是 64 位小写十六进制")
	}
	if req.Size < 0 {
		return nil, httpx.Invalid("size 不能是负数")
	}
	if req.Size > s.opts.MaxUploadBytes {
		return nil, tooLarge(s.opts.MaxUploadBytes)
	}
	sha, _ := hex.DecodeString(req.SHA256)
	ffID, err := findRetry(ctx, s.pool, folderID, req.FileName, sha)
	if err != nil {
		return nil, err
	}
	if ffID != nil {
		r, err := ingested(ctx, s.pool, *ffID)
		if err != nil {
			return nil, err
		}
		return &CheckResponse{AlreadyUploaded: true, FinalName: r.FileName}, nil
	}
	out := &CheckResponse{}
	if err := s.FindDuplicate(ctx, s.pool, projectID, sha, nil); err != nil {
		e, ok := httpx.AsError(err)
		if !ok || e.Code != CodeDuplicateContent {
			return nil, err
		}
		d := e.Details.(DuplicateDetails)
		out.Duplicate = &d
	}
	if out.FinalName, err = s.folders.PreviewName(ctx, folderID, req.FileName); err != nil {
		return nil, err
	}
	return out, nil
}

// ProjectOfFolder 是文件夹所在的项目（上传、预检的项目授权检查用）；不存在或已删除 404。
func (s *Service) ProjectOfFolder(ctx context.Context, folderID uuid.UUID) (uuid.UUID, error) {
	return s.folders.ProjectOf(ctx, folderID)
}
