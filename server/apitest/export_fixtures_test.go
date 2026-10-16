package apitest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/app"
	"doclite/server/internal/config"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/jobs"
)

// 导出（M5-2）的测试支撑：一个能上传、能审核、能归类、能执行导出的项目。
//
//   - 执行器默认不启动（e.start() 再启动），心跳、重试退避、轮询都调短；
//   - withProxy：存储地址指向一个转发到 SeaweedFS 的代理，可以让读原图变慢（让导出"执行中"持续一会儿）或者失败
//     （测重试）。代理不改 Host 头，v4 签名照样对。

type exportEnv struct {
	*reviewEnv
	cat     *catalogEnv
	profile uuid.UUID
	prefix  string
	proxy   *s3Proxy
	org     catNode
	started bool
}

type exportOpts struct {
	proxy        bool
	retryBackoff time.Duration
	partSize     int64
	// app 是另外要改的 server 配置（存储迁移的测试调每批大小）。
	app func(*app.Options)
}

func withProxy(o *exportOpts) { o.proxy = true }

func newExportEnv(t *testing.T, code string, opts ...func(*exportOpts)) *exportEnv {
	t.Helper()
	var o exportOpts
	for _, f := range opts {
		f(&o)
	}
	if o.retryBackoff == 0 {
		o.retryBackoff = 200 * time.Millisecond
	}
	tmp := t.TempDir()
	env := NewEnv(t, func(a *app.Options) {
		a.Upload = file.Options{TmpDir: tmp}
		a.Jobs = jobs.Options{Concurrency: 2, Heartbeat: 300 * time.Millisecond, RetryBackoff: o.retryBackoff,
			PollInterval: 100 * time.Millisecond}
		a.ExportCleanupInterval = time.Hour
		a.ExportPartSize = o.partSize
		if o.app != nil {
			o.app(a)
		}
	})
	e := &exportEnv{}
	endpoint := S3Env().Endpoint
	if o.proxy {
		e.proxy = newS3Proxy(t)
		endpoint = e.proxy.URL
	}
	e.prefix = S3Prefix(t)
	c := S3Env()
	if err := env.App.EnsureInitialStorage(context.Background(), &config.InitialStorage{
		Endpoint: endpoint, Bucket: c.Bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey, PathPrefix: e.prefix,
	}); err != nil {
		t.Fatal(err)
	}
	e.profile = QueryValue[uuid.UUID](env, "SELECT id FROM storage_profiles WHERE is_default")

	re := &reviewEnv{Env: env, t: t, admin: env.Admin(), p: env.CreateProject(code)}
	re.mgrID = env.CreateUser("mgr", "manager")
	re.rv1ID = env.CreateUser("rv1", "reviewer")
	re.rv2ID = env.CreateUser("rv2", "reviewer")
	re.scanID = env.CreateUser("scan1", "scanner")
	env.GrantProject(re.p.ID, re.mgrID, re.rv1ID, re.rv2ID, re.scanID)
	re.mgr = env.Login("mgr", DefaultPassword)
	re.rv1 = env.Login("rv1", DefaultPassword)
	re.rv2 = env.Login("rv2", DefaultPassword)
	re.scan = env.Login("scan1", DefaultPassword)
	re.box = mkdir(re.admin, re.p.RootFolderID, "盒 1")
	e.reviewEnv = re
	e.cat = &catalogEnv{Env: env, t: t, pid: re.p.ID, rootID: re.p.CategoryRootID, folder: re.p.RootFolderID,
		adminID: env.UserID(AdminUsername), admin: re.admin}
	_, _, _, _, e.org = e.cat.mkChain()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		env.App.StopBackground(ctx)
	})
	return e
}

// start 启动执行器（先把 running 的放回队列）。
func (e *exportEnv) start() {
	e.t.Helper()
	if err := e.App.StartBackground(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.started = true
}

// stop 优雅停下执行器（执行中的放回队列）。
func (e *exportEnv) stop() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.App.StopBackground(ctx)
	e.started = false
}

