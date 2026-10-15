package apitest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestReviewConcurrency：审核（单个通过 / 驳回、批量通过、撤回、改派）和整理（批量移动文件、移动文件夹、删除文件）
// 同时进行：不死锁（PG 检测到死锁会变成 500）、不出 500、在时限内做完，结束后状态和分配记录一致。
func TestReviewConcurrency(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "CC")
	a := e.box
	a1 := mkdir(e.admin, a.ID, "A1")
	b := mkdir(e.admin, e.p.RootFolderID, "B")
	var files []ingestResponse
	for i := range 30 {
		folder := a.ID
		switch {
		case i%3 == 1:
			folder = a1.ID
		case i%3 == 2:
			folder = b.ID
		}
		files = append(files, e.upload(folder, fmt.Sprintf("p%02d.tif", i)))
	}
	e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "folder_id": e.p.RootFolderID}).Expect(200)
	ids := func(fs []ingestResponse) []uuid.UUID {
		out := make([]uuid.UUID, len(fs))
		for i, f := range fs {
			out[i] = f.FileID
		}
		return out
	}
	entries := func(fs []ingestResponse) []uuid.UUID {
		out := make([]uuid.UUID, len(fs))
		for i, f := range fs {
			out[i] = f.FolderFileID
		}
		return out
	}

	var mu sync.Mutex
	var bad []string
	allow := func(who string, r *Response, ok ...int) {
		for _, s := range ok {
			if r.Status == s {
				return
			}
		}
		mu.Lock()
		bad = append(bad, fmt.Sprintf("%s：%d %s", who, r.Status, r.Body))
		mu.Unlock()
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	// 审核员：逐个通过或驳回，最后批量通过剩下的（有被删的就 404 整体不做）。
	run(func() {
		for i, f := range files[:24] {
			r := approve(e.rv1, f.FileID)
			if i%2 == 1 {
				r = reject(e.rv1, f.FileID)
			}
			allow("审核", r, 200, 404, 409, 403)
		}
		allow("批量通过", e.rv1.Post(e.url("/reviews:approve"), map[string]any{"file_ids": ids(files[24:])}), 200, 404, 409, 403)
	})
	// 负责人：改派、撤回通过。
	run(func() {
		for round := range 3 {
			to := e.rv2ID
			if round%2 == 1 {
				to = e.rv1ID
			}
			allow("改派", e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": to,
				"file_ids": ids(files[20:30])}), 200, 404)
			for _, f := range files[:6] {
				allow("撤回", e.mgr.Post("/api/files/"+f.FileID.String()+"/reopen", nil), 200, 404, 409)
			}
		}
	})
	// 整理：批量移动文件 A ⇄ B，移动文件夹 A1 到 B 下再移回来（文件树排他锁）。
	run(func() {
		for round := range 4 {
			dest := b.ID
			if round%2 == 1 {
				dest = a.ID
			}
			allow("批量移动", e.admin.Post("/api/folder-files:move", map[string]any{"ids": entries(files[3:15]), "folder_id": dest}), 200, 404)
			parent := b.ID
			if round%2 == 1 {
				parent = a.ID
			}
			allow("移动文件夹", e.admin.Patch("/api/folders/"+a1.ID.String(), map[string]any{"parent_id": parent}), 200)
		}
	})
	// 删除：每 5 个删一个。
	run(func() {
		for i := 0; i < len(files); i += 5 {
			allow("删除", e.admin.Delete("/api/folder-files/"+files[i].FolderFileID.String()), 204)
		}
	})
	began := time.Now()
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("60 秒没做完（死锁？）")
	}
	if len(bad) > 0 {
		t.Fatalf("意外的响应：\n%v", bad)
	}
	t.Logf("用时 %v", time.Since(began))

	// 一致性：已删除的文件没有 open；待审核的恰好一行 open，其他状态没有 open；已通过、已驳回的最近一次 done 的结果一致；
	// 文件都在未删除的文件夹里；每次通过、驳回都有日志。
	check := func(what, sql string) {
		if n := QueryValue[int](e.Env, sql, e.p.ID); n != 0 {
			t.Errorf("%s：%d 个", what, n)
		}
	}
	check("已删除的文件还有 open 的分配", `SELECT count(*) FROM file_reviews r JOIN folder_files ff ON ff.file_id = r.file_id
WHERE ff.project_id = $1 AND ff.deleted_at IS NOT NULL AND r.status = 'open'`)
	check("待审核和 open 的行数对不上", `SELECT count(*) FROM folder_files ff
WHERE ff.project_id = $1 AND ff.deleted_at IS NULL
  AND (ff.review_status = 'pending') <> ((SELECT count(*) FROM file_reviews r WHERE r.file_id = ff.file_id AND r.status = 'open') = 1)`)
	check("结果和最近一次 done 对不上", `SELECT count(*) FROM folder_files ff
WHERE ff.project_id = $1 AND ff.deleted_at IS NULL AND ff.review_status IN ('approved', 'rejected')
  AND ff.review_status IS DISTINCT FROM (SELECT r.result FROM file_reviews r WHERE r.file_id = ff.file_id AND r.status = 'done'
                                         ORDER BY r.id DESC LIMIT 1)`)
	check("文件在已删除的文件夹里", `SELECT count(*) FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.project_id = $1 AND ff.deleted_at IS NULL AND f.deleted_at IS NOT NULL`)
	check("通过、驳回和日志对不上", `SELECT abs((SELECT count(*) FROM file_reviews WHERE project_id = $1 AND status = 'done')
  - (SELECT count(*) FROM file_logs WHERE project_id = $1 AND action IN ('approve', 'reject')))`)
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM folder_files WHERE project_id = $1 AND deleted_at IS NOT NULL`, e.p.ID); n != 6 {
		t.Errorf("删除了 %d 个，期望 6 个", n)
	}
}
