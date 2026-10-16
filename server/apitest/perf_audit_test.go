//go:build perf

package apitest

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 审计日志查询的性能抽测（M7-3），和 TestPerf 一起跑：
//
//	cd server && go test -tags perf -run TestPerfAudit -v ./apitest/
//
// 用 SQL 造 10 万条审计（created_at 和 id 同序，每分钟一条、约 69 天）：20 个操作人（perf_op1 占一半，perf_op20 只有
// 10 条）、10 个项目（七成带项目），动作按真实比例混着，其中 3 万条是一次存储迁移（file_snapshot / storage_migrate，
// 集中在第 30 天）；每条 detail 约 100 字节。各种筛选组合的第一页（默认 50 条）中位数要在 200 ms 以内。
const (
	perfAuditRows = 100_000
	perfAuditOps  = 20
)

func TestPerfAudit(t *testing.T) {
	env := NewEnv(t)
	admin := env.Admin()
	ops := make([]uuid.UUID, perfAuditOps)
	for i := range ops {
		ops[i] = env.CreateUser(fmt.Sprintf("perf_op%d", i+1))
	}
	projects := make([]uuid.UUID, 10)
	for i := range projects {
		projects[i] = env.CreateProject(fmt.Sprintf("PA%02d", i+1)).ID
	}

	t0 := time.Now()
	base := time.Now().UTC().Add(time.Minute).Truncate(time.Minute) // 在已有的审计之后（created_at 和 id 同序）
	// 第 n 条：操作人 n 为偶数时是 perf_op1，否则在 2–19 之间轮，每 10000 条有一条是 perf_op20；
	// 动作：28801–58800（第 21–41 天）是存储迁移，其余按 n % 20 混着；项目：n % 10 < 7 时带项目。
	env.Exec(`INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail, created_at)
SELECT CASE WHEN n % 10 < 7 OR k.target_type = 'file_snapshot' THEN ($2::uuid[])[n % 10 + 1] END,
  CASE WHEN n % 10000 = 5 THEN ($1::uuid[])[20] WHEN n % 2 = 0 THEN ($1::uuid[])[1] ELSE ($1::uuid[])[n % 18 + 2] END,
  k.target_type, gen_random_uuid(), k.action,
  jsonb_build_object('before', jsonb_build_object('folder_name', '盒 ' || n), 'after', jsonb_build_object('folder_name', '盒 ' || n || '（改）')),
  $3::timestamptz + n * interval '1 minute'
FROM generate_series(1, $4::int) n
CROSS JOIN LATERAL (SELECT CASE
  WHEN n BETWEEN 28801 AND 58800 THEN 'file_snapshot'
  ELSE (ARRAY['folder','folder','folder','folder','archive_item','archive_item','archive_item','archive_item','archive_item',
    'archive_item','archive_item','archive_item','export_task','export_task','archive_category','project','user','role',
    'storage_profile','folder'])[n % 20 + 1] END AS target_type,
  CASE
  WHEN n BETWEEN 28801 AND 58800 THEN 'storage_migrate'
  ELSE (ARRAY['create','update','move','delete','create','update','update','mark_done','lock','renumber','reorder','move',
    'create','download','create','update','update','set_menus','update','create'])[n % 20 + 1] END AS action) k`,
		ops, projects, base, perfAuditRows)
	env.Exec(`ANALYZE audit_logs`)
	t.Logf("造数据：%d 条审计，用时 %v", perfAuditRows, time.Since(t0).Round(time.Millisecond))

	var res []perfResult
	const target = 200 * time.Millisecond
	at := func(d time.Duration) string { return url.QueryEscape(base.Add(d).Format(time.RFC3339)) }
	day := 24 * time.Hour
	list := func(q string) func() string {
		return func() string {
			var out struct {
				Items      []struct{} `json:"items"`
				NextCursor *string    `json:"next_cursor"`
			}
			admin.Get("/api/system/audit-logs?" + q).Expect(http.StatusOK).JSON(&out)
			more := ""
			if out.NextCursor != nil {
				more = "，有下一页"
			}
			return fmt.Sprintf("%d 条%s", len(out.Items), more)
		}
	}
	op := func(i int) string { return "operator_id=" + ops[i-1].String() }
	cases := []struct{ name, q string }{
		{"不筛选，第 1 页", ""},
		{"不筛选，每页 200", "limit=200"},
		{"不筛选，翻到中间（cursor 5 万）", "cursor=" + fmt.Sprint(QueryValue[int64](env, `SELECT min(id) + 50000 FROM audit_logs`))},
		{"操作人：perf_op1（一半）", op(1)},
		{"操作人：perf_op20（10 条，全表扫）", op(20)},
		{"项目：PA01", "project_id=" + projects[0].String()},
		{"项目 + 操作人", "project_id=" + projects[3].String() + "&" + op(3)},
		{"类型：archive_item / lock", "target_type=archive_item&action=lock"},
		{"动作：delete", "action=delete"},
		{"类型：user（最少的几种之一）", "target_type=user"},
		{"动作：不存在（全表扫）", "action=no_such_action"},
		{"时间：一天（第 50 天）", "from=" + at(50*day) + "&to=" + at(51*day)},
		{"时间：一天 + perf_op20", "from=" + at(50*day) + "&to=" + at(51*day) + "&" + op(20)},
		{"时间：最近 7 天 + export_task", "from=" + at(62*day) + "&target_type=export_task"},
		{"时间：迁移那几天 + delete", "from=" + at(20*day) + "&to=" + at(42*day) + "&action=delete"},
		{"时间：到第 10 天为止", "to=" + at(10*day)},
		{"时间：超出范围（没有记录）", "from=" + at(100*day) + "&to=" + at(101*day)},
	}
	for _, c := range cases {
		measure(t, &res, "审计 "+c.name, target, perfRuns, list(c.q))
	}
	measure(t, &res, "审计 筛选栏的选项", target, perfRuns, func() string {
		admin.Get("/api/system/audit-logs/filter-options").Expect(http.StatusOK)
		return ""
	})

	var b strings.Builder
	b.WriteString("\n| 查询 | 目标 | 中位数 | 最慢 | 结果 |\n|---|---|---|---|---|\n")
	for _, r := range res {
		fmt.Fprintf(&b, "| %s | %v | %v | %v | %s |\n", r.name, r.target, r.median.Round(100*time.Microsecond),
			r.max.Round(100*time.Microsecond), r.note)
	}
	t.Log(b.String())
}
