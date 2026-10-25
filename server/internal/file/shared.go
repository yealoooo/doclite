package file

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/xslices"
)

// 给其他模块在自己的事务里调用的函数（文件夹的整理、以后的审核、归类、导出）。

// file_logs 的 action（requirements 3.5）。
const (
	ActionUpload         = "upload"
	ActionRescan         = "rescan"
	ActionRename         = "rename"
	ActionMove           = "move"
	ActionCopy           = "copy"
	ActionDelete         = "delete"
	ActionAssignReview   = "assign_review"
	ActionReassignReview = "reassign_review"
	ActionApprove        = "approve"
	ActionReject         = "reject"
	ActionReopen         = "reopen"
	ActionLink           = "link"
	ActionUnlink         = "unlink"
	ActionExport         = "export"
)

// 快照类型（file_snapshots.snapshot_type）。
const (
	SnapshotOriginal = "original"
	SnapshotRescan   = "rescan"
	SnapshotCopy     = "copy"
)

// Log 是一条文件日志。Detail 存当时的名字（文件名、文件夹名）和从根文件夹起的文件夹路径（folder_path），
// 只看日志就能还原一个文件的全过程（requirements 3.3、3.5）。
type Log struct {
	ProjectID  uuid.UUID
	FileID     uuid.UUID
	Action     string
	OperatorID uuid.UUID
	// Detail 序列化成 JSON 对象；nil 时是 `{}`。
	Detail any
}

// logBatch 是 WriteLogs 一条 INSERT 最多写的行数（导出完成一次写全部页，几万行时分几条语句，参数不至于太大）。
const logBatch = 10000

// WriteLogs 在调用方的事务里写文件日志（每 logBatch 条一条 INSERT，按给的顺序，id 递增）。
func WriteLogs(ctx context.Context, q db.Querier, logs ...Log) error {
	for len(logs) > logBatch {
		if err := writeLogs(ctx, q, logs[:logBatch]); err != nil {
			return err
		}
		logs = logs[logBatch:]
	}
	return writeLogs(ctx, q, logs)
}

func writeLogs(ctx context.Context, q db.Querier, logs []Log) error {
	if len(logs) == 0 {
		return nil
	}
	args, err := logArgs(logs)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, insertLogsSQL, args...)
	return err
}

// QueueLogs 把写文件日志排进调用方的批量（和别的写入一次往返发出去；条数少，不分批）。
func QueueLogs(b *pgx.Batch, logs ...Log) error {
	if len(logs) == 0 {
		return nil
	}
	args, err := logArgs(logs)
	if err != nil {
		return err
	}
	b.Queue(insertLogsSQL, args...)
	return nil
}

const insertLogsSQL = `INSERT INTO file_logs (project_id, file_id, action, operator_id, detail)
SELECT t.pid, t.fid, t.action, t.op, t.detail::jsonb
FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::uuid[], $5::text[]) WITH ORDINALITY AS t(pid, fid, action, op, detail, i)
ORDER BY t.i`

func logArgs(logs []Log) ([]any, error) {
	pids := make([]uuid.UUID, len(logs))
	fids := make([]uuid.UUID, len(logs))
	actions := make([]string, len(logs))
	ops := make([]uuid.UUID, len(logs))
	details := make([]string, len(logs))
	for i, l := range logs {
		pids[i], fids[i], actions[i], ops[i] = l.ProjectID, l.FileID, l.Action, l.OperatorID
		d := l.Detail
		if d == nil {
			d = map[string]any{}
		}
		b, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		details[i] = string(b)
	}
	return []any{pids, fids, actions, ops, details}, nil
}

