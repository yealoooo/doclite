package apitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/project"
)

// 存储迁移（M7-2）：端到端（同一个桶换前缀、换到第二个桶；共用对象只复制一次；看图地址和导出照常；原存储能停用）、
// 参数和状态检查、迁移进行中对存储配置的限制、迁移中上传（含查重命中正在迁的对象）、重扫和复制、核对不一致不改、
// 取消（排队中、执行中）、优雅退出和被杀后从断点接着做、权限表。

type copiedEntry struct {
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileID       uuid.UUID `json:"file_id"`
	SnapshotID   uuid.UUID `json:"snapshot_id"`
}

func (e *exportEnv) copyEntry(folderFileID uuid.UUID) copiedEntry {
	e.t.Helper()
	return Decode[copiedEntry](e.admin.Post("/api/folder-files/"+folderFileID.String()+"/copy", map[string]any{}).
		Expect(http.StatusCreated))
}

// checkImages 用 files/batch 给的地址取原图和封面：原图的 sha256 和快照记的一致，封面和 want 一致（nil 表示不查）。
func (e *exportEnv) checkImages(fileIDs []uuid.UUID, wantCover []byte) {
	e.t.Helper()
	var br batchResponse
	e.admin.Post("/api/files/batch", map[string]any{"file_ids": fileIDs}).Expect(http.StatusOK).JSON(&br)
	if len(br.Items) != len(fileIDs) {
		e.t.Fatalf("batch：%d 个，缺 %v", len(br.Items), br.Missing)
	}
	for _, it := range br.Items {
		if it.OriginalURL == nil {
			e.t.Fatalf("%s 没有原图地址", it.FileID)
		}
		st, data := httpGet(e.t, *it.OriginalURL)
		if st != http.StatusOK || sha256hex(data) != it.ContentSHA256 {
			e.t.Fatalf("%s 的原图取不到或不对：%d", it.FileID, st)
		}
		if wantCover != nil && it.CoverURL != nil {
			st, data := httpGet(e.t, *it.CoverURL)
			if st != http.StatusOK || !bytes.Equal(data, wantCover) {
				e.t.Fatalf("%s 的封面取不到或不对：%d", it.FileID, st)
			}
		}
	}
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestStorageMigrate：A（测试桶、前缀 pA）→ D（同一个桶、前缀 pD）→ B（第二个桶）。共用对象（复制）、重扫的两个版本、
// 带封面和不带封面的都迁过去；对象只复制一次；每个快照一条审计；源对象不删；看图地址、导出照常；迁空的存储能停用。
// 另外：参数和状态检查（400、404、409）、迁移进行中对源和目标的修改 409。
func TestStorageMigrate(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "SMG", withMigrateBatch(3, 2))
	a, pA := e.profile, e.prefix
	bucket := S3Env().Bucket

	ups := make([]ingestResponse, 4)
	for i := range ups {
		ups[i] = e.uploadCovered(e.box.ID, fmt.Sprintf("p%d.tif", i))
	}
	plain := e.upload(e.box.ID, "plain.tif")
	cp := e.copyEntry(ups[0].FolderFileID) // 和 ups[0] 共用原图和封面
	e.assign(e.rv1ID, ups[1].FileID)
	reject(e.rv1, ups[1].FileID).Expect(http.StatusOK)
	rescan(e.scan, ups[1].FileID, "p1-重扫.tif", unique(sampleBytes(t, "tiff-g4-1bit.tif"), "rescan-"+uuid.NewString())).
		Expect(http.StatusCreated)
	pages := e.approvedPages(2, "exp")
	item := e.lockedItem(e.org.ID, "迁移后导出", pages...)
	before := e.snapshots()
	if len(before) != 9 {
		t.Fatalf("迁移前的快照：%d 个", len(before))
	}
	var cpKey string
	for _, s := range before {
		if s.FileID == cp.FileID {
			cpKey = s.Key
		}
	}
	if cpKey == "" || !strings.HasPrefix(cpKey, pA) {
		t.Fatalf("复制的快照 key：%q", cpKey)
	}

	pC, pD := S3Prefix(t), S3Prefix(t)
	c := e.addStorage("新的默认", "", bucket, pC)
	d := e.addStorage("同桶换前缀", "", bucket, pD)
	b2name, b2, pB := bucket2(t)
	b := e.addStorage("第二个桶", "", b2name, pB)

	// 参数和状态检查。
	e.admin.Get(storagePath(a, "/migrate")).ExpectError(http.StatusNotFound, "not_found")
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": d}).ExpectError(http.StatusConflict, "storage_is_default")
	e.setDefault(c)
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{}).ExpectError(http.StatusBadRequest, "invalid_input")
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": "x"}).ExpectError(http.StatusBadRequest, "invalid_input")
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": a}).ExpectError(http.StatusBadRequest, "invalid_input")
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": uuid.New()}).ExpectError(http.StatusNotFound, "not_found")
	e.admin.Post(storagePath(uuid.New(), "/migrate"), map[string]any{"target_profile_id": d}).ExpectError(http.StatusNotFound, "not_found")
	same := e.addStorage("同一个位置", "", bucket, pA)
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": same}).ExpectError(http.StatusBadRequest, "invalid_input")
	e.admin.Patch(storagePath(b, ""), map[string]any{"status": "readonly"}).Expect(http.StatusOK)
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": b}).ExpectError(http.StatusConflict, "storage_not_active")
	e.admin.Patch(storagePath(b, ""), map[string]any{"status": "active"}).Expect(http.StatusOK)
	e.admin.Post(storagePath(d, "/migrate"), map[string]any{"target_profile_id": b}).ExpectError(http.StatusConflict, "invalid_state") // 没有文件

	maxAudit := QueryValue[int64](e.Env, `SELECT max(id) FROM audit_logs`)
	m := e.startMigration(a, d)
	if mark := QueryValue[int64](e.Env, `SELECT (payload->'state'->>'audit_mark')::bigint FROM background_jobs WHERE id = $1`, m.ID); mark != maxAudit {
		t.Errorf("建迁移时的 audit_mark：%d（当时最大的审计 id %d）", mark, maxAudit)
	}
	if m.Status != "queued" || m.Total != 9 || m.Migrated != 0 || m.SourceProfileID != a || m.TargetProfileID != d ||
		m.TargetProfileName != "同桶换前缀" || m.MaxAttempts != 5 || m.CreatedByName == nil || m.Remaining == nil || *m.Remaining != 9 {
		t.Fatalf("建迁移：%+v", m)
	}
	// 进行中（排队）：再建迁移（同一个源、把正在迁入的当源、把正在迁出的当目标——整个系统只有一个迁移）、源设默认、目标改
	// 只读或停用、删源删目标都 409；源改只读可以。
	e.admin.Post(storagePath(a, "/migrate"), map[string]any{"target_profile_id": b}).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Post(storagePath(d, "/migrate"), map[string]any{"target_profile_id": b}).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Post(storagePath(b, "/migrate"), map[string]any{"target_profile_id": a}).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Post(storagePath(a, "/set-default"), nil).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Patch(storagePath(d, ""), map[string]any{"status": "readonly"}).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Delete(storagePath(d, "")).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Delete(storagePath(a, "")).ExpectError(http.StatusConflict, "invalid_state")
	e.admin.Patch(storagePath(a, ""), map[string]any{"status": "readonly"}).Expect(http.StatusOK)
	var list []profileWithMigration
	e.admin.Get("/api/system/storage-profiles").Expect(http.StatusOK).JSON(&list)
	for _, p := range list {
		switch p.ID {
		case a:
			if p.Migration == nil || p.Migration.ID != m.ID || p.IncomingMigration != nil {
				t.Errorf("列表里源存储的迁移：%+v %+v", p.Migration, p.IncomingMigration)
			}
		case d:
			if p.Migration != nil || p.IncomingMigration == nil || p.IncomingMigration.ID != m.ID {
				t.Errorf("列表里目标存储的迁移：%+v %+v", p.Migration, p.IncomingMigration)
			}
		}
	}

	e.start()
	done := e.waitMigrationDone(a)
	if done.Migrated != 9 || done.Failed != 0 || done.Total != 9 || *done.Remaining != 0 || len(done.Failures) != 0 ||
		done.FinishedAt == nil {
		t.Fatalf("A → D 完成：%+v", done)
	}
	after := e.snapshots()
	byID := map[uuid.UUID]snapRow{}
	for _, s := range before {
		byID[s.ID] = s
	}
	keys, covers := map[string]bool{}, map[string]bool{}
	for _, s := range after {
		old := byID[s.ID]
		if s.Profile != d || s.Key != pD+tail3(old.Key) || !bytes.Equal(s.SHA, old.SHA) {
			t.Fatalf("快照 %s 迁后：%s %s（原来 %s）", s.ID, s.Profile, s.Key, old.Key)
		}
		if (old.Cover == nil) != (s.Cover == nil) || (s.Cover != nil && *s.Cover != pD+tail3(*old.Cover)) {
			t.Fatalf("快照 %s 的封面：%v → %v", s.ID, str(old.Cover), str(s.Cover))
		}
		if got := objectSHA(t, S3Client(t), s.Key); !bytes.Equal(got, s.SHA) {
			t.Fatalf("目标里的原图 %s 不对", s.Key)
		}
		if objectSHA(t, S3Client(t), old.Key) == nil {
			t.Errorf("源存储里的对象不应删除：%s", old.Key)
		}
		keys[s.Key] = true
		if s.Cover != nil {
			covers[*s.Cover] = true
		}
		if s.FileID == cp.FileID && s.Key != pD+tail3(cpKey) {
			t.Errorf("复制的快照应和源文件共用对象：%s", s.Key)
		}
	}
	// 9 个快照、8 个对象（复制共用一个）、4 个封面（复制共用一个；重扫件、plain、导出页没有封面）。
	if objs := listAll(t, S3Client(t), pD); len(objs) != len(keys)+len(covers) || len(keys) != 8 || len(covers) != 4 {
		t.Errorf("目标里的对象 %d 个（原图 %d、封面 %d）：%v", len(objs), len(keys), len(covers), objs)
	}
	if n := e.auditCount("file_snapshot", "storage_migrate"); n != 9 {
		t.Errorf("storage_migrate 审计 %d 条", n)
	}
	detail := QueryValue[map[string]any](e.Env, `SELECT detail FROM audit_logs WHERE target_type = 'file_snapshot' AND target_id = $1`,
		ups[2].SnapshotID)
	if detail["job_id"] != m.ID.String() || detail["before"].(map[string]any)["storage_profile_id"] != a.String() ||
		detail["after"].(map[string]any)["storage_key"] != pD+tail3(byID[ups[2].SnapshotID].Key) {
		t.Errorf("审计的 detail：%v", detail)
	}
	if e.auditCount("storage_profile", "migrate_start") != 1 || e.auditCount("storage_profile", "migrate_end") != 1 {
		t.Errorf("开始、结束的审计")
	}
	coverBytes := coverFor(t, 2480, 3508)
	allFiles := []uuid.UUID{ups[0].FileID, ups[1].FileID, ups[2].FileID, ups[3].FileID, plain.FileID, cp.FileID, pages[0], pages[1]}
	e.checkImages(allFiles, coverBytes)

	// D → B：换到第二个桶。
	m2 := e.startMigration(d, b)
	done = e.waitMigrationDone(d)
	if done.ID != m2.ID || done.Migrated != 9 || *done.Remaining != 0 {
		t.Fatalf("D → B 完成：%+v", done)
	}
	for _, s := range e.snapshots() {
		if s.Profile != b || !strings.HasPrefix(s.Key, pB) || objectSHA(t, b2, s.Key) == nil {
			t.Fatalf("快照 %s 应在第二个桶：%s %s", s.ID, s.Profile, s.Key)
		}
	}
	if objs := listAll(t, b2, pB); len(objs) != 12 {
		t.Errorf("第二个桶里的对象：%d 个", len(objs))
	}

	// 迁空的存储能停用；停用后照常看图、导出（导出物写在默认存储 C）。
	e.admin.Patch(storagePath(a, ""), map[string]any{"status": "disabled"}).Expect(http.StatusOK)
	e.admin.Patch(storagePath(d, ""), map[string]any{"status": "disabled"}).Expect(http.StatusOK)
	e.checkImages(allFiles, coverBytes)
	task := e.createTask(map[string]any{"scope_type": "item", "scope_ids": []uuid.UUID{item.ID}, "export_format": "images"})
	e.waitDone(task.ID)
	dl := Decode[downloadInfo](e.mgr.Get("/api/export-tasks/" + task.ID.String() + "/download").Expect(http.StatusOK))
	st, _, data := fetch(t, dl.URL)
	if st != http.StatusOK {
		t.Fatalf("下载：%d", st)
	}
	entries, _ := zipEntries(t, data)
	want := map[string]bool{}
	for _, s := range e.snapshots() {
		if s.FileID == pages[0] || s.FileID == pages[1] {
			want[hex.EncodeToString(s.SHA)] = true
		}
	}
	got := 0
	for name, b := range entries {
		if strings.HasSuffix(name, ".tif") {
			got++
			if !want[sha256hex(b)] {
				t.Errorf("导出的 %s 内容不对", name)
			}
		}
	}
	if got != 2 {
		t.Errorf("导出了 %d 页", got)
	}
}

