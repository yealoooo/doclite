package storage

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
)

// 存储迁移的 SQL：background_jobs 里 job_type = storage_migrate 的行（payload、进度、状态）。
// 跨模块：file_snapshots（读，另外改三列——硬性规则 2 唯一的例外）、folder_files、folders、projects（只读：失败清单里的
// 文件名和位置、审计的 project_id）、audit_logs（数已经迁过的快照，见 countMovedSince）。

// migrationColumns 是 scanMigration 读的列（j 是 background_jobs，u 是建任务的人，ss、ts 是源和目标存储——可能已经删了）。
const migrationColumns = `j.id, j.status, j.payload, j.attempts, j.max_attempts, j.error_message,
  COALESCE(j.created_by, '00000000-0000-0000-0000-000000000000'::uuid), u.display_name, j.created_at, j.started_at, j.finished_at,
  ss.profile_name, ts.profile_name`

const migrationFrom = `background_jobs j
LEFT JOIN users u ON u.id = j.created_by
LEFT JOIN storage_profiles ss ON ss.id = (j.payload->>'source_profile_id')::uuid
LEFT JOIN storage_profiles ts ON ts.id = (j.payload->>'target_profile_id')::uuid`

// scanMigration 读一行迁移任务（不含 remaining，失败清单在 payload 里）。
func scanMigration(sc scanner) (*Migration, error) {
	var m Migration
	var raw []byte
	var srcName, dstName *string
	if err := sc.Scan(&m.ID, &m.Status, &raw, &m.Attempts, &m.MaxAttempts, &m.ErrorMessage, &m.CreatedBy, &m.CreatedByName,
		&m.CreatedAt, &m.StartedAt, &m.FinishedAt, &srcName, &dstName); err != nil {
		return nil, err
	}
	p, err := parsePayload(raw)
	if err != nil {
		return nil, err
	}
	m.fill(p)
	if srcName != nil {
		m.SourceProfileName = *srcName
	}
	if dstName != nil {
		m.TargetProfileName = *dstName
	}
	return &m, nil
}

