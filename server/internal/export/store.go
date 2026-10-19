package export

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
)

// export 的 SQL：export_tasks、export_task_files，以及导出任务的 background_jobs 行（payload、进度、错误）。
//
// 跨模块的只读查询（requirements 3.6：建任务、预检、交付后变更在一致快照里读，不加锁）：
//   - projects（settings、project_code）；users（建任务的人的显示名）；
//   - archive_categories（范围、名称链）、archive_items（条目、著录、状态）、archive_item_files（条目的页，页号 1..n）；
//   - folder_files、folders（文件和文件夹未删除才算页、审核状态读那一刻的值、文件名）；
//   - file_snapshots（冻结的快照的大小、sha256、mime、存储和 key；交付后变更时导出的版本号）。
//
// 当前快照用 file.CurrentOf（file 模块），文件日志经 file.WriteLogs；审计经 audit.Write。

// readSnapshot 在一个只读的 REPEATABLE READ 事务里执行 fn（预检、交付后变更的几条查询看到同一个快照）。
func readSnapshot(ctx context.Context, pool interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// projectSettings 读未删除项目的 settings（跨模块的读）；项目不存在或已删除时 found 为 false。
func projectSettings(ctx context.Context, q db.Querier, projectID uuid.UUID) (raw []byte, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT settings FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).Scan(&raw)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if raw == nil {
		raw = []byte("{}")
	}
	return raw, err == nil, err
}

// liveCategories 是 ids 里这个项目未删除的节点（id → path_code）。
func liveCategories(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := q.Query(ctx, `SELECT id, path_code FROM archive_categories WHERE id = ANY($2) AND project_id = $1 AND deleted_at IS NULL`,
		projectID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		out[id] = path
	}
	return out, rows.Err()
}

// liveItemIDs 是 ids 里这个项目未删除的条目。
func liveItemIDs(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := q.Query(ctx, `SELECT id FROM archive_items WHERE id = ANY($2) AND project_id = $1 AND deleted_at IS NULL`, projectID, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(list))
	for _, id := range list {
		out[id] = true
	}
	return out, nil
}

// subtreeItemIDs 是这些节点（path_code）子树里未删除的条目（子树重叠只算一次）。
func subtreeItemIDs(ctx context.Context, q db.Querier, projectID uuid.UUID, paths []string) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT a.id
FROM unnest($2::text[]) AS s(path_code)
JOIN archive_categories c ON c.project_id = $1 AND c.deleted_at IS NULL AND c.path_code LIKE s.path_code || '%'
JOIN archive_items a ON a.category_id = c.id AND a.deleted_at IS NULL`, projectID, paths)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// scopeItem 是范围里的一个条目（现在的样子）和它的页的统计。
type scopeItem struct {
	ID            uuid.UUID
	CategoryID    uuid.UUID
	ItemNo        int
	ArchiveNo     *string
	Title         string
	ExtraFields   map[string]any
	Status        string
	SourceStatus  string
	RowVersion    int
	PageCount     int
	Unapproved    int
	Examples      []PageRef // 前 5 个不是 approved 的页
	CategoryNames []string
}

// scopeItems 读这些条目（未删除、这个项目的）和它们的页数、不是通过的页数和前 5 页，按导出顺序（所在节点的
// path_code、件号、id）。页 = 挂接行里文件和所在文件夹都未删除的。
func scopeItems(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) ([]*scopeItem, error) {
	rows, err := q.Query(ctx, `WITH pages AS (
  SELECT af.archive_item_id AS item_id, af.page_no, af.file_id, ff.file_name, ff.review_status
  FROM archive_item_files af
  JOIN folder_files ff ON ff.file_id = af.file_id AND ff.deleted_at IS NULL
  JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
  WHERE af.archive_item_id = ANY($2)
), agg AS (
  SELECT item_id, count(*) AS page_count,
    count(*) FILTER (WHERE review_status <> 'approved') AS unapproved,
    (array_agg(page_no ORDER BY page_no) FILTER (WHERE review_status <> 'approved'))[1:5] AS ex_page,
    (array_agg(file_id ORDER BY page_no) FILTER (WHERE review_status <> 'approved'))[1:5] AS ex_file,
    (array_agg(file_name ORDER BY page_no) FILTER (WHERE review_status <> 'approved'))[1:5] AS ex_name,
    (array_agg(review_status ORDER BY page_no) FILTER (WHERE review_status <> 'approved'))[1:5] AS ex_status
  FROM pages GROUP BY item_id
)
SELECT a.id, a.category_id, a.item_no, a.archive_no, a.title, a.extra_fields, a.status, a.source_status, a.row_version,
  COALESCE(g.page_count, 0), COALESCE(g.unapproved, 0), g.ex_page, g.ex_file, g.ex_name, g.ex_status
