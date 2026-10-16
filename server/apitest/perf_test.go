//go:build perf

package apitest

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 性能抽测（M5-3，requirements 1.4 的目标），默认不跑：
//
//	cd server && go test -tags perf -run TestPerf -v -timeout 20m ./apitest/
//
// 用 SQL 批量造一个 10 万文件的项目（1000 个盒 × 100 页，审核状态和分配按规律分布），其中 6 万页挂在归类里：
// 机构 BGS 下 2000 件 × 25 页 = 5 万页（全部定稿、档号齐全、页全部通过，导出就用它）、机构 RSC 下 400 件 × 25 页
// （草稿、已完成、定稿混着，页的审核状态混着）。造完 ANALYZE。每个接口先跑 1 次热身，再跑 perfRuns 次取中位数和最慢，
// 中位数超过目标时报错（打印的就是记录下来的数字）。
//
// 目标：审核读接口、看板矩阵 < 200 ms；500 个文件的 catalog-placements < 100 ms；2000 件 / 5 万页的导出预检、
// 建任务、交付后变更各 < 1 秒。

const (
	perfBoxes      = 1000 // 盒数（20 个上层文件夹 × 50 个盒）
	perfPerBox     = 100  // 每盒页数
	perfFiles      = perfBoxes * perfPerBox
	perfExportItem = 2000 // BGS 的件数
	perfOtherItem  = 400  // RSC 的件数
	perfPages      = 25   // 每件页数
	perfRuns       = 5
)

// perfID 是造数据用的 id：前缀区分表，n 递增（同一张表里按 n 排序，和 UUIDv7 一样"后建的大"）。
func perfID(prefix string, n string) string {
	return fmt.Sprintf("('%s000000-0000-7000-8000-' || lpad(to_hex(%s), 12, '0'))::uuid", prefix, n)
}

type perfResult struct {
	name   string
	target time.Duration
	median time.Duration
	max    time.Duration
	note   string
}

// measure 热身 1 次，再跑 runs 次，记中位数和最慢。f 自己检查状态码，返回给结果的备注。
func measure(t *testing.T, out *[]perfResult, name string, target time.Duration, runs int, f func() string) {
	t.Helper()
	f()
	ds := make([]time.Duration, runs)
	note := ""
	for i := range ds {
		t0 := time.Now()
		note = f()
		ds[i] = time.Since(t0)
	}
	slices.Sort(ds)
	r := perfResult{name: name, target: target, median: ds[len(ds)/2], max: ds[len(ds)-1], note: note}
	*out = append(*out, r)
	if r.median > target {
		t.Errorf("%s：中位数 %v，超过目标 %v", name, r.median.Round(time.Millisecond), target)
	}
}

// perfFixture 是造好的数据。
type perfFixture struct {
	env             *Env
	admin, rv1      *Client
	p               projectDetail
	rv              []uuid.UUID
	profile         uuid.UUID
	fonds, bgs, rsc catNode
}