func parsePayload(raw []byte) (*migratePayload, error) {
	var p migratePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// latestMigration 是源存储最近的一次迁移；没有时为 nil。
func latestMigration(ctx context.Context, q db.Querier, sourceID uuid.UUID) (*Migration, error) {
	m, err := scanMigration(q.QueryRow(ctx, `SELECT `+migrationColumns+` FROM `+migrationFrom+`
WHERE j.job_type = $1 AND j.payload->>'source_profile_id' = $2
ORDER BY j.created_at DESC, j.id DESC LIMIT 1`, MigrateJobType, sourceID.String()))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return m, err
}

// migrationSummaries 是列表上每个存储的迁移情况：每个源存储最近的一次（latest，按源存储），和正在迁入的（incoming，
// 按目标存储；排队中、执行中的）。background_jobs 一年几千行，顺序扫描是毫秒级。
func migrationSummaries(ctx context.Context, q db.Querier) (latest, incoming map[uuid.UUID]*Migration, err error) {
	collect := func(sql string) ([]*Migration, error) {
		rows, err := q.Query(ctx, sql, MigrateJobType)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []*Migration
		for rows.Next() {
			m, err := scanMigration(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, rows.Err()
	}
	last, err := collect(`SELECT DISTINCT ON (j.payload->>'source_profile_id') ` + migrationColumns + ` FROM ` + migrationFrom + `
WHERE j.job_type = $1
ORDER BY j.payload->>'source_profile_id', j.created_at DESC, j.id DESC`)
	if err != nil {
		return nil, nil, err
	}
	active, err := collect(`SELECT ` + migrationColumns + ` FROM ` + migrationFrom + `
WHERE j.job_type = $1 AND j.status IN ('queued', 'running')`)
	if err != nil {
		return nil, nil, err
	}
	latest, incoming = map[uuid.UUID]*Migration{}, map[uuid.UUID]*Migration{}
	for _, m := range append(last, active...) {
		m.Failures, m.FailuresTruncated = nil, false // 摘要不带失败清单（详情接口才给）
	}
	for _, m := range last {
		latest[m.SourceProfileID] = m
	}
	for _, m := range active {
		incoming[m.TargetProfileID] = m
	}
	return latest, incoming, nil
}

// activeMigration 是排队中、执行中的一次迁移的源和目标。
type activeMigration struct {
	ID       uuid.UUID
	SourceID uuid.UUID
	TargetID uuid.UUID
}

// activeMigrations 是全部排队中、执行中的迁移（建迁移、设默认、改状态、删除时检查；调用方拿着 Service.mu）。
func activeMigrations(ctx context.Context, q db.Querier) ([]activeMigration, error) {
	rows, err := q.Query(ctx, `SELECT id, (payload->>'source_profile_id')::uuid, (payload->>'target_profile_id')::uuid
FROM background_jobs WHERE job_type = $1 AND status IN ('queued', 'running')`, MigrateJobType)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (activeMigration, error) {
		var a activeMigration
		err := r.Scan(&a.ID, &a.SourceID, &a.TargetID)
		return a, err
	})
}

// lockActiveMigration 锁住源存储排队中、执行中的那次迁移的 job 行（取消用；FOR NO KEY UPDATE，和执行器、改位置的
// 事务一样先锁 job 行）。没有时为 nil。
func lockActiveMigration(ctx context.Context, tx pgx.Tx, sourceID uuid.UUID) (id uuid.UUID, status string, p *migratePayload, err error) {
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT id, status, payload FROM background_jobs
WHERE job_type = $1 AND status IN ('queued', 'running') AND payload->>'source_profile_id' = $2
ORDER BY created_at DESC LIMIT 1
FOR NO KEY UPDATE`, MigrateJobType, sourceID.String()).Scan(&id, &status, &raw)
	if db.IsNoRows(err) {
		return uuid.Nil, "", nil, nil
	}
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	p, err = parsePayload(raw)
	return id, status, p, err
}

// maxAuditID 是现在最大的审计 id（没有审计时 0）。
func maxAuditID(ctx context.Context, q db.Querier) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM audit_logs`).Scan(&id)
	return id, err
}

// countSnapshots 是存在这个存储里的快照数。
func countSnapshots(ctx context.Context, q db.Querier, profileID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM file_snapshots WHERE storage_profile_id = $1`, profileID).Scan(&n)
	return n, err
}

// insertMigrationJob 建迁移任务的 job 行（排队；优先级 -1：和导出一起排队时导出先领）。
func insertMigrationJob(ctx context.Context, q db.Querier, id uuid.UUID, p *migratePayload, total, maxAttempts int, createdBy uuid.UUID) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO background_jobs (id, job_type, payload, status, priority, max_attempts, progress_total, created_by)
VALUES ($1, $2, $3, 'queued', -1, $4, $5, $6)`, id, MigrateJobType, string(raw), maxAttempts, total, createdBy)
	return err
}

