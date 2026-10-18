package apitest

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/project"
)

// 归档的并发（requirements 3.6、5.9；CLAUDE.md 点名的"多人同时归档""同一文件夹同时归档和上传""审核和归类同时操作"）。

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

// holdFolderRow 开一个手工事务锁住这个文件夹行（和归档、文件目录的写操作同一种锁；测试结束时回滚）。
func holdFolderRow(t *testing.T, pool *pgxpool.Pool, folderID uuid.UUID) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `SELECT id FROM folders WHERE id = $1 FOR NO KEY UPDATE`, folderID); err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestCatalogArchiveRace：两个人同时把同一个文件夹归档到两个节点，只有一个成功（另一个跳过 archived、一件都建不出
// 409 invalid_state）。先用手工事务锁住文件夹行，让两边都排在它后面，放开后同时去抢。
func TestCatalogArchiveRace(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "RC")
	_, _, _, ret, org := c.mkChain()
	org2 := c.mkCat(&ret, "org", "RSC", "人事处")
	f, _ := c.mkFolder(uuid.Nil, "抢", "approved", "approved")

	hold := holdFolderRow(t, c.Pool, f)
	results := make(chan *Response, 2)
	for _, cat := range []uuid.UUID{org.ID, org2.ID} {
		go func() { results <- c.archive(cat, map[string]any{"folder_ids": []uuid.UUID{f}}) }()
	}
	waitLockWaiters(t, c.Env, "%FROM folders WHERE id = ANY%FOR NO KEY UPDATE%", 2)
	if err := hold.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok, lost := 0, 0
	for range 2 {
		r := <-results
		switch {
		case r.Status == 201:
			ok++
		case r.Status == 409 && r.Code() == "invalid_state" && strings.Contains(string(r.Body), `"reason":"archived"`):
			lost++
		default:
			t.Fatalf("意外的结果：%d %s", r.Status, r.Body)
		}
	}
	if ok != 1 || lost != 1 {
		t.Fatalf("成功 %d、409 %d", ok, lost)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*)::int FROM archive_items WHERE source_folder_id = $1`, f); n != 1 {
		t.Fatalf("文件夹对应了 %d 件", n)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*)::int FROM file_logs WHERE action = 'link'`); n != 2 {
		t.Fatalf("link 日志 %d 条", n)
	}
}

// TestCatalogArchiveUploadRace：同一个文件夹同时归档和上传（requirements 3.6"封存检查和归档读文件清单要互斥"）。
// 两边都排在手工事务锁住的文件夹行后面，放开后谁先都行，但结果必须满足：归档成功，条目的页 = 文件夹里未删除的文件；
// 上传要么成功（先到，成了这一件的页），要么 409 folder_archived（后到，什么都没写）。做两轮。
func TestCatalogArchiveUploadRace(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "AU")
	c := &catalogEnv{Env: e.Env, t: t, pid: e.p.ID, rootID: e.p.CategoryRootID, folder: e.p.RootFolderID,
		adminID: e.UserID(AdminUsername), admin: e.admin}
	_, _, _, _, org := c.mkChain()
	outcomes := map[int]int{}
	for round := range 2 {
		f := mkdir(e.admin, e.p.RootFolderID, "件 "+strconv.Itoa(round)).ID
		e.upload(f, "first.tif")
		data := unique(sampleBytes(t, "tiff-g4-1bit.tif"), "AU-race-"+strconv.Itoa(round))

		hold := holdFolderRow(t, c.Pool, f)
		archived := make(chan *Response, 1)
		uploaded := make(chan *Response, 1)
		go func() { archived <- c.archive(org.ID, map[string]any{"folder_ids": []uuid.UUID{f}}) }()
		go func() { uploaded <- e.scan.Upload(f, "second.tif", data, nil) }()
		waitLockWaiters(t, c.Env, "%FROM folders%WHERE%id = ANY%FOR NO KEY UPDATE%", 2)
		if err := hold.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		ar, up := <-archived, <-uploaded
		res := Decode[archiveResult](ar.Expect(http.StatusCreated))
		if up.Status != http.StatusCreated && (up.Status != http.StatusConflict || up.Code() != "folder_archived") {
			t.Fatalf("第 %d 轮：上传 %d %s", round, up.Status, up.Body)
		}
		outcomes[up.Status]++
		it := c.item(*res.Items[0].ItemID)
		live := QueryValue[[]uuid.UUID](c.Env, `SELECT coalesce(array_agg(file_id ORDER BY sort_no, file_name), '{}')
FROM folder_files WHERE folder_id = $1 AND deleted_at IS NULL`, f)
		if !slices.Equal(it.pageIDs(), live) || len(live) != map[int]int{201: 2, 409: 1}[up.Status] {
			t.Fatalf("第 %d 轮（上传 %d）：条目的页 %v，文件夹里 %v", round, up.Status, it.pageIDs(), live)
		}
	}
	t.Logf("两轮的上传结果：%v", outcomes)
}

