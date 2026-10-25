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
	"doclite/server/internal/platform/imagehdr"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/platform/s3"
	"doclite/server/internal/project"
	"doclite/server/internal/storage"
)

// 上传入库（requirements 5.6）：
//
//  1. [Service.Receive]：原图流式写进临时文件同时算 sha（超限 413、sha 不符 400）→ 读文件头 → 校验封面；
//  2. 预判重复：同一文件夹里 upload_name 和 sha 都相同 → 200 返回已有文件（断网重试）；同项目里当前版本 sha 相同 →
//     409 duplicate_content；
//  3. 文件夹已封存（归档了）409 folder_archived（这里不加锁先查一次，第 4 步锁住文件夹行之后再查）；事务外把原图和
//     封面传到默认存储（key 按硬性规则 7）；
//  4. 短事务：文件树共享锁 → 目标文件夹行（封存检查，领名字和 sort_no）→ 存储配置行（仍是 active 的默认）→ 内容锁 →
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

// keysFor 生成一个版本的对象 key。扩展名按文件头识别出的格式，不用原文件的扩展名。
func keysFor(pathPrefix, projectCode string, fileID uuid.UUID, versionNo int, format imagehdr.Format) ObjectKeys {
	base := pathPrefix + projectCode + "/" + fileID.String() + "/v" + strconv.Itoa(versionNo)
	return ObjectKeys{Original: base + "." + format.Ext(), Cover: base + ".cover.jpg"}
}

// metaOriginalName 是原图对象的自定义元数据（x-amz-meta-original-name）：原始文件名，URL 编码。
const metaOriginalName = "original-name"

// Staged 是已经传到存储、还没入库的一个版本（上传是 v1，重扫是当前版本 + 1）。入库没成功时调 [Staged.Orphans]。
type Staged struct {
	fileID    uuid.UUID
	versionNo int
	target    *storage.Target
	keys      ObjectKeys
	upload    *Upload
}