// TestStorageMigrateWhileWorking：迁移执行中照常上传（新文件进默认存储）、查重命中正在迁的对象（别的文件夹 409、断网重试
// 200）、重扫、反复复制正在迁的文件（复用对象）。结束时源存储里一个快照都不剩：复制出来的要么一起迁了，要么锁内重读到的
// 已经是新位置。
func TestStorageMigrateWhileWorking(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "SMW", withProxy, withMigrateBatch(4, 2))
	a := e.profile
	data0 := unique(sampleBytes(t, "tiff-g4-1bit.tif"), "keep-"+uuid.NewString())
	keep := Decode[ingestResponse](e.scan.Upload(e.box.ID, "keep.tif", data0, coverFor(t, 2480, 3508)).Expect(http.StatusCreated))
	files := []ingestResponse{keep}
	for i := range 9 {
		files = append(files, e.uploadCovered(e.box.ID, fmt.Sprintf("w%d.tif", i)))
	}
	rej := e.upload(e.box.ID, "rej.tif")
	e.assign(e.rv1ID, rej.FileID)
	reject(e.rv1, rej.FileID).Expect(http.StatusOK)
	// 另一个有文件、不是默认的存储 X（临时设它默认传一个文件）：A 迁移期间它不能迁（整个系统只有一个迁移）。
	x := e.addStorage("另一个", "", S3Env().Bucket, S3Prefix(t))
	e.setDefault(x)
	inX := e.upload(e.box.ID, "x.tif")
	c := e.addStorage("新的默认", "", S3Env().Bucket, S3Prefix(t))
	e.setDefault(c)
	box2 := mkdir(e.admin, e.p.RootFolderID, "盒 2")

	e.proxy.delay.Store(int64(120 * time.Millisecond)) // 读源存储每次慢 120 毫秒：迁移持续一两秒
	e.proxy.failGets.Store(2)                          // 开头两次读源存储失败（403）：对象就地重试，不停下整次执行
	e.startMigration(a, c)
	e.start()
	e.waitMigration(a, 10*time.Second, "开始执行", func(m migration) bool { return m.Status == "running" })
	e.admin.Post(storagePath(x, "/migrate"), map[string]any{"target_profile_id": c}).ExpectError(http.StatusConflict, "invalid_state")

	var wg sync.WaitGroup
	var mu sync.Mutex
	var copies []copiedEntry
	for _, f := range files {
		wg.Go(func() {
			for range 3 {
				cp := e.copyEntry(f.FolderFileID)
				mu.Lock()
				copies = append(copies, cp)
				mu.Unlock()
			}
		})
	}
	// 查重命中正在迁的对象：别的文件夹 409 duplicate_content；同一个文件夹、同名同内容是断网重试，返回已有文件。
	e.scan.Upload(box2.ID, "dup.tif", data0, nil).ExpectError(http.StatusConflict, "duplicate_content")
	again := Decode[ingestResponse](e.scan.Upload(e.box.ID, "keep.tif", data0, nil).Expect(http.StatusOK))
	if again.FileID != keep.FileID {
		t.Errorf("断网重试应返回已有文件：%s", again.FileID)
	}
	nu := e.uploadCovered(e.box.ID, "new.tif")
	rs := Decode[rescanResponse](rescan(e.scan, rej.FileID, "rej-2.tif", unique(sampleBytes(t, "tiff-g4-1bit.tif"), "r2-"+uuid.NewString())).
		Expect(http.StatusCreated))
	if st := e.migration(a); st.Status != "running" {
		t.Logf("上传、重扫时迁移已经是 %s（机器太快，并发的覆盖少了）", st.Status)
	}
	wg.Wait()
	done := e.waitMigrationDone(a)
	if n := e.snapshotsIn(a); n != 0 || *done.Remaining != 0 || done.Failed != 0 || done.Attempts != 1 {
		t.Fatalf("源存储里还剩 %d 个快照（读失败应在对象里重试，不重新执行）：%+v", n, done)
	}
	// A 迁完了，X 可以迁（迁到同一个目标）。
	e.startMigration(x, c)
	if dx := e.waitMigrationDone(x); dx.Migrated != 1 || e.snapshotsIn(x) != 0 {
		t.Fatalf("X 迁完：%+v", dx)
	}
	cur := map[uuid.UUID]snapRow{}
	for _, s := range e.snapshots() {
		cur[s.FileID] = s // 按版本号排序，最后一个是当前版本
		if s.Profile != c {
			t.Errorf("快照 %s 不在目标存储：%s", s.ID, s.Profile)
		}
	}
	if cur[nu.FileID].Profile != c || cur[rs.FileID].VersionNo != 2 || cur[rs.FileID].Profile != c || cur[inX.FileID].Profile != c {
		t.Errorf("迁移中新上传、重扫的应在默认存储")
	}
	srcKey := map[uuid.UUID]string{}
	for _, f := range files {
		srcKey[f.FolderFileID] = cur[f.FileID].Key
	}
	if len(copies) != 30 {
		t.Fatalf("复制了 %d 个", len(copies))
	}
	for _, cp := range copies {
		if s := cur[cp.FileID]; s.Profile != c || objectSHA(t, S3Client(t), s.Key) == nil {
			t.Errorf("复制出来的 %s：%s %s", cp.FileID, s.Profile, s.Key)
		}
	}
	keysOf := map[string]int{}
	for _, s := range e.snapshots() {
		keysOf[s.Key]++
	}
	for _, f := range files {
		if keysOf[cur[f.FileID].Key] != 4 { // 自己和 3 个复制
			t.Errorf("%s 的对象应被 4 个快照共用：%d", f.FileName, keysOf[cur[f.FileID].Key])
		}
	}
	if n := e.auditCount("file_snapshot", "storage_migrate"); n != done.Migrated+1 {
		t.Errorf("审计 %d 条，迁了 %d+1 个", n, done.Migrated)
	}
}

