package apitest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 整个文件夹归档、封存、解除封存、重新归档、撤销归档（M9-1，requirements 5.5"封存"、5.9"归档"）。

// sealedError 是 409 folder_archived 的错误体。
type sealedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details struct {
		FolderID      uuid.UUID   `json:"folder_id"`
		FolderName    string      `json:"folder_name"`
		ItemID        uuid.UUID   `json:"item_id"`
		ItemNo        int         `json:"item_no"`
		ArchiveNo     *string     `json:"archive_no"`
		Title         string      `json:"title"`
		CategoryNames []string    `json:"category_names"`
		Locked        bool        `json:"locked"`
		FolderIDs     []uuid.UUID `json:"folder_ids"`
	} `json:"details"`
}

// folderArchive 是 children、GET folders/{id} 的 archive。
type folderArchive struct {
	ItemID         uuid.UUID `json:"item_id"`
	ItemNo         int       `json:"item_no"`
	ArchiveNo      *string   `json:"archive_no"`
	Title          string    `json:"title"`
	CategoryNames  []string  `json:"category_names"`
	Status         string    `json:"status"`
	Locked         bool      `json:"locked"`
	SourceStatus   string    `json:"source_status"`
	UnsealedByName *string   `json:"unsealed_by_name"`
	UnsealedAt     *string   `json:"unsealed_at"`
	UnsealReason   *string   `json:"unseal_reason"`
}

// archiveOf 是 parent 的 children 里子文件夹 id 的归档信息（没有这个子文件夹时测试失败）。
func archiveOf(t *testing.T, c *Client, parent, id uuid.UUID) *folderArchive {
	t.Helper()
	var res struct {
		Folders []struct {
			ID      uuid.UUID      `json:"id"`
			Archive *folderArchive `json:"archive"`
		} `json:"folders"`
	}
	c.Get("/api/folders/" + parent.String() + "/children").Expect(http.StatusOK).JSON(&res)
	for _, f := range res.Folders {
		if f.ID == id {
			return f.Archive
		}
	}
	t.Fatalf("children 里没有 %s", id)
	return nil
}