// Stage 把原图和封面（有的话）传到默认存储（事务外），key 按硬性规则 7。对象带 `Cache-Control: private,
// max-age=<预签名有效期>, immutable`，原图的元数据里存原始文件名。封面传失败时，已经传上去的原图记成孤儿对象。
func (s *Service) Stage(ctx context.Context, projectID, fileID uuid.UUID, versionNo int, u *Upload) (*Staged, error) {
	target, err := s.storage.DefaultTarget(ctx)
	if err != nil {
		return nil, err
	}
	code, err := ProjectCode(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	st := &Staged{fileID: fileID, versionNo: versionNo, target: target, upload: u,
		keys: keysFor(target.PathPrefix, code, fileID, versionNo, u.Info.Format)}
	cc := fmt.Sprintf("private, max-age=%d, immutable", int64(s3.PresignExpiry(target.URLExpireHours).Seconds()))
	err = target.Client.Put(ctx, st.keys.Original, u.Original(), s3.PutOptions{
		ContentType: u.Info.Format.MIME(), CacheControl: cc,
		Metadata: map[string]string{metaOriginalName: url.PathEscape(u.FileName)},
	})
	if err != nil {
		return nil, fmt.Errorf("原图传到存储失败：%w", err)
	}
	if u.Cover != nil {
		err := target.Client.Put(ctx, st.keys.Cover, bytes.NewReader(u.Cover), s3.PutOptions{ContentType: "image/jpeg", CacheControl: cc})
		if err != nil {
			orphans(ctx, target, st.keys.Original)
			return nil, fmt.Errorf("封面传到存储失败：%w", err)
		}
	}
	return st, nil
}

// Lock 在入库的事务里拿存储配置行（仍是 active 的默认）和内容锁（requirements 3.6：在文件树锁、文件夹行或 files 行
// 之后），之后在锁内复查重、插入快照。
func (s *Service) Lock(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, st *Staged) error {
	if err := s.storage.LockForSnapshot(ctx, tx, st.target.ProfileID); err != nil {
		return err
	}
	return project.LockContent(ctx, tx, projectID, st.upload.SHA256)
}

// Snapshot 是这个版本要插入的快照（snapshotType 是 original 或 rescan）。
func (st *Staged) Snapshot(snapshotType string, createdBy uuid.UUID) *Snapshot {
	u := st.upload
	var cover *string
	if u.Cover != nil {
		cover = &st.keys.Cover
	}
	w, h := u.Info.Width, u.Info.Height
	snap := &Snapshot{ID: db.NewID(), FileID: st.fileID, VersionNo: st.versionNo, SnapshotType: snapshotType,
		StorageProfileID: st.target.ProfileID, StorageKey: st.keys.Original, CoverKey: cover, ContentSHA256: u.SHA256,
		FileSize: u.Size, MimeType: u.Info.Format.MIME(), UploadName: &u.FileName, WidthPx: &w, HeightPx: &h,
		CreatedBy: &createdBy}
	if u.Info.DPI > 0 {
		d := u.Info.DPI
		snap.DPI = &d
	}
	if u.Info.ColorMode != "" {
		c := string(u.Info.ColorMode)
		snap.ColorMode = &c
	}
	return snap
}

// Response 是入库的响应里和快照有关的字段（文件名、位置由调用方填）。
func (st *Staged) Response(snap *Snapshot) IngestResponse {
	return IngestResponse{FileID: st.fileID, SnapshotID: snap.ID, VersionNo: st.versionNo, WidthPx: snap.WidthPx,
		HeightPx: snap.HeightPx, DPI: snap.DPI, ColorMode: snap.ColorMode, FileSize: snap.FileSize,
		HasCover: snap.CoverKey != nil, Warnings: st.upload.Warnings()}
}

// Orphans 记下传上去但没有入库的对象（requirements 5.6：只记警告）。
func (st *Staged) Orphans(ctx context.Context) {
	keys := []string{st.keys.Original}
	if st.upload.Cover != nil {
		keys = append(keys, st.keys.Cover)
	}
	orphans(ctx, st.target, keys...)
}

func orphans(ctx context.Context, target *storage.Target, keys ...string) {
	httpx.LoggerFrom(ctx).Warn("上传的对象没有入库，成了孤儿对象", "storage_profile_id", target.ProfileID, "keys", keys)
}

// Ingest 把收好的上传入库到文件夹 folderID（项目 projectID 的授权已经查过）。created 为 false 表示之前已上传过（200）。
// ctx 要已经脱离请求的取消（context.WithoutCancel）。
func (s *Service) Ingest(ctx context.Context, projectID, folderID, userID uuid.UUID, u *Upload) (*IngestResponse, bool, error) {
	if r, err := s.precheck(ctx, s.pool, projectID, folderID, u); r != nil || err != nil {
		return r, false, err
	}
	if err := s.folders.CheckUpload(ctx, folderID); err != nil {
		return nil, false, err
	}
	fileID := db.NewID()
	st, err := s.Stage(ctx, projectID, fileID, 1, u)
	if err != nil {
		return nil, false, err
	}

	var out *IngestResponse
	created := false
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		slot, err := s.folders.ClaimSlot(ctx, tx, projectID, folderID, u.FileName)
		if err != nil {
			// 文件夹刚被归档（封存）：ClaimSlot 已经锁住了文件夹行，锁内先看是不是断网重试（同一文件夹里 upload_name 和
			// sha 都相同 → 200 返回已有文件），不是才报 folder_archived。重叠的重试请求在文件夹刚归档时仍得到 200。
			if e, ok := httpx.AsError(err); ok && e.Code == httpx.CodeFolderArchived {
				if r, rerr := s.retried(ctx, tx, folderID, u); rerr != nil || r != nil {
					out = r
					return rerr
				}
			}
			return err
		}
		if err := s.Lock(ctx, tx, projectID, st); err != nil {
			return err
		}
		if r, err := s.precheck(ctx, tx, projectID, folderID, u); r != nil || err != nil {
			out = r
			return err
		}
		snap := st.Snapshot(SnapshotOriginal, userID)
		if err := InsertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		ffID, err := s.folders.AddFile(ctx, tx, slot, fileID, userID, Log{ProjectID: projectID, FileID: fileID,
			Action: ActionUpload, OperatorID: userID, Detail: map[string]any{"folder": slot.FolderName,
				"folder_path": slot.FolderPath, "file_name": slot.FileName, "upload_name": u.FileName}})
		if err != nil {
			return err
		}
		r := st.Response(snap)
		r.FolderFileID, r.FileName, r.Renamed, r.SortNo = ffID, slot.FileName, slot.Renamed, slot.SortNo
		out = &r
		created = true
		ev.Add(events.FilesChanged(projectID, []uuid.UUID{folderID}, []uuid.UUID{fileID}))
		return nil
	})
	if !created {
		st.Orphans(ctx)
	}
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// precheck 按顺序查重（预判和锁内复查都用它，一条语句查两样）：先看断网重试（同一文件夹里 upload_name 和 sha 都相同 →
// 返回已有文件），再看项目范围（当前版本 sha 相同 → 409 duplicate_content）。都没有时返回 nil, nil。
func (s *Service) precheck(ctx context.Context, q db.Querier, projectID, folderID uuid.UUID, u *Upload) (*IngestResponse, error) {
	ffID, same, err := findRetryOrSame(ctx, q, projectID, folderID, u.FileName, u.SHA256)
	if err != nil {
		return nil, err
	}
	if ffID != nil {
		return retriedResponse(ctx, q, *ffID, u)
	}
	if same != nil {
		return nil, duplicateError(ctx, q, same, nil)
	}
	return nil, nil
}

// retried 是断网重试的判断：同一文件夹里 upload_name 和 sha 都相同的已有文件（现在的信息），没有为 nil。
func (s *Service) retried(ctx context.Context, q db.Querier, folderID uuid.UUID, u *Upload) (*IngestResponse, error) {
	ffID, err := findRetry(ctx, q, folderID, u.FileName, u.SHA256)
	if err != nil || ffID == nil {
		return nil, err
	}
	return retriedResponse(ctx, q, *ffID, u)
}

func retriedResponse(ctx context.Context, q db.Querier, ffID uuid.UUID, u *Upload) (*IngestResponse, error) {
	r, err := ingested(ctx, q, ffID)
	if err != nil {
		return nil, err
	}
	r.Renamed = r.FileName != u.FileName
	return r, nil
}

// FindDuplicate：项目里未删除的文件当前版本的 sha 和 sha 相同时返回 409 duplicate_content（details 是那个文件的位置），
// 没有时返回 nil。self 不为 nil 时（重扫）优先报它自己；别的项目里有相同内容不算。
func (s *Service) FindDuplicate(ctx context.Context, q db.Querier, projectID uuid.UUID, sha []byte, self *uuid.UUID) error {
	c, err := findSameContent(ctx, q, projectID, sha, self)
	if err != nil || c == nil {
		return err
	}
	return duplicateError(ctx, q, c, self)
}

// duplicateError 是查到内容相同的文件 c 时的 409 duplicate_content。
func duplicateError(ctx context.Context, q db.Querier, c *sameContent, self *uuid.UUID) error {
	path, err := FolderPath(ctx, q, c.FolderID)
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
	ffID, same, err := findRetryOrSame(ctx, s.pool, projectID, folderID, req.FileName, sha)
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
	if same != nil {
		err := duplicateError(ctx, s.pool, same, nil)
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