// TestStorageMigrateMismatch：源对象内容不对（sha256 和快照记的不一样，另有 21 个复制共用它：清单只列 20 个快照、另记
// 总数）、源对象没了、封面没了、共用一个对象的两个快照记的大小不一样：这些快照不改、进失败清单（带项目、文件名、位置），
// 别的照常迁；任务照样完成；列表里的摘要不带失败清单；源存储因为还有快照不能停用。
func TestStorageMigrateMismatch(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "SMM", withMigrateBatch(2, 2))
	a := e.profile
	good := e.uploadCovered(e.box.ID, "good.tif")
	bad := e.uploadCovered(e.box.ID, "bad.tif")
	gone := e.uploadCovered(e.box.ID, "gone.tif")
	nocover := e.uploadCovered(e.box.ID, "nocover.tif")
	for range 21 {
		e.copyEntry(bad.FolderFileID)
	}
	inc := e.uploadCovered(e.box.ID, "inc.tif")
	incCopy := e.copyEntry(inc.FolderFileID)
	// 造一个"共用对象的快照记录不一致"（快照只追加，测试库里直接改）。
	e.Exec(`UPDATE file_snapshots SET file_size = file_size + 1 WHERE id = $1`, incCopy.SnapshotID)
	snaps := map[uuid.UUID]snapRow{}
	for _, s := range e.snapshots() {
		snaps[s.FileID] = s
	}
	ctx := context.Background()
	// bad：同样大小，改一个字节。
	rc, err := S3Client(t).Get(ctx, snaps[bad.FileID].Key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	raw[len(raw)-1] ^= 0xFF
	PutObject(t, snaps[bad.FileID].Key, "image/tiff", raw)
	if err := S3Client(t).Delete(ctx, snaps[gone.FileID].Key); err != nil {
		t.Fatal(err)
	}
	if err := S3Client(t).Delete(ctx, *snaps[nocover.FileID].Cover); err != nil {
		t.Fatal(err)
	}
	c := e.addStorage("目标", "", S3Env().Bucket, S3Prefix(t))
	e.setDefault(c)
	e.startMigration(a, c)
	e.start()
	done := e.waitMigrationDone(a)
	if done.Migrated != 1 || done.Failed != 26 || done.FailedObjects != 4 || done.Total != 27 || *done.Remaining != 26 ||
		len(done.Failures) != 4 || done.FailuresTruncated {
		t.Fatalf("完成：%+v", done)
	}
	var list []profileWithMigration
	e.admin.Get("/api/system/storage-profiles").Expect(http.StatusOK).JSON(&list)
	for _, p := range list {
		if p.ID == a && (p.Migration == nil || p.Migration.Failed != 26 || p.Migration.Failures != nil) {
			t.Errorf("列表里的摘要不带失败清单：%+v", p.Migration)
		}
	}
	byCode := map[string]migrationFailure{}
	for _, f := range done.Failures {
		byCode[f.ReasonCode] = f
		for _, s := range f.Snapshots {
			if s.ProjectCode == nil || *s.ProjectCode != "SMM" || s.FileName == nil || s.FolderPath == nil ||
				*s.FolderPath != "SMM 项目/盒 1" {
				t.Errorf("失败快照的信息：%+v", s)
			}
		}
	}
	if f := byCode["sha256_mismatch"]; len(f.Snapshots) != 20 || f.SnapshotCount != 22 || f.StorageKey != snaps[bad.FileID].Key {
		t.Errorf("sha256 不对：%d 个，共 %d", len(f.Snapshots), f.SnapshotCount)
	}
	if f := byCode["inconsistent"]; len(f.Snapshots) != 2 || f.SnapshotCount != 2 || f.StorageKey != snaps[inc.FileID].Key {
		t.Errorf("记录不一致：%+v", f)
	}
	if f := byCode["source_missing"]; len(f.Snapshots) != 1 || f.Snapshots[0].FileID != gone.FileID {
		t.Errorf("源对象没了：%+v", f)
	}
	if f := byCode["cover_missing"]; len(f.Snapshots) != 1 || f.Snapshots[0].FileID != nocover.FileID {
		t.Errorf("封面没了：%+v", f)
	}
	for _, s := range e.snapshots() {
		old := snaps[s.FileID]
		switch s.FileID {
		case good.FileID:
			if s.Profile != c {
				t.Errorf("good 应迁走")
			}
		default: // bad 和它的 21 个复制、gone、nocover、inc 和它的复制
			if s.Profile != a || s.Key != old.Key || str(s.Cover) != str(old.Cover) {
				t.Errorf("核对不一致的 %s 不应改：%s %s", s.FileID, s.Profile, s.Key)
			}
		}
	}
	if n := e.auditCount("file_snapshot", "storage_migrate"); n != 1 {
		t.Errorf("storage_migrate 审计 %d 条", n)
	}
	end := QueryValue[map[string]any](e.Env, `SELECT detail FROM audit_logs WHERE action = 'migrate_end'`)
	if end["result"] != "done" || end["failed"] != float64(26) || end["remaining"] != float64(26) {
		t.Errorf("结束的审计：%v", end)
	}
	e.admin.Patch(storagePath(a, ""), map[string]any{"status": "disabled"}).ExpectError(http.StatusConflict, "storage_in_use")
}