// TestCatalogArchive：归档的预览和确认、展开上级文件夹、跳过的原因、题名、件号档号、页序、日志、审计、事件、错误码。
func TestCatalogArchive(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "AR")
	_, _, _, _, org := c.mkChain()
	admin := c.admin

	// 盒 1：件 A（3 页）、件 B（1 页）、空、有下级（自己 1 个文件、下面 1 个子文件夹）；另有单独的件 C（2 页）。
	box, _ := c.mkFolder(uuid.Nil, "盒 1")
	fa, aFiles := c.mkFolder(box, "件 A", "approved", "pending", "unassigned")
	fb, _ := c.mkFolder(box, "件 B", "approved")
	empty, _ := c.mkFolder(box, "空")
	nested, _ := c.mkFolder(box, "有下级", "approved")
	c.mkFolder(nested, "下级", "approved")
	fc, _ := c.mkFolder(uuid.Nil, "件 C", "approved", "approved")
	c.mkFile(box, "approved") // 盒子里直接放的两个文件：不归档，loose_files 提示
	c.mkFile(box, "pending")
	path := "/api/archive-categories/" + org.ID.String() + "/items:from-folders"
	const prefix = "Z109-WS·2025-Y-BGS-"

	// 预览：不写库；盒子展开成直接子文件夹，件 C 自己成一件；件号依次、档号按模板算；跳过的给原因。
	titles := map[string]any{fa.String(): " 关于 A 的通知 "}
	pv := Decode[archiveResult](admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{box, fc, box}, "titles": titles,
		"dry_run": true}).Expect(http.StatusOK))
	var got []string
	for _, it := range pv.Items {
		got = append(got, it.FolderName+":"+it.Title+":"+str(it.ArchiveNo)+":"+string(rune('0'+it.PageCount)))
	}
	if !pv.DryRun || pv.CategoryID != org.ID || strings.Join(got, " ") !=
		"件 A:关于 A 的通知:"+prefix+"0001:3 件 B:件 B:"+prefix+"0002:1 件 C:件 C:"+prefix+"0003:2" ||
		pv.Items[0].ItemID != nil || pv.Items[0].FolderPath != "AR 项目/盒 1/件 A" || len(pv.Skipped) != 2 ||
		pv.Skipped[0].FolderID != empty || pv.Skipped[0].Reason != "empty" ||
		pv.Skipped[1].FolderID != nested || pv.Skipped[1].Reason != "has_subfolders" || pv.Skipped[1].SubfolderCount != 1 ||
		len(pv.LooseFiles) != 1 || pv.LooseFiles[0].FolderID != box || pv.LooseFiles[0].FileCount != 2 ||
		pv.LooseFiles[0].FolderPath != "AR 项目/盒 1" {
		t.Fatalf("预览：%v %+v", got, pv)
	}
	// 选中的是件文件夹本身（没有当上级展开）：loose_files 为空数组。
	if one := Decode[archiveResult](admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{fc}, "dry_run": true}).
		Expect(http.StatusOK)); one.LooseFiles == nil || len(one.LooseFiles) != 0 {
		t.Fatalf("loose_files：%+v", one.LooseFiles)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM archive_items WHERE project_id = $1`, c.pid); n != 0 {
		t.Fatalf("预览写了库：%d", n)
	}

	// 归档：一个事务建全部件（draft、sealed），页 = 文件夹里的文件按 sort_no；先 files.changed、folders.changed，
	// 再 catalog.changed。
	s := openStream(t, admin, c.pid)
	res := Decode[archiveResult](admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{box, fc}, "titles": titles}).
		Expect(http.StatusCreated))
	if res.DryRun || len(res.Items) != 3 || res.Items[0].ItemID == nil || len(res.Skipped) != 2 {
		t.Fatalf("归档：%+v", res)
	}
	a := c.item(*res.Items[0].ItemID)
	if a.Title != "关于 A 的通知" || a.ItemNo != 1 || str(a.ArchiveNo) != prefix+"0001" || a.Status != "draft" ||
		a.SourceStatus != "sealed" || a.SourceFolderID != fa || a.SourceFolderPath != "AR 项目/盒 1/件 A" || a.RowVersion != 1 ||
		!slices.Equal(a.pageIDs(), aFiles) || a.Pages[2].PageNo != 3 || a.PageCounts["pending"] != 1 {
		t.Fatalf("A：%+v", a)
	}
	ev := s.next(t)
	if ev["type"] != "files.changed" || !hasIDs(ev["folder_ids"], fa, fb, fc) || len(idsOf(ev["file_ids"])) != 6 {
		t.Fatalf("事件 1：%v", ev)
	}
	if ev = s.next(t); ev["type"] != "folders.changed" || len(idsOf(ev["folder_ids"])) != 3 || !hasIDs(ev["folder_ids"], fa, fb, fc) {
		t.Fatalf("事件 2：%v", ev)
	}
	if ev = s.next(t); ev["type"] != "catalog.changed" || len(idsOf(ev["item_ids"])) != 3 || !hasIDs(ev["category_ids"], org.ID) {
		t.Fatalf("事件 3：%v", ev)
	}
	// 审计 archive（来源文件夹和路径、页数）；每页一条 link。
	au := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'archive'`, a.ID)
	if af := au["after"].(map[string]any); af["page_count"] != float64(3) || af["source_folder_path"] != "AR 项目/盒 1/件 A" ||
		af["source_folder_id"] != fa.String() || af["title"] != "关于 A 的通知" {
		t.Fatalf("archive 审计：%v", au)
	}
	if l := fileLogs(c.Env, aFiles[1]); len(l) != 1 || l[0].Action != "link" || l[0].Detail["page_no"] != float64(2) ||
		l[0].Detail["item_no"] != float64(1) || l[0].Detail["folder_path"] != "AR 项目/盒 1/件 A" {
		t.Fatalf("link 日志：%+v", l)
	}

	// 再归档：都已归档或不合格，一件都建不出 409 invalid_state，details 是预览（archived 带那一件）。
	r := admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{box}}).ExpectError(http.StatusConflict, "invalid_state")
	var again struct {
		Details archiveResult `json:"details"`
	}
	r.JSON(&again)
	if len(again.Details.Items) != 0 || len(again.Details.Skipped) != 4 || again.Details.Skipped[0].Reason != "archived" ||
		again.Details.Skipped[0].Item == nil || again.Details.Skipped[0].Item.ItemNo != 1 ||
		!slices.Equal(again.Details.Skipped[0].Item.CategoryNames, []string{"全宗", "文书", "2025 年", "永久", "办公室"}) {
		t.Fatalf("再归档：%+v", again.Details)
	}

	// 超过 2000 页跳过（直接写库造 2001 个文件）。
	big, _ := c.mkFolder(uuid.Nil, "大")
	c.Exec(`WITH f AS (INSERT INTO files (id) SELECT gen_random_uuid() FROM generate_series(1, 2001) RETURNING id)
INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, created_by)
SELECT gen_random_uuid(), $1, $2, f.id, 'p' || row_number() OVER () || '.jpg', row_number() OVER (), u.id
FROM f, users u WHERE u.username = 'admin'`, c.pid, big)
	pv = Decode[archiveResult](admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{big}, "dry_run": true}).Expect(http.StatusOK))
	if len(pv.Items) != 0 || len(pv.Skipped) != 1 || pv.Skipped[0].Reason != "too_many_pages" || pv.Skipped[0].PageCount != 2001 {
		t.Fatalf("超过 2000 页：%+v", pv)
	}

	// 一次要建的件合计超过 5 万页：400（预览也是）。大盒里 26 个件文件夹：1 + 25 × 2000 = 50001 页（直接写库）。
	bigBox, _ := c.mkFolder(uuid.Nil, "大盒")
	var parts []uuid.UUID
	for i := range 26 {
		f, _ := c.mkFolder(bigBox, "卷 "+string(rune('A'+i)))
		parts = append(parts, f)
	}
	c.Exec(`WITH t AS (SELECT gen_random_uuid() AS fid, f.id AS folder_id, g
  FROM unnest($2::uuid[]) WITH ORDINALITY AS f(id, i)
  CROSS JOIN LATERAL generate_series(1, CASE WHEN f.i = 1 THEN 1 ELSE 2000 END) g),
ins AS (INSERT INTO files (id) SELECT fid FROM t)
INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, created_by)
SELECT gen_random_uuid(), $1, t.folder_id, t.fid, 'p' || t.g || '.jpg', t.g, u.id FROM t, users u WHERE u.username = 'admin'`,
		c.pid, parts)
	for _, dry := range []bool{true, false} {
		if m := errOf(admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{bigBox}, "dry_run": dry}).
			ExpectError(http.StatusBadRequest, "invalid_input")).Message; !strings.Contains(m, "合计 50001 页") || !strings.Contains(m, "50000") {
			t.Fatalf("页数上限：%s", m)
		}
	}

	// 错误码：参数 400；文件夹不存在、别的项目的 404（details.ids）；节点不存在 404；根节点 409；题名不合法 400。
	admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{}}).ExpectError(http.StatusBadRequest, "invalid_input")
	many := make([]uuid.UUID, 501)
	for i := range many {
		many[i] = uuid.New()
	}
	admin.Post(path, map[string]any{"folder_ids": many}).ExpectError(http.StatusBadRequest, "invalid_input")
	other := c.CreateProject("AY")
	ghost := uuid.New()
	e := errOf(admin.Post(path, map[string]any{"folder_ids": []uuid.UUID{fb, ghost, other.RootFolderID}}).
		ExpectError(http.StatusNotFound, "not_found"))
	if !slices.Equal(e.Details.IDs, []uuid.UUID{ghost, other.RootFolderID}) {
		t.Fatalf("404 details：%+v", e)
	}
	admin.Post("/api/archive-categories/"+uuid.NewString()+"/items:from-folders", map[string]any{"folder_ids": []uuid.UUID{fb}}).
		ExpectError(http.StatusNotFound, "not_found")
	f2, _ := c.mkFolder(uuid.Nil, "件 D", "approved")
	c.archive(c.rootID, map[string]any{"folder_ids": []uuid.UUID{f2}}).ExpectError(http.StatusConflict, "invalid_state")
	c.archive(org.ID, map[string]any{"folder_ids": []uuid.UUID{f2}, "titles": map[string]any{f2.String(): "a\nb"}}).
		ExpectError(http.StatusBadRequest, "invalid_input")
}