// LockFiles 在调用方的事务里按 id 升序锁住这些文件的 files 行（FOR NO KEY UPDATE，不挡外键检查）。
// files 行是每个文件的写锁（requirements 3.6）：分配、审核、重扫、移动、复制、删除、挂接、移出都先锁它，锁住之后
// 再重读 folder_files（是否已删除、在哪个文件夹、审核状态）。调用前要先拿项目文件树锁（project.LockFileTree）；
// 两棵树都要的操作先拿归类的锁和行。不存在的 id 忽略。
func LockFiles(ctx context.Context, q db.Querier, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM files WHERE id = ANY($1) ORDER BY id FOR NO KEY UPDATE`, xslices.SortedIDs(ids))
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

// LockProjectFiles 同 [LockFiles]，只锁在项目 projectID 里的文件（folder_files.project_id，含已删除的），一条语句；
// 返回锁住的 id（升序）。
func LockProjectFiles(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return db.CollectIDs(ctx, q, `SELECT x.id FROM files x WHERE x.id = ANY($1)
  AND EXISTS (SELECT 1 FROM folder_files ff WHERE ff.file_id = x.id AND ff.project_id = $2)
ORDER BY x.id FOR NO KEY UPDATE OF x`, ids, projectID)
}

// Snapshot 是 file_snapshots 的一行。
type Snapshot struct {
	ID           uuid.UUID
	FileID       uuid.UUID
	VersionNo    int
	SnapshotType string
	// ParentSnapshotID、EditOps：复制时记来源快照（edit_ops 是 JSON），其他为 nil。
	ParentSnapshotID *uuid.UUID
	EditOps          []byte
	StorageProfileID uuid.UUID
	StorageKey       string
	// CoverKey 为 nil 表示没有封面。
	CoverKey      *string
	ContentSHA256 []byte
	FileSize      int64
	MimeType      string
	UploadName    *string
	WidthPx       *int
	HeightPx      *int
	DPI           *int
	ColorMode     *string
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
}

const snapshotColumns = `s.id, s.file_id, s.version_no, s.snapshot_type, s.parent_snapshot_id, s.edit_ops,
  s.storage_profile_id, s.storage_key, s.cover_key, s.content_sha256, s.file_size, s.mime_type, s.upload_name, s.width_px, s.height_px, s.dpi, s.color_mode, s.created_by, s.created_at`

func scanSnapshot(sc pgx.Row, extra ...any) (*Snapshot, error) {
	var s Snapshot
	dest := []any{&s.ID, &s.FileID, &s.VersionNo, &s.SnapshotType, &s.ParentSnapshotID, &s.EditOps, &s.StorageProfileID, &s.StorageKey, &s.CoverKey,
		&s.ContentSHA256, &s.FileSize, &s.MimeType, &s.UploadName, &s.WidthPx, &s.HeightPx, &s.DPI, &s.ColorMode,
		&s.CreatedBy, &s.CreatedAt}
	if err := sc.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	return &s, nil
}

// Current 是文件的当前快照（max(version_no)）；文件不存在时为 nil。不看文件是否已删除。
func Current(ctx context.Context, q db.Querier, fileID uuid.UUID) (*Snapshot, error) {
	s, err := scanSnapshot(q.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM file_snapshots s
WHERE s.file_id = $1 ORDER BY s.version_no DESC LIMIT 1`, fileID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return s, err
}

