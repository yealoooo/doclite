package file

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/storage"
)

// 看图（requirements 5.7）：列表不返回图片地址，图片信息统一由 batch 给；全部版本由 snapshots 给。
// 图片地址是按天取整签名的预签名 URL（storage.Service.ImageURLs），图片不经过 server。

// MaxBatchIDs 是 batch 一次最多的 file_id 数。
const MaxBatchIDs = 500

// ImageFields 是快照的规格和图片地址（batch、snapshots 共用）。
type ImageFields struct {
	SnapshotID uuid.UUID `json:"snapshot_id"`
	VersionNo  int       `json:"version_no"`
	WidthPx    *int      `json:"width_px"`
	HeightPx   *int      `json:"height_px"`
	DPI        *int      `json:"dpi"`
	ColorMode  *string   `json:"color_mode"`
	FileSize   int64     `json:"file_size"`
	MimeType   string    `json:"mime_type"`
	// ContentSHA256 是小写 hex。
	ContentSHA256 string `json:"content_sha256"`
	storage.ImageURLs
}

func imageFields(s *Snapshot, urls storage.ImageURLs) ImageFields {
	return ImageFields{SnapshotID: s.ID, VersionNo: s.VersionNo, WidthPx: s.WidthPx, HeightPx: s.HeightPx, DPI: s.DPI,
		ColorMode: s.ColorMode, FileSize: s.FileSize, MimeType: s.MimeType, ContentSHA256: hex.EncodeToString(s.ContentSHA256),
		ImageURLs: urls}
}

func objectRef(s *Snapshot) storage.ObjectRef {
	return storage.ObjectRef{StorageProfileID: s.StorageProfileID, StorageKey: s.StorageKey, CoverKey: s.CoverKey}
}

// ImageItem 是 batch 的一项：文件当前版本的信息和图片地址。
type ImageItem struct {
	FileID    uuid.UUID `json:"file_id"`
	ProjectID uuid.UUID `json:"project_id"`
	ImageFields
}

// BatchResponse 是 `POST /api/files/batch` 的响应。
type BatchResponse struct {
	// Items 按请求里的顺序（去重后）。
	Items []ImageItem `json:"items"`
	// Missing 是不存在、无权访问、已删除（含所在文件夹已删除）的 file_id。
	Missing []uuid.UUID `json:"missing"`
}