// TestCatalogSeal：封存的文件夹挡住文件目录的每个写操作（409 folder_archived），重扫、审核、改备注照常；上级文件夹不
// 封存；children 和 GET folders/{id} 带归档信息；解除封存后可以整理。
func TestCatalogSeal(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "SL")
	c := &catalogEnv{Env: e.Env, t: t, pid: e.p.ID, rootID: e.p.CategoryRootID, folder: e.p.RootFolderID,
		adminID: e.UserID(AdminUsername), admin: e.admin}
	_, _, _, _, org := c.mkChain()
	admin := e.admin

	box := mkdir(admin, e.p.RootFolderID, "盒 A").ID
	fa := mkdir(admin, box, "件 A").ID
	a1, a2 := e.upload(fa, "a1.tif"), e.upload(fa, "a2.tif")
	e.assign(e.rv1ID, a2.FileID)
	reject(e.rv1, a2.FileID).Expect(http.StatusOK)
	o1 := e.upload(e.box.ID, "o1.tif") // 外面（盒 1）的文件
	other := mkdir(admin, e.p.RootFolderID, "别的").ID
	it := c.archiveOne(org.ID, fa, "")

	// 归档信息：children 的子文件夹、GET folders/{id}；没归档的为 null。
	arch := archiveOf(t, admin, box, fa)
	if arch == nil || arch.ItemID != it.ID || arch.ItemNo != 1 || str(arch.ArchiveNo) != "Z109-WS·2025-Y-BGS-0001" ||
		arch.Title != "件 A" || arch.Locked || arch.Status != "draft" || arch.SourceStatus != "sealed" || arch.UnsealedByName != nil ||
		len(arch.CategoryNames) != 5 {
		t.Fatalf("children 的归档信息：%+v", arch)
	}
	if archiveOf(t, admin, e.p.RootFolderID, box) != nil {
		t.Fatal("盒子不该有归档信息")
	}
	var fv struct {
		Archive *folderArchive `json:"archive"`
	}
	admin.Get("/api/folders/" + fa.String()).Expect(http.StatusOK).JSON(&fv)
	if fv.Archive == nil || fv.Archive.ItemID != it.ID {
		t.Fatalf("GET folders/{id}：%+v", fv.Archive)
	}

	// 封存挡住的写操作（details 带条目，界面据此引导"解除封存去修改"）。
	ff := func(id uuid.UUID) string { return "/api/folder-files/" + id.String() }
	blocked := []struct {
		name string
		do   func() *Response
	}{
		{"上传", func() *Response {
			return e.scan.Upload(fa, "new.tif", unique(sampleBytes(t, "tiff-g4-1bit.tif"), "SL-new"), nil)
		}},
		{"移出文件", func() *Response { return admin.Patch(ff(a1.FolderFileID), map[string]any{"folder_id": other}) }},
		{"移入文件", func() *Response { return admin.Patch(ff(o1.FolderFileID), map[string]any{"folder_id": fa}) }},
		{"批量移入", func() *Response {
			return admin.Post("/api/folder-files:move", map[string]any{"ids": []uuid.UUID{o1.FolderFileID}, "folder_id": fa})
		}},
		{"批量移出", func() *Response {
			return admin.Post("/api/folder-files:move", map[string]any{"ids": []uuid.UUID{a1.FolderFileID, a2.FolderFileID},
				"folder_id": other})
		}},
		{"复制进去", func() *Response { return admin.Post(ff(o1.FolderFileID)+"/copy", map[string]any{"folder_id": fa}) }},
		{"在里面复制", func() *Response { return admin.Post(ff(a1.FolderFileID)+"/copy", map[string]any{}) }},
		{"删除文件", func() *Response { return admin.Delete(ff(a1.FolderFileID)) }},
		{"批量删除（一个封存就整体不做）", func() *Response {
			return admin.Post("/api/folder-files:delete", map[string]any{"ids": []uuid.UUID{o1.FolderFileID, a1.FolderFileID}})
		}},
		{"文件改名", func() *Response { return admin.Patch(ff(a1.FolderFileID), map[string]any{"file_name": "x.tif"}) }},
		{"建子文件夹", func() *Response {
			return admin.Post("/api/folders", map[string]any{"parent_id": fa, "folder_name": "子"})
		}},
		{"文件夹改名", func() *Response {
			return admin.Patch("/api/folders/"+fa.String(), map[string]any{"folder_name": "改"})
		}},
		{"移动文件夹", func() *Response { return admin.Patch("/api/folders/"+fa.String(), map[string]any{"parent_id": other}) }},
		{"把文件夹移进去", func() *Response { return admin.Patch("/api/folders/"+other.String(), map[string]any{"parent_id": fa}) }},
		{"删除文件夹", func() *Response { return admin.Delete("/api/folders/" + fa.String()) }},
	}
	for _, b := range blocked {
		r := b.do()
		if r.Status != http.StatusConflict || r.Code() != "folder_archived" {
			t.Errorf("%s：%d %s", b.name, r.Status, r.Body)
			continue
		}
		d := Decode[sealedError](r).Details
		if d.FolderID != fa || d.ItemID != it.ID || d.ItemNo != 1 || d.Title != "件 A" || d.Locked || len(d.CategoryNames) != 5 ||
			!slices.Equal(d.FolderIDs, []uuid.UUID{fa}) {
			t.Errorf("%s 的 details：%+v", b.name, d)
		}
	}
	if n := QueryValue[int](e.Env, `SELECT count(*) FROM folder_files WHERE folder_id = $1 AND deleted_at IS NULL`, fa); n != 2 {
		t.Fatalf("封存的文件夹里的文件变了：%d", n)
	}
	if QueryValue[bool](e.Env, `SELECT deleted_at IS NULL FROM folder_files WHERE id = $1`, o1.FolderFileID) != true {
		t.Fatal("批量删除整体不做却删了外面的文件")
	}

	// 可以：改备注、重扫（同一文件出新版本，条目里自动是新版本）、从里面复制到别处；上级文件夹改名、移动（封存的跟着走）。
	admin.Patch("/api/folders/"+fa.String(), map[string]any{"remark": "A 架 3 层"}).Expect(http.StatusOK)
	rescan(e.scan, a2.FileID, "a2-重扫.tif", unique(sampleBytes(t, "tiff-g4-1bit.tif"), "SL-rescan")).Expect(http.StatusCreated)
	if p := c.item(it.ID).Pages[1]; p.FileID != a2.FileID || p.ReviewStatus == "rejected" {
		t.Fatalf("重扫后的页：%+v", p)
	}
	admin.Post(ff(a1.FolderFileID)+"/copy", map[string]any{"folder_id": other}).Expect(http.StatusCreated)
	admin.Patch("/api/folders/"+box.String(), map[string]any{"folder_name": "盒 A 改"}).Expect(http.StatusOK)
	admin.Patch("/api/folders/"+box.String(), map[string]any{"parent_id": other}).Expect(http.StatusOK)
	if p := c.item(it.ID).SourceFolderPath; p != "SL 项目/别的/盒 A 改/件 A" {
		t.Fatalf("盒子移动后来源文件夹的路径：%s", p)
	}
	// 删除上级：子树里有已归类的文件，照旧 409 file_cataloged。
	admin.Delete("/api/folders/"+box.String()).ExpectError(http.StatusConflict, "file_cataloged")

	// 锁定的：message 提示先解锁。
	approve(e.rv1, a2.FileID).Expect(http.StatusOK)
	e.assign(e.rv1ID, a1.FileID)
	approve(e.rv1, a1.FileID).Expect(http.StatusOK)
	c.act(it.ID, "lock").Expect(http.StatusOK)
	if r := admin.Delete(ff(a1.FolderFileID)).ExpectError(http.StatusConflict, "folder_archived"); !Decode[sealedError](r).Details.Locked ||
		!strings.Contains(errOf(r).Message, "先解锁") {
		t.Fatalf("锁定的：%s", r.Body)
	}

	// 解除封存（修改中）：和未归档的一样可以整理——上传、改名、删除（修改中的条目的页也能删）。
	c.act(it.ID, "unlock").Expect(http.StatusOK)
	c.unseal(it.ID).Expect(http.StatusOK)
	e.upload(fa, "a3.tif")
	admin.Patch(ff(a1.FolderFileID), map[string]any{"file_name": "a1-改.tif"}).Expect(http.StatusOK)
	admin.Delete(ff(a2.FolderFileID)).Expect(http.StatusNoContent)
	if arch := archiveOf(t, admin, box, fa); arch == nil || arch.SourceStatus != "modifying" || arch.UnsealedByName == nil ||
		*arch.UnsealedByName != "系统管理员" || arch.UnsealedAt == nil {
		t.Fatalf("修改中的归档信息：%+v", arch)
	}
}

