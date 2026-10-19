// Package db 是数据库的基础设施：连接池、事务辅助、迁移执行器、错误判断。
//
// 写法约定：
//   - store 里的函数接收 [Querier]：传连接池就是单条语句，传 pgx.Tx 就在调用方的事务里执行；
//   - 事务边界由 service 决定，用 [InTx]；只读的几条查询要看到同一个快照时用 [ReadTx]；
//   - SQL 手写，参数用 $1、$2……；主键由应用生成 UUIDv7（[NewID]）。
package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier 是连接池和事务共有的方法。
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DefaultMaxConns 是连接串里没写 pool_max_conns 时连接池的连接数上限（pgx 自己的默认是 CPU 核数、至少 4 个，
// 少核的服务器上几个导出、SSE 之外的请求就要排队）。连接用到时才建。
const DefaultMaxConns = 20

// LockTimeout 是每个连接的 lock_timeout（连接串里没写时）：等锁（行锁、咨询锁，比如删大文件夹拿着的文件树排他锁）
// 超过这么久就报错（SQLSTATE 55P03，接口返回 503 busy），不无限地等下去。不设 statement_timeout：导出要读很久。
const LockTimeout = "30s"

// Open 建连接池并确认能连上。连接串含密码，不要打到日志里。
//
// 每个连接的 timestamptz 都按 UTC 读出，接口返回的时间统一是 `…Z` 的形式。连接串里没写 pool_max_conns、lock_timeout
// 时用 [DefaultMaxConns]、[LockTimeout]。
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL 格式不对：%w", err)
	}
	if !strings.Contains(databaseURL, "pool_max_conns") {
		cfg.MaxConns = DefaultMaxConns
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["lock_timeout"]; !ok {
		cfg.ConnConfig.RuntimeParams["lock_timeout"] = LockTimeout // 连接时带上，不多一次往返
	}
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamptz",
			OID:   pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败：%w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接数据库失败：%w", err)
	}
	return pool, nil
}

// InTx 在一个事务里执行 fn：fn 返回 nil 时提交，返回错误或 panic 时回滚。
// 事务里"没有变化、直接返回"的分支照常返回 nil 即可（提交一个什么都没写的事务，等于回滚）。
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReadTx 在一个只读的 REPEATABLE READ 事务里执行 fn：一个读接口的几条查询（计数、列表、路径……）看到同一个快照。
func ReadTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	return fn(tx)
}

// AdvisoryXactLocks 在事务 tx 里拿 (class, key) 的排他咨询锁（事务级），一条语句拿完：键去重、按升序拿（几个事务
// 各拿几把时顺序一致，不会互相等成环）。
func AdvisoryXactLocks(ctx context.Context, tx Querier, class int32, keys []int32) error {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	switch len(keys) {
	case 0:
		return nil
	case 1:
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`, class, keys[0])
		return err
	}
	// 数组已经升序；ORDER BY 再保证一次（易变函数在排序之后才算）。
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, k) FROM unnest($2::int4[]) AS k ORDER BY k`, class, keys)
	return err
}

// CollectIDs 执行只返回一列 uuid 的查询，按返回的顺序收集。
func CollectIDs(ctx context.Context, q Querier, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// ContainsPattern 把 %、_、\ 转义后包成 %关键字%，给 ILIKE 用。
func ContainsPattern(keyword string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(keyword) + "%"
}

// NewID 生成主键（UUIDv7）。
func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// UniqueViolation 判断 err 是不是撞了唯一约束（或唯一索引）constraint；constraint 为空时只要是唯一冲突就算。
func UniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// LockTimedOut 判断 err 是不是等锁超过了 lock_timeout（SQLSTATE 55P03）。
func LockTimedOut(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// IsNoRows 判断 err 是不是"查询没有结果"。
func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
