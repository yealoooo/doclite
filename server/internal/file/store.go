package file

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
)

// file 的 SQL：files、file_snapshots、file_logs。
//
// 跨模块的读（只读）：folder_files、folders（文件在哪、是否已删除、查重的范围）、projects（项目编码、是否已删除）、
// users（操作人、创建人的显示名）、storage_profiles（存储名称）。folder_files 的写经 [Folders]。

// currentVersion 是"s 是这个文件的当前快照"的条件。
const currentVersion = `s.version_no = (SELECT max(x.version_no) FROM file_snapshots x WHERE x.file_id = s.file_id)`

// projectCode 是项目编码（进 S3 key）。
func ProjectCode(ctx context.Context, q db.Querier, projectID uuid.UUID) (string, error) {
	var code string
	err := q.QueryRow(ctx, `SELECT project_code FROM projects WHERE id = $1`, projectID).Scan(&code)
	return code, err
}

// findRetry：同一文件夹里未删除的文件，当前快照的 upload_name 和 sha 都相同（断网重试）→ folder_files 的 id。
func findRetry(ctx context.Context, q db.Querier, folderID uuid.UUID, uploadName string, sha []byte) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT ff.id FROM file_snapshots s JOIN folder_files ff ON ff.file_id = s.file_id
WHERE s.content_sha256 = $1 AND s.upload_name = $2 AND ff.folder_id = $3 AND ff.deleted_at IS NULL AND `+currentVersion+`
ORDER BY ff.sort_no, ff.id LIMIT 1`, sha, uploadName, folderID).Scan(&id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// sameContent 是项目里内容相同的文件（查重的结果）。
type sameContent struct {
	FileID   uuid.UUID
	FolderID uuid.UUID
	FileName string
}

// findSameContent：项目里未删除的文件（所在文件夹也未删除）当前版本的 sha 相同。self 不为 nil 时优先返回它自己（重扫）。
func findSameContent(ctx context.Context, q db.Querier, projectID uuid.UUID, sha []byte, self *uuid.UUID) (*sameContent, error) {
	var c sameContent
	err := q.QueryRow(ctx, `SELECT ff.file_id, ff.folder_id, ff.file_name FROM file_snapshots s
JOIN folder_files ff ON ff.file_id = s.file_id JOIN folders f ON f.id = ff.folder_id
WHERE s.content_sha256 = $1 AND ff.project_id = $2 AND ff.deleted_at IS NULL AND f.deleted_at IS NULL AND `+currentVersion+`
ORDER BY (ff.file_id = $3) IS TRUE DESC, f.path_code, ff.sort_no, ff.id
LIMIT 1`, sha, projectID, self).Scan(&c.FileID, &c.FolderID, &c.FileName)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// NewSnapshot 是要插入的快照（上传的 original；M3-1 的重扫是 rescan、version_no = max + 1）。
type NewSnapshot struct {
	ID, FileID       uuid.UUID
	VersionNo        int
	SnapshotType     string
	StorageProfileID uuid.UUID
	StorageKey       string
	CoverKey         *string
	SHA256           []byte
	FileSize         int64
	MimeType         string
	UploadName       string
	Width, Height    int
	DPI              *int
	ColorMode        *string
	CreatedBy        uuid.UUID
}

func insertFile(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO files (id) VALUES ($1)`, id)
	return err
}

// InsertSnapshot 在调用方的事务里插入快照（只追加，硬性规则 2）。created_at 用 clock_timestamp()（插入的这一刻）：
// 事务可能等了锁，用事务开始的 now() 会早于别的事务里刚发生的事（例如条目定稿），"定稿后有新版本"会漏标。
func InsertSnapshot(ctx context.Context, q db.Querier, s *NewSnapshot) error {
	_, err := q.Exec(ctx, `INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, storage_profile_id, storage_key,
  cover_key, content_sha256, file_size, mime_type, upload_name, width_px, height_px, dpi, color_mode, created_by, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, clock_timestamp())`,
		s.ID, s.FileID, s.VersionNo, s.SnapshotType, s.StorageProfileID, s.StorageKey, s.CoverKey, s.SHA256, s.FileSize,
		s.MimeType, s.UploadName, s.Width, s.Height, s.DPI, s.ColorMode, s.CreatedBy)
	return err
}