// TestCatalogReseal：解除封存 → 改文件夹 → 重新归档的对比和结果；file_cataloged、来源文件夹不再是件文件夹的 409；
// 撤销归档。
func TestCatalogReseal(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "RS")
	_, _, _, _, org := c.mkChain()
	admin := c.admin
	fx, xs := c.mkFolder(uuid.Nil, "件 X", "approved", "approved", "approved")
	fy, ys := c.mkFolder(uuid.Nil, "件 Y", "approved")
	x := c.archiveOne(org.ID, fx, "")
	y := c.archiveOne(org.ID, fy, "")
	preview := func(id uuid.UUID) resealPreview {
		t.Helper()
		return Decode[resealPreview](admin.Get("/api/archive-items/" + id.String() + "/reseal-preview").Expect(http.StatusOK))
	}

	// 解除封存：→ modifying，记下谁、何时、原因；件号档号著录页不变；审计 unseal；先 folders.changed 再 catalog.changed。
	s := openStream(t, admin, c.pid)
	u := Decode[itemView](admin.Post("/api/archive-items/"+x.ID.String()+"/unseal",
		map[string]any{"row_version": x.RowVersion, "reason": " 漏扫 "}).Expect(http.StatusOK))
	if u.SourceStatus != "modifying" || u.Status != "draft" || u.UnsealedBy == nil || *u.UnsealedBy != c.adminID ||
		str(u.UnsealReason) != "漏扫" || u.RowVersion != x.RowVersion+1 || str(u.ArchiveNo) != str(x.ArchiveNo) || len(u.Pages) != 3 {
		t.Fatalf("解除封存：%+v", u)
	}
	if ev := s.next(t); ev["type"] != "folders.changed" || !hasIDs(ev["folder_ids"], fx) {
		t.Fatalf("解除封存的事件：%v", ev)
	}
	if ev := s.next(t); ev["type"] != "catalog.changed" {
		t.Fatalf("解除封存的事件：%v", ev)
	}
	if d := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'unseal'`, x.ID); d["reason"] != "漏扫" {
		t.Fatalf("unseal 审计：%v", d)
	}
	if p := preview(x.ID); p.Changed || p.OldPageCount != 3 || p.NewPageCount != 3 || p.Problem != nil {
		t.Fatalf("没改时的对比：%+v", p)
	}

	// 改：删掉第 2 页、加一个文件、第 1 页移出去再移回来（排到最后）。
	ffOf := func(fid uuid.UUID) uuid.UUID {
		return QueryValue[uuid.UUID](c.Env, `SELECT id FROM folder_files WHERE file_id = $1`, fid)
	}
	admin.Delete("/api/folder-files/" + ffOf(xs[1]).String()).Expect(http.StatusNoContent)
	x4 := c.mkFile(fx, "pending")
	c.moveFiles(c.folder, xs[0])
	c.moveFiles(fx, xs[0])
	p := preview(x.ID)
	if !p.Changed || p.OldPageCount != 3 || p.NewPageCount != 3 || len(p.Added) != 1 || p.Added[0].FileID != x4 || p.Added[0].PageNo != 2 ||
		len(p.Removed) != 1 || p.Removed[0].FileID != xs[1] || p.Removed[0].PageNo != 2 || !p.Removed[0].Deleted ||
		len(p.Moved) != 1 || p.Problem != nil || p.FolderPath != "RS 项目/件 X" {
		t.Fatalf("对比：%+v", p)
	}

	// 从另一件（修改中）的文件夹移过来的文件还在那一件里：对比给 problem，重新归档 409 file_cataloged；归档别的
	// 文件夹时跳过 file_cataloged（带那一件），一件都建不出 409 invalid_state，预览照常 200。
	c.unseal(y.ID).Expect(http.StatusOK)
	fz, _ := c.mkFolder(uuid.Nil, "件 Z")
	c.moveFiles(fz, ys[0])
	var zr struct {
		Details archiveResult `json:"details"`
	}
	c.archive(org.ID, map[string]any{"folder_ids": []uuid.UUID{fz}}).ExpectError(http.StatusConflict, "invalid_state").JSON(&zr)
	zp := Decode[archiveResult](c.archive(org.ID, map[string]any{"folder_ids": []uuid.UUID{fz}, "dry_run": true}).Expect(http.StatusOK))
	for _, r := range []archiveResult{zr.Details, zp} {
		if len(r.Items) != 0 || len(r.Skipped) != 1 || r.Skipped[0].Reason != "file_cataloged" || r.Skipped[0].Item == nil ||
			r.Skipped[0].Item.ItemID != y.ID || len(r.Skipped[0].Item.CategoryNames) != 5 {
			t.Fatalf("file_cataloged 跳过：%+v", r)
		}
	}
	c.moveFiles(fx, ys[0])
	if p := preview(x.ID); p.Problem == nil || p.Problem.Code != "file_cataloged" || len(p.Problem.Items) != 1 || p.Problem.Items[0].ItemID != y.ID {
		t.Fatalf("对比的 file_cataloged：%+v", p.Problem)
	}
	ce := Decode[catalogedError](c.reseal(x.ID).ExpectError(http.StatusConflict, "file_cataloged"))
	if len(ce.Details.IDs) != 1 || ce.Details.IDs[0] != ys[0] || ce.Details.Items[0].ItemID != y.ID {
		t.Fatalf("file_cataloged：%+v", ce)
	}
	// Y 的文件夹空了：重新归档 409 invalid_state（不要就撤销归档）。撤销归档：页一并移出，文件夹回到未归档。
	if m := errOf(c.reseal(y.ID).ExpectError(http.StatusConflict, "invalid_state")).Message; !strings.Contains(m, "撤销归档") {
		t.Fatalf("空了：%s", m)
	}
	admin.Delete("/api/archive-items/" + y.ID.String()).Expect(http.StatusNoContent)
	if l := fileLogs(c.Env, ys[0]); l[len(l)-1].Action != "unlink" || l[len(l)-1].Detail["item_deleted"] != true {
		t.Fatalf("撤销归档的 unlink：%+v", l[len(l)-1])
	}
	if archiveOf(t, admin, c.folder, fy) != nil {
		t.Fatal("撤销归档后文件夹应回到未归档")
	}

	// 重新归档：页换成文件夹现在的清单，→ sealed，清掉解除封存的人；link / unlink 日志、reseal 和 reorder 审计、事件。
	s = openStream(t, admin, c.pid)
	before := c.item(x.ID)
	got := Decode[itemView](c.reseal(x.ID).Expect(http.StatusOK))
	if !slices.Equal(got.pageIDs(), []uuid.UUID{xs[2], x4, xs[0], ys[0]}) || got.SourceStatus != "sealed" || got.UnsealedBy != nil ||
		got.UnsealReason != nil || got.RowVersion != before.RowVersion+1 || got.ItemNo != x.ItemNo || str(got.ArchiveNo) != str(x.ArchiveNo) {
		t.Fatalf("重新归档：%+v", got)
	}
	if l := fileLogs(c.Env, xs[1]); l[len(l)-1].Action != "unlink" {
		t.Fatalf("去掉的页：%+v", l)
	}
	if l := fileLogs(c.Env, x4); len(l) != 1 || l[0].Action != "link" || l[0].Detail["page_no"] != float64(2) {
		t.Fatalf("新增的页：%+v", l)
	}
	if acts := auditActions(c.Env, x.ID); !slices.Contains(acts, "reseal") || !slices.Contains(acts, "reorder") {
		t.Fatalf("审计：%v", acts)
	}
	if ev := s.next(t); ev["type"] != "files.changed" || !hasIDs(ev["file_ids"], x4, xs[1], ys[0]) {
		t.Fatalf("重新归档的事件 1：%v", ev)
	}
	if ev := s.next(t); ev["type"] != "folders.changed" || !hasIDs(ev["folder_ids"], fx) {
		t.Fatalf("重新归档的事件 2：%v", ev)
	}
	c.reseal(x.ID).ExpectError(http.StatusConflict, "invalid_state")

	// 来源文件夹有了子文件夹、被删了：409 invalid_state；只能撤销归档（修改中的文件夹可以删，页的文件随着删）。
	c.unseal(x.ID).Expect(http.StatusOK)
	sub := mkdir(admin, fx, "子").ID
	if p := preview(x.ID); p.Problem == nil || p.Problem.Code != "has_subfolders" {
		t.Fatalf("有子文件夹：%+v", p.Problem)
	}
	c.reseal(x.ID).ExpectError(http.StatusConflict, "invalid_state")
	admin.Delete("/api/folders/" + sub.String()).Expect(http.StatusNoContent)
	admin.Delete("/api/folders/" + fx.String()).Expect(http.StatusNoContent)
	if p := preview(x.ID); p.Problem == nil || p.Problem.Code != "folder_deleted" || len(p.Removed) != 4 {
		t.Fatalf("文件夹删了：%+v", p)
	}
	c.reseal(x.ID).ExpectError(http.StatusConflict, "invalid_state")
	admin.Delete("/api/archive-items/" + x.ID.String()).Expect(http.StatusNoContent)
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM archive_item_files WHERE project_id = $1`, c.pid); n != 0 {
		t.Fatalf("撤销归档后还有 %d 个挂接行", n)
	}
}

