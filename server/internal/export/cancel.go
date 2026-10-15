package export

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/platform/events"
)

// 取消（requirements 5.11"取消"，总控决定）：
//   - job 排队中（含重试前的等待）→ 任务和 job 一起改成 canceled；
//   - job 执行中 → 任务立即改成 canceled，job 保持 running；执行器（心跳、Interrupt）发现后停下、中止上传、删临时
//     文件，再把 job 改成 canceled。这期间任务的 cleaning_up 为 true；
//   - 任务已经 done、failed、canceled，或者 job 已经取消 → 409 invalid_state"任务{状态}，不能取消"。
//
// 加锁顺序：job 行 → 任务行（和执行器一样）。每次成功写一条审计，提交后发 export.changed。

// Cancel 取消任务，返回取消后的任务。
func (s *Service) Cancel(ctx context.Context, userID, id uuid.UUID) (*Task, error) {
	var interrupt *uuid.UUID
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, out *events.Pending) error {
		pid, err := taskProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if pid == uuid.Nil {
			return errTaskNotFound
		}
		taskStatus, jobStatus, jobID, err := lockTaskForCancel(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case taskStatus == StatusDone || taskStatus == StatusFailed || taskStatus == StatusCanceled:
			return conflictState("任务%s，不能取消", statusLabel(taskStatus))
		case jobStatus == StatusCanceled:
			return conflictState("任务%s，不能取消", statusLabel(StatusCanceled))
		case jobStatus == StatusQueued:
			if _, err := tx.Exec(ctx, `UPDATE background_jobs SET status = 'canceled', finished_at = now() WHERE id = $1`, *jobID); err != nil {
				return err
			}
		case jobStatus == StatusRunning:
			interrupt = jobID
		default:
			return conflictState("任务是“%s”而后台任务是“%s”，状态对不上，不能取消", statusLabel(taskStatus), statusLabel(jobStatus))
		}
		if _, err := tx.Exec(ctx, `UPDATE export_tasks SET status = 'canceled', finished_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &pid, OperatorID: audit.Operator(userID),
			TargetType: audit.TargetExportTask, TargetID: id, Action: "cancel",
			Detail: map[string]any{"before": map[string]any{"status": taskStatus}, "after": map[string]any{"status": StatusCanceled}}}); err != nil {
			return err
		}
		out.Add(events.ExportChanged(pid, id))
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 先读出取消后的任务（执行中的这时 cleaning_up 一定是 true），再叫执行器停下。
	t, err := s.Get(ctx, id)
	if interrupt != nil {
		s.jobs.Interrupt(*interrupt)
	}
	return t, err
}