// ingested 是一个文件现在的信息（上传的响应、断网重试的 200）。
func ingested(ctx context.Context, q db.Querier, folderFileID uuid.UUID) (*IngestResponse, error) {
	var r IngestResponse
	err := q.QueryRow(ctx, `SELECT ff.file_id, s.id, ff.id, ff.file_name, ff.sort_no, s.version_no, s.width_px, s.height_px,
  s.dpi, s.color_mode, s.file_size, s.cover_key IS NOT NULL
FROM folder_files ff
CROSS JOIN LATERAL (SELECT * FROM file_snapshots x WHERE x.file_id = ff.file_id ORDER BY x.version_no DESC LIMIT 1) s
WHERE ff.id = $1`, folderFileID).Scan(&r.FileID, &r.SnapshotID, &r.FolderFileID, &r.FileName, &r.SortNo, &r.VersionNo,
		&r.WidthPx, &r.HeightPx, &r.DPI, &r.ColorMode, &r.FileSize, &r.HasCover)
	if err != nil {
		return nil, err
	}
	r.Warnings = warnings(derefInt(r.DPI))
	return &r, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// liveFile 是文件现在所在的位置（未删除，所在文件夹也未删除）。
type liveFile struct {
	FolderFileID uuid.UUID
	ProjectID    uuid.UUID
	FolderID     uuid.UUID
	FileName     string
}

// getLiveFile：file_id 对应的未删除文件（所在文件夹、项目也未删除）；没有时为 nil。
func getLiveFile(ctx context.Context, q db.Querier, fileID uuid.UUID) (*liveFile, error) {
	var f liveFile
	err := q.QueryRow(ctx, `SELECT ff.id, ff.project_id, ff.folder_id, ff.file_name
FROM folder_files ff JOIN folders f ON f.id = ff.folder_id JOIN projects p ON p.id = ff.project_id
WHERE ff.file_id = $1 AND ff.deleted_at IS NULL AND f.deleted_at IS NULL AND p.deleted_at IS NULL`, fileID).
		Scan(&f.FolderFileID, &f.ProjectID, &f.FolderID, &f.FileName)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// batchRow 是 batch 的一行：文件和它的当前快照。
type batchRow struct {
	ProjectID uuid.UUID
	Snapshot  *Snapshot
}

// batchRows：这些 file_id 里未删除的文件（所在文件夹、项目也未删除）和当前快照。
func batchRows(ctx context.Context, q db.Querier, fileIDs []uuid.UUID) (map[uuid.UUID]batchRow, error) {
	rows, err := q.Query(ctx, `SELECT `+snapshotColumns+`, ff.project_id
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
JOIN projects p ON p.id = ff.project_id AND p.deleted_at IS NULL
CROSS JOIN LATERAL (SELECT * FROM file_snapshots x WHERE x.file_id = ff.file_id ORDER BY x.version_no DESC LIMIT 1) s
WHERE ff.file_id = ANY($1) AND ff.deleted_at IS NULL`, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]batchRow{}
	for rows.Next() {
		var pid uuid.UUID
		s, err := scanSnapshot(rows, &pid)
		if err != nil {
			return nil, err
		}
		out[s.FileID] = batchRow{ProjectID: pid, Snapshot: s}
	}
	return out, rows.Err()
}

// snapshotRow 是 snapshots 接口的一行。
type snapshotRow struct {
	Snapshot      *Snapshot
	CreatedByName *string
	ProfileName   string
}

func listSnapshots(ctx context.Context, q db.Querier, fileID uuid.UUID) ([]snapshotRow, error) {
	rows, err := q.Query(ctx, `SELECT `+snapshotColumns+`, u.display_name, sp.profile_name
FROM file_snapshots s
LEFT JOIN users u ON u.id = s.created_by
JOIN storage_profiles sp ON sp.id = s.storage_profile_id
WHERE s.file_id = $1 ORDER BY s.version_no`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []snapshotRow
	for rows.Next() {
		var r snapshotRow
		var err error
		if r.Snapshot, err = scanSnapshot(rows, &r.CreatedByName, &r.ProfileName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LogItem 是 `GET /api/files/{fid}/logs` 的一条。
type LogItem struct {
	ID         int64     `json:"id"`
	Action     string    `json:"action"`
	OperatorID uuid.UUID `json:"operator_id"`
	// OperatorName 是操作人现在的显示名。
	OperatorName string          `json:"operator_name"`
	Detail       json.RawMessage `json:"detail"`
	CreatedAt    time.Time       `json:"created_at"`
}

func listLogs(ctx context.Context, q db.Querier, fileID uuid.UUID) ([]LogItem, error) {
	rows, err := q.Query(ctx, `SELECT l.id, l.action, l.operator_id, u.display_name, l.detail, l.created_at
FROM file_logs l JOIN users u ON u.id = l.operator_id
WHERE l.file_id = $1 ORDER BY l.id DESC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogItem{}
	for rows.Next() {
		var l LogItem
		var detail []byte
		if err := rows.Scan(&l.ID, &l.Action, &l.OperatorID, &l.OperatorName, &detail, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.Detail = detail
		out = append(out, l)
	}
	return out, rows.Err()
}