// TestStorageMigrateCancelResume：排队中取消（直接 canceled）；执行中取消（取消接口返回后不再改快照，写完断点再 canceled）；
// 再迁一次：优雅退出（放回队列、不算一次尝试、断点留着）→ 模拟被杀（断点退回到最开始、job 停在 running）→ 重启后回收、
// 接着做完，迁过的按审计补数，不重复迁、不重复记。已结束的再取消 409。
func TestStorageMigrateCancelResume(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "SMC", withProxy, withMigrateBatch(3, 1))
	a := e.profile
	const total = 30
	for i := range total {
		e.upload(e.box.ID, fmt.Sprintf("c%d.tif", i))
	}
	c := e.addStorage("目标", "", S3Env().Bucket, S3Prefix(t))
	e.setDefault(c)

	// 排队中取消。
	m1 := e.startMigration(a, c)
	got := Decode[migration](e.admin.Post(storagePath(a, "/migrate/cancel"), nil).Expect(http.StatusOK))
	if got.ID != m1.ID || got.Status != "canceled" || got.Migrated != 0 || *got.Remaining != total {
		t.Fatalf("排队中取消：%+v", got)
	}
	e.admin.Post(storagePath(a, "/migrate/cancel"), nil).ExpectError(http.StatusConflict, "invalid_state")

	// 执行中取消（读源存储每次慢 150 毫秒、一个 worker：每秒迁六七个，断点约每秒一次）。
	e.proxy.delay.Store(int64(150 * time.Millisecond))
	m2 := e.startMigration(a, c)
	e.start()
	e.waitMigration(a, 10*time.Second, "写下断点", func(m migration) bool { return m.Migrated >= 1 })
	got = Decode[migration](e.admin.Post(storagePath(a, "/migrate/cancel"), nil).Expect(http.StatusOK))
	moved := e.snapshotsIn(c)
	if got.ID != m2.ID || (got.Status != "canceled" && !(got.Status == "running" && got.CancelRequested)) {
		t.Fatalf("执行中取消：%+v", got)
	}
	fin := e.waitMigration(a, 10*time.Second, "取消", func(m migration) bool { return m.Status == "canceled" })
	if n := e.snapshotsIn(c); n != moved || fin.Migrated != moved || *fin.Remaining != total-moved || moved > total/2 {
		t.Fatalf("取消后：目标 %d（取消时 %d），%+v", n, moved, fin)
	}
	if e.auditCount("storage_profile", "migrate_cancel") != 2 || e.auditCount("storage_profile", "migrate_end") != 2 {
		t.Errorf("取消、结束的审计")
	}
	e.admin.Post(storagePath(a, "/migrate/cancel"), nil).ExpectError(http.StatusConflict, "invalid_state")

	// 再迁：优雅退出时放回队列，断点留着。
	m3 := e.startMigration(a, c)
	if m3.Total != total-moved {
		t.Fatalf("再迁的总数：%+v", m3)
	}
	e.waitMigration(a, 10*time.Second, "写下断点", func(m migration) bool { return m.Migrated >= 1 })
	e.stop()
	q := e.migration(a)
	if q.Status != "queued" || q.Attempts != 0 || q.Migrated < 1 || q.Migrated >= total-moved {
		t.Fatalf("优雅退出后：%+v", q)
	}
	// 模拟被杀：断点退回到最开始（之后迁过的只记在审计里），job 停在 running。
	e.Exec(`UPDATE background_jobs SET status = 'running', attempts = 1, runner_id = 'killed:1', heartbeat_at = now(),
  payload = jsonb_set(payload, '{state}', '{"cursor": "", "total": 0, "migrated": 0, "failed": 0, "failed_objects": 0,
    "audit_mark": 0, "failures": []}'::jsonb)
WHERE id = $1`, m3.ID)
	e.proxy.delay.Store(0)
	e.start()
	done := e.waitMigrationDone(a)
	if done.ID != m3.ID || done.Migrated != total-moved || done.Total != total-moved || done.Attempts != 2 || *done.Remaining != 0 {
		t.Fatalf("重启后完成：%+v", done)
	}
	if n := e.auditCount("file_snapshot", "storage_migrate"); n != total {
		t.Errorf("storage_migrate 审计 %d 条（应每个快照一条）", n)
	}
	if n := QueryValue[int](e.Env, `SELECT count(DISTINCT target_id) FROM audit_logs WHERE action = 'storage_migrate'`); n != total {
		t.Errorf("迁过的快照 %d 个", n)
	}
	e.admin.Post(storagePath(a, "/migrate/cancel"), nil).ExpectError(http.StatusConflict, "invalid_state")
	// 源存储迁空，可以停用。
	e.admin.Patch(storagePath(a, ""), map[string]any{"status": "disabled"}).Expect(http.StatusOK)
}

