package apitest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/s3"
)

// 后台执行（M5-2）：恢复（执行中 server 被杀后重启、优雅退出放回队列、可以重试的失败）。
// 存储经过测试代理：读原图可以变慢（让任务"执行中"持续一会儿）、可以失败。

// exportReady 建一个就绪的条目（n 页），返回导出它的请求体。
func (e *exportEnv) exportReady(title string, n int) map[string]any {
	e.t.Helper()
	it := e.lockedItem(e.org.ID, title, e.approvedPages(n, title)...)
	return map[string]any{"scope_type": "item", "scope_ids": []uuid.UUID{it.ID}, "export_format": "pdf"}
}

func (e *exportEnv) jobOf(taskID uuid.UUID) (status string, attempts int, errMsg *string) {
	e.t.Helper()
	if err := e.Pool.QueryRow(context.Background(), `SELECT j.status, j.attempts, j.error_message FROM export_tasks t
JOIN background_jobs j ON j.id = t.job_id WHERE t.id = $1`, taskID).Scan(&status, &attempts, &errMsg); err != nil {
		e.t.Fatal(err)
	}
	return
}

// TestExportRecovery：执行中 server 被杀（job 停在 running）→ 重启时放回队列、接着完成（上次留下的导出物删掉）；
// 优雅退出时执行中的放回队列（不算一次尝试）→ 再启动后完成；读原图失败（可以重试）→ 退避后重试成功。
func TestExportRecovery(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "EXV", withProxy, func(o *exportOpts) { o.retryBackoff = time.Second })
	body := e.exportReady("一", 4)

	// 1. 被杀：建任务后把 job、任务改成执行中，并留下上一次执行写到一半的导出物。
	t1 := e.createTask(body)
	leftover := e.prefix + "exports/EXV/" + t1.ID.String() + "/export-1.zip"
	PutObject(t, leftover, "application/zip", []byte("PK partial"))
	e.Exec(`UPDATE background_jobs SET status = 'running', attempts = 1, started_at = now(), heartbeat_at = now(),
  runner_id = 'killed:1', progress_done = 2 WHERE id = (SELECT job_id FROM export_tasks WHERE id = $1)`, t1.ID)
	e.Exec(`UPDATE export_tasks SET status = 'running', storage_profile_id = $2, output_key = $3 WHERE id = $1`, t1.ID, e.profile, leftover)
	e.start() // 启动时放回队列（算一次尝试），再领取
	t1 = e.waitDone(t1.ID)
	if t1.Attempts != 2 || t1.ErrorMessage != nil || t1.ProgressDone != 4 {
		t.Fatalf("重启后完成：%+v", t1)
	}
	key := QueryValue[string](e.Env, `SELECT output_key FROM export_tasks WHERE id = $1`, t1.ID)
	if !strings.HasSuffix(key, "/export-2.zip") {
		t.Errorf("第 2 次执行的导出物：%s", key)
	}
	if _, err := S3Client(t).Get(context.Background(), leftover); !errors.Is(err, s3.ErrNotFound) {
		t.Errorf("上次执行留下的导出物应删掉：%v", err)
	}
	dl := Decode[downloadInfo](e.mgr.Get("/api/export-tasks/" + t1.ID.String() + "/download").Expect(http.StatusOK))
	if st, _, data := fetch(t, dl.URL); st != http.StatusOK || int64(len(data)) != dl.Size {
		t.Fatalf("下载：%d %d", st, len(data))
	}

	// 2. 优雅退出：执行中的停下、放回队列（attempts 退回 0），不留导出物；再启动后完成（第 1 次执行）。
	e.proxy.delay.Store(int64(400 * time.Millisecond))
	t2 := e.createTask(body)
	e.waitTask(t2.ID, 10*time.Second, "开始执行", func(t exportTask) bool { return t.Status == "running" })
	e.stop()
	if tk := e.task(t2.ID); tk.Status != "queued" || tk.Attempts != 0 || tk.CleaningUp {
		t.Fatalf("退出后应放回队列：%+v", tk)
	}
	if st, _, _ := e.jobOf(t2.ID); st != "queued" {
		t.Fatalf("job：%s", st)
	}
	for _, k := range e.exportObjects() {
		if strings.Contains(k, t2.ID.String()) {
			t.Errorf("退出时不应留下导出物：%s", k)
		}
	}
	e.proxy.delay.Store(0)
	e.start()
	if t2 = e.waitDone(t2.ID); t2.Attempts != 1 {
		t.Errorf("放回队列不算一次尝试：%+v", t2)
	}

	// 3. 读原图失败（可以重试）：第 1 次失败后排队（"第 1 次执行失败，1 秒后自动重试"），再执行成功。
	e.proxy.failGets.Store(1)
	t3 := e.createTask(body)
	retry := e.waitTask(t3.ID, 10*time.Second, "排队重试", func(t exportTask) bool { return t.Status == "queued" && t.Attempts == 1 })
	if retry.ErrorMessage == nil || !strings.HasPrefix(*retry.ErrorMessage, "第 1 次执行失败，1 秒后自动重试：") ||
		!strings.Contains(*retry.ErrorMessage, "读原图失败") {
		t.Fatalf("重试前的错误信息：%v", str(retry.ErrorMessage))
	}
	t3 = e.waitDone(t3.ID)
	if t3.Attempts != 2 || t3.ErrorMessage != nil {
		t.Errorf("重试后完成：%+v", t3)
	}
}

// TestPermsExport：导出的接口都要 export 菜单（内置只有负责人和 admin）；进不了任务所在的项目 403。
func TestPermsExport(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                                   admin  manager  export  cataloger  reviewer  scanner  none
	POST   /api/projects/{pid}/export-tasks:precheck     Y       Y        Y        -          -         -       -
	POST   /api/projects/{pid}/export-tasks              Y       Y        Y        -          -         -       -
	GET    /api/projects/{pid}/export-tasks              Y       Y        Y        -          -         -       -
	GET    /api/export-tasks/{id}                        Y       Y        Y        -          -         -       -
	GET    /api/export-tasks/{id}/files                  Y       Y        Y        -          -         -       -
	GET    /api/export-tasks/{id}/changes                Y       Y        Y        -          -         -       -
	GET    /api/export-tasks/{id}/download               Y       Y        Y        -          -         -       -
	POST   /api/export-tasks/{id}/cancel                 Y       Y        Y        -          -         -       -
	`)
	// 有 export 菜单但进不了项目：403；任务不存在 404。
	p := env.CreateProject("PX")
	tid := uuid.Must(uuid.NewV7())
	env.Exec(`INSERT INTO export_tasks (id, project_id, scope_type, scope_ids, export_format, created_by)
SELECT $1, $2, 'item', '{}', 'pdf', id FROM users WHERE username = 'admin'`, tid, p.ID)
	mgr := env.As("manager")
	mgr.Get("/api/export-tasks/"+tid.String()).ExpectError(http.StatusForbidden, "forbidden")
	mgr.Get("/api/projects/"+p.ID.String()+"/export-tasks").ExpectError(http.StatusForbidden, "forbidden")
	env.GrantProject(p.ID, env.PersonaID("manager"))
	mgr.Get("/api/export-tasks/" + tid.String()).Expect(http.StatusOK)
	mgr.Get("/api/export-tasks/"+uuid.NewString()).ExpectError(http.StatusNotFound, "not_found")
	mgr.Get("/api/export-tasks/x").ExpectError(http.StatusBadRequest, "invalid_input")
}