// setPayloadKey 改 payload 里的一个键（state 或 cancel），不动别的键。
func setPayloadKey(ctx context.Context, q db.Querier, jobID uuid.UUID, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE background_jobs SET payload = jsonb_set(payload, ARRAY[$2::text], $3::jsonb) WHERE id = $1`,
		jobID, key, string(raw))
	return err
}

// ---------------------------------------------------------------------------
// 执行

// nextKeys 是源存储里 cursor 之后的 n 个对象（按 storage_key 排序、去重；走 ix_file_snapshots_object）。迁走的快照
// 不在源存储里了，所以只有核对失败留下的在 cursor 之前。
func nextKeys(ctx context.Context, q db.Querier, sourceID uuid.UUID, cursor string, n int) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT storage_key FROM file_snapshots
WHERE storage_profile_id = $1 AND storage_key > $2
GROUP BY storage_key ORDER BY storage_key LIMIT $3`, sourceID, cursor, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// groupRow 是引用一个对象的一个快照（恢复、复制的快照和原来的共用对象）。
type groupRow struct {
	SnapshotID uuid.UUID
	FileID     uuid.UUID
	VersionNo  int
	CoverKey   *string
	SHA256     []byte
	Size       int64
	MimeType   string
	// ProjectID 是文件所在的项目（folder_files；没有时为 nil）。
	ProjectID *uuid.UUID
}

// loadGroups 读这些对象（源存储里的 key）的全部快照，按 key 分组。
func loadGroups(ctx context.Context, q db.Querier, sourceID uuid.UUID, keys []string) (map[string][]groupRow, error) {
	rows, err := q.Query(ctx, `SELECT s.storage_key, s.id, s.file_id, s.version_no, s.cover_key, s.content_sha256, s.file_size,
  s.mime_type, ff.project_id
FROM file_snapshots s
LEFT JOIN folder_files ff ON ff.file_id = s.file_id
WHERE s.storage_profile_id = $1 AND s.storage_key = ANY($2)
ORDER BY s.storage_key, s.id`, sourceID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]groupRow{}
	for rows.Next() {
		var key string
		var g groupRow
		if err := rows.Scan(&key, &g.SnapshotID, &g.FileID, &g.VersionNo, &g.CoverKey, &g.SHA256, &g.Size, &g.MimeType,
			&g.ProjectID); err != nil {
			return nil, err
		}
		out[key] = append(out[key], g)
	}
	return out, rows.Err()
}

// movedSnapshot 是改了位置的一个快照（UPDATE … RETURNING）。
type movedSnapshot struct {
	ID        uuid.UUID
	FileID    uuid.UUID
	VersionNo int
	OldCover  *string
	NewCover  *string
}

// moveObject 把源存储里引用 key、内容记录和核对过的一致（sha256、大小）的快照改到目标存储的 newKey，封面按 covers
// （旧 → 新，nil 表示没有封面）改。封面不在 covers 里、或者记的 sha256、大小不一样的快照不改（调用方随后用 countAt
// 发现、回滚重来，再不行记 inconsistent）。
func moveObject(ctx context.Context, tx pgx.Tx, sourceID uuid.UUID, key string, targetID uuid.UUID, newKey string,
	sha []byte, size int64, oldCovers, newCovers []*string) ([]movedSnapshot, error) {
	rows, err := tx.Query(ctx, `UPDATE file_snapshots s SET storage_profile_id = $3, storage_key = $4, cover_key = m.new_cover
FROM unnest($5::text[], $6::text[]) AS m(old_cover, new_cover)
WHERE s.storage_profile_id = $1 AND s.storage_key = $2 AND s.content_sha256 = $7 AND s.file_size = $8
  AND s.cover_key IS NOT DISTINCT FROM m.old_cover
RETURNING s.id, s.file_id, s.version_no, m.old_cover, m.new_cover`, sourceID, key, targetID, newKey, oldCovers, newCovers,
		sha, size)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (movedSnapshot, error) {
		var m movedSnapshot
		err := r.Scan(&m.ID, &m.FileID, &m.VersionNo, &m.OldCover, &m.NewCover)
		return m, err
	})
}

// countAt 是存储里引用 key 的快照数。
func countAt(ctx context.Context, q db.Querier, profileID uuid.UUID, key string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM file_snapshots WHERE storage_profile_id = $1 AND storage_key = $2`, profileID, key).Scan(&n)
	return n, err
}

// auditMoved 给改了位置的每个快照写一条审计（target_type file_snapshot、action storage_migrate；project_id 是文件所在
// 的项目；detail 带 job_id、file_id、version_no、文件现在的名字，和改前改后的存储（id、名字）、key、封面 key）。
func auditMoved(ctx context.Context, tx pgx.Tx, jobID uuid.UUID, operator *uuid.UUID, src, dst *row, key, newKey string,
	moved []movedSnapshot) error {
	ids := make([]uuid.UUID, len(moved))
	fids := make([]uuid.UUID, len(moved))
	vers := make([]int32, len(moved))
	olds := make([]*string, len(moved))
	news := make([]*string, len(moved))
	for i, m := range moved {
		ids[i], fids[i], vers[i], olds[i], news[i] = m.ID, m.FileID, int32(m.VersionNo), m.OldCover, m.NewCover
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
SELECT ff.project_id, $1, 'file_snapshot', t.id, 'storage_migrate', jsonb_build_object(
  'job_id', $2::text, 'file_id', t.file_id, 'version_no', t.version_no, 'file_name', ff.file_name,
  'before', jsonb_build_object('storage_profile_id', $3::text, 'storage_profile_name', $4::text, 'storage_key', $5::text,
    'cover_key', t.old_cover),
  'after', jsonb_build_object('storage_profile_id', $6::text, 'storage_profile_name', $7::text, 'storage_key', $8::text,
    'cover_key', t.new_cover))
FROM unnest($9::uuid[], $10::uuid[], $11::int[], $12::text[], $13::text[]) WITH ORDINALITY AS t(id, file_id, version_no, old_cover, new_cover, i)
LEFT JOIN folder_files ff ON ff.file_id = t.file_id
ORDER BY t.i`, operator, jobID.String(), src.ID.String(), src.ProfileName, key, dst.ID.String(), dst.ProfileName, newKey,
		ids, fids, vers, olds, news)
	return err
}

