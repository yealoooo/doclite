// Package db 是数据库的基础设施：连接池、事务辅助、迁移执行器、错误判断。
//
// 写法约定：
//   - store 里的函数接收 [Querier]：传连接池就是单条语句，传 pgx.Tx 就在调用方的事务里执行；
//   - 事务边界由 service 决定，用 [InTx]；
//   - SQL 手写，参数用 $1、$2……；主键由应用生成 UUIDv7（[NewID]）。
package db

import (
	"context"
	"errors"
	"fmt"
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

// Open 建连接池并确认能连上。连接串含密码，不要打到日志里。
//
// 每个连接的 timestamptz 都按 UTC 读出，接口返回的时间统一是 `…Z` 的形式。
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL 格式不对：%w", err)
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

// IsNoRows 判断 err 是不是"查询没有结果"。
func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