// setupPerf 建项目、用户、归类树，用 SQL 造 10 万文件的数据，最后 ANALYZE。
func setupPerf(t *testing.T) *perfFixture {
	t.Helper()
	env := NewEnv(t)
	profile, prefix := env.UseS3()
	admin := env.Admin()
	p := env.CreateProject("PERF")
	adminID := env.UserID(AdminUsername)
	rv := make([]uuid.UUID, 4)
	for i := range rv {
		rv[i] = env.CreateUser(fmt.Sprintf("perf_rv%d", i+1), "reviewer")
	}
	env.GrantProject(p.ID, rv...)
	rv1 := env.Login("perf_rv1", DefaultPassword)

	// 归类树经接口建（路径编码、last_child_no 都对）：全宗 Z1 / 门类 WS / 年度 2025 / 保管期限 Y / 机构 BGS、RSC。
	cat := &catalogEnv{Env: env, t: t, pid: p.ID, rootID: p.CategoryRootID, folder: p.RootFolderID, adminID: adminID, admin: admin}
	fonds := cat.mkCat(nil, "fonds", "Z1", "全宗")
	wsCat := cat.mkCat(&fonds, "category", "WS", "文书")
	year := cat.mkCat(&wsCat, "year", "2025", "2025 年")
	ret := cat.mkCat(&year, "retention", "Y", "永久")
	bgs := cat.mkCat(&ret, "org", "BGS", "办公室")
	rsc := cat.mkCat(&ret, "org", "RSC", "人事处")

	t0 := time.Now()
	rootPath := QueryValue[string](env, "SELECT path_code FROM folders WHERE id = $1", p.RootFolderID)
	top := perfID("0a", "t")
	box := perfID("0b", "b")
	fid := perfID("0c", "n")
	steps := []string{
		// 上层文件夹 20 个、盒 1000 个。
		fmt.Sprintf(`INSERT INTO folders (id, project_id, parent_id, folder_name, path_code, last_child_no, sort_no, created_by)
SELECT %s, $1, $2, '年度 ' || t, $3 || lpad(t::text, 5, '0') || '.', 50, t, $4 FROM generate_series(1, 20) t`, top),
		`UPDATE folders SET last_child_no = 20 WHERE id = $1`,
		fmt.Sprintf(`INSERT INTO folders (id, project_id, parent_id, folder_name, path_code, sort_no, created_by)
SELECT %s, $1, %s, '盒 ' || lpad(((b - 1) %% 50 + 1)::text, 3, '0'),
  $2 || lpad(((b - 1) / 50 + 1)::text, 5, '0') || '.' || lpad(((b - 1) %% 50 + 1)::text, 5, '0') || '.', (b - 1) %% 50 + 1, $3
FROM generate_series(1, %d) b`, box, perfID("0a", "((b - 1) / 50 + 1)"), perfBoxes),
		// 每个文件的计划：审核状态、审核人、挂在哪个条目。
		//   1–50000：通过，挂在 BGS 第 1–2000 件；50001–60000：挂在 RSC 第 1–400 件，状态按 n % 10 混着；
		//   其余没归类，状态按 n % 10 混着。审核人一半是 rv1（造一个待办很多的人），其余在 rv2–rv4 之间轮。
		fmt.Sprintf(`CREATE TABLE perf_plan AS
SELECT n, %s AS file_id, %s AS folder_id,
  CASE WHEN n <= 50000 THEN 'approved'
       WHEN n <= 60000 THEN (ARRAY['approved','approved','approved','approved','pending','pending','pending','rejected','unassigned','unassigned'])[n %% 10 + 1]
       ELSE (ARRAY['approved','approved','pending','pending','pending','rejected','unassigned','unassigned','unassigned','unassigned'])[n %% 10 + 1]
  END AS review_status,
  CASE WHEN n %% 2 = 0 THEN $1::uuid ELSE ($2::uuid[])[(n / 2) %% 3 + 1] END AS reviewer_id,
  CASE WHEN n <= 50000 THEN %s WHEN n <= 60000 THEN %s END AS item_id,
  CASE WHEN n <= 60000 THEN (n - 1) %% %d + 1 END AS page_no
FROM generate_series(1, %d) n`, fid, perfID("0b", "((n - 1) / 100 + 1)"),
			perfID("1a", "((n - 1) / 25 + 1)"), perfID("1b", "((n - 50001) / 25 + 1)"), perfPages, perfFiles),
		`INSERT INTO files (id) SELECT file_id FROM perf_plan`,
		fmt.Sprintf(`INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, storage_profile_id, storage_key, cover_key,
  content_sha256, file_size, mime_type, upload_name, width_px, height_px, dpi, color_mode, created_by)
SELECT %s, file_id, 1, 'original', $1, $2 || 'PERF/' || file_id || '/v1.tif', $2 || 'PERF/' || file_id || '/v1.cover.jpg',
  sha256(('perf-' || n)::bytea), 120000, 'image/tiff', 'IMG_' || lpad(((n - 1) %% 100 + 1)::text, 4, '0') || '.tif',
  2480, 3508, CASE WHEN n %% 50 = 0 THEN 150 ELSE 300 END, 'bw', $3
FROM perf_plan`, perfID("0d", "n")),
		fmt.Sprintf(`INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, review_status, created_by)
SELECT %s, $1, folder_id, file_id, 'IMG_' || lpad(((n - 1) %% 100 + 1)::text, 4, '0') || '.tif', (n - 1) %% 100 + 1, review_status, $2
FROM perf_plan`, perfID("0e", "n")),
		fmt.Sprintf(`INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by, status, result, snapshot_id, reject_reasons, reviewed_at)
SELECT %s, $1, file_id, reviewer_id, $2,
  CASE WHEN review_status = 'pending' THEN 'open' ELSE 'done' END,
  CASE WHEN review_status = 'pending' THEN NULL ELSE review_status END,
  CASE WHEN review_status = 'pending' THEN NULL ELSE %s END,
  CASE WHEN review_status = 'rejected' THEN '{blur}'::text[] ELSE '{}'::text[] END,
  CASE WHEN review_status = 'pending' THEN NULL ELSE now() END
FROM perf_plan WHERE review_status <> 'unassigned'`, perfID("0f", "n"), perfID("0d", "n")),
		// 条目：BGS 2000 件全部定稿；RSC 400 件草稿、已完成、定稿轮着。
		fmt.Sprintf(`INSERT INTO archive_items (id, project_id, category_id, item_level, item_no, archive_no, title, status, row_version, created_by)
SELECT %s, $1, $2, 'item', k, 'Z1-WS·2025-Y-BGS-' || lpad(k::text, 4, '0'), '文件 ' || k, 'locked', 3, $3
FROM generate_series(1, %d) k`, perfID("1a", "k"), perfExportItem),
		fmt.Sprintf(`INSERT INTO archive_items (id, project_id, category_id, item_level, item_no, archive_no, title, status, row_version, created_by)
SELECT %s, $1, $2, 'item', k, 'Z1-WS·2025-Y-RSC-' || lpad(k::text, 4, '0'), '人事文件 ' || k,
  (ARRAY['draft', 'done', 'locked'])[k %% 3 + 1], 2, $3
FROM generate_series(1, %d) k`, perfID("1b", "k"), perfOtherItem),
		`INSERT INTO archive_item_files (project_id, archive_item_id, file_id, page_no, linked_by)
SELECT $1, item_id, file_id, page_no, $2 FROM perf_plan WHERE item_id IS NOT NULL`,
	}
	args := [][]any{
		{p.ID, p.RootFolderID, rootPath, adminID},
		{p.RootFolderID},
		{p.ID, rootPath, adminID},
		{rv[0], []uuid.UUID{rv[1], rv[2], rv[3]}},
		nil,
		{profile, prefix, adminID},
		{p.ID, adminID},
		{p.ID, adminID},
		{p.ID, bgs.ID, adminID},
		{p.ID, rsc.ID, adminID},
		{p.ID, adminID},
	}
	for i, sql := range steps {
		env.Exec(sql, args[i]...)
	}
	env.Exec(`DROP TABLE perf_plan`)
	env.Exec(`ANALYZE`)
	t.Logf("造数据：%d 个文件、%d 个盒、%d 件（挂 %d 页），用时 %v", perfFiles, perfBoxes, perfExportItem+perfOtherItem,
		(perfExportItem+perfOtherItem)*perfPages, time.Since(t0).Round(time.Millisecond))
	return &perfFixture{env: env, admin: admin, rv1: rv1, p: p, rv: rv, profile: profile, fonds: fonds, bgs: bgs, rsc: rsc}
}