FROM archive_items a
JOIN archive_categories c ON c.id = a.category_id
LEFT JOIN agg g ON g.item_id = a.id
WHERE a.project_id = $1 AND a.id = ANY($2) AND a.deleted_at IS NULL
ORDER BY c.path_code, a.item_no, a.id`, projectID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*scopeItem
	for rows.Next() {
		var it scopeItem
		var exPage []int32
		var exFile []uuid.UUID
		var exName, exStatus []string
		if err := rows.Scan(&it.ID, &it.CategoryID, &it.ItemNo, &it.ArchiveNo, &it.Title, &it.ExtraFields, &it.Status,
			&it.SourceStatus, &it.RowVersion, &it.PageCount, &it.Unapproved, &exPage, &exFile, &exName, &exStatus); err != nil {
			return nil, err
		}
		for i := range exPage {
			it.Examples = append(it.Examples, PageRef{PageNo: int(exPage[i]), FileID: exFile[i], FileName: exName[i], ReviewStatus: exStatus[i]})
		}
		if it.ExtraFields == nil {
			it.ExtraFields = map[string]any{}
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

// categoryNames 是这些节点的名称链（从根节点的下一级起到节点自己；根节点为空数组）。
func categoryNames(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID][]string, error) {
	out := make(map[uuid.UUID][]string, len(ids))
	for _, id := range ids {
		out[id] = []string{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT c.id, array_agg(x.category_name ORDER BY x.path_code)
FROM archive_categories c
CROSS JOIN LATERAL generate_series(12, length(c.path_code), 6) AS g(n)
JOIN archive_categories x ON x.project_id = c.project_id AND x.path_code = left(c.path_code, g.n)
WHERE c.id = ANY($1)
GROUP BY c.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var names []string
		if err := rows.Scan(&id, &names); err != nil {
			return nil, err
		}
		out[id] = names
	}
	return out, rows.Err()
}

// pageRow 是条目的一页（冻结用）。
type pageRow struct {
	ItemID uuid.UUID
	FileID uuid.UUID
	PageNo int
}

// itemPageRows 是这些条目的页（文件和所在文件夹未删除的），按条目、页号。
func itemPageRows(ctx context.Context, q db.Querier, itemIDs []uuid.UUID) ([]pageRow, error) {
	rows, err := q.Query(ctx, `SELECT af.archive_item_id, af.file_id, af.page_no
FROM archive_item_files af
JOIN folder_files ff ON ff.file_id = af.file_id AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
WHERE af.archive_item_id = ANY($1)
ORDER BY af.archive_item_id, af.page_no`, itemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pageRow
	for rows.Next() {
		var p pageRow
		if err := rows.Scan(&p.ItemID, &p.FileID, &p.PageNo); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// taskFileRow 是 export_task_files 的一行。
type taskFileRow struct {
	ItemID, FileID, SnapshotID uuid.UUID
	PageNo                     int
}

// insertTaskFiles 写冻结的明细（COPY）。
func insertTaskFiles(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, rows []taskFileRow) error {
	n, err := tx.CopyFrom(ctx, pgx.Identifier{"export_task_files"},
		[]string{"export_task_id", "archive_item_id", "file_id", "snapshot_id", "page_no"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{taskID, r.ItemID, r.FileID, r.SnapshotID, int32(r.PageNo)}, nil
		}))
	if err != nil {
		return err
	}
	if int(n) != len(rows) {
		return errInternal("写明细的行数 %d 和页数 %d 对不上", n, len(rows))
	}
	return nil
}

func insertTask(ctx context.Context, q db.Querier, t *taskInsert) error {
	_, err := q.Exec(ctx, `INSERT INTO export_tasks (id, project_id, scope_type, scope_ids, export_format, only_approved, status, job_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7, $8)`, t.ID, t.ProjectID, t.ScopeType, t.ScopeIDs, t.Format, t.OnlyApproved, t.JobID, t.CreatedBy)
	return err
}

func insertJob(ctx context.Context, q db.Querier, jobID, projectID, createdBy uuid.UUID, payload []byte, pages int) error {
	_, err := q.Exec(ctx, `INSERT INTO background_jobs (id, project_id, job_type, payload, status, progress_total, created_by)