// TestCatalogResealDoesNotBlockReview：审核和重新归档同时进行不互相等（requirements 3.6 的加锁顺序）。
// 一个只拿归类锁的长事务占着条目行（像批量锁定、改著录）；这时重新归档在等条目行，手里只有归类树的共享锁、没有
// 文件树锁，所以：移动文件夹（文件树排他锁）不排在它后面，再后面的审核通过（真实接口，文件树共享锁 → files 行）也
// 不用等。加锁顺序反过来（先文件树锁再等条目行）时，移动文件夹会排在重新归档后面，审核又排在移动后面，这个测试失败。
// 被拖住的请求要一直等到第 5 步放开条目行，所以下面的时限只要比一次请求的正常耗时宽就行：开发库在远程，一次审核
// 要好几次数据库往返，约 1 秒。
func TestCatalogResealDoesNotBlockReview(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "RV")
	admin := e.admin
	fonds := Decode[catNode](admin.Post("/api/projects/"+e.p.ID.String()+"/archive-categories",
		map[string]any{"category_type": "fonds", "category_code": "Z1", "category_name": "全宗"}).Expect(201))
	f := e.fileIn("pending", "p1.tif").FileID // 在盒 1 里，分给了 rv1、待审
	res := Decode[archiveResult](admin.Post("/api/archive-categories/"+fonds.ID.String()+"/items:from-folders",
		map[string]any{"folder_ids": []uuid.UUID{e.box.ID}}).Expect(201))
	itemID := *res.Items[0].ItemID
	it := Decode[itemView](admin.Get("/api/archive-items/" + itemID.String()).Expect(200))
	it = Decode[itemView](admin.Post("/api/archive-items/"+itemID.String()+"/unseal", map[string]any{"row_version": it.RowVersion}).Expect(200))
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
	if _, err := hold.Exec(ctx, `SELECT id FROM archive_items WHERE id = $1 FOR NO KEY UPDATE`, itemID); err != nil {
		t.Fatal(err)
	}

	// 2. 重新归档：在等条目行。
	resealDone := make(chan *Response, 1)
	go func() {
		resealDone <- admin.Post("/api/archive-items/"+itemID.String()+"/reseal", map[string]any{"row_version": it.RowVersion})
	}()
	waitLockWaiters(t, e.Env, "%FOR NO KEY UPDATE OF i%", 1)

	// 3. 移动文件夹（文件树排他锁）：不排在重新归档后面。
	moveDone := make(chan *Response, 1)
	start := time.Now()
	go func() {
		moveDone <- admin.Patch("/api/folders/"+box.ID.String(), map[string]any{"parent_id": dest.ID})
	}()
	select {
	case r := <-moveDone:
		r.Expect(200)
	case <-time.After(10 * time.Second):
		t.Fatal("移动文件夹被等条目行的重新归档拖住了")
	}
	moveTook := time.Since(start)

	// 4. 审核通过（真实接口）：不等重新归档（要求 3 秒内返回）。
	start = time.Now()
	reviewDone := make(chan *Response, 1)
	go func() {
		reviewDone <- e.rv1.Post("/api/files/"+f.String()+"/review", map[string]any{"result": "approved"})
	}()
	select {
	case r := <-reviewDone:
		r.Expect(200)
	case <-time.After(10 * time.Second):
		t.Fatal("审核被等条目行的重新归档拖住了")
	}
	reviewTook := time.Since(start)
	if reviewTook > 3*time.Second {
		t.Fatalf("审核等了 %v", reviewTook)
	}
	t.Logf("重新归档等条目行期间：移动文件夹 %v，审核通过 %v", moveTook, reviewTook)
	select {
	case r := <-resealDone:
		t.Fatalf("条目行放开之前重新归档就返回了：%d %s", r.Status, r.Body)
	default:
	}

	// 5. 放开条目行：重新归档完成，页是审核后的状态。
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-resealDone:
		got := Decode[itemView](r.Expect(200))
		if len(got.Pages) != 1 || got.Pages[0].FileID != f || got.Pages[0].ReviewStatus != "approved" || got.SourceStatus != "sealed" {
			t.Fatalf("重新归档的结果：%+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("条目行放开后重新归档没有完成")
	}
}