// CurrentOf 是一批文件的当前快照（按 file_id）；没有快照的不在结果里。
func CurrentOf(ctx context.Context, q db.Querier, fileIDs []uuid.UUID) (map[uuid.UUID]*Snapshot, error) {
	out := map[uuid.UUID]*Snapshot{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT `+snapshotColumns+` FROM unnest($1::uuid[]) AS f(id)
CROSS JOIN LATERAL (SELECT * FROM file_snapshots x WHERE x.file_id = f.id ORDER BY x.version_no DESC LIMIT 1) s`, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out[s.FileID] = s
	}
	return out, rows.Err()
}

// InsertCopy 在调用方的事务里为复制建一个新文件：files 行和 v1 copy 快照（requirements 5.5）。复用源快照的存储、
// 对象 key、封面 key、sha、大小和规格，不复制字节；parent_snapshot_id 指向源快照，edit_ops 记来源，upload_name 为空。
// 源快照所在存储的检查（不是 disabled）由调用方先做（storage.Service.LockForReuse）。返回新快照的 id。
func InsertCopy(ctx context.Context, q db.Querier, newFileID uuid.UUID, src *Snapshot, createdBy uuid.UUID) (uuid.UUID, error) {
	ops, err := json.Marshal(map[string]any{"source_file_id": src.FileID, "source_snapshot_id": src.ID})
	if err != nil {
		return uuid.Nil, err
	}
	snap := *src
	snap.ID, snap.FileID, snap.VersionNo, snap.SnapshotType = db.NewID(), newFileID, 1, SnapshotCopy
	snap.ParentSnapshotID, snap.EditOps, snap.UploadName, snap.CreatedBy = &src.ID, ops, nil, &createdBy
	return snap.ID, InsertSnapshot(ctx, q, &snap)
}

// FolderPaths 是一批文件夹从根文件夹起用 / 连接的路径（按 id；日志 detail 的 folder_path、界面显示的路径）。
// 跨模块的只读查询（folders 属于 internal/folder），文件夹、审核、归类都用。
func FolderPaths(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	infos, err := FolderInfos(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(infos))
	for id, f := range infos {
		out[id] = f.Path
	}
	return out, nil
}

// FolderInfo 是文件夹现在的名字和路径（见 [FolderPaths]）。
type FolderInfo struct {
	Name string
	Path string
}

// FolderInfos 是一批文件夹的名字和路径（按 id；跨模块的只读查询，同 [FolderPaths]）。
func FolderInfos(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]FolderInfo, error) {
	out := map[uuid.UUID]FolderInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT f.id, f.folder_name, `+FolderPathSQL("f.id")+` FROM folders f WHERE f.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var f FolderInfo
		if err := rows.Scan(&id, &f.Name, &f.Path); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}

// LiveFolder 是项目里一个未删除的文件夹（看板、审核的范围文件夹）。
type LiveFolder struct {
	ID         uuid.UUID
	ParentID   *uuid.UUID
	FolderName string
	PathCode   string
}

// GetLiveFolder 读项目里未删除的文件夹（folderID 为 nil 时是项目的根文件夹）；不在这个项目里或已删除为 nil。
// 跨模块的只读查询（同 FolderPaths）。
func GetLiveFolder(ctx context.Context, q db.Querier, projectID uuid.UUID, folderID *uuid.UUID) (*LiveFolder, error) {
	var f LiveFolder
	var row pgx.Row
	if folderID == nil {
		row = q.QueryRow(ctx, `SELECT id, parent_id, folder_name, path_code FROM folders
WHERE project_id = $1 AND parent_id IS NULL AND deleted_at IS NULL`, projectID)
	} else {
		row = q.QueryRow(ctx, `SELECT id, parent_id, folder_name, path_code FROM folders
WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`, *folderID, projectID)
	}
	err := row.Scan(&f.ID, &f.ParentID, &f.FolderName, &f.PathCode)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// FolderPathSQL 是文件夹 idExpr 的路径（见 [FolderPaths]）的标量子查询，给别的模块拼进自己的查询、一次往返带上路径。
// 跨模块的只读查询（同 FolderPaths）。
func FolderPathSQL(idExpr string) string {
	return `(SELECT coalesce(string_agg(a.folder_name, '/' ORDER BY g.n), '') FROM folders fp
  CROSS JOIN LATERAL generate_series(6, length(fp.path_code), 6) AS g(n)
  JOIN folders a ON a.project_id = fp.project_id AND a.path_code = left(fp.path_code, g.n)
  WHERE fp.id = ` + idExpr + `)`
}

// FolderPath 是一个文件夹的路径（见 [FolderPaths]）。
func FolderPath(ctx context.Context, q db.Querier, folderID uuid.UUID) (string, error) {
	paths, err := FolderPaths(ctx, q, []uuid.UUID{folderID})
	if err != nil {
		return "", err
	}
	return paths[folderID], nil
}
