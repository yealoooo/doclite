package apitest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/jobs"
)

// 执行器的兜底（M5-2 审查第 1 轮）：改状态写库失败后由回收接手、单实例锁、真的走分片上传。

// failSettles 让 background_jobs 从 running 改成别的状态都失败（模拟执行器改状态时写库出错）；返回删掉触发器的函数。
func failSettles(e *Env) func() {
	e.t.Helper()
	e.Exec(`CREATE OR REPLACE FUNCTION test_fail_settle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status = 'running' AND NEW.status <> 'running' THEN
    RAISE EXCEPTION 'test: settle fails';
  END IF;
  RETURN NEW;
END $$`)
	e.Exec(`CREATE TRIGGER test_fail_settle BEFORE UPDATE ON background_jobs FOR EACH ROW EXECUTE FUNCTION test_fail_settle()`)
	return func() { e.Exec(`DROP TRIGGER IF EXISTS test_fail_settle ON background_jobs`) }
}

// waitOrphan 等任务的 job 在库里是 running、而执行器已经不在执行它了（改状态失败，留给回收）。
func (e *exportEnv) waitOrphan(taskID uuid.UUID) {
	e.t.Helper()
	jobID := QueryValue[uuid.UUID](e.Env, `SELECT job_id FROM export_tasks WHERE id = $1`, taskID)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if st, _, _ := e.jobOf(taskID); st == "running" && !e.App.Jobs.Executing(jobID) {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("任务 %s 的 job 没有停在“running、没人执行”", taskID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestExportReaper：执行器改状态写库失败时 job 留在 running；写库恢复后一个回收周期（心跳间隔）内收尾——取消的变成
// canceled（cleaning_up 变成 false，到期清理接着清），没取消的放回队列（算一次尝试）再执行完。另外：同一个库上第二个
// 执行器拿不到单实例锁，不启动。
func TestExportReaper(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "EXO", withProxy)
	body := e.exportReady("一", 6)
	e.start()

	// 同一个库上的第二个执行器：拿不到锁，不启动、不回收。
	other := jobs.New(e.Pool, jobs.Options{Concurrency: 1}, nil)
	if err := other.Start(context.Background()); !errors.Is(err, jobs.ErrBusy) || other.Active() {
		t.Fatalf("第二个执行器应拿不到锁：%v", err)
	}

	// 1. 取消后收尾写库失败：一直 cleaning_up；恢复后一个回收周期内变成 canceled。
	e.proxy.delay.Store(int64(300 * time.Millisecond))
	t1 := e.createTask(body)
	e.waitTask(t1.ID, 10*time.Second, "开始执行", func(t exportTask) bool { return t.Status == "running" })
	restore := failSettles(e.Env)
	c := Decode[exportTask](e.mgr.Post("/api/export-tasks/"+t1.ID.String()+"/cancel", nil).Expect(http.StatusOK))
	if !c.CleaningUp {
		t.Fatalf("取消执行中的：%+v", c)
	}
	e.waitOrphan(t1.ID)
	time.Sleep(time.Second) // 回收也一直失败
	if tk := e.task(t1.ID); !tk.CleaningUp {
		t.Fatalf("改状态失败时应一直是 cleaning_up：%+v", tk)
	}
	if st, _, _ := e.jobOf(t1.ID); st != "running" {
		t.Fatalf("job：%s", st)
	}
	restore()
	start := time.Now()
	e.waitTask(t1.ID, 2*time.Second, "清理完", func(t exportTask) bool { return !t.CleaningUp })
	t.Logf("恢复后 %s 收尾完", time.Since(start).Round(time.Millisecond))
	if st, _, _ := e.jobOf(t1.ID); st != "canceled" {
		t.Fatalf("回收后 job：%s", st)
	}
	if n, err := e.App.Exports.Cleanup(context.Background()); err != nil || n > 1 {
		t.Errorf("收尾后到期清理：%d %v", n, err)
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM export_tasks WHERE id = $1 AND output_key IS NOT NULL`, t1.ID); n != 0 {
		t.Errorf("收尾后到期清理应清空导出物")
	}

	// 2. 完成时写库失败：放回队列也失败，job、任务都停在 running；恢复后回收放回队列（算一次尝试），再执行完。
	e.proxy.delay.Store(0)
	restore = failSettles(e.Env)
	t2 := e.createTask(body)
	e.waitOrphan(t2.ID)
	time.Sleep(time.Second)
	if tk := e.task(t2.ID); tk.Status != "running" || tk.Attempts != 1 {
		t.Fatalf("写库失败时应停在 running：%+v", tk)
	}
	restore()
	t2 = e.waitDone(t2.ID)
	if t2.Attempts != 2 {
		t.Errorf("回收后再执行一次：%+v", t2)
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM file_logs WHERE action = 'export'`); n != 6 {
		t.Errorf("export 日志 %d 条（第 1 次的事务回滚了，只有第 2 次的 6 条）", n)
	}

	// 第一个执行器停下（释放锁）之后，第二个能拿到。
	e.stop()
	if err := other.Start(context.Background()); err != nil || !other.Active() {
		t.Fatalf("释放锁之后应能启动：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	other.Stop(ctx)
}

// TestExportMultipart：导出物真的走分片上传（测试把每片调到 S3 允许的最小 5 MiB，导出约 11 MB）：正常完成时下载的
// zip 完整；执行中取消时中止分片上传，之后既没有对象，也没有没完成的分片上传。
func TestExportMultipart(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "EXP2", withProxy, func(o *exportOpts) { o.partSize = 5 << 20 })
	var ids []uuid.UUID
	sha := map[uuid.UUID]string{}
	samples := []string{"jpeg-rgb.jpg", "jpeg-gray.jpg", "tiff-lzw-rgb.tif", "tiff-none-gray8.tif"}
	for i := range 16 {
		name := samples[i%len(samples)]
		data := unique(sampleBytes(t, name), "EXP2-"+strconv.Itoa(i))
		name = strconv.Itoa(i) + "-" + name
		f := Decode[ingestResponse](e.scan.Upload(e.box.ID, name, data, nil).Expect(http.StatusCreated))
		sum := sha256.Sum256(data)
		sha[f.FileID] = hex.EncodeToString(sum[:])
		ids = append(ids, f.FileID)
	}
	e.assign(e.rv1ID, ids...)
	for _, id := range ids {
		approve(e.rv1, id).Expect(http.StatusOK)
	}
	it := e.lockedItem(e.org.ID, "大件", ids...)
	body := map[string]any{"scope_type": "item", "scope_ids": []uuid.UUID{it.ID}, "export_format": "images"}
	e.start()

	// 1. 正常完成：分了几片，下载的 zip 里每页的 sha256 都对。
	t1 := e.waitDone(e.createTask(body).ID)
	if n := e.proxy.parts.Load(); n < 3 || *t1.OutputSize < 10<<20 {
		t.Fatalf("应分片上传（每片 5 MiB），实际 %d 片、%d 字节", n, *t1.OutputSize)
	}
	dl := Decode[downloadInfo](e.mgr.Get("/api/export-tasks/" + t1.ID.String() + "/download").Expect(http.StatusOK))
	st, _, data := fetch(t, dl.URL)
	if st != http.StatusOK || int64(len(data)) != dl.Size || dl.Size != *t1.OutputSize {
		t.Fatalf("下载：%d，%d 字节，记的是 %d", st, len(data), dl.Size)
	}
	entries, _ := zipEntries(t, data)
	for _, r := range e.files(t1.ID, 500) {
		ext := map[string]string{"image/jpeg": "jpg", "image/tiff": "tif"}[r.MimeType]
		sum := sha256.Sum256(entries[fmt.Sprintf("%s/%04d.%s", str(r.ArchiveNo), r.PageNo, ext)])
		if hex.EncodeToString(sum[:]) != sha[r.FileID] {
			t.Errorf("第 %d 页的 sha256 不对", r.PageNo)
		}
	}

	// 2. 执行中取消：分片上传已经开始（S3 里能看到没完成的分片上传）→ 取消 → 中止，不留对象、不留分片上传。
	pending := func() []string {
		t.Helper()
		keys, err := S3Client(t).PendingUploads(context.Background(), e.prefix+"exports/")
		if err != nil {
			t.Fatal(err)
		}
		return keys
	}
	e.proxy.delay.Store(int64(200 * time.Millisecond))
	t2 := e.createTask(body)
	deadline := time.Now().Add(15 * time.Second)
	for len(pending()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("15 秒内没看到进行中的分片上传")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if keys := pending(); len(keys) != 1 || !strings.Contains(keys[0], t2.ID.String()) {
		t.Fatalf("进行中的分片上传：%v", keys)
	}
	e.mgr.Post("/api/export-tasks/"+t2.ID.String()+"/cancel", nil).Expect(http.StatusOK)
	e.waitTask(t2.ID, 10*time.Second, "清理完", func(t exportTask) bool { return !t.CleaningUp })
	if keys := pending(); len(keys) != 0 {
		t.Errorf("取消后不应留下没完成的分片上传：%v", keys)
	}
	for _, k := range e.exportObjects() {
		if strings.Contains(k, t2.ID.String()) {
			t.Errorf("取消后不应留下导出物：%s", k)
		}
	}
}
