package apitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/s3"
)

// TestExportRunAndDownload：端到端。样本经接口上传、分配、通过、整个文件夹归档、锁定 → 预检 → 导出 PDF 和原图包 → 执行器
// 生成并分片上传 → 下载（预签名地址带 Content-Disposition）→ 核对 zip 里的文件、清单和明细、sha256、file_logs、
// 审计、事件；过期后下载 409、到期清理删对象。
func TestExportRunAndDownload(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "EXR")
	admin := e.admin
	// 著录项：目录.xlsx 的列。
	e.cat.setSettings(map[string]any{"item_fields": []any{map[string]any{"key": "resp", "label": "责任者", "type": "text"}}})

	// 上传样本（常见扫描格式；带透明的 PNG 在 PDF 里要转换）。
	samples := []string{"jpeg-rgb.jpg", "png-alpha.png", "tiff-lzw-rgb.tif", "tiff-g4-1bit.tif", "jpeg-gray.jpg"}
	sha := map[uuid.UUID]string{}
	var ids []uuid.UUID
	for i, name := range samples {
		data := unique(sampleBytes(t, name), "EXR-"+strconv.Itoa(i))
		f := Decode[ingestResponse](e.scan.Upload(e.box.ID, name, data, nil).Expect(http.StatusCreated))
		sum := sha256.Sum256(data)
		sha[f.FileID] = hex.EncodeToString(sum[:])
		ids = append(ids, f.FileID)
	}
	e.assign(e.rv1ID, ids...)
	for _, id := range ids {
		approve(e.rv1, id).Expect(http.StatusOK)
	}
	it1 := e.cat.mkItem(e.org.ID, map[string]any{"title": "第一件", "extra_fields": map[string]any{"resp": "办公室"}, "file_ids": ids[:2]})
	it2 := e.cat.mkItem(e.org.ID, map[string]any{"title": "第二件", "file_ids": ids[2:]})
	for _, it := range []itemView{it1, it2} {
		e.cat.act(it.ID, "lock").Expect(http.StatusOK)
	}
	it1, it2 = e.cat.item(it1.ID), e.cat.item(it2.ID)
	no1, no2 := str(it1.ArchiveNo), str(it2.ArchiveNo)
	if no1 != "Z109-WS·2025-Y-BGS-0001" || no2 != "Z109-WS·2025-Y-BGS-0002" {
		t.Fatalf("档号：%s %s", no1, no2)
	}

	stream := openStream(t, admin, e.p.ID)
	scope := map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{e.org.ID}}
	body := func(format string) map[string]any {
		b := map[string]any{"export_format": format}
		for k, v := range scope {
			b[k] = v
		}
		return b
	}
	p := Decode[precheckResult](e.mgr.Post(e.url("/export-tasks:precheck"), body("pdf")).Expect(http.StatusOK))
	if p.ItemCount != 2 || p.ReadyItemCount != 2 || p.ReadyPageCount != 5 || len(p.NotReady) != 0 {
		t.Fatalf("预检：%+v", p)
	}
	pdf := e.createTask(body("pdf"))
	images := e.createTask(body("images"))
	if pdf.Status != "queued" || pdf.ItemCount != 2 || pdf.PageCount != 5 || pdf.ProgressTotal != 5 || pdf.Attempts != 0 ||
		pdf.MaxAttempts != 3 || !pdf.OnlyApproved || pdf.OnlyReady || !pdf.RequireLockedItem || pdf.CleaningUp ||
		len(pdf.ScopeCategories) != 1 || strings.Join(pdf.ScopeCategories[0].CategoryNames, "/") != "全宗/文书/2025 年/永久/办公室" ||
		pdf.CreatedBy != e.mgrID || pdf.CreatedByName == nil || pdf.ExportFormat != "pdf" {
		t.Fatalf("建任务：%+v", pdf)
	}

	e.start()
	pdf = e.waitDone(pdf.ID)
	images = e.waitDone(images.ID)
	for _, tk := range []exportTask{pdf, images} {
		if tk.ProgressDone != 5 || tk.Attempts != 1 || tk.ErrorMessage != nil || tk.OutputSize == nil || *tk.OutputSize <= 0 ||
			tk.ExpiresAt == nil || tk.Expired || tk.StartedAt == nil || tk.FinishedAt == nil || tk.CleaningUp {
			t.Fatalf("完成的任务：%+v", tk)
		}
		if d := time.Until(*tk.ExpiresAt); d < 6*24*time.Hour || d > 7*24*time.Hour+time.Minute {
			t.Errorf("到期时间应是 7 天后：%s", tk.ExpiresAt)
		}
	}

	// 列表：最新的在前，keyset 分页。
	var list struct {
		Items      []exportTask `json:"items"`
		NextCursor *string      `json:"next_cursor"`
	}
	e.mgr.Get(e.url("/export-tasks?limit=1")).Expect(http.StatusOK).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].ID != images.ID || list.NextCursor == nil {
		t.Fatalf("列表第一页：%+v", list)
	}
	e.mgr.Get(e.url("/export-tasks?limit=1&cursor=" + url.QueryEscape(*list.NextCursor))).Expect(http.StatusOK).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].ID != pdf.ID || list.NextCursor != nil {
		t.Fatalf("列表第二页：%+v", list)
	}
	e.mgr.Get(e.url("/export-tasks?limit=201")).ExpectError(http.StatusBadRequest, "invalid_input")
	e.mgr.Get(e.url("/export-tasks?cursor=x")).ExpectError(http.StatusBadRequest, "invalid_input")

	// 明细：按件、页，分页走完一样。
	rows := e.files(pdf.ID, 2)
	if len(rows) != 5 {
		t.Fatalf("明细 %d 页", len(rows))
	}
	want := []struct {
		no   string
		seq  int
		page int
		id   uuid.UUID
	}{{no1, 1, 1, ids[0]}, {no1, 1, 2, ids[1]}, {no2, 2, 1, ids[2]}, {no2, 2, 2, ids[3]}, {no2, 2, 3, ids[4]}}
	for i, r := range rows {
		w := want[i]
		if str(r.ArchiveNo) != w.no || r.Seq != w.seq || r.PageNo != w.page || r.FileID != w.id || r.VersionNo != 1 ||
			r.ContentSHA256 != sha[r.FileID] || r.FileName == nil || r.UploadName == nil || r.FileSize <= 0 {
			t.Errorf("明细第 %d 行：%+v", i+1, r)
		}
	}

	// 下载：JSON（不是 302），预签名地址带 Content-Disposition，浏览器直连 S3。
	date := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format("20060102")
	for _, tk := range []exportTask{pdf, images} {
		dl := Decode[downloadInfo](e.mgr.Get("/api/export-tasks/" + tk.ID.String() + "/download").Expect(http.StatusOK))
		if dl.FileName != "EXR_办公室_"+date+".zip" || dl.Size != *tk.OutputSize || dl.ExpiresAt == nil ||
			!dl.ExpiresAt.Equal(*tk.ExpiresAt) || !dl.URLExpiresAt.After(time.Now().Add(24*time.Hour)) {
			t.Fatalf("下载：%+v", dl)
		}
		status, hdr, data := fetch(t, dl.URL)
		if status != http.StatusOK || int64(len(data)) != dl.Size {
			t.Fatalf("下载 %d，%d 字节：%s", status, len(data), data[:min(len(data), 200)])
		}
		cd := hdr.Get("Content-Disposition")
		if !strings.Contains(cd, `filename="EXR_export_`+date+`.zip"`) || !strings.Contains(cd, "filename*=UTF-8''EXR_%E5%8A%9E%E5%85%AC%E5%AE%A4_"+date+".zip") {
			t.Errorf("Content-Disposition：%s", cd)
		}
		entries, names := zipEntries(t, data)
		manifest := manifestRows(t, entries["清单.csv"])
		if len(manifest) != 6 || strings.Join(manifest[0], ",") != "档号,页号,zip 内文件名,file_id,snapshot_id,版本号,sha256,原文件名,审核状态" {
			t.Fatalf("清单：%v", manifest)
		}
		if _, ok := entries["目录.xlsx"]; !ok {
			t.Errorf("没有目录.xlsx：%v", names)
		}
		for i, r := range rows {
			m := manifest[i+1]
			zipName := str(r.ArchiveNo) + ".pdf"
			if tk.ExportFormat == "images" {
				ext := map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/tiff": "tif"}[r.MimeType]
				zipName = str(r.ArchiveNo) + "/" + "000" + strconv.Itoa(r.PageNo) + "." + ext
				sum := sha256.Sum256(entries[zipName])
				if hex.EncodeToString(sum[:]) != r.ContentSHA256 {
					t.Errorf("%s 的 sha256 和明细不一致", zipName)
				}
			}
			got := []string{m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[8]}
			exp := []string{str(r.ArchiveNo), strconv.Itoa(r.PageNo), zipName, r.FileID.String(), r.SnapshotID.String(),
				strconv.Itoa(r.VersionNo), r.ContentSHA256, "通过"}
			if strings.Join(got, "|") != strings.Join(exp, "|") {
				t.Errorf("%s 清单第 %d 行：%v，明细：%v", tk.ExportFormat, i+1, got, exp)
			}
		}
		if tk.ExportFormat == "pdf" {
			for _, no := range []string{no1, no2} {
				if !bytes.HasPrefix(entries[no+".pdf"], []byte("%PDF-1.7")) {
					t.Errorf("%s.pdf 不是 PDF", no)
				}
			}
			if len(names) != 4 {
				t.Errorf("PDF 的 zip：%v", names)
			}
		}
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM audit_logs WHERE target_type = 'export_task' AND action = 'download'`); n != 2 {
		t.Errorf("下载审计 %d 条", n)
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM audit_logs WHERE target_type = 'export_task' AND action = 'create'`); n != 2 {
		t.Errorf("建任务审计 %d 条", n)
	}

	// file_logs：每页一条 export，带任务、档号、页号、版本、快照、格式。
	logs := fileLogs(e.Env, ids[1])
	var exportLogs []fileLog
	for _, l := range logs {
		if l.Action == "export" {
			exportLogs = append(exportLogs, l)
		}
	}
	if len(exportLogs) != 2 || exportLogs[0].Detail["archive_no"] != no1 || exportLogs[0].Detail["page_no"] != float64(2) ||
		exportLogs[0].Detail["version_no"] != float64(1) || exportLogs[0].Detail["snapshot_id"] != rows[1].SnapshotID.String() {
		t.Errorf("export 日志：%+v", exportLogs)
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM file_logs WHERE action = 'export'`); n != 10 {
		t.Errorf("export 日志 %d 条，期望 10", n)
	}
	// 导出物的 key。
	key := QueryValue[string](e.Env, `SELECT output_key FROM export_tasks WHERE id = $1`, pdf.ID)
	if key != e.prefix+"exports/EXR/"+pdf.ID.String()+"/export-1.zip" {
		t.Errorf("导出物的 key：%s", key)
	}

	// 事件：建任务、开始、完成都发了 export.changed（只带 id）。
	seen := map[string]int{}
	deadline := time.After(3 * time.Second)
collect:
	for {
		select {
		case ev := <-stream.events:
			if ev["type"] == "export.changed" {
				for _, id := range idsOf(ev["task_ids"]) {
					seen[id]++
				}
			}
		case <-deadline:
			break collect
		default:
			if seen[pdf.ID.String()] >= 3 && seen[images.ID.String()] >= 3 {
				break collect
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if seen[pdf.ID.String()] < 3 || seen[images.ID.String()] < 3 {
		t.Errorf("export.changed 事件：%v", seen)
	}

	// 交付后变更：没有变化。
	var ch struct {
		ChangedCount int `json:"changed_count"`
		ItemCount    int `json:"item_count"`
	}
	e.mgr.Get("/api/export-tasks/" + pdf.ID.String() + "/changes").Expect(http.StatusOK).JSON(&ch)
	if ch.ChangedCount != 0 || ch.ItemCount != 2 {
		t.Errorf("交付后变更：%+v", ch)
	}
	// 已完成的不能取消。
	e.mgr.Post("/api/export-tasks/"+pdf.ID.String()+"/cancel", nil).ExpectError(http.StatusConflict, "invalid_state")

	// 过期：下载 409；过了宽限期（url_expire_hours + 24 小时）到期清理删对象、清空 output_key。
	e.Exec(`UPDATE export_tasks SET expires_at = now() - interval '1 hour' WHERE id = $1`, images.ID)
	e.mgr.Get("/api/export-tasks/"+images.ID.String()+"/download").ExpectError(http.StatusConflict, "invalid_state")
	if tk := e.task(images.ID); !tk.Expired {
		t.Errorf("expired 应为 true")
	}
	imagesKey := QueryValue[string](e.Env, `SELECT output_key FROM export_tasks WHERE id = $1`, images.ID)
	if n, err := e.App.Exports.Cleanup(context.Background()); err != nil || n != 0 {
		t.Fatalf("宽限期内不该清理：%d %v", n, err)
	}
	e.Exec(`UPDATE export_tasks SET expires_at = now() - interval '49 hours' WHERE id = $1`, images.ID)
	if n, err := e.App.Exports.Cleanup(context.Background()); err != nil || n != 1 {
		t.Fatalf("到期清理：%d %v", n, err)
	}
	if _, err := S3Client(t).Get(context.Background(), imagesKey); !errors.Is(err, s3.ErrNotFound) {
		t.Errorf("过期的导出物应已删除：%v", err)
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM export_tasks WHERE id = $1 AND output_key IS NULL AND storage_profile_id IS NOT NULL`, images.ID); n != 1 {
		t.Errorf("清理后应清空 output_key、保留存储")
	}
	e.mgr.Get("/api/export-tasks/"+images.ID.String()+"/download").ExpectError(http.StatusConflict, "invalid_state")
	// 明细、任务永久保留。
	if got := e.files(images.ID, 500); len(got) != 5 {
		t.Errorf("清理后明细 %d 页", len(got))
	}
	// 没过期的照样能下载。
	e.mgr.Get("/api/export-tasks/" + pdf.ID.String() + "/download").Expect(http.StatusOK)
}