// TestPermsStorageMigrate：迁移的接口要 system.storage（和存储配置一样）。
func TestPermsStorageMigrate(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                                            admin  system.storage  system.projects  manager  none
	POST   /api/system/storage-profiles/{id}/migrate              Y          Y              -            -       -
	GET    /api/system/storage-profiles/{id}/migrate              Y          Y              -            -       -
	POST   /api/system/storage-profiles/{id}/migrate/cancel       Y          Y              -            -       -
	`)
	admin := env.Admin()
	admin.Get(storagePath(uuid.New(), "/migrate")).ExpectError(http.StatusNotFound, "not_found")
	admin.Post(storagePath(uuid.New(), "/migrate/cancel"), nil).ExpectError(http.StatusNotFound, "not_found")
	admin.Get("/api/system/storage-profiles/x/migrate").ExpectError(http.StatusBadRequest, "invalid_input")
}

// TestStorageMigrateCopyRace：复制和迁移改位置抢同一把内容锁（项目, sha256），两种先后都不漏。测试先拿着这把锁，让迁移的
// 改位置和复制都排在后面：
//   - 迁移先排上：迁移改完位置后复制才拿到锁，锁内重读源快照，新快照指向目标存储；
//   - 复制先排上：复制插入的新快照（还指向源存储）提交后，迁移的 UPDATE 一起改掉。
//
// 两种情况结束后源存储里都没有快照。复制不在锁内重读的话，第一种会留下一个指向源存储的新快照。
func TestStorageMigrateCopyRace(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "SMR", withMigrateBatch(1, 1))
	a := e.profile
	first := e.uploadCovered(e.box.ID, "first.tif")   // 迁移先排上
	second := e.uploadCovered(e.box.ID, "second.tif") // 复制先排上
	c := e.addStorage("目标", "", S3Env().Bucket, S3Prefix(t))
	e.setDefault(c)
	ctx := context.Background()
	waiters := func() int {
		return QueryValue[int](e.Env, `SELECT count(*) FROM pg_locks
WHERE locktype = 'advisory' AND classid = 6003 AND NOT granted
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`)
	}
	waitWaiters := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for waiters() < n {
			if time.Now().After(deadline) {
				t.Fatalf("等内容锁的事务没有到 %d 个", n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	hold := func(fileID uuid.UUID) pgx.Tx {
		t.Helper()
		sha := QueryValue[[]byte](e.Env, `SELECT content_sha256 FROM file_snapshots WHERE file_id = $1`, fileID)
		tx, err := e.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) }) // 中途失败时也放开（否则连接池关不掉）
		if err := project.LockContent(ctx, tx, e.p.ID, sha); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	// 复制到各自的文件夹（复制要锁目标文件夹的行，同一个文件夹的两个复制会排在文件夹行上，而不是内容锁上）。
	copyAsync := func(ffID uuid.UUID, folder string) chan *Response {
		dest := mkdir(e.admin, e.p.RootFolderID, folder)
		ch := make(chan *Response, 1)
		go func() {
			ch <- e.admin.Do("POST", "/api/folder-files/"+ffID.String()+"/copy", map[string]any{"folder_id": dest.ID})
		}()
		return ch
	}
	copied := func(ch chan *Response) copiedEntry {
		t.Helper()
		return Decode[copiedEntry]((<-ch).Expect(http.StatusCreated))
	}

	// 两个对象按 key 排序，迁移一个一个来（每批 1 个）：拿着两个的锁，先看排在前面的那个。
	keyOf := func(id uuid.UUID) string {
		return QueryValue[string](e.Env, `SELECT storage_key FROM file_snapshots WHERE file_id = $1`, id)
	}
	if keyOf(first.FileID) > keyOf(second.FileID) {
		first, second = second, first
	}
	tx1, tx2 := hold(first.FileID), hold(second.FileID)
	e.startMigration(a, c)
	e.start()
	// 迁移卡在 first 的锁上；复制 first 排在它后面；复制 second 排上 second 的锁（迁移还到不了那里）。
	waitWaiters(1)
	ch1 := copyAsync(first.FolderFileID, "复制 1")
	waitWaiters(2)
	ch2 := copyAsync(second.FolderFileID, "复制 2")
	waitWaiters(3)
	// 1. 放开 first：迁移先改位置，复制 first 在锁内重读到目标存储。
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	cp1 := copied(ch1)
	// 2. 迁移接着排到 second 的锁上，排在复制 second 后面；放开后复制先插入，迁移再一起改。
	waitWaiters(2)
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	cp2 := copied(ch2)
	done := e.waitMigrationDone(a)
	if n := e.snapshotsIn(a); n != 0 || done.Migrated != 3 || *done.Remaining != 0 { // first、second 和第 2 个复制
		t.Fatalf("源存储里还剩 %d 个快照：%+v", n, done)
	}
	for _, cp := range []copiedEntry{cp1, cp2} {
		if p := QueryValue[uuid.UUID](e.Env, `SELECT storage_profile_id FROM file_snapshots WHERE id = $1`, cp.SnapshotID); p != c {
			t.Errorf("复制出来的快照 %s 在 %s", cp.SnapshotID, p)
		}
	}
	// 第 1 个复制是在迁移之后插入的（没有 storage_migrate 审计），第 2 个是迁移一起改的。
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM audit_logs WHERE action = 'storage_migrate' AND target_id = $1`, cp1.SnapshotID); n != 0 {
		t.Errorf("第 1 个复制应直接指向目标存储：%d", n)
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM audit_logs WHERE action = 'storage_migrate' AND target_id = $1`, cp2.SnapshotID); n != 1 {
		t.Errorf("第 2 个复制应被迁移一起改：%d", n)
	}
}
