package apitest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/file"
	"doclite/server/internal/project"
)

// 挂接的并发（requirements 3.6、5.9；CLAUDE.md 点名的"多人同时挂接""审核和归类同时操作"）。

// waitLockWaiters 等到这个测试库里至少有 n 个会话在等锁（pg_stat_activity 的 wait_event_type = 'Lock'），查询文本
// 符合 pattern（LIKE）。最多等 5 秒。
func waitLockWaiters(t *testing.T, e *Env, pattern string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := QueryValue[int](e, `SELECT count(*)::int FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock' AND state = 'active' AND query LIKE $1`, pattern)
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("5 秒内没有等到 %d 个在等锁的会话（%s），现在 %d 个", n, pattern, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// holdFileRow 开一个手工事务锁住这个文件的 files 行（测试结束时回滚）。
func holdFileRow(t *testing.T, pool *pgxpool.Pool, fid uuid.UUID) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if err := file.LockFiles(ctx, tx, []uuid.UUID{fid}); err != nil {
		t.Fatal(err)
	}
	return tx
}

type raceResult struct {
	itemID uuid.UUID
	res    *Response
}

// TestCatalogLinkRace：两个条目同时抢同一个文件，只有一个成功（另一个 409 file_cataloged，整体不做）。
// 先用手工事务锁住这个文件的 files 行，让两边都排在它后面，放开后同时去抢。
func TestCatalogLinkRace(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "RC")
	_, _, _, ret, org := c.mkChain()
	org2 := c.mkCat(&ret, "org", "RSC", "人事处")

	// PUT 页：两个条目各自整体替换成 [f]。
	x := c.mkItem(org.ID, map[string]any{"title": "X"})
	y := c.mkItem(org.ID, map[string]any{"title": "Y"})
	f := c.mkFile(uuid.Nil, "approved")
	hold := holdFileRow(t, c.Pool, f)
	results := make(chan raceResult, 2)
	for _, it := range []itemView{x, y} {
		go func() {
			results <- raceResult{it.ID, c.admin.Put(filesPath(it.ID), map[string]any{"row_version": it.RowVersion, "file_ids": []uuid.UUID{f}})}
		}()
	}
	waitLockWaiters(t, c.Env, "%FROM files WHERE id = ANY%", 2)
	if err := hold.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var winner uuid.UUID
	ok, lost := 0, 0
	for range 2 {
		r := <-results
		switch {
		case r.res.Status == 200:
			ok++
			winner = r.itemID
		case r.res.Status == 409 && r.res.Code() == "file_cataloged":
			lost++
		default:
			t.Fatalf("意外的结果：%d %s", r.res.Status, r.res.Body)
		}
	}
	if ok != 1 || lost != 1 {
		t.Fatalf("PUT：成功 %d、409 %d", ok, lost)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*)::int FROM archive_item_files WHERE file_id = $1 AND archive_item_id = $2`, f, winner); n != 1 {
		t.Fatal("文件没有挂在成功的那个条目里")
	}
	if n := QueryValue[int](c.Env, `SELECT count(*)::int FROM file_logs WHERE file_id = $1 AND action = 'link'`, f); n != 1 {
		t.Fatalf("link 日志 %d 条", n)
	}

	// 带 file_ids 新建（两个节点各建一个）：只有一个建成，另一个整体回滚、条目不建。
	g := c.mkFile(uuid.Nil, "approved")
	hold = holdFileRow(t, c.Pool, g)
	created := make(chan *Response, 2)
	for _, cat := range []uuid.UUID{org.ID, org2.ID} {
		go func() {
			created <- c.admin.Post("/api/archive-categories/"+cat.String()+"/items", map[string]any{"title": "抢", "file_ids": []uuid.UUID{g}})
		}()
	}
	waitLockWaiters(t, c.Env, "%FROM files WHERE id = ANY%", 2)
	if err := hold.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok, lost = 0, 0
	for range 2 {
		r := <-created
		switch {
		case r.Status == 201:
			ok++
		case r.Status == 409 && r.Code() == "file_cataloged":
			lost++
		default:
			t.Fatalf("意外的结果：%d %s", r.Status, r.Body)
		}
	}
	if ok != 1 || lost != 1 || QueryValue[int](c.Env, `SELECT count(*)::int FROM archive_items WHERE title = '抢'`) != 1 {
		t.Fatalf("新建：成功 %d、409 %d", ok, lost)
	}
}

// TestCatalogLinkDoesNotBlockReview：审核和挂接同时进行不互相等（requirements 3.6 的加锁顺序）。
// 一个只拿归类锁的长事务占着条目行（像批量定稿、改著录）；这时 PUT 页在等条目行，手里只有归类树的共享锁、没有文件
// 树锁，所以：移动文件夹（文件树排他锁）不排在它后面，再后面的审核通过（真实接口，文件树共享锁 → files 行）也不用等。
// 加锁顺序反过来（先文件树锁再等条目行）时，移动文件夹会排在 PUT 后面，审核又排在移动后面，这个测试失败。
// 被拖住的请求要一直等到第 5 步放开条目行，所以下面的时限只要比一次请求的正常耗时宽就行：开发库在远程，一次审核
// 要好几次数据库往返，约 1 秒。
func TestCatalogLinkDoesNotBlockReview(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "RV")
	admin := e.admin
	fonds := Decode[catNode](admin.Post("/api/projects/"+e.p.ID.String()+"/archive-categories",
		map[string]any{"category_type": "fonds", "category_code": "Z1", "category_name": "全宗"}).Expect(201))
	it := Decode[itemView](admin.Post("/api/archive-categories/"+fonds.ID.String()+"/items",
		map[string]any{"title": "挂接中"}).Expect(201))
	f := e.fileIn("pending", "p1.tif").FileID // 分给了 rv1、待审
	box := mkdir(admin, e.p.RootFolderID, "要移动的盒")
	dest := mkdir(admin, e.p.RootFolderID, "目标")
	ctx := context.Background()

	// 1. 只拿归类锁的长事务占着条目行。
	hold, err := e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if err := project.LockCatalogTree(ctx, hold, e.p.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, `SELECT id FROM archive_items WHERE id = $1 FOR NO KEY UPDATE`, it.ID); err != nil {
		t.Fatal(err)
	}

	// 2. 挂接：在等条目行。
	putDone := make(chan *Response, 1)
	go func() {
		putDone <- admin.Put(filesPath(it.ID), map[string]any{"row_version": it.RowVersion, "file_ids": []uuid.UUID{f}})
	}()
	waitLockWaiters(t, e.Env, "%FOR NO KEY UPDATE OF i%", 1)

	// 3. 移动文件夹（文件树排他锁）：不排在挂接后面。
	moveDone := make(chan *Response, 1)
	start := time.Now()
	go func() {
		moveDone <- admin.Patch("/api/folders/"+box.ID.String(), map[string]any{"parent_id": dest.ID})
	}()
	select {
	case r := <-moveDone:
		r.Expect(200)
	case <-time.After(10 * time.Second):
		t.Fatal("移动文件夹被等条目行的挂接拖住了")
	}
	moveTook := time.Since(start)

	// 4. 审核通过（真实接口）：不等挂接（要求 3 秒内返回）。
	start = time.Now()
	reviewDone := make(chan *Response, 1)
	go func() {
		reviewDone <- e.rv1.Post("/api/files/"+f.String()+"/review", map[string]any{"result": "approved"})
	}()
	select {
	case r := <-reviewDone:
		r.Expect(200)
	case <-time.After(10 * time.Second):
		t.Fatal("审核被等条目行的挂接拖住了")
	}
	reviewTook := time.Since(start)
	if reviewTook > 3*time.Second {
		t.Fatalf("审核等了 %v", reviewTook)
	}
	t.Logf("挂接等条目行期间：移动文件夹 %v，审核通过 %v", moveTook, reviewTook)
	select {
	case r := <-putDone:
		t.Fatalf("条目行放开之前挂接就返回了：%d %s", r.Status, r.Body)
	default:
	}

	// 5. 放开条目行：挂接完成，页是审核后的状态。
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-putDone:
		got := Decode[itemView](r.Expect(200))
		if len(got.Pages) != 1 || got.Pages[0].FileID != f || got.Pages[0].ReviewStatus != "approved" {
			t.Fatalf("挂接的结果：%+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("条目行放开后挂接没有完成")
	}
}
