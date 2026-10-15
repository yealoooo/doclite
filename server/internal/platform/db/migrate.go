package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 迁移执行器：
//   - 迁移文件是 embed 进来的 `000N_说明.sql`，按文件名顺序执行；
//   - 每个文件在一个事务里执行，成功后在 `schema_migrations` 里记一行（版本 = 文件名去掉 .sql、校验和、时间）；
//   - 已经执行过的文件再核对一次校验和：文件在执行之后被改过就报错，不启动（改表只能新增文件）；
//   - 库里有程序不认识的版本（库被更新的程序迁移过）也报错，不启动；
//   - 执行期间拿一把会话级的咨询锁（6000, 1），同时启动的两个进程不会一起迁移。
//
// 校验和按 LF 换行计算（CRLF 先换成 LF），Windows 上签出时换行被改不算改动。

// migrateLockClass 是迁移用的咨询锁类别（和 key-queries.sql 里 6001 起的业务锁类别错开）。
const migrateLockClass = 6000

var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

// Migration 是一个迁移文件。
type Migration struct {
	// Version 是文件名去掉 .sql，例如 `0001_init`。
	Version  string
	SQL      string
	Checksum string
}

// LoadMigrations 读出 fsys 根目录下的全部 `*.sql`，按文件名排序。文件名不合规则时报错。
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("读迁移目录失败：%w", err)
	}
	var out []Migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if !migrationName.MatchString(name) {
			return nil, fmt.Errorf("迁移文件名 %q 不合规则，应为 000N_小写说明.sql", name)
		}
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("读迁移文件 %s 失败：%w", name, err)
		}
		sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
		sum := sha256.Sum256([]byte(sql))
		out = append(out, Migration{
			Version:  strings.TrimSuffix(name, ".sql"),
			SQL:      sql,
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	slices.SortFunc(out, func(a, b Migration) int { return strings.Compare(a.Version, b.Version) })
	return out, nil
}

// MigrationsHash 是全部迁移（版本和内容）的指纹，测试用它给模板库命名：迁移变了就换一个模板库。
func MigrationsHash(ms []Migration) string {
	h := sha256.New()
	for _, m := range ms {
		fmt.Fprintf(h, "%s\n%s\n", m.Version, m.Checksum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Migrate 执行还没执行过的迁移，返回这次执行了哪些版本。
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, log *slog.Logger) ([]string, error) {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, 1)", migrateLockClass); err != nil {
		return nil, fmt.Errorf("拿迁移锁失败：%w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, 1)", migrateLockClass)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    text PRIMARY KEY,                 -- 迁移文件名去掉 .sql，例如 0001_init
  checksum   text NOT NULL,                    -- 文件内容的 sha256（换行统一成 LF）
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return nil, fmt.Errorf("建 schema_migrations 失败：%w", err)
	}

	applied := map[string]string{}
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	known := map[string]bool{}
	for _, m := range migrations {
		known[m.Version] = true
	}
	for v := range applied {
		if !known[v] {
			return nil, fmt.Errorf("数据库里有程序不认识的迁移 %s：数据库被更新版本的程序迁移过，请换用对应版本的程序", v)
		}
	}

	var ran []string
	for _, m := range migrations {
		if sum, ok := applied[m.Version]; ok {
			if sum != m.Checksum {
				return nil, fmt.Errorf("迁移 %s 执行之后被改过（校验和不一致）：已发布的迁移不能修改，改表请新增迁移文件", m.Version)
			}
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// 没有参数时 pgx 用简单查询协议发送，一个文件里可以有多条语句。
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)", m.Version, m.Checksum)
			return err
		})
		if err != nil {
			return ran, fmt.Errorf("执行迁移 %s 失败：%w", m.Version, err)
		}
		if log != nil {
			log.Info("已执行迁移", "version", m.Version)
		}
		ran = append(ran, m.Version)
	}
	return ran, nil
}