// approvedPages 上传 n 页（G4 TIFF，内容唯一），分给 rv1 并通过，返回 file_id。
func (e *exportEnv) approvedPages(n int, name string) []uuid.UUID {
	e.t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = e.upload(e.box.ID, name+"-"+uuid.NewString()[:8]+".tif").FileID
	}
	e.assign(e.rv1ID, ids...)
	for _, id := range ids {
		approve(e.rv1, id).Expect(http.StatusOK)
	}
	return ids
}

// lockedItem 在 cat 下建一个带这些页的条目，标记完成并定稿。
func (e *exportEnv) lockedItem(cat uuid.UUID, title string, fileIDs ...uuid.UUID) itemView {
	e.t.Helper()
	it := e.cat.mkItem(cat, map[string]any{"title": title, "file_ids": fileIDs})
	e.cat.act(it.ID, "mark-done").Expect(http.StatusOK)
	e.cat.act(it.ID, "lock").Expect(http.StatusOK)
	return e.cat.item(it.ID)
}

func (e *exportEnv) url(path string) string { return "/api/projects/" + e.p.ID.String() + path }

// exportTask 是任务的响应。
type exportTask struct {
	ID              uuid.UUID   `json:"id"`
	ProjectID       uuid.UUID   `json:"project_id"`
	ScopeType       string      `json:"scope_type"`
	ScopeIDs        []uuid.UUID `json:"scope_ids"`
	ScopeCategories []struct {
		ID            uuid.UUID `json:"id"`
		CategoryNames []string  `json:"category_names"`
	} `json:"scope_categories"`
	ResubmitOf        *uuid.UUID `json:"resubmit_of"`
	ExportFormat      string     `json:"export_format"`
	OnlyApproved      bool       `json:"only_approved"`
	OnlyReady         bool       `json:"only_ready"`
	RequireLockedItem bool       `json:"require_locked_item"`
	Status            string     `json:"status"`
	CleaningUp        bool       `json:"cleaning_up"`
	ItemCount         int        `json:"item_count"`
	PageCount         int        `json:"page_count"`
	SkippedItemCount  int        `json:"skipped_item_count"`
	ProgressDone      int        `json:"progress_done"`
	ProgressTotal     int        `json:"progress_total"`
	Attempts          int        `json:"attempts"`
	MaxAttempts       int        `json:"max_attempts"`
	ErrorMessage      *string    `json:"error_message"`
	OutputSize        *int64     `json:"output_size"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Expired           bool       `json:"expired"`
	CreatedBy         uuid.UUID  `json:"created_by"`
	CreatedByName     *string    `json:"created_by_name"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

// createTask 由负责人建导出任务，要求 201。
func (e *exportEnv) createTask(body map[string]any) exportTask {
	e.t.Helper()
	return Decode[exportTask](e.mgr.Post(e.url("/export-tasks"), body).Expect(http.StatusCreated))
}

func (e *exportEnv) task(id uuid.UUID) exportTask {
	e.t.Helper()
	return Decode[exportTask](e.mgr.Get("/api/export-tasks/" + id.String()).Expect(http.StatusOK))
}

// waitTask 等任务满足 cond（每 30 毫秒看一次，最多 timeout）。
func (e *exportEnv) waitTask(id uuid.UUID, timeout time.Duration, what string, cond func(exportTask) bool) exportTask {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		t := e.task(id)
		if cond(t) {
			return t
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("任务 %s 在 %s 内没有%s：%+v（error_message %s）", id, timeout, what, t, str(t.ErrorMessage))
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func (e *exportEnv) waitDone(id uuid.UUID) exportTask {
	e.t.Helper()
	return e.waitTask(id, 30*time.Second, "完成", func(t exportTask) bool {
		if t.Status == "failed" {
			e.t.Fatalf("任务 %s 失败了：%s", id, str(t.ErrorMessage))
		}
		return t.Status == "done"
	})
}

// taskFile 是明细的一页。
type taskFile struct {
	Seq           int       `json:"seq"`
	ItemID        uuid.UUID `json:"item_id"`
	ItemNo        int       `json:"item_no"`
	ArchiveNo     *string   `json:"archive_no"`
	Title         string    `json:"title"`
	PageNo        int       `json:"page_no"`
	FileID        uuid.UUID `json:"file_id"`
	SnapshotID    uuid.UUID `json:"snapshot_id"`
	VersionNo     int       `json:"version_no"`
	MimeType      string    `json:"mime_type"`
	FileSize      int64     `json:"file_size"`
	ContentSHA256 string    `json:"content_sha256"`
	UploadName    *string   `json:"upload_name"`
	FileName      *string   `json:"file_name"`
}

// files 取全部明细（每页 limit 个，走完 cursor）。
func (e *exportEnv) files(id uuid.UUID, limit int) []taskFile {
	e.t.Helper()
	var out []taskFile
	cursor := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			ExportTaskID uuid.UUID  `json:"export_task_id"`
			ItemCount    int        `json:"item_count"`
			PageCount    int        `json:"page_count"`
			Items        []taskFile `json:"items"`
			NextCursor   *string    `json:"next_cursor"`
		}
		e.mgr.Get("/api/export-tasks/" + id.String() + "/files?" + q.Encode()).Expect(http.StatusOK).JSON(&page)
		out = append(out, page.Items...)
		if page.NextCursor == nil {
			return out
		}
		cursor = *page.NextCursor
	}
}

// download 是下载接口的响应。
type downloadInfo struct {
	URL          string     `json:"url"`
	FileName     string     `json:"file_name"`
	Size         int64      `json:"size"`
	ExpiresAt    *time.Time `json:"expires_at"`
	URLExpiresAt time.Time  `json:"url_expires_at"`
}

// fetch 用预签名地址下载（不带 Cookie），返回状态码、响应头和内容。
func fetch(t *testing.T, u string) (int, http.Header, []byte) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, data
}

