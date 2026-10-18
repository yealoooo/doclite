package apitest

import (
	"testing"

	"github.com/google/uuid"
)

// 项目看板（M4-2，requirements 5.10）。

type reviewCounts struct {
	Unassigned int `json:"unassigned"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	Total      int `json:"total"`
}

type rowCounts struct {
	Uncataloged int `json:"uncataloged"`
	Unlocked    int `json:"unlocked"`
	Locked      int `json:"locked"`
	Total       int `json:"total"`
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
	Matrix     map[string]rowCounts `json:"matrix"`
	Exportable reviewCounts         `json:"exportable"`
}

type summaryView struct {
	FolderID          uuid.UUID     `json:"folder_id"`
	FolderName        string        `json:"folder_name"`
	ParentID          *uuid.UUID    `json:"parent_id"`
	RequireLockedItem bool          `json:"require_locked_item"`
	Counts            summaryCounts `json:"counts"`
	DirectCounts      summaryCounts `json:"direct_counts"`
	Children          []struct {
		FolderID   uuid.UUID     `json:"folder_id"`
		FolderName string        `json:"folder_name"`
		Counts     summaryCounts `json:"counts"`
	} `json:"children"`
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

// checkCounts 核对一组计数：矩阵每格和 SQL 一致，行列合计、可导出按 requireLocked 算。
func checkCounts(t *testing.T, what string, got summaryCounts, want map[string]map[string]int, requireLocked bool) {
	t.Helper()
	total, cataloged := 0, 0
	col := map[string]int{}
	for _, rs := range []string{"unassigned", "pending", "approved", "rejected"} {
		row := got.Matrix[rs]
		w := want[rs]
		if row.Uncataloged != w["uncataloged"] || row.Unlocked != w["unlocked"] || row.Locked != w["locked"] ||
			row.Total != w["uncataloged"]+w["unlocked"]+w["locked"] {
			t.Errorf("%s：%s 行 %+v，SQL 数出 %v", what, rs, row, w)
		}
		rowTotal := row.Uncataloged + row.Unlocked + row.Locked
		exp := row.Locked
		if !requireLocked {
			exp += row.Unlocked
		}
		var reviewN, exportN int
		switch rs {
		case "unassigned":
			reviewN, exportN = got.Review.Unassigned, got.Exportable.Unassigned
		case "pending":
			reviewN, exportN = got.Review.Pending, got.Exportable.Pending
		case "approved":
			reviewN, exportN = got.Review.Approved, got.Exportable.Approved
		case "rejected":
			reviewN, exportN = got.Review.Rejected, got.Exportable.Rejected
		}
		if reviewN != rowTotal || exportN != exp {
			t.Errorf("%s：%s 的审核合计 %d、可导出 %d，期望 %d、%d", what, rs, reviewN, exportN, rowTotal, exp)
		}
		total += rowTotal
		cataloged += row.Unlocked + row.Locked
		col["uncataloged"] += row.Uncataloged
		col["unlocked"] += row.Unlocked
		col["locked"] += row.Locked
	}
	c := got.Catalog
	if got.Total != total || got.Review.Total != total || c.Total != total || c.Cataloged != cataloged ||
		c.Uncataloged != col["uncataloged"] || c.Unlocked != col["unlocked"] || c.Locked != col["locked"] {
		t.Errorf("%s：合计不对：%+v", what, got)
	}
}

// TestCatalogSummary：看板矩阵（4 种审核状态 × 3 种归档状态）、可导出、按文件夹逐层展开，计数和 SQL 直接数的一致。
func TestCatalogSummary(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "DB")
	_, _, _, _, org := c.mkChain()
	admin := c.admin

	// 根 → A（盒：直接放 3 个文件，下面 A1、A2）；根 → B。根里直接放 2 个文件。
	c.mkFile(uuid.Nil, "unassigned")
	c.mkFile(uuid.Nil, "approved")
	fa, _ := c.mkFolder(uuid.Nil, "A", "pending", "approved", "rejected")
	fa1, a1Files := c.mkFolder(fa, "A1", "approved", "approved", "unassigned", "pending", "approved")
	c.mkFolder(fa, "A2", "approved", "pending")
	fb, _ := c.mkFolder(uuid.Nil, "B", "approved", "approved", "approved")
	c.Exec(`UPDATE folder_files SET deleted_at = now() WHERE file_id = $1`, a1Files[4]) // 已删除的不算

	// A1 归档成未锁定的一件（4 页，含一页待审、一页待分配），B 归档并锁定（3 页都通过）。
	draft := c.archiveOne(org.ID, fa1, "")
	locked := c.archiveOne(org.ID, fb, "")
	c.act(locked.ID, "lock").Expect(200)

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
		checkCounts(t, s.FolderName+" 子树", s.Counts, sqlMatrix(c.Env, c.pid, s.FolderID, false), requireLocked)
		checkCounts(t, s.FolderName+" 直接", s.DirectCounts, sqlMatrix(c.Env, c.pid, s.FolderID, true), requireLocked)
		for _, ch := range s.Children {
			checkCounts(t, ch.FolderName, ch.Counts, sqlMatrix(c.Env, c.pid, ch.FolderID, false), requireLocked)
		}
	}

	// 根（整个项目）：总数 14（不含已删除的），子文件夹 A、B 按 sort_no 排。
	root := get(nil)
	if root.FolderID != c.folder || root.ParentID != nil || len(root.Children) != 2 || root.Children[0].FolderID != fa ||
		root.Children[1].FolderID != fb || root.Counts.Total != 14 || root.DirectCounts.Total != 2 ||
		root.Children[0].Counts.Total != 9 {
		t.Fatalf("根：%+v", root)
	}
	// 几个关键格子（防止 SQL 和接口一起错）：通过且已锁定 3，通过且未锁定 2（A1），通过且未归档 3。
	if m := root.Counts.Matrix["approved"]; m.Locked != 3 || m.Unlocked != 2 || m.Uncataloged != 3 ||
		root.Counts.Exportable.Approved != 3 || root.Counts.Exportable.Total != 3 || root.Counts.Catalog.Cataloged != 7 {
		t.Fatalf("矩阵：%+v", root.Counts)
	}
	check(root, true)
	sub := get(&fa)
	if sub.ParentID == nil || *sub.ParentID != c.folder || len(sub.Children) != 2 || sub.Children[0].FolderID != fa1 {
		t.Fatalf("A：%+v", sub)
	}
	check(sub, true)

	// export.require_locked_item 关掉：可导出算全部已归档的（修改中的除外，导出预检不就绪）。
	c.setSettings(map[string]any{"export": map[string]any{"require_locked_item": false}})
	root = get(nil)
	if root.Counts.Exportable.Total != 7 || root.Counts.Exportable.Pending != 1 {
		t.Fatalf("不要求锁定：%+v", root.Counts.Exportable)
	}
	check(root, false)
	c.unseal(draft.ID).Expect(200)
	root = get(nil)
	if root.Counts.Exportable.Total != 3 || root.Counts.Matrix["approved"].Unlocked != 2 {
		t.Fatalf("修改中的不可导出：%+v", root.Counts)
	}

	// 参数：不合法 400；别的项目的、不存在的文件夹 404。
	base := "/api/projects/" + c.pid.String() + "/catalog-summary?folder_id="
	admin.Get(base+"x").ExpectError(400, "invalid_input")
	admin.Get(base+uuid.NewString()).ExpectError(404, "not_found")
	other := c.CreateProject("DZ")
	admin.Get(base+other.RootFolderID.String()).ExpectError(404, "not_found")
}
