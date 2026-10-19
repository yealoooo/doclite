package apitest

import (
	"testing"

	"github.com/google/uuid"
)

// 项目看板（M4-2，requirements 5.10；2026-10-19 简化：按件算归档、锁定、导出）。

type reviewCounts struct {
	Unassigned int `json:"unassigned"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	Total      int `json:"total"`
}

type itemCounts struct {
	Total           int `json:"total"`
	Locked          int `json:"locked"`
	Modifying       int `json:"modifying"`
	Exportable      int `json:"exportable"`
	ExportablePages int `json:"exportable_pages"`
}

type summaryCounts struct {
	Total   int          `json:"total"`
	Review  reviewCounts `json:"review"`
	Catalog struct {
		Uncataloged int `json:"uncataloged"`
		Unlocked    int `json:"unlocked"`
		Locked      int `json:"locked"`
		Cataloged   int `json:"cataloged"`
		Total       int `json:"total"`
	} `json:"catalog"`
	Items itemCounts `json:"items"`
}

type summaryChild struct {
	FolderID    uuid.UUID      `json:"folder_id"`
	FolderName  string         `json:"folder_name"`
	FolderCount int            `json:"folder_count"`
	Archive     *folderArchive `json:"archive"`
	Counts      summaryCounts  `json:"counts"`
}

type summaryView struct {
	FolderID          uuid.UUID      `json:"folder_id"`
	FolderName        string         `json:"folder_name"`
	ParentID          *uuid.UUID     `json:"parent_id"`
	RequireLockedItem bool           `json:"require_locked_item"`
	Counts            summaryCounts  `json:"counts"`
	DirectCounts      summaryCounts  `json:"direct_counts"`
	Children          []summaryChild `json:"children"`
}

// sqlMatrix 用 SQL 直接数（和接口不同的写法：按 path_code 前缀 LIKE）：review_status → 归类状态 → 个数。
// direct 为 true 时只数直接放在 folder 里的。
func sqlMatrix(e *Env, pid, folder uuid.UUID, direct bool) map[string]map[string]int {
	e.t.Helper()
	rows, err := e.Pool.Query(e.t.Context(), `SELECT ff.review_status,
  CASE WHEN af.file_id IS NULL THEN 'uncataloged' WHEN a.status = 'locked' THEN 'locked' ELSE 'unlocked' END, count(*)
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id
JOIN folders top ON top.id = $2
LEFT JOIN archive_item_files af ON af.file_id = ff.file_id
LEFT JOIN archive_items a ON a.id = af.archive_item_id
WHERE ff.project_id = $1 AND ff.deleted_at IS NULL AND f.deleted_at IS NULL
  AND (CASE WHEN $3 THEN f.id = top.id ELSE f.path_code LIKE top.path_code || '%' END)
GROUP BY 1, 2`, pid, folder, direct)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var rs, cs string
		var n int
		if err := rows.Scan(&rs, &cs, &n); err != nil {
			e.t.Fatal(err)
		}
		if out[rs] == nil {
			out[rs] = map[string]int{}
		}
		out[rs][cs] = n
	}
	return out
}

// checkCounts 核对一组计数的页数部分：按审核状态、按归档状态的合计和 SQL 直接数的一致。
func checkCounts(t *testing.T, what string, got summaryCounts, want map[string]map[string]int) {
	t.Helper()
	review := map[string]int{}
	col := map[string]int{}
	total := 0
	for rs, row := range want {
		for cs, n := range row {
			review[rs] += n
			col[cs] += n
			total += n
		}
	}
	r := got.Review
	if r.Unassigned != review["unassigned"] || r.Pending != review["pending"] || r.Approved != review["approved"] ||
		r.Rejected != review["rejected"] || r.Total != total || got.Total != total {
		t.Errorf("%s：审核 %+v（共 %d），SQL 数出 %v", what, r, got.Total, review)
	}
	c := got.Catalog
	if c.Uncataloged != col["uncataloged"] || c.Unlocked != col["unlocked"] || c.Locked != col["locked"] ||
		c.Cataloged != col["unlocked"]+col["locked"] || c.Total != total {
		t.Errorf("%s：归档 %+v，SQL 数出 %v", what, c, col)
	}
}

// TestCatalogSummary：看板的页数（按审核状态、归档状态）、件数（已归档、已锁定、修改中、可导出）、子文件夹的归档信息、
// 按文件夹逐层展开；页数和 SQL 直接数的一致，可导出和导出预检（勾"只导出审核通过的"）的就绪件数、页数一致。
func TestCatalogSummary(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "DB")
	_, _, _, _, org := c.mkChain()
	fonds2 := c.mkCat(nil, "fonds", "Z2", "全宗二") // 只有全宗的节点：档号为空
	admin := c.admin
	// 锁定不要求全部页通过（造"已锁定但有驳回页"的件）。
	c.setSettings(map[string]any{"catalog": map[string]any{"lock_requires_approved": false}})

	// 根 → A（盒：直接放 3 个文件，下面 A1、A2）；根 → B、C、D、E。根里直接放 2 个文件。
	c.mkFile(uuid.Nil, "unassigned")
	c.mkFile(uuid.Nil, "approved")
	fa, _ := c.mkFolder(uuid.Nil, "A", "pending", "approved", "rejected")
	fa1, a1Files := c.mkFolder(fa, "A1", "approved", "approved", "unassigned", "pending", "approved")
	fa2, _ := c.mkFolder(fa, "A2", "approved", "pending")
	fb, _ := c.mkFolder(uuid.Nil, "B", "approved", "approved", "approved")
	fc, _ := c.mkFolder(uuid.Nil, "C", "approved")
	fd, _ := c.mkFolder(uuid.Nil, "D", "approved", "rejected")
	fe, _ := c.mkFolder(uuid.Nil, "E", "approved", "approved")
	c.Exec(`UPDATE folder_files SET deleted_at = now() WHERE file_id = $1`, a1Files[4]) // 已删除的不算

	// A1：未锁定（4 页，含待审、待分配）；B：锁定，3 页都通过（可导出）；C：档号为空（1 页通过）；D：锁定、有一页驳回；
	// E：未锁定、2 页都通过（只在不要求锁定时可导出）。
	a1 := c.archiveOne(org.ID, fa1, "")
	b := c.archiveOne(org.ID, fb, "")
	c.act(b.ID, "lock").Expect(200)
	if it := c.archiveOne(fonds2.ID, fc, ""); it.ArchiveNo != nil {
		t.Fatalf("C 应该没有档号：%s", *it.ArchiveNo)
	}
	d := c.archiveOne(org.ID, fd, "")
	c.act(d.ID, "lock").Expect(200)
	e := c.archiveOne(org.ID, fe, "")

	get := func(folder *uuid.UUID) summaryView {
		t.Helper()
		path := "/api/projects/" + c.pid.String() + "/catalog-summary"
		if folder != nil {
			path += "?folder_id=" + folder.String()
		}
		return Decode[summaryView](admin.Get(path).Expect(200))
	}
	check := func(s summaryView, requireLocked bool) {
		t.Helper()
		if s.RequireLockedItem != requireLocked {
			t.Fatalf("require_locked_item：%v", s.RequireLockedItem)
		}
		checkCounts(t, s.FolderName+" 子树", s.Counts, sqlMatrix(c.Env, c.pid, s.FolderID, false))
		checkCounts(t, s.FolderName+" 直接", s.DirectCounts, sqlMatrix(c.Env, c.pid, s.FolderID, true))
		for _, ch := range s.Children {
			checkCounts(t, ch.FolderName, ch.Counts, sqlMatrix(c.Env, c.pid, ch.FolderID, false))
		}
	}
	// 可导出和导出预检（根节点 = 整个项目，默认勾"只导出审核通过的"）的就绪件数、页数一致，已归档件数 = 预检的件数。
	checkPrecheck := func(s summaryView) {
		t.Helper()
		p := Decode[precheckResult](admin.Post("/api/projects/"+c.pid.String()+"/export-tasks:precheck",
			map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{c.rootID}, "export_format": "pdf"}).Expect(200))
		it := s.Counts.Items
		if !p.OnlyApproved || it.Total != p.ItemCount || it.Exportable != p.ReadyItemCount || it.ExportablePages != p.ReadyPageCount {
			t.Errorf("和导出预检不一致：看板 %+v，预检 %d 件、就绪 %d 件 %d 页", it, p.ItemCount, p.ReadyItemCount, p.ReadyPageCount)
		}
	}
	child := func(s summaryView, id uuid.UUID) summaryChild {
		t.Helper()
		for _, ch := range s.Children {
			if ch.FolderID == id {
				return ch
			}
		}
		t.Fatalf("%s 的 children 里没有 %s", s.FolderName, id)
		return summaryChild{}
	}

	// 根（整个项目）：19 页（不含已删除的），子文件夹 A–E 按 sort_no 排。
	root := get(nil)
	if root.FolderID != c.folder || root.ParentID != nil || len(root.Children) != 5 || root.Children[0].FolderID != fa ||
		root.Children[4].FolderID != fe || root.Counts.Total != 19 || root.DirectCounts.Total != 2 ||
		root.Children[0].Counts.Total != 9 {
		t.Fatalf("根：%+v", root)
	}
	// 件：5 件，锁定 2（B、D），可导出只有 B（3 页）：C 档号为空、D 有驳回页、E 没锁定、A1 有页没通过。
	if want := (itemCounts{Total: 5, Locked: 2, Exportable: 1, ExportablePages: 3}); root.Counts.Items != want ||
		root.DirectCounts.Items != (itemCounts{}) {
		t.Fatalf("根的件数：%+v，直接 %+v", root.Counts.Items, root.DirectCounts.Items)
	}
	if root.Counts.Catalog.Cataloged != 12 || root.Counts.Catalog.Locked != 5 || root.Counts.Review.Approved != 12 {
		t.Fatalf("根的页数：%+v", root.Counts)
	}
	check(root, true)
	checkPrecheck(root)

	// 子文件夹的归档信息（和文件目录 children 的 archive 一样）、直接子文件夹数。
	if ch := child(root, fa); ch.FolderCount != 2 || ch.Archive != nil || ch.Counts.Items != (itemCounts{Total: 1}) {
		t.Errorf("A：%+v", ch)
	}
	if ch := child(root, fb); ch.FolderCount != 0 || ch.Archive == nil || ch.Archive.ItemID != b.ID || !ch.Archive.Locked ||
		ch.Archive.Status != "locked" || ch.Archive.ArchiveNo == nil || *ch.Archive.ArchiveNo != *b.ArchiveNo ||
		ch.Archive.SourceStatus != "sealed" || len(ch.Archive.CategoryNames) != 5 ||
		ch.Counts.Items != (itemCounts{Total: 1, Locked: 1, Exportable: 1, ExportablePages: 3}) {
		t.Errorf("B：%+v %+v", ch, ch.Archive)
	}
	if ch := child(root, fc); ch.Archive == nil || ch.Archive.ArchiveNo != nil || ch.Archive.Locked ||
		ch.Counts.Items != (itemCounts{Total: 1}) {
		t.Errorf("C（档号为空）：%+v %+v", ch, ch.Archive)
	}
	if ch := child(root, fd); ch.Archive == nil || !ch.Archive.Locked || ch.Counts.Review.Rejected != 1 ||
		ch.Counts.Items != (itemCounts{Total: 1, Locked: 1}) {
		t.Errorf("D（锁定、有驳回页）：%+v", ch)
	}

	// 下一层：A 的件算在来源文件夹 A1 里；A 自己（直接）没有件。
	sub := get(&fa)
	if sub.ParentID == nil || *sub.ParentID != c.folder || len(sub.Children) != 2 || sub.Children[0].FolderID != fa1 ||
		sub.Counts.Items != (itemCounts{Total: 1}) || sub.DirectCounts.Items != (itemCounts{}) {
		t.Fatalf("A：%+v", sub)
	}
	if ch := child(sub, fa1); ch.Archive == nil || ch.Archive.ItemID != a1.ID || ch.Archive.Status != "draft" ||
		ch.Counts.Items != (itemCounts{Total: 1}) {
		t.Errorf("A1：%+v", ch)
	}
	if ch := child(sub, fa2); ch.Archive != nil || ch.Counts.Items != (itemCounts{}) {
		t.Errorf("A2：%+v", ch)
	}
	check(sub, true)
	// 整个归档成一件的文件夹自己：件算在 direct_counts 里。
	if s := get(&fb); len(s.Children) != 0 || s.Counts.Items != s.DirectCounts.Items ||
		s.DirectCounts.Items != (itemCounts{Total: 1, Locked: 1, Exportable: 1, ExportablePages: 3}) {
		t.Fatalf("B 自己：%+v", s)
	}

	// export.require_locked_item 关掉：E 也可导出（没锁定）；C（档号为空）、D（有驳回页）、A1（有页没通过）仍然不行。
	c.setSettings(map[string]any{"catalog": map[string]any{"lock_requires_approved": false}, "export": map[string]any{"require_locked_item": false}})
	root = get(nil)
	if want := (itemCounts{Total: 5, Locked: 2, Exportable: 2, ExportablePages: 5}); root.Counts.Items != want {
		t.Fatalf("不要求锁定：%+v", root.Counts.Items)
	}
	check(root, false)
	checkPrecheck(root)

	// E 解除封存（修改中）：不可导出（导出预检不就绪），页仍算已归档未锁定。
	c.unseal(e.ID).Expect(200)
	root = get(nil)
	if want := (itemCounts{Total: 5, Locked: 2, Modifying: 1, Exportable: 1, ExportablePages: 3}); root.Counts.Items != want ||
		root.Counts.Catalog.Unlocked != 7 {
		t.Fatalf("修改中：%+v", root.Counts)
	}
	if ch := child(root, fe); ch.Archive == nil || ch.Archive.SourceStatus != "modifying" || ch.Archive.UnsealedByName == nil ||
		ch.Counts.Items != (itemCounts{Total: 1, Modifying: 1}) {
		t.Errorf("E（修改中）：%+v %+v", ch, ch.Archive)
	}
	checkPrecheck(root)

	// 参数：不合法 400；别的项目的、不存在的文件夹 404。
	base := "/api/projects/" + c.pid.String() + "/catalog-summary?folder_id="
	admin.Get(base+"x").ExpectError(400, "invalid_input")
	admin.Get(base+uuid.NewString()).ExpectError(404, "not_found")
	other := c.CreateProject("DZ")
	admin.Get(base+other.RootFolderID.String()).ExpectError(404, "not_found")
}