VALUES ($1, $2, $3, $4, 'queued', $5, $6)`, jobID, projectID, JobType, string(payload), pages, createdBy)
	return err
}

type taskInsert struct {
	ID, ProjectID, JobID, CreatedBy uuid.UUID
	ScopeType, Format               string
	ScopeIDs                        []uuid.UUID
	OnlyApproved                    bool
}

// ---------------------------------------------------------------------------
// 任务的读

// taskRow 是任务和它的 job（payload 只取 items 以外的部分，见 [fillHeaders]）。
type taskRow struct {
	ID               uuid.UUID
	ProjectID        uuid.UUID
	ScopeType        string
	ScopeIDs         []uuid.UUID
	Format           string
	OnlyApproved     bool
	Status           string
	StorageProfileID *uuid.UUID
	OutputKey        *string
	OutputSize       *int64
	ExpiresAt        *time.Time
	Expired          bool
	CreatedBy        uuid.UUID
	CreatedByName    *string
	CreatedAt        time.Time
	FinishedAt       *time.Time
	JobID            *uuid.UUID
	JobStatus        *string
	ProgressDone     int
	ProgressTotal    int
	Attempts         int
	MaxAttempts      int
	ErrorMessage     *string
	StartedAt        *time.Time
	Header           payload
}

const taskSelect = `SELECT t.id, t.project_id, t.scope_type, t.scope_ids, t.export_format, t.only_approved, t.status,
  t.storage_profile_id, t.output_key, t.output_size, t.expires_at, (t.expires_at IS NOT NULL AND t.expires_at <= now()),
  t.created_by, u.display_name, t.created_at, t.finished_at,
  t.job_id, j.status, COALESCE(j.progress_done, 0), COALESCE(j.progress_total, 0), COALESCE(j.attempts, 0),
  COALESCE(j.max_attempts, 0), j.error_message, j.started_at
FROM export_tasks t
LEFT JOIN background_jobs j ON j.id = t.job_id
LEFT JOIN users u ON u.id = t.created_by`

func scanTask(sc interface{ Scan(...any) error }) (*taskRow, error) {
	var r taskRow
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.ScopeType, &r.ScopeIDs, &r.Format, &r.OnlyApproved, &r.Status,
		&r.StorageProfileID, &r.OutputKey, &r.OutputSize, &r.ExpiresAt, &r.Expired,
		&r.CreatedBy, &r.CreatedByName, &r.CreatedAt, &r.FinishedAt,
		&r.JobID, &r.JobStatus, &r.ProgressDone, &r.ProgressTotal, &r.Attempts, &r.MaxAttempts, &r.ErrorMessage,
		&r.StartedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// headers 缓存 job id → payload 去掉 items 的部分（taskRow.Header）。导出的 payload 写后不改、job 行永久保留，
// 所以缓存不用失效：执行中的任务每秒被轮询一次，不用每次把整个 payload（几万个条目时几 MB）从 TOAST 里解压出来。
// job id 是 UUIDv7，几个 Service（测试里几个库）共用一份也不会串。条目太多时整个清掉重来。
var headers = struct {
	sync.Mutex
	m map[uuid.UUID]payload
}{m: map[uuid.UUID]payload{}}

const maxCachedHeaders = 4096

// fillHeaders 填这些任务的 Header：缓存里没有的用一条语句读 `payload - 'items'`。
func fillHeaders(ctx context.Context, q db.Querier, rows []*taskRow) error {
	var miss []uuid.UUID
	headers.Lock()
	for _, r := range rows {
		if r.JobID == nil {
			continue
		}
		if h, ok := headers.m[*r.JobID]; ok {
			r.Header = h
		} else {
			miss = append(miss, *r.JobID)
		}
	}
	headers.Unlock()
	if len(miss) == 0 {
		return nil
	}
	res, err := q.Query(ctx, `SELECT id, payload - 'items' FROM background_jobs WHERE id = ANY($1)`, miss)
	if err != nil {
		return err
	}
	defer res.Close()
	got := map[uuid.UUID]payload{}
	for res.Next() {
		var id uuid.UUID
		var raw []byte
		if err := res.Scan(&id, &raw); err != nil {
			return err
		}
		var h payload
		if err := json.Unmarshal(raw, &h); err != nil {
			return errInternal("后台任务 %s 的 payload 不对：%v", id, err)
		}
		got[id] = h
	}
	if err := res.Err(); err != nil {
		return err
	}
	headers.Lock()
	if len(headers.m)+len(got) > maxCachedHeaders {
		clear(headers.m)
	}
	for id, h := range got {
		headers.m[id] = h
	}
	headers.Unlock()
	for _, r := range rows {
		if r.JobID != nil {
			if h, ok := got[*r.JobID]; ok {
				r.Header = h
			}
		}
	}
	return nil
}

// getTask 读一个任务；不存在为 nil。
func getTask(ctx context.Context, q db.Querier, id uuid.UUID) (*taskRow, error) {
	r, err := scanTask(q.QueryRow(ctx, taskSelect+` WHERE t.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := fillHeaders(ctx, q, []*taskRow{r}); err != nil {
		return nil, err
	}
	return r, nil
}

