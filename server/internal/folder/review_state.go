package folder

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// 审核状态的写（M3-1）：folder_files.review_status、review_round 属于这张表，由审核模块（internal/review）在它自己的
// 事务里调用这里的方法改，不直接写 folder_files。调用方要已经按 requirements 3.6 拿了文件树共享锁、按 id 升序锁了
// files 行（file.LockFiles），并在锁住之后重读过状态；这里只写，不再检查。

// 审核状态（folder_files.review_status）。
const (
	ReviewUnassigned = "unassigned"
	ReviewPending    = "pending"
	ReviewApproved   = "approved"
	ReviewRejected   = "rejected"
)

// SetReviewStatus 把这些文件（file_id，未删除的）的审核状态改成 status。
func (s *Service) SetReviewStatus(ctx context.Context, tx pgx.Tx, fileIDs []uuid.UUID, status string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE folder_files SET review_status = $2, updated_at = now()
WHERE file_id = ANY($1) AND deleted_at IS NULL`, fileIDs, status)
	return err
}

// SetReviewStatusAndRound 改一个文件（file_id，未删除的）的审核状态和轮次（重扫：轮次 +1）。
func (s *Service) SetReviewStatusAndRound(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, status string, round int) error {
	_, err := tx.Exec(ctx, `UPDATE folder_files SET review_status = $2, review_round = $3, updated_at = now()
WHERE file_id = $1 AND deleted_at IS NULL`, fileID, status, round)
	return err
}