// resealPreview 是 reseal-preview 的响应。
type resealPreview struct {
	ItemID       uuid.UUID `json:"item_id"`
	FolderPath   string    `json:"folder_path"`
	OldPageCount int       `json:"old_page_count"`
	NewPageCount int       `json:"new_page_count"`
	Changed      bool      `json:"changed"`
	Added        []struct {
		FileID  uuid.UUID `json:"file_id"`
		PageNo  int       `json:"page_no"`
		Deleted bool      `json:"deleted"`
	} `json:"added"`
	Removed []struct {
		FileID  uuid.UUID `json:"file_id"`
		PageNo  int       `json:"page_no"`
		Deleted bool      `json:"deleted"`
	} `json:"removed"`
	Moved []struct {
		FileID   uuid.UUID `json:"file_id"`
		FromPage int       `json:"from_page"`
		ToPage   int       `json:"to_page"`
	} `json:"moved"`
	Problem *struct {
		Code  string `json:"code"`
		Items []struct {
			ItemID uuid.UUID `json:"item_id"`
		} `json:"items"`
	} `json:"problem"`
}

// TestCatalogModifying：项目里修改中的件（GET archive-items:modifying，M9-2）：没有时空清单；解除封存后列出（名称链、
// 来源路径、谁、原因、row_version），按解除封存的时间倒序；重新归档、撤销归档后不再列出；别的项目的不列；进不了项目 403。
func TestCatalogModifying(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "MD")
	_, _, _, _, org := c.mkChain()
	path := "/api/projects/" + c.pid.String() + "/archive-items:modifying"
	type modItem struct {
		ItemID           uuid.UUID `json:"item_id"`
		ItemNo           int       `json:"item_no"`
		ArchiveNo        *string   `json:"archive_no"`
		Title            string    `json:"title"`
		CategoryID       uuid.UUID `json:"category_id"`
		CategoryNames    []string  `json:"category_names"`
		SourceFolderID   uuid.UUID `json:"source_folder_id"`
		SourceFolderName string    `json:"source_folder_name"`
		SourceFolderPath string    `json:"source_folder_path"`
		UnsealedBy       *string   `json:"unsealed_by"`
		UnsealedByName   *string   `json:"unsealed_by_name"`
		UnsealedAt       *string   `json:"unsealed_at"`
		UnsealReason     *string   `json:"unseal_reason"`
		RowVersion       int       `json:"row_version"`
	}
	type modList struct {
		Items []modItem `json:"items"`
		Total int       `json:"total"`
	}
	list := func() modList {
		t.Helper()
		return Decode[modList](c.admin.Get(path).Expect(http.StatusOK))
	}

	// 没有修改中的：空清单（items 是 [] 不是 null）。
	x := c.mkItem(org.ID, map[string]any{"title": "甲"})
	y := c.mkItem(org.ID, map[string]any{"title": "乙"})
	if r := c.admin.Get(path).Expect(http.StatusOK); !strings.Contains(string(r.Body), `"items":[]`) || list().Total != 0 {
		t.Fatalf("没有修改中的：%s", r.Body)
	}

	// 解除封存两件（x 带原因），后解除的在前。
	c.admin.Post("/api/archive-items/"+x.ID.String()+"/unseal", map[string]any{"row_version": x.RowVersion, "reason": "漏扫"}).Expect(http.StatusOK)
	c.unseal(y.ID).Expect(http.StatusOK)
	got := list()
	if got.Total != 2 || len(got.Items) != 2 || got.Items[0].ItemID != y.ID || got.Items[1].ItemID != x.ID {
		t.Fatalf("两件修改中：%+v", got)
	}
	m := got.Items[1]
	xi := c.item(x.ID)
	if m.ItemNo != 1 || str(m.ArchiveNo) != "Z109-WS·2025-Y-BGS-0001" || m.Title != "甲" || m.CategoryID != org.ID ||
		strings.Join(m.CategoryNames, "/") != "全宗/文书/2025 年/永久/办公室" || m.SourceFolderName != "件 001" ||
		!strings.HasSuffix(m.SourceFolderPath, "/件 001") || str(m.UnsealReason) != "漏扫" || m.UnsealedByName == nil ||
		m.UnsealedAt == nil || m.UnsealedBy == nil || m.RowVersion != xi.RowVersion {
		t.Fatalf("修改中的件：%+v", m)
	}

	// 重新归档、撤销归档后不再列出。
	c.reseal(x.ID).Expect(http.StatusOK)
	c.admin.Delete("/api/archive-items/" + y.ID.String() + "?row_version=" + strconv.Itoa(c.item(y.ID).RowVersion)).Expect(http.StatusNoContent)
	if got := list(); got.Total != 0 || len(got.Items) != 0 {
		t.Fatalf("重新归档、撤销归档之后：%+v", got)
	}

	// 别的项目的不列；进不了项目 403。
	d := c.CreateProject("ME")
	other := &catalogEnv{Env: c.Env, t: t, pid: d.ID, rootID: d.CategoryRootID, folder: d.RootFolderID, adminID: c.adminID, admin: c.admin}
	_, _, _, _, org2 := other.mkChain()
	z := other.mkItem(org2.ID, nil)
	other.unseal(z.ID).Expect(http.StatusOK)
	if got := list(); got.Total != 0 {
		t.Fatalf("别的项目的修改中的件不该列出：%+v", got)
	}
	cat := c.As("cataloger")
	cat.Get(path).ExpectError(http.StatusForbidden, "forbidden")
	c.GrantProject(c.pid, c.PersonaID("cataloger"))
	cat.Get(path).Expect(http.StatusOK)
}
