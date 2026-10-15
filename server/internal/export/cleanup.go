package export

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// 到期清理（requirements 5.11"下载和保留"）：定时（EXPORT_CLEANUP_INTERVAL_SECS，默认 10 分钟，启动时也做一次）删掉
// 两类导出物：done 而过期超过宽限期的（expires_at + 存储的 url_expire_hours + 24 小时：按天取整的下载地址最长能用到
// 这时候；下载接口在 expires_at 之后就拒绝），failed、canceled 留下的（job 已经不在 running）。删对象（不存在也算
// 成功）、清空 output_key；任务、明细、job（payload）永久保留。导出物在已停用的存储上的不选；删不掉的跳过，下一轮再试。
// （可简化：不扫描"没人记着"的残留对象。）

const cleanupBatch = 100

// Cleanup 清理一轮，返回清掉了几个。
func (s *Service) Cleanup(ctx context.Context) (int, error) {
	var afterAt *time.Time
	var afterID uuid.UUID
	n := 0
	for {
		list, err := staleOutputs(ctx, s.pool, afterAt, afterID, cleanupBatch)
		if err != nil {
			return n, err
		}
		for _, o := range list {
			if err := ctx.Err(); err != nil {
				return n, err
			}
			c, err := s.storage.Client(ctx, o.ProfileID)
			if err == nil {
				err = c.Delete(ctx, o.Key)
			}
			if err == nil {
				err = clearOutput(ctx, s.pool, o.TaskID, o.Key)
			}
			if err != nil {
				s.log.Warn("导出：到期清理删导出物失败，下一轮再试", "task_id", o.TaskID, "key", o.Key, "error", err)
				continue
			}
			n++
		}
		if len(list) < cleanupBatch {
			return n, nil
		}
		last := list[len(list)-1]
		afterAt, afterID = &last.CreatedAt, last.TaskID
	}
}

// RunCleanup 每 every 清理一轮（马上先做一次），直到 ctx 结束。
func (s *Service) RunCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := s.Cleanup(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.Warn("导出：到期清理出错", "error", err)
		case n > 0:
			s.log.Info("导出：到期清理", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