func TestPerf(t *testing.T) {
	fx := setupPerf(t)
	env, admin, rv1, p, rv, profile, fonds, bgs := fx.env, fx.admin, fx.rv1, fx.p, fx.rv, fx.profile, fx.fonds, fx.bgs
	fid := perfID("0c", "n")

	var res []perfResult
	pp := "/api/projects/" + p.ID.String()
	topFolder := uuid.MustParse("0a000000-0000-7000-8000-00000000000a") // 第 10 个上层文件夹（50 个盒、5000 页）
	get := func(c *Client, path string) func() string {
		return func() string {
			r := c.Get(path).Expect(http.StatusOK)
			return fmt.Sprintf("%d KB", len(r.Body)/1024)
		}
	}

	// ---- 审核读接口（< 200 ms）----
	my := "/api/me/reviews?project_id=" + p.ID.String()
	var first struct {
		NextCursor *string `json:"next_cursor"`
		Counts     struct {
			All int `json:"all"`
		} `json:"counts"`
	}
	rv1.Get(my).Expect(http.StatusOK).JSON(&first)
	if first.NextCursor == nil || first.Counts.All < 30000 {
		t.Fatalf("rv1 的我的审核应有 3 万多个、要翻页：%d，next_cursor %v", first.Counts.All, first.NextCursor)
	}
	measure(t, &res, "我的审核 全部（rv1 约 3.6 万个，第 1 页 500 行）", 200*time.Millisecond, perfRuns, get(rv1, my))
	measure(t, &res, "我的审核 第 2 页（cursor）", 200*time.Millisecond, perfRuns, get(rv1, my+"&cursor="+url.QueryEscape(*first.NextCursor)))
	measure(t, &res, "我的审核 待审", 200*time.Millisecond, perfRuns, get(rv1, my+"&state=open"))
	measure(t, &res, "我的审核 DPI 不足", 200*time.Millisecond, perfRuns, get(rv1, my+"&low_dpi=true"))
	measure(t, &res, "我的审核 一个上层文件夹（子树 5000 页）", 200*time.Millisecond, perfRuns, get(rv1, my+"&folder_id="+topFolder.String()))
	measure(t, &res, "审核计数 review-summary（整个项目）", 200*time.Millisecond, perfRuns, get(admin, pp+"/review-summary"))
	measure(t, &res, "审核计数 review-summary（一个上层文件夹）", 200*time.Millisecond, perfRuns, get(admin, pp+"/review-summary?folder_id="+topFolder.String()))
	measure(t, &res, "open 分配 review-assignments（第 1 页）", 200*time.Millisecond, perfRuns, get(admin, pp+"/review-assignments"))
	measure(t, &res, "open 分配 review-assignments（rv1）", 200*time.Millisecond, perfRuns, get(admin, pp+"/review-assignments?reviewer_id="+rv[0].String()))
	measure(t, &res, "可分配的人 reviewers（带待办数）", 200*time.Millisecond, perfRuns, get(admin, pp+"/reviewers"))
	measure(t, &res, "待重扫 rescans（第 1 页）", 200*time.Millisecond, perfRuns, get(admin, pp+"/rescans"))

	// ---- 看板矩阵（< 200 ms）----
	measure(t, &res, "看板 catalog-summary（整个项目）", 200*time.Millisecond, perfRuns, get(admin, pp+"/catalog-summary"))
	measure(t, &res, "看板 catalog-summary（一个上层文件夹）", 200*time.Millisecond, perfRuns, get(admin, pp+"/catalog-summary?folder_id="+topFolder.String()))

	// ---- 500 个文件的"挂在哪"（< 100 ms）：BGS、RSC 里的 450 个 + 没归类的 50 个 ----
	ids := make([]string, 0, 500)
	for i := 0; i < 450; i++ {
		ids = append(ids, fmt.Sprintf("0c000000-0000-7000-8000-%012x", 1+i*133))
	}
	for i := 0; i < 50; i++ {
		ids = append(ids, fmt.Sprintf("0c000000-0000-7000-8000-%012x", 60001+i*791))
	}
	measure(t, &res, "归类信息 catalog-placements（500 个文件）", 100*time.Millisecond, perfRuns, func() string {
		var out struct {
			Items []struct {
				Placement any `json:"placement"`
			} `json:"items"`
		}
		r := admin.Post(pp+"/catalog-placements", map[string]any{"file_ids": ids}).Expect(http.StatusOK)
		r.JSON(&out)
		return fmt.Sprintf("%d 项", len(out.Items))
	})

	// ---- 导出 2000 件 / 5 万页（各 < 1 秒）----
	body := map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{bgs.ID}, "export_format": "pdf", "only_approved": true}
	measure(t, &res, "导出预检（BGS：2000 件 / 5 万页）", time.Second, perfRuns, func() string {
		var out struct {
			ItemCount      int `json:"item_count"`
			PageCount      int `json:"page_count"`
			ReadyItemCount int `json:"ready_item_count"`
		}
		admin.Post(pp+"/export-tasks:precheck", body).Expect(http.StatusOK).JSON(&out)
		if out.ItemCount != perfExportItem || out.PageCount != perfExportItem*perfPages || out.ReadyItemCount != perfExportItem {
			t.Fatalf("预检结果不对：%+v", out)
		}
		return fmt.Sprintf("%d 件 %d 页，就绪 %d 件", out.ItemCount, out.PageCount, out.ReadyItemCount)
	})
	all := map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{fonds.ID}, "export_format": "pdf", "only_approved": true}
	measure(t, &res, "导出预检（全宗：2400 件 / 6 万页，有不就绪的）", time.Second, perfRuns, func() string {
		var out struct {
			ItemCount     int `json:"item_count"`
			NotReadyCount int `json:"not_ready_count"`
		}
		admin.Post(pp+"/export-tasks:precheck", all).Expect(http.StatusOK).JSON(&out)
		return fmt.Sprintf("%d 件，不就绪 %d", out.ItemCount, out.NotReadyCount)
	})
	var taskID uuid.UUID
	// 目标是 1 秒；本机实测中位数 0.97–1.04 秒（大头是 COPY 明细），机器一忙就过线。用户 2026-10-16 接受现状、
	// 正式硬件上重测，所以这里按 1.5 秒判失败，报告里照样记实测数字。
	measure(t, &res, "建导出任务（冻结 2000 件 / 5 万页）", 1500*time.Millisecond, 3, func() string {
		var task exportTask
		admin.Post(pp+"/export-tasks", body).Expect(http.StatusCreated).JSON(&task)
		if task.ItemCount != perfExportItem || task.PageCount != perfExportItem*perfPages {
			t.Fatalf("任务的件数页数不对：%d 件 %d 页", task.ItemCount, task.PageCount)
		}
		taskID = task.ID
		return fmt.Sprintf("%d 件 %d 页", task.ItemCount, task.PageCount)
	})

	// 交付后变更：最后一个任务直接改成 done（不真的生成），再造几类变化：200 页出了 v2、100 页变回待审、50 件改了
	// 题名、10 件里第 1、2 页对调。
	env.Exec(`UPDATE background_jobs SET status = 'done', finished_at = now() WHERE id = (SELECT job_id FROM export_tasks WHERE id = $1)`, taskID)
	env.Exec(`UPDATE export_tasks SET status = 'done', finished_at = now(), expires_at = now() + interval '7 days',
  storage_profile_id = $2, output_key = 'perf/none.zip', output_size = 1 WHERE id = $1`, taskID, profile)
	env.Exec(fmt.Sprintf(`INSERT INTO file_snapshots (id, file_id, version_no, snapshot_type, storage_profile_id, storage_key,
  content_sha256, file_size, mime_type, width_px, height_px, dpi, color_mode)
SELECT %s, %s, 2, 'rescan', $1, 'perf/v2-' || n, sha256(('perf-v2-' || n)::bytea), 1, 'image/tiff', 2480, 3508, 300, 'bw'
FROM generate_series(1, 50000, 250) n`, perfID("2a", "n"), fid), profile)
	env.Exec(fmt.Sprintf(`UPDATE folder_files SET review_status = 'pending', review_round = 2
WHERE file_id IN (SELECT %s FROM generate_series(7, 50000, 500) n)`, fid))
	env.Exec(fmt.Sprintf(`UPDATE archive_items SET title = title || '（改）', row_version = row_version + 1
WHERE id IN (SELECT %s FROM generate_series(3, 2000, 40) k)`, perfID("1a", "k")))
	env.Exec(fmt.Sprintf(`UPDATE archive_item_files SET page_no = 3 - page_no
WHERE archive_item_id IN (SELECT %s FROM generate_series(11, 2000, 200) k) AND page_no IN (1, 2)`, perfID("1a", "k")))
	env.Exec(`ANALYZE`)
	measure(t, &res, "交付后变更（2000 件 / 5 万页的任务）", time.Second, perfRuns, func() string {
		var out struct {
			ChangedCount int            `json:"changed_count"`
			ReasonCounts map[string]int `json:"reason_counts"`
		}
		admin.Get("/api/export-tasks/" + taskID.String() + "/changes").Expect(http.StatusOK).JSON(&out)
		if out.ChangedCount == 0 || out.ReasonCounts["new_version"] == 0 || out.ReasonCounts["not_approved"] == 0 ||
			out.ReasonCounts["item_changed"] == 0 || out.ReasonCounts["pages_reordered"] == 0 {
			t.Fatalf("交付后变更不对：%+v", out)
		}
		return fmt.Sprintf("%d 件有变更 %v", out.ChangedCount, out.ReasonCounts)
	})

	var b strings.Builder
	b.WriteString("\n| 接口 | 目标 | 中位数 | 最慢 | 备注 |\n|---|---|---|---|---|\n")
	for _, r := range res {
		fmt.Fprintf(&b, "| %s | %v | %v | %v | %s |\n", r.name, r.target, r.median.Round(100*time.Microsecond),
			r.max.Round(100*time.Microsecond), r.note)
	}
	t.Log(b.String())
}