// Batch 给一批文件当前版本的信息和图片地址（登录即可；只给用户能进的项目里的文件，其余放 missing）。
// 空列表返回空结果；超过 500 个 400。
func (s *Service) Batch(ctx context.Context, userID uuid.UUID, fileIDs []uuid.UUID) (*BatchResponse, error) {
	if len(fileIDs) > MaxBatchIDs {
		return nil, httpx.Invalid("一次最多取 %d 个文件的图片信息", MaxBatchIDs)
	}
	ids := dedup(fileIDs)
	out := &BatchResponse{Items: []ImageItem{}, Missing: []uuid.UUID{}}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := batchRows(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	var pids []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, r := range rows {
		if !seen[r.ProjectID] {
			seen[r.ProjectID] = true
			pids = append(pids, r.ProjectID)
		}
	}
	allowed, err := s.auth.AccessibleProjects(ctx, userID, pids)
	if err != nil {
		return nil, err
	}
	var found []*Snapshot
	var refs []storage.ObjectRef
	for _, id := range ids {
		r, ok := rows[id]
		if !ok || !allowed[r.ProjectID] {
			out.Missing = append(out.Missing, id)
			continue
		}
		found = append(found, r.Snapshot)
		refs = append(refs, objectRef(r.Snapshot))
		out.Items = append(out.Items, ImageItem{FileID: id, ProjectID: r.ProjectID})
	}
	urls, err := s.storage.ImageURLs(ctx, refs)
	if err != nil {
		return nil, err
	}
	for i := range out.Items {
		out.Items[i].ImageFields = imageFields(found[i], urls[i])
	}
	return out, nil
}

// SnapshotItem 是 snapshots 的一个版本。
type SnapshotItem struct {
	ImageFields
	SnapshotType string     `json:"snapshot_type"`
	UploadName   *string    `json:"upload_name"`
	CreatedAt    time.Time  `json:"created_at"`
	CreatedBy    *uuid.UUID `json:"created_by"`
	// CreatedByName 是创建人现在的显示名；null 表示系统。
	CreatedByName      *string   `json:"created_by_name"`
	StorageProfileID   uuid.UUID `json:"storage_profile_id"`
	StorageProfileName string    `json:"storage_profile_name"`
	StorageKey         string    `json:"storage_key"`
}

// SnapshotsResponse 是 `GET /api/files/{fid}/snapshots` 的响应。
type SnapshotsResponse struct {
	FileID    uuid.UUID `json:"file_id"`
	ProjectID uuid.UUID `json:"project_id"`
	// Snapshots 按版本号从小到大。
	Snapshots []SnapshotItem `json:"snapshots"`
}

var errFileNotFound = httpx.NotFound("文件")

// Snapshots 是文件的全部版本（登录即可）。文件不存在、已删除（含所在文件夹）、或用户进不了它的项目时都是 404
// （和 batch 一样不透露进不了的项目里有什么）。
func (s *Service) Snapshots(ctx context.Context, userID, fileID uuid.UUID) (*SnapshotsResponse, error) {
	f, err := getLiveFile(ctx, s.pool, fileID)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errFileNotFound
	}
	allowed, err := s.auth.AccessibleProjects(ctx, userID, []uuid.UUID{f.ProjectID})
	if err != nil {
		return nil, err
	}
	if !allowed[f.ProjectID] {
		return nil, errFileNotFound
	}
	rows, err := listSnapshots(ctx, s.pool, fileID)
	if err != nil {
		return nil, err
	}
	refs := make([]storage.ObjectRef, len(rows))
	for i, r := range rows {
		refs[i] = objectRef(r.Snapshot)
	}
	urls, err := s.storage.ImageURLs(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := &SnapshotsResponse{FileID: fileID, ProjectID: f.ProjectID, Snapshots: make([]SnapshotItem, len(rows))}
	for i, r := range rows {
		sn := r.Snapshot
		out.Snapshots[i] = SnapshotItem{ImageFields: imageFields(sn, urls[i]), SnapshotType: sn.SnapshotType,
			UploadName: sn.UploadName, CreatedAt: sn.CreatedAt, CreatedBy: sn.CreatedBy, CreatedByName: r.CreatedByName,
			StorageProfileID: sn.StorageProfileID, StorageProfileName: r.ProfileName, StorageKey: sn.StorageKey}
	}
	return out, nil
}

// LogsResponse 是 `GET /api/files/{fid}/logs` 的响应。
type LogsResponse struct {
	FileID uuid.UUID `json:"file_id"`
	// Logs 新的在前。
	Logs []LogItem `json:"logs"`
}

// Logs 是文件日志（新的在前，带操作人现在的显示名）。项目授权由路由检查。
func (s *Service) Logs(ctx context.Context, fileID uuid.UUID) (*LogsResponse, error) {
	logs, err := listLogs(ctx, s.pool, fileID)
	if err != nil {
		return nil, err
	}
	return &LogsResponse{FileID: fileID, Logs: logs}, nil
}

// ProjectOfFile 是文件所在的项目：文件不存在、已删除（含所在文件夹、项目）404。以 file_id 为参数的接口用它反查项目
// （auth.Gate.InProject）。
func (s *Service) ProjectOfFile(ctx context.Context, fileID uuid.UUID) (uuid.UUID, error) {
	f, err := getLiveFile(ctx, s.pool, fileID)
	if err != nil {
		return uuid.Nil, err
	}
	if f == nil {
		return uuid.Nil, errFileNotFound
	}
	return f.ProjectID, nil
}

// dedup 去重，保持第一次出现的顺序。
func dedup(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
