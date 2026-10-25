package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
)

// storage 的 SQL：storage_profiles。
// 跨模块的只读查询：file_snapshots、ocr_results、export_tasks（存储有没有被引用，决定能不能停用、删除）。

// row 是 storage_profiles 的一行（含密文），只在包内用。
type row struct {
	ID             uuid.UUID
	ProfileName    string
	StorageType    string
	Endpoint       string
	PublicEndpoint *string
	Bucket         string
	Region         string
	PathPrefix     string
	AccessKey      string
	SecretKeyEnc   []byte
	ForcePathStyle bool
	URLExpireHours int
	Status         string
	IsDefault      bool
	Remark         *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// usage 是存储被引用的情况。
type usage struct {
	// HasContents：有文件存在里面（file_snapshots 或 ocr_results 引用）：不能停用。
	HasContents bool
	// HasReferences：被任何数据引用（HasContents，或 export_tasks 引用）：不能删除。
	HasReferences bool
}

const rowColumns = `p.id, p.profile_name, p.storage_type, p.endpoint, p.public_endpoint, p.bucket, p.region, p.path_prefix,
  p.access_key, p.secret_key_enc, p.force_path_style, p.url_expire_hours, p.status, p.is_default, p.remark,
  p.created_at, p.updated_at`

const usageColumns = `
  EXISTS (SELECT 1 FROM file_snapshots s WHERE s.storage_profile_id = p.id)
    OR EXISTS (SELECT 1 FROM ocr_results o WHERE o.storage_profile_id = p.id),
  EXISTS (SELECT 1 FROM export_tasks t WHERE t.storage_profile_id = p.id)`

func scanRow(sc pgx.Row, extra ...any) (*row, error) {
	var r row
	dest := []any{&r.ID, &r.ProfileName, &r.StorageType, &r.Endpoint, &r.PublicEndpoint, &r.Bucket, &r.Region, &r.PathPrefix,
		&r.AccessKey, &r.SecretKeyEnc, &r.ForcePathStyle, &r.URLExpireHours, &r.Status, &r.IsDefault, &r.Remark,
		&r.CreatedAt, &r.UpdatedAt}
	if err := sc.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	return &r, nil
}

func scanWithUsage(sc pgx.Row) (*row, usage, error) {
	var u usage
	var exports bool
	r, err := scanRow(sc, &u.HasContents, &exports)
	if err != nil {
		return nil, u, err
	}
	u.HasReferences = u.HasContents || exports
	return r, u, nil
}

type rowWithUsage struct {
	row   *row
	usage usage
}

// listRows：默认的在最前，其余按创建时间。
func listRows(ctx context.Context, q db.Querier) ([]rowWithUsage, error) {
	rows, err := q.Query(ctx, `SELECT `+rowColumns+`,`+usageColumns+`
FROM storage_profiles p ORDER BY p.is_default DESC, p.created_at, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rowWithUsage
	for rows.Next() {
		r, u, err := scanWithUsage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rowWithUsage{r, u})
	}
	return out, rows.Err()
}

// snapshotTotal 是一个存储里快照的行数和 file_size 之和。
type snapshotTotal struct {
	Count int
	Bytes int64
}

// snapshotTotals：各存储的快照用量，一次分组查询；id 不为 nil 时只算这一个。file_snapshots 上没有带 file_size 的
// 索引，要读整张表（存储页很少打开，可以接受）。没有快照的存储不在结果里。
func snapshotTotals(ctx context.Context, q db.Querier, id *uuid.UUID) (map[uuid.UUID]snapshotTotal, error) {
	rows, err := q.Query(ctx, `SELECT storage_profile_id, count(*), COALESCE(sum(file_size), 0)::bigint
FROM file_snapshots WHERE $1::uuid IS NULL OR storage_profile_id = $1
GROUP BY storage_profile_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]snapshotTotal{}
	for rows.Next() {
		var pid uuid.UUID
		var t snapshotTotal
		if err := rows.Scan(&pid, &t.Count, &t.Bytes); err != nil {
			return nil, err
		}
		out[pid] = t
	}
	return out, rows.Err()
}

func getRowWithUsage(ctx context.Context, q db.Querier, id uuid.UUID) (*row, usage, error) {
	r, u, err := scanWithUsage(q.QueryRow(ctx, `SELECT `+rowColumns+`,`+usageColumns+` FROM storage_profiles p WHERE p.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, u, nil
	}
	return r, u, err
}

func getRow(ctx context.Context, q db.Querier, id uuid.UUID) (*row, error) {
	r, err := scanRow(q.QueryRow(ctx, `SELECT `+rowColumns+` FROM storage_profiles p WHERE p.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return r, err
}

// allRows 读全部行（按 id；缓存用）。
func allRows(ctx context.Context, q db.Querier) (map[uuid.UUID]*row, error) {
	rows, err := q.Query(ctx, `SELECT `+rowColumns+` FROM storage_profiles p`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

func getDefaultRow(ctx context.Context, q db.Querier) (*row, error) {
	r, err := scanRow(q.QueryRow(ctx, `SELECT `+rowColumns+` FROM storage_profiles p WHERE p.is_default`))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return r, err
}

// lockRow 锁住一行。forUpdate：改成停用、删除用 FOR UPDATE（和写快照时外键检查的 FOR KEY SHARE 冲突，正在写快照的
// 事务提交之前这里会等它，之后的"有没有文件"检查才准）；其他修改用 FOR NO KEY UPDATE（不等写快照的事务）。
func lockRow(ctx context.Context, q db.Querier, id uuid.UUID, forUpdate bool) (*row, error) {
	mode := "FOR NO KEY UPDATE"
	if forUpdate {
		mode = "FOR UPDATE"
	}
	r, err := scanRow(q.QueryRow(ctx, `SELECT `+rowColumns+` FROM storage_profiles p WHERE p.id = $1 `+mode, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return r, err
}

// lockForSnapshot 是写快照的事务里对存储配置的锁（FOR KEY SHARE）：和停用、删除互斥，不挡其他修改。
func lockForSnapshot(ctx context.Context, q db.Querier, id uuid.UUID) (status string, isDefault, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT status, is_default FROM storage_profiles WHERE id = $1 FOR KEY SHARE`, id).Scan(&status, &isDefault)
	if db.IsNoRows(err) {
		return "", false, false, nil
	}
	return status, isDefault, err == nil, err
}

func usageOf(ctx context.Context, q db.Querier, id uuid.UUID) (usage, error) {
	var u usage
	var exports bool
	err := q.QueryRow(ctx, `SELECT `+usageColumns+` FROM storage_profiles p WHERE p.id = $1`, id).Scan(&u.HasContents, &exports)
	u.HasReferences = u.HasContents || exports
	return u, err
}

func countRows(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM storage_profiles`).Scan(&n)
	return n, err
}

func insertRow(ctx context.Context, q db.Querier, r *row) error {
	_, err := q.Exec(ctx, `INSERT INTO storage_profiles
  (id, profile_name, storage_type, endpoint, public_endpoint, bucket, region, path_prefix, access_key, secret_key_enc,
   force_path_style, url_expire_hours, status, is_default, remark)
VALUES ($1, $2, 's3', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		r.ID, r.ProfileName, r.Endpoint, r.PublicEndpoint, r.Bucket, r.Region, r.PathPrefix, r.AccessKey, r.SecretKeyEnc,
		r.ForcePathStyle, r.URLExpireHours, r.Status, r.IsDefault, r.Remark)
	return err
}

// updateRow 写回可以修改的字段（不含 is_default），刷新 updated_at。
func updateRow(ctx context.Context, q db.Querier, r *row) error {
	_, err := q.Exec(ctx, `UPDATE storage_profiles SET profile_name = $2, endpoint = $3, public_endpoint = $4, bucket = $5,
  region = $6, path_prefix = $7, access_key = $8, secret_key_enc = $9, force_path_style = $10, url_expire_hours = $11,
  status = $12, remark = $13, updated_at = now()
WHERE id = $1`,
		r.ID, r.ProfileName, r.Endpoint, r.PublicEndpoint, r.Bucket, r.Region, r.PathPrefix, r.AccessKey, r.SecretKeyEnc,
		r.ForcePathStyle, r.URLExpireHours, r.Status, r.Remark)
	return err
}

func setDefaultFlag(ctx context.Context, q db.Querier, id uuid.UUID, isDefault bool) error {
	_, err := q.Exec(ctx, `UPDATE storage_profiles SET is_default = $2, updated_at = now() WHERE id = $1`, id, isDefault)
	return err
}

func deleteRow(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM storage_profiles WHERE id = $1`, id)
	return err
}