// countMovedSince 数这次迁移在审计 id mark 之后改了位置的快照（每个快照一条 storage_migrate 审计，和改位置在同一个
// 事务里提交，所以数审计就是数改过的快照，进程被杀也不会数错）。返回条数和其中最大的审计 id（没有时是 mark）。
// 按主键范围扫 mark 之后的审计（一般只有一批），不需要索引。
func countMovedSince(ctx context.Context, q db.Querier, jobID uuid.UUID, mark int64) (int, int64, error) {
	var n int
	var last int64
	err := q.QueryRow(ctx, `SELECT count(*), COALESCE(max(id), $2) FROM audit_logs
WHERE id > $2 AND target_type = 'file_snapshot' AND action = 'storage_migrate' AND detail->>'job_id' = $1`,
		jobID.String(), mark).Scan(&n, &last)
	return n, last, err
}

// failedSnapshotInfo 补全失败清单里快照的项目编码、文件名和文件夹路径（从项目的根文件夹起，/ 连接）。
func failedSnapshotInfo(ctx context.Context, q db.Querier, fileIDs []uuid.UUID) (map[uuid.UUID]FailedSnapshot, error) {
	rows, err := q.Query(ctx, `SELECT ff.file_id, p.project_code, ff.file_name, (ff.deleted_at IS NOT NULL),
  (SELECT string_agg(a.folder_name, '/' ORDER BY g.n)
   FROM folders f
   CROSS JOIN LATERAL generate_series(6, length(f.path_code), 6) AS g(n)
   JOIN folders a ON a.project_id = f.project_id AND a.path_code = left(f.path_code, g.n)
   WHERE f.id = ff.folder_id)
FROM folder_files ff
JOIN projects p ON p.id = ff.project_id
WHERE ff.file_id = ANY($1)`, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]FailedSnapshot{}
	for rows.Next() {
		var f FailedSnapshot
		var code, name string
		var path *string
		if err := rows.Scan(&f.FileID, &code, &name, &f.FileDeleted, &path); err != nil {
			return nil, err
		}
		f.ProjectCode, f.FileName, f.FolderPath = &code, &name, path
		out[f.FileID] = f
	}
	return out, rows.Err()
}

// lockJobForMove 在改位置的事务里锁住 job 行（FOR SHARE：同一次迁移的几个并行的改位置不互相等；取消接口、断点的
// FOR NO KEY UPDATE 和它互斥），返回它是不是还归这次执行、是不是已经要求取消。
func lockJobForMove(ctx context.Context, tx pgx.Tx, jobID uuid.UUID, attempt int) (owned, canceled bool, err error) {
	var status string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT status, attempts, payload ? 'cancel' FROM background_jobs WHERE id = $1 FOR SHARE`, jobID).
		Scan(&status, &attempts, &canceled)
	if db.IsNoRows(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return status == "running" && attempts == attempt, canceled, nil
}

// cancelRequested：payload 里有 cancel（取消接口记下的）。
func cancelRequested(ctx context.Context, q db.Querier, jobID uuid.UUID) (bool, error) {
	var c bool
	err := q.QueryRow(ctx, `SELECT payload ? 'cancel' FROM background_jobs WHERE id = $1`, jobID).Scan(&c)
	if db.IsNoRows(err) {
		return false, nil
	}
	return c, err
}

// saveState 写断点（payload.state）和进度列。
func saveState(ctx context.Context, q db.Querier, jobID uuid.UUID, st *migrateState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE background_jobs SET payload = jsonb_set(payload, '{state}', $2::jsonb),
  progress_done = $3, progress_total = $4, heartbeat_at = now()
WHERE id = $1`, jobID, string(raw), st.Migrated+st.Failed, st.Total)
	return err
}

// jobPayload 读 job 现在的 payload 和错误信息（收尾时用）。
func jobPayload(ctx context.Context, q db.Querier, jobID uuid.UUID) (*migratePayload, *string, error) {
	var raw []byte
	var msg *string
	if err := q.QueryRow(ctx, `SELECT payload, error_message FROM background_jobs WHERE id = $1`, jobID).Scan(&raw, &msg); err != nil {
		return nil, nil, err
	}
	p, err := parsePayload(raw)
	return p, msg, err
}
