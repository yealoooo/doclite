// Package project 是项目和项目授权（requirements 5.3）。
//
// M1-1 只有 `GET /api/projects`（当前用户能进的项目，给项目切换器用）；项目的增删改、授权、项目授权检查在 M1-3。
package project

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service 是项目的业务规则。
type Service struct {
	pool *pgxpool.Pool
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// ListMine 是用户能进的未删除项目（admin 是全部），含已归档的，按编码排序。
func (s *Service) ListMine(ctx context.Context, userID uuid.UUID) ([]MyProject, error) {
	return listMyProjects(ctx, s.pool, userID)
}