// zipEntries 解开 zip：名字 → 内容（按 zip 里的顺序另给名字列表）。
func zipEntries(t *testing.T, data []byte) (map[string][]byte, []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("不是 zip：%v", err)
	}
	out := map[string][]byte{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
		names = append(names, f.Name)
	}
	return out, names
}

// manifestRows 解析清单.csv（去掉 BOM）。
func manifestRows(t *testing.T, data []byte) [][]string {
	t.Helper()
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("清单.csv 读不了：%v", err)
	}
	return rows
}

// s3Proxy 是转发到测试 S3 的代理：读对象（GET /<bucket>/<key>）可以变慢、可以失败。
type s3Proxy struct {
	URL string
	// delay 是每次读对象前等多久（纳秒）。
	delay atomic.Int64
	// failGets 是接下来要失败（403 AccessDenied）的读对象次数。
	failGets atomic.Int32
	// gets 是读对象的次数；parts 是上传分片（UploadPart）的次数。
	gets  atomic.Int64
	parts atomic.Int64
}

func newS3Proxy(t *testing.T) *s3Proxy {
	t.Helper()
	target, err := url.Parse(S3Env().Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	S3Client(t) // SeaweedFS 没启动时明确失败
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorLog = log.New(io.Discard, "", 0) // 取消时转发中断是预期的
	p := &s3Proxy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPut && r.URL.Query().Has("partNumber") {
			p.parts.Add(1)
		}
		if r.Method == http.MethodGet && len(segs) >= 2 {
			p.gets.Add(1)
			if p.takeFailure() {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>test proxy: simulated failure</Message></Error>`)
				return
			}
			if d := time.Duration(p.delay.Load()); d > 0 {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
					return
				}
			}
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	return p
}

// takeFailure：还有要失败的次数时减一并返回 true。
func (p *s3Proxy) takeFailure() bool {
	for {
		n := p.failGets.Load()
		if n <= 0 {
			return false
		}
		if p.failGets.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// exportObjects 是测试前缀下 exports/ 里的对象。
func (e *exportEnv) exportObjects() []string {
	e.t.Helper()
	keys, err := S3Client(e.t).List(context.Background(), e.prefix+"exports/", 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	return keys
}