// listTasks 是项目的任务，最新的在前（keyset：created_at、id 倒序），多取一个判断还有没有下一页。
func listTasks(ctx context.Context, q db.Querier, projectID uuid.UUID, afterAt *time.Time, afterID uuid.UUID, limit int) ([]*taskRow, error) {
	rows, err := q.Query(ctx, taskSelect+`
WHERE t.project_id = $1 AND ($2::timestamptz IS NULL OR (t.created_at, t.id) < ($2, $3))
ORDER BY t.created_at DESC, t.id DESC
LIMIT $4`, projectID, afterAt, afterID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*taskRow
	for rows.Next() {
		r, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := fillHeaders(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

// taskProject 是任务所在的项目；没有为 uuid.Nil。
func taskProject(ctx context.Context, q db.Querier, id uuid.UUID) (uuid.UUID, error) {
	var pid uuid.UUID
	err := q.QueryRow(ctx, `SELECT project_id FROM export_tasks WHERE id = $1`, id).Scan(&pid)
	if db.IsNoRows(err) {
		return uuid.Nil, nil
	}
	return pid, err
}

// jobPayload 读任务的 job 的整个 payload。
func jobPayload(ctx context.Context, q db.Querier, jobID uuid.UUID) (*payload, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT payload FROM background_jobs WHERE id = $1`, jobID).Scan(&raw); err != nil {
		return nil, err
	}
	return parsePayload(raw)
}

// TaskFile 是明细的一页（冻结时的件号、档号、题名和快照，文件现在的名字）。
type TaskFile struct {
	Seq           int       `json:"seq"`
	ItemID        uuid.UUID `json:"item_id"`
	ItemNo        int       `json:"item_no"`
	ArchiveNo     *string   `json:"archive_no"`
	Title         string    `json:"title"`
	PageNo        int       `json:"page_no"`
	FileID        uuid.UUID `json:"file_id"`
	SnapshotID    uuid.UUID `json:"snapshot_id"`
	VersionNo     int       `json:"version_no"`
	MimeType      string    `json:"mime_type"`
	FileSize      int64     `json:"file_size"`
	ContentSHA256 string    `json:"content_sha256"`
	UploadName    *string   `json:"upload_name"`
	FileName      *string   `json:"file_name"`
}

// taskFiles 是明细的一页：按条目在导出里的序号（payload 的 items 的位置）、页号，keyset。
func taskFiles(ctx context.Context, q db.Querier, taskID, jobID uuid.UUID, afterSeq, afterPage, limit int) ([]TaskFile, error) {
	rows, err := q.Query(ctx, `WITH items AS (
  SELECT (e.value->>'id')::uuid AS item_id, e.ord::int AS seq, (e.value->>'item_no')::int AS item_no,
    e.value->>'archive_no' AS archive_no, COALESCE(e.value->>'title', '') AS title
  FROM background_jobs j
  CROSS JOIN LATERAL jsonb_array_elements(j.payload->'items') WITH ORDINALITY AS e(value, ord)
  WHERE j.id = $2
)
SELECT i.seq, i.item_id, i.item_no, i.archive_no, i.title, x.page_no, x.file_id, x.snapshot_id,
  s.version_no, s.mime_type, s.file_size, encode(s.content_sha256, 'hex'), s.upload_name, ff.file_name
FROM export_task_files x
JOIN items i ON i.item_id = x.archive_item_id
JOIN file_snapshots s ON s.id = x.snapshot_id
LEFT JOIN folder_files ff ON ff.file_id = x.file_id
WHERE x.export_task_id = $1 AND (i.seq, x.page_no) > ($3, $4)
ORDER BY i.seq, x.page_no
LIMIT $5`, taskID, jobID, afterSeq, afterPage, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskFile
	for rows.Next() {
		var f TaskFile
		if err := rows.Scan(&f.Seq, &f.ItemID, &f.ItemNo, &f.ArchiveNo, &f.Title, &f.PageNo, &f.FileID, &f.SnapshotID,
			&f.VersionNo, &f.MimeType, &f.FileSize, &f.ContentSHA256, &f.UploadName, &f.FileName); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 交付后变更的读

// exportedPage 是导出的一页和那个文件现在的样子。
type exportedPage struct {
	ItemID          uuid.UUID
	FileID          uuid.UUID
	PageNo          int
	ExportedVersion int
	ItemNow         *uuid.UUID // 现在挂在哪个条目（没挂接为 nil）
	PageNow         *int
	ReviewStatus    *string // 文件或文件夹已删除时为 nil
	FileName        *string
	VersionNow      int // 由 file.CurrentOf 补上
}

func exportedPages(ctx context.Context, q db.Querier, taskID uuid.UUID) ([]*exportedPage, error) {
	rows, err := q.Query(ctx, `SELECT x.archive_item_id, x.file_id, x.page_no, s.version_no, af.archive_item_id, af.page_no,
  CASE WHEN ff.deleted_at IS NULL AND f.deleted_at IS NULL THEN ff.review_status END, ff.file_name
FROM export_task_files x
JOIN file_snapshots s ON s.id = x.snapshot_id
LEFT JOIN archive_item_files af ON af.file_id = x.file_id
LEFT JOIN folder_files ff ON ff.file_id = x.file_id
LEFT JOIN folders f ON f.id = ff.folder_id
WHERE x.export_task_id = $1`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*exportedPage
	for rows.Next() {
		var p exportedPage
		if err := rows.Scan(&p.ItemID, &p.FileID, &p.PageNo, &p.ExportedVersion, &p.ItemNow, &p.PageNow, &p.ReviewStatus, &p.FileName); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// addedPage 是导出的条目里现在有、导出时不在这个条目里的页。
type addedPage struct {
	ItemID       uuid.UUID
	FileID       uuid.UUID
	PageNo       int
	FileName     *string
	ReviewStatus *string
}

func addedPages(ctx context.Context, q db.Querier, taskID uuid.UUID, itemIDs []uuid.UUID) ([]addedPage, error) {
	rows, err := q.Query(ctx, `SELECT af.archive_item_id, af.file_id, af.page_no, ff.file_name,
  CASE WHEN ff.deleted_at IS NULL THEN ff.review_status END
FROM archive_item_files af
LEFT JOIN folder_files ff ON ff.file_id = af.file_id
WHERE af.archive_item_id = ANY($2)
  AND NOT EXISTS (SELECT 1 FROM export_task_files x WHERE x.export_task_id = $1
                  AND x.file_id = af.file_id AND x.archive_item_id = af.archive_item_id)`, taskID, itemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []addedPage
	for rows.Next() {
		var a addedPage
		if err := rows.Scan(&a.ItemID, &a.FileID, &a.PageNo, &a.FileName, &a.ReviewStatus); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// itemNow 是条目现在的样子（含已删除的）。
type itemNow struct {
	ID          uuid.UUID
	Deleted     bool
	ItemNo      int
	ArchiveNo   *string
	Title       string
	ExtraFields map[string]any
	Status      string
}

func itemsNow(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]*itemNow, error) {
	rows, err := q.Query(ctx, `SELECT id, deleted_at IS NOT NULL, item_no, archive_no, title, extra_fields, status
FROM archive_items WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*itemNow{}
	for rows.Next() {
		var it itemNow
		if err := rows.Scan(&it.ID, &it.Deleted, &it.ItemNo, &it.ArchiveNo, &it.Title, &it.ExtraFields, &it.Status); err != nil {
			return nil, err
		}
		out[it.ID] = &it
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 取消

// lockTaskForCancel 按 job 行 → 任务行的顺序锁住（和执行器一样），返回两边的状态（没有 job 时 jobStatus 为空）。
func lockTaskForCancel(ctx context.Context, tx pgx.Tx, taskID uuid.UUID) (taskStatus, jobStatus string, jobID *uuid.UUID, err error) {
	if err = tx.QueryRow(ctx, `SELECT job_id FROM export_tasks WHERE id = $1`, taskID).Scan(&jobID); err != nil {
		return
	}
	if jobID != nil {
		err = tx.QueryRow(ctx, `SELECT status FROM background_jobs WHERE id = $1 FOR NO KEY UPDATE`, *jobID).Scan(&jobStatus)
		if db.IsNoRows(err) {
			err = nil
		}
		if err != nil {
			return
		}
	}
	err = tx.QueryRow(ctx, `SELECT status FROM export_tasks WHERE id = $1 FOR NO KEY UPDATE`, taskID).Scan(&taskStatus)
	return
}

// ---------------------------------------------------------------------------
// 执行

// runPage 是执行时读出的一页：冻结的快照（大小、sha256、mime、存储和 key）和那一刻的审核状态。
type runPage struct {
	ItemID           uuid.UUID
	PageNo           int
	FileID           uuid.UUID
	SnapshotID       uuid.UUID
	VersionNo        int
	SHA256           []byte
	Size             int64
	MIME             string
	UploadName       *string
	ReviewStatus     string
	StorageProfileID uuid.UUID
	StorageKey       string
}

// runPages 一条语句读出全部冻结的页（不排序，调用方按条目分组后按页号排）。
func runPages(ctx context.Context, q db.Querier, taskID uuid.UUID) ([]runPage, error) {
	rows, err := q.Query(ctx, `SELECT x.archive_item_id, x.page_no, x.file_id, x.snapshot_id, s.version_no, s.content_sha256,
  s.file_size, s.mime_type, s.upload_name, COALESCE(ff.review_status, 'unassigned'), s.storage_profile_id, s.storage_key
FROM export_task_files x
JOIN file_snapshots s ON s.id = x.snapshot_id
LEFT JOIN folder_files ff ON ff.file_id = x.file_id
WHERE x.export_task_id = $1`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runPage
	for rows.Next() {
		var p runPage
		if err := rows.Scan(&p.ItemID, &p.PageNo, &p.FileID, &p.SnapshotID, &p.VersionNo, &p.SHA256, &p.Size, &p.MIME,
			&p.UploadName, &p.ReviewStatus, &p.StorageProfileID, &p.StorageKey); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 到期清理

// staleOutput 是要清理的导出物。
type staleOutput struct {
	TaskID    uuid.UUID
	ProjectID uuid.UUID
	Status    string
	ProfileID uuid.UUID
	Key       string
	CreatedAt time.Time
}

// staleOutputs 是一页要清理的导出物：done 而过期超过宽限期（expires_at + 存储的 url_expire_hours + 24 小时）的，
// failed、canceled 而 job 不在 running 的；导出物在已停用的存储上的不选。按 (created_at, id) 分页。
func staleOutputs(ctx context.Context, q db.Querier, afterAt *time.Time, afterID uuid.UUID, limit int) ([]staleOutput, error) {
	rows, err := q.Query(ctx, `SELECT t.id, t.project_id, t.status, t.storage_profile_id, t.output_key, t.created_at
FROM export_tasks t
JOIN storage_profiles sp ON sp.id = t.storage_profile_id AND sp.status <> 'disabled'
LEFT JOIN background_jobs j ON j.id = t.job_id
WHERE t.output_key IS NOT NULL
  AND ((t.status = 'done' AND t.expires_at + make_interval(hours => sp.url_expire_hours + 24) <= now())
       OR (t.status IN ('failed', 'canceled') AND (j.status IS NULL OR j.status <> 'running')))
  AND ($1::timestamptz IS NULL OR (t.created_at, t.id) > ($1, $2))
ORDER BY t.created_at, t.id
LIMIT $3`, afterAt, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []staleOutput
	for rows.Next() {
		var o staleOutput
		if err := rows.Scan(&o.TaskID, &o.ProjectID, &o.Status, &o.ProfileID, &o.Key, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// clearOutput 清掉任务上记着的导出物（还是这个 key 时）。done 的保留存储（"当时存在哪"），其他的连存储一起清空。
func clearOutput(ctx context.Context, q db.Querier, taskID uuid.UUID, key string) error {
	_, err := q.Exec(ctx, `UPDATE export_tasks SET output_key = NULL,
  storage_profile_id = CASE WHEN status = 'done' THEN storage_profile_id END
WHERE id = $1 AND output_key = $2`, taskID, key)
	return err
}
