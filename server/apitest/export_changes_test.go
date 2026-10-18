package apitest

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// exportChanges 是交付后变更的响应。
type exportChanges struct {
	ExportTaskID    uuid.UUID      `json:"export_task_id"`
	OnlyApproved    bool           `json:"only_approved"`
	ItemCount       int            `json:"item_count"`
	ChangedCount    int            `json:"changed_count"`
	ReasonCounts    map[string]int `json:"reason_counts"`
	ResubmitItemIDs []uuid.UUID    `json:"resubmit_item_ids"`
	Truncated       bool           `json:"truncated"`
	Items           []struct {
		Seq       int       `json:"seq"`
		ItemID    uuid.UUID `json:"item_id"`
		ArchiveNo *string   `json:"archive_no"`
		Title     string    `json:"title"`
		Deleted   bool      `json:"deleted"`
		Now       *struct {
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"now"`
		Reasons []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Count   *int   `json:"count"`
			Pages   []struct {
				FileID            uuid.UUID  `json:"file_id"`
				ExportedPageNo    *int       `json:"exported_page_no"`
				PageNo            *int       `json:"page_no"`
				ExportedVersionNo *int       `json:"exported_version_no"`
				VersionNo         *int       `json:"version_no"`
				ReviewStatus      *string    `json:"review_status"`
				ToItemID          *uuid.UUID `json:"to_item_id"`
			} `json:"pages"`
			Fields []struct {
				Key    string `json:"key"`
				Label  string `json:"label"`
				Before any    `json:"before"`
				After  any    `json:"after"`
			} `json:"fields"`
		} `json:"reasons"`
	} `json:"items"`
}

// TestExportFreezeAndChanges：建任务时冻结（之后重扫、移出、挪页、改著录、删条目都不影响这次导出），完成后交付后
// 变更列出 8 种原因，补交（resubmit_of）只含有变更、没删除的条目。
func TestExportFreezeAndChanges(t *testing.T) {
	t.Parallel()
	e := newExportEnv(t, "EXF")
	type item struct {
		v     itemView
		pages []uuid.UUID
	}
	mk := func(title string, n int) item {
		pages := e.approvedPages(n, title)
		return item{v: e.lockedItem(e.org.ID, title, pages...), pages: pages}
	}
	a, b, c, d := mk("A", 2), mk("B", 2), mk("C", 2), mk("D", 2)
	ee, f, g, h := mk("E", 3), mk("F", 1), mk("G", 1), mk("H", 1)
	all := []item{a, b, c, d, ee, f, g, h}

	task := e.createTask(map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{e.org.ID}, "export_format": "images"})
	if task.ItemCount != 8 || task.PageCount != 14 {
		t.Fatalf("任务：%+v", task)
	}
	// 建任务之后（执行之前）改：都不影响这次导出。
	unlock := func(it item) {
		t.Helper()
		e.cat.act(it.v.ID, "unlock").Expect(http.StatusOK)
	}
	// A：第 1 页撤回通过、驳回、重扫出 v2（现在待审）。
	e.mgr.Post("/api/files/"+a.pages[0].String()+"/reopen", nil).Expect(http.StatusOK)
	reject(e.rv1, a.pages[0]).Expect(http.StatusOK)
	rescan(e.scan, a.pages[0], "A-rescan.tif", unique(sampleBytes(t, "tiff-g4-1bit.tif"), "EXF-rescan")).Expect(http.StatusCreated)
	// 改页都走"解锁 → 解除封存 → 在文件目录里改 → 重新归档"。
	// B：第 2 页移出来源文件夹（放回盒 1）。
	unlock(b)
	e.cat.unseal(b.v.ID).Expect(http.StatusOK)
	e.cat.moveFiles(e.box.ID, b.pages[1])
	e.cat.reseal(b.v.ID).Expect(http.StatusOK)
	// C 的第 2 页挪到 D 的末尾（先重新归档 C，它才离开 C）。
	unlock(c)
	unlock(d)
	e.cat.unseal(c.v.ID).Expect(http.StatusOK)
	e.cat.unseal(d.v.ID).Expect(http.StatusOK)
	e.cat.moveFiles(d.v.SourceFolderID, c.pages[1])
	e.cat.reseal(c.v.ID).Expect(http.StatusOK)
	e.cat.reseal(d.v.ID).Expect(http.StatusOK)
	// E：第 1 页移出去再移回来（排到最后），只有它挪过。
	unlock(ee)
	e.cat.unseal(ee.v.ID).Expect(http.StatusOK)
	e.cat.moveFiles(e.box.ID, ee.pages[0])
	e.cat.moveFiles(ee.v.SourceFolderID, ee.pages[0])
	e.cat.reseal(ee.v.ID).Expect(http.StatusOK)
	// F：改题名。
	unlock(f)
	e.cat.patchItem(f.v.ID, map[string]any{"title": "F 改过"}).Expect(http.StatusOK)
	// G：撤销归档（页一并移出）。
	unlock(g)
	e.admin.Delete("/api/archive-items/" + g.v.ID.String()).Expect(http.StatusNoContent)

	e.start()
	task = e.waitDone(task.ID)

	// 冻结：明细（和导出物）是建任务那一刻的条目、页序、快照。
	rows := e.files(task.ID, 500)
	var got []string
	for _, r := range rows {
		got = append(got, r.Title+":"+r.FileID.String()[:8]+":v"+strconv.Itoa(r.VersionNo))
	}
	var exp []string
	for _, it := range all {
		for _, p := range it.pages {
			exp = append(exp, it.v.Title+":"+p.String()[:8]+":v1")
		}
	}
	if strings.Join(got, " ") != strings.Join(exp, " ") {
		t.Fatalf("冻结的明细\n  %v\n期望\n  %v", got, exp)
	}
	if l := fileLogs(e.Env, a.pages[0]); l[len(l)-1].Action != "export" || l[len(l)-1].Detail["version_no"] != float64(1) {
		t.Errorf("A 第 1 页的 export 日志应是 v1：%+v", l[len(l)-1])
	}

	// 交付后变更。
	ch := Decode[exportChanges](e.mgr.Get("/api/export-tasks/" + task.ID.String() + "/changes").Expect(http.StatusOK))
	if ch.ItemCount != 8 || ch.ChangedCount != 7 || ch.Truncated || !ch.OnlyApproved {
		t.Fatalf("交付后变更：%+v", ch)
	}
	for code, n := range map[string]int{"new_version": 1, "pages_removed": 1, "pages_moved": 1, "pages_reordered": 1,
		"pages_added": 1, "not_approved": 1, "item_changed": 1, "item_deleted": 1} {
		if ch.ReasonCounts[code] != n {
			t.Errorf("reason_counts[%s] = %d，期望 %d（%v）", code, ch.ReasonCounts[code], n, ch.ReasonCounts)
		}
	}
	if !slicesEqual(ch.ResubmitItemIDs, []uuid.UUID{a.v.ID, b.v.ID, c.v.ID, d.v.ID, ee.v.ID, f.v.ID}) {
		t.Errorf("resubmit_item_ids：%v", ch.ResubmitItemIDs)
	}
	var reasons []string
	for _, it := range ch.Items {
		var codes []string
		for _, r := range it.Reasons {
			codes = append(codes, r.Code)
		}
		reasons = append(reasons, it.Title+":"+strings.Join(codes, ","))
	}
	if strings.Join(reasons, " ") != "A:new_version,not_approved B:pages_removed C:pages_moved D:pages_added E:pages_reordered F:item_changed G:item_deleted" {
		t.Errorf("原因：%v", reasons)
	}
	byTitle := map[string]int{}
	for i, it := range ch.Items {
		byTitle[it.Title] = i
	}
	if r := ch.Items[byTitle["A"]].Reasons[0]; *r.Count != 1 || *r.Pages[0].ExportedVersionNo != 1 || *r.Pages[0].VersionNo != 2 ||
		r.Message != "有 1 页出了新版本（第 1 页：导出的是第 1 版，现在是第 2 版）" {
		t.Errorf("new_version：%+v", r)
	}
	if r := ch.Items[byTitle["A"]].Reasons[1]; *r.Pages[0].ReviewStatus != "pending" {
		t.Errorf("not_approved：%+v", r)
	}
	if r := ch.Items[byTitle["C"]].Reasons[0]; *r.Pages[0].ToItemID != d.v.ID || *r.Pages[0].PageNo != 3 || *r.Pages[0].ExportedPageNo != 2 {
		t.Errorf("pages_moved：%+v", r)
	}
	if r := ch.Items[byTitle["E"]].Reasons[0]; *r.Count != 1 {
		t.Errorf("pages_reordered 应只算挪过的 1 页：%+v", r)
	}
	if r := ch.Items[byTitle["F"]].Reasons[0]; len(r.Fields) != 1 || r.Fields[0].Key != "title" || r.Fields[0].Before != "F" ||
		r.Fields[0].After != "F 改过" || r.Message != "题名和导出时不一样" {
		t.Errorf("item_changed：%+v", r)
	}
	if it := ch.Items[byTitle["G"]]; !it.Deleted || it.Now != nil {
		t.Errorf("item_deleted：%+v", it)
	}
	if it := ch.Items[byTitle["F"]]; it.Now == nil || it.Now.Title != "F 改过" || it.Title != "F" {
		t.Errorf("now：%+v", it)
	}

	// 补交：范围是有变更、没删除的条目（server 在同一个快照里算）。A 现在有页待审、B–F 没锁定，先都处理好。
	pre := map[string]any{"resubmit_of": task.ID, "export_format": "images"}
	p := Decode[precheckResult](e.mgr.Post(e.url("/export-tasks:precheck"), pre).Expect(http.StatusOK))
	if p.ScopeType != "item" || p.ResubmitOf == nil || *p.ResubmitOf != task.ID || p.ItemCount != 6 || p.ReadyItemCount != 0 ||
		!slicesEqual(p.ScopeIDs, ch.ResubmitItemIDs) {
		t.Fatalf("补交的预检：%+v", p)
	}
	e.mgr.Post(e.url("/export-tasks"), pre).ExpectError(http.StatusConflict, "invalid_state")
	approve(e.rv1, a.pages[0]).Expect(http.StatusOK)
	for _, it := range []item{b, c, d, ee, f} {
		e.cat.act(it.v.ID, "lock").Expect(http.StatusOK)
	}
	re := e.createTask(pre)
	if re.ResubmitOf == nil || *re.ResubmitOf != task.ID || re.ScopeType != "item" || !slicesEqual(re.ScopeIDs, ch.ResubmitItemIDs) ||
		re.ItemCount != 6 || re.PageCount != 11 {
		t.Fatalf("补交的任务：%+v", re)
	}
	// 只能补交已完成的任务。
	e.mgr.Post(e.url("/export-tasks:precheck"), map[string]any{"resubmit_of": re.ID, "export_format": "pdf"}).
		ExpectError(http.StatusConflict, "invalid_state")
	re = e.waitDone(re.ID)
	if rows := e.files(re.ID, 500); rows[0].FileID != a.pages[0] || rows[0].VersionNo != 2 {
		t.Errorf("补交的第 1 页应是重扫后的 v2：%+v", rows[0])
	}
	// 没完成的任务没有交付后变更。
	e.mgr.Get("/api/export-tasks/"+uuid.Nil.String()+"/changes").ExpectError(http.StatusNotFound, "not_found")
}
