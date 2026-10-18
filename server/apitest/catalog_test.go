package apitest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestPermsCatalog 是归类接口的菜单权限（归类的全部操作含解锁只要 catalog，用户 2026-10-19；归类信息、设置能进项目
// 即可；看板要 dashboard）。
func TestPermsCatalog(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                                   admin  manager  cataloger  review_assign  reviewer  dashboard  none
	GET    /api/projects/{pid}/catalog-settings          Y      Y         Y           Y             Y        Y          Y
	POST   /api/projects/{pid}/catalog-placements        Y      Y         Y           Y             Y        Y          Y
	GET    /api/files/{fid}/catalog                      Y      Y         Y           Y             Y        Y          Y
	GET    /api/projects/{pid}/catalog-summary           Y      Y         -           -             -        Y          -
	GET    /api/projects/{pid}/archive-categories        Y      Y         Y           -             -        -          -
	POST   /api/projects/{pid}/archive-categories        Y      Y         Y           -             -        -          -
	PATCH  /api/projects/{pid}/archive-categories/{id}   Y      Y         Y           -             -        -          -
	DELETE /api/projects/{pid}/archive-categories/{id}   Y      Y         Y           -             -        -          -
	GET    /api/archive-categories/{id}/items            Y      Y         Y           -             -        -          -
	POST   /api/archive-categories/{id}/items:from-folders Y    Y         Y           -             -        -          -
	GET    /api/archive-items/{id}                       Y      Y         Y           -             -        -          -
	PATCH  /api/archive-items/{id}                       Y      Y         Y           -             -        -          -
	DELETE /api/archive-items/{id}                       Y      Y         Y           -             -        -          -
	POST   /api/archive-items/{id}/lock                  Y      Y         Y           -             -        -          -
	POST   /api/archive-items/{id}/unlock                Y      Y         Y           -             -        -          -
	POST   /api/archive-items/{id}/unseal                Y      Y         Y           -             -        -          -
	GET    /api/archive-items/{id}/reseal-preview        Y      Y         Y           -             -        -          -
	POST   /api/archive-items/{id}/reseal                Y      Y         Y           -             -        -          -
	POST   /api/projects/{pid}/archive-items:lock        Y      Y         Y           -             -        -          -
	POST   /api/projects/{pid}/archive-items:renumber    Y      Y         Y           -             -        -          -
	`)

	// 去掉的接口：405 / 404（不再有新建空条目、按页挂接、标记完成、退回草稿）。
	admin := env.Admin()
	d := env.CreateProject("PC")
	for _, r := range []*Response{
		admin.Post("/api/archive-categories/"+d.CategoryRootID.String()+"/items", map[string]any{}),
		admin.Put("/api/archive-items/"+uuid.NewString()+"/files", map[string]any{}),
		admin.Post("/api/archive-items/"+uuid.NewString()+"/mark-done", map[string]any{}),
		admin.Post("/api/archive-items/"+uuid.NewString()+"/back-to-draft", map[string]any{}),
	} {
		if r.Status != http.StatusNotFound && r.Status != http.StatusMethodNotAllowed {
			t.Errorf("去掉的接口应是 404 / 405：%d %s", r.Status, r.Body)
		}
	}

	// 项目授权：有菜单但进不了项目 403（以资源 id 为参数的接口也一样），授权后放行。
	cat := env.As("cataloger")
	path := "/api/projects/" + d.ID.String() + "/archive-categories"
	cat.Get(path).ExpectError(http.StatusForbidden, "forbidden")
	cat.Get("/api/archive-categories/"+d.CategoryRootID.String()+"/items").ExpectError(http.StatusForbidden, "forbidden")
	env.GrantProject(d.ID, env.PersonaID("cataloger"))
	cat.Get(path).Expect(http.StatusOK)
}

// TestCatalogCategories：节点的新建、按需加载、改名、改编码、移动、删除，规则和错误码，审计和事件。
func TestCatalogCategories(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "CC")
	admin := c.admin
	s := openStream(t, admin, c.pid)

	fonds := c.mkCat(nil, "fonds", "Z109", " 全宗一 ")
	if fonds.ParentID == nil || *fonds.ParentID != c.rootID || fonds.PathCode != "00001.00001." || fonds.CategoryName != "全宗一" ||
		fonds.SortNo != 1 || fonds.ChildCount != 0 || fonds.ItemCount != 0 {
		t.Fatalf("新建：%+v", fonds)
	}
	ev := s.next(t)
	if ev["type"] != "catalog.changed" || len(ev["category_ids"].([]any)) != 2 || len(ev["item_ids"].([]any)) != 0 {
		t.Fatalf("新建的事件：%v", ev)
	}

	// 参数：类型、编码（默认模板的分隔符是 - 和 ·）、名称；同层编码重复（区分大小写）；父节点不存在。
	for _, b := range []map[string]any{
		{"category_type": "root", "category_code": "R", "category_name": "根"},
		{"category_type": "box", "category_code": "B", "category_name": "盒"},
		{"category_type": "fonds", "category_code": "Z-1", "category_name": "x"},
		{"category_type": "fonds", "category_code": "Z·1", "category_name": "x"},
		{"category_type": "fonds", "category_code": "Z 1", "category_name": "x"},
		{"category_type": "fonds", "category_code": "Z/1", "category_name": "x"},
		{"category_type": "fonds", "category_code": strings.Repeat("Z", 33), "category_name": "x"},
		{"category_type": "fonds", "category_code": "Z2", "category_name": "  "},
		{"category_type": "fonds", "category_code": "Z2", "category_name": strings.Repeat("名", 101)},
	} {
		admin.Post(c.catPath(), b).ExpectError(http.StatusBadRequest, "invalid_input")
	}
	admin.Post(c.catPath(), map[string]any{"category_type": "fonds", "category_code": "Z109", "category_name": "重复"}).
		ExpectError(http.StatusConflict, "conflict")
	z109lower := c.mkCat(nil, "fonds", "z109", "小写可以")
	admin.Post(c.catPath(), map[string]any{"parent_id": uuid.New(), "category_type": "year", "category_code": "2025", "category_name": "x"}).
		ExpectError(http.StatusNotFound, "not_found")

	// 按需加载：根节点下两个全宗；子树条目数含下级。
	ws := c.mkCat(&fonds, "category", "WS", "文书")
	year := c.mkCat(&ws, "year", "2025", "2025 年")
	c.mkItem(year.ID, nil)
	c.mkItem(ws.ID, nil)
	var list struct {
		Parent catNode   `json:"parent"`
		Items  []catNode `json:"items"`
	}
	admin.Get(c.catPath()).Expect(200).JSON(&list)
	if list.Parent.ID != c.rootID || list.Parent.ItemCount != 2 || len(list.Items) != 2 ||
		list.Items[0].ID != fonds.ID || list.Items[0].ChildCount != 1 || list.Items[0].ItemCount != 2 || list.Items[1].ID != z109lower.ID {
		t.Fatalf("根节点下：%+v", list)
	}
	admin.Get(c.catPath() + "?parent_id=" + ws.ID.String()).Expect(200).JSON(&list)
	if list.Parent.ItemCount != 2 || len(list.Items) != 1 || list.Items[0].ItemCount != 1 {
		t.Fatalf("门类下：%+v", list)
	}
	admin.Get(c.catPath()+"?parent_id="+uuid.NewString()).ExpectError(http.StatusNotFound, "not_found")
	admin.Get(c.catPath()+"?parent_id=x").ExpectError(http.StatusBadRequest, "invalid_input")

	// 根节点：原样提交是没有变化；改名、移动、删除 409 invalid_state。
	rootPath := c.catPath() + "/" + c.rootID.String()
	admin.Patch(rootPath, map[string]any{"category_code": "CC", "category_name": "CC 项目"}).Expect(200)
	admin.Patch(rootPath, map[string]any{"category_name": "新名字"}).ExpectError(http.StatusConflict, "invalid_state")
	admin.Delete(rootPath).ExpectError(http.StatusConflict, "invalid_state")

	// 改名（共享锁，不重算档号）；没有变化不写审计。
	wsPath := c.catPath() + "/" + ws.ID.String()
	var up catNode
	admin.Patch(wsPath, map[string]any{"category_name": "文书档案"}).Expect(200).JSON(&up)
	if up.CategoryName != "文书档案" || up.Renumbered != 0 {
		t.Fatalf("改名：%+v", up)
	}
	before := len(auditActions(c.Env, ws.ID))
	admin.Patch(wsPath, map[string]any{"category_name": " 文书档案 ", "category_code": "WS", "category_type": "category", "parent_id": fonds.ID}).Expect(200)
	if got := auditActions(c.Env, ws.ID); len(got) != before || got[0] != "create" || got[1] != "update" {
		t.Fatalf("审计：%v", got)
	}
	// 改编码：同层重复 409，含分隔符 400。
	z2 := c.mkCat(&fonds, "category", "KJ", "科技")
	admin.Patch(wsPath, map[string]any{"category_code": "KJ"}).ExpectError(http.StatusConflict, "conflict")
	admin.Patch(wsPath, map[string]any{"category_code": "W-S"}).ExpectError(http.StatusBadRequest, "invalid_input")

	// 移动：进自己的子树 409 tree_cycle；移到另一个全宗下，整棵子树换前缀，sort_no 排在最后。
	admin.Patch(wsPath, map[string]any{"parent_id": year.ID}).ExpectError(http.StatusConflict, "tree_cycle")
	admin.Patch(wsPath, map[string]any{"parent_id": ws.ID}).ExpectError(http.StatusConflict, "tree_cycle")
	admin.Patch(wsPath, map[string]any{"parent_id": uuid.New()}).ExpectError(http.StatusNotFound, "not_found")
	admin.Patch(wsPath, map[string]any{"parent_id": z109lower.ID}).Expect(200).JSON(&up)
	if *up.ParentID != z109lower.ID || up.PathCode != "00001.00002.00001." || up.ItemCount != 2 {
		t.Fatalf("移动：%+v", up)
	}
	if p := QueryValue[string](c.Env, "SELECT path_code FROM archive_categories WHERE id = $1", year.ID); p != "00001.00002.00001.00001." {
		t.Fatalf("子树换前缀：%s", p)
	}
	if got := auditActions(c.Env, ws.ID); got[len(got)-1] != "move" {
		t.Fatalf("移动的审计：%v", got)
	}

	// 删除：子树里还有条目 409 category_not_empty；删掉条目后可以删，整棵子树软删除，编码可以再用。
	admin.Delete(wsPath).ExpectError(http.StatusConflict, "category_not_empty")
	items := Decode[struct {
		Items []itemView `json:"items"`
	}](admin.Get("/api/archive-categories/" + year.ID.String() + "/items"))
	items2 := Decode[struct {
		Items []itemView `json:"items"`
	}](admin.Get("/api/archive-categories/" + ws.ID.String() + "/items"))
	for _, it := range append(items.Items, items2.Items...) {
		admin.Delete("/api/archive-items/" + it.ID.String()).Expect(http.StatusNoContent)
	}
	admin.Delete(wsPath).Expect(http.StatusNoContent)
	if n := QueryValue[int](c.Env, "SELECT count(*) FROM archive_categories WHERE id IN ($1, $2) AND deleted_at IS NOT NULL", ws.ID, year.ID); n != 2 {
		t.Fatalf("子树软删除：%d", n)
	}
	admin.Get(c.catPath()+"?parent_id="+ws.ID.String()).ExpectError(http.StatusNotFound, "not_found")
	admin.Delete(wsPath).ExpectError(http.StatusNotFound, "not_found")
	c.mkCat(&z109lower, "category", "WS", "编码可以再用")
	_ = z2
}

// TestItemStatusTable 是条目状态表（requirements 5.9）：每个动作在 draft·sealed、draft·modifying、locked 下的结果。
func TestItemStatusTable(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "ST")
	_, _, _, ret, org := c.mkChain()
	other := c.mkCat(&ret, "org", "RSC", "人事处")
	admin := c.admin

	// newItem 造一个处在 state 的条目（一页审核通过的，锁定要有页、有档号、页都通过）。
	newItem := func(state string) itemView {
		it := c.mkItem(org.ID, map[string]any{"title": "条目"})
		switch state {
		case "modifying":
			c.unseal(it.ID).Expect(200)
		case "locked":
			c.act(it.ID, "lock").Expect(200)
		}
		return c.item(it.ID)
	}
	type outcome struct {
		status int
		code   string // 错误码；成功时为空
		after  string // 成功后的"状态·封存状态"（删除为空）
	}
	ok := func(after string) outcome { return outcome{status: 200, after: after} }
	fail := func(status int, code string) outcome { return outcome{status: status, code: code} }
	edit := func(it itemView, body map[string]any) *Response {
		body["row_version"] = it.RowVersion
		return admin.Patch("/api/archive-items/"+it.ID.String(), body)
	}
	act := func(it itemView, action string) *Response {
		return admin.Post("/api/archive-items/"+it.ID.String()+"/"+action, map[string]any{"row_version": it.RowVersion})
	}
	const sealed, modifying, locked = "draft·sealed", "draft·modifying", "locked·sealed"
	states := map[string]string{"sealed": sealed, "modifying": modifying, "locked": locked}
	actions := []struct {
		name string
		do   func(it itemView) *Response
		want map[string]outcome
	}{
		{"改著录", func(it itemView) *Response { return edit(it, map[string]any{"title": "新题名"}) },
			map[string]outcome{"sealed": ok(sealed), "modifying": ok(modifying), "locked": fail(409, "item_locked")}},
		{"移动", func(it itemView) *Response { return edit(it, map[string]any{"category_id": other.ID}) },
			map[string]outcome{"sealed": ok(sealed), "modifying": ok(modifying), "locked": fail(409, "item_locked")}},
		{"改件号", func(it itemView) *Response { return edit(it, map[string]any{"item_no": 100 + it.ItemNo}) },
			map[string]outcome{"sealed": ok(sealed), "modifying": ok(modifying), "locked": fail(409, "item_locked")}},
		{"解除封存", func(it itemView) *Response { return act(it, "unseal") },
			map[string]outcome{"sealed": ok(modifying), "modifying": fail(409, "invalid_state"), "locked": fail(409, "item_locked")}},
		{"重新归档", func(it itemView) *Response { return act(it, "reseal") },
			map[string]outcome{"sealed": fail(409, "invalid_state"), "modifying": ok(sealed), "locked": fail(409, "item_locked")}},
		{"锁定", func(it itemView) *Response { return act(it, "lock") },
			map[string]outcome{"sealed": ok(locked), "modifying": fail(409, "invalid_state"), "locked": fail(409, "invalid_state")}},
		{"解锁", func(it itemView) *Response { return act(it, "unlock") },
			map[string]outcome{"sealed": fail(409, "invalid_state"), "modifying": fail(409, "invalid_state"), "locked": ok(sealed)}},
		{"撤销归档", func(it itemView) *Response {
			return admin.Delete("/api/archive-items/" + it.ID.String() + "?row_version=" + strconv.Itoa(it.RowVersion))
		}, map[string]outcome{"sealed": {status: 204}, "modifying": {status: 204}, "locked": fail(409, "item_locked")}},
	}
	for _, a := range actions {
		for _, state := range []string{"sealed", "modifying", "locked"} {
			want := a.want[state]
			it := newItem(state)
			res := a.do(it)
			if res.Status != want.status || (want.code != "" && res.Code() != want.code) {
				t.Errorf("%s / %s：%d %s，期望 %d %s；%s", a.name, state, res.Status, res.Code(), want.status, want.code, res.Body)
				continue
			}
			if want.code != "" {
				if got := c.item(it.ID); got.Status+"·"+got.SourceStatus != states[state] || got.RowVersion != it.RowVersion {
					t.Errorf("%s / %s：失败后条目变了：%s·%s v%d", a.name, state, got.Status, got.SourceStatus, got.RowVersion)
				}
				continue
			}
			if want.status == 204 {
				admin.Get("/api/archive-items/"+it.ID.String()).ExpectError(404, "not_found")
				continue
			}
			got := Decode[itemView](res)
			if got.Status+"·"+got.SourceStatus != want.after || got.RowVersion != it.RowVersion+1 {
				t.Errorf("%s / %s：→ %s·%s v%d，期望 %s v%d", a.name, state, got.Status, got.SourceStatus, got.RowVersion, want.after, it.RowVersion+1)
			}
		}
	}
	// row_version 过期 409 conflict（状态先于版本）；缺 row_version 400；没有变化的 PATCH 不加版本。
	it := c.mkItem(org.ID, map[string]any{"title": "版本"})
	admin.Patch("/api/archive-items/"+it.ID.String(), map[string]any{"row_version": it.RowVersion + 1, "title": "x"}).ExpectError(409, "conflict")
	admin.Post("/api/archive-items/"+it.ID.String()+"/lock", map[string]any{"row_version": 99}).ExpectError(409, "conflict")
	admin.Post("/api/archive-items/"+it.ID.String()+"/unseal", map[string]any{"row_version": 99}).ExpectError(409, "conflict")
	admin.Post("/api/archive-items/"+it.ID.String()+"/unlock", map[string]any{"row_version": 99}).ExpectError(409, "invalid_state")
	admin.Post("/api/archive-items/"+it.ID.String()+"/reseal", map[string]any{"row_version": 99}).ExpectError(409, "invalid_state")
	admin.Post("/api/archive-items/"+it.ID.String()+"/unseal", map[string]any{}).ExpectError(400, "invalid_input")
	admin.Post("/api/archive-items/"+it.ID.String()+"/unseal", map[string]any{"row_version": it.RowVersion,
		"reason": strings.Repeat("因", 201)}).ExpectError(400, "invalid_input")
	admin.Patch("/api/archive-items/"+it.ID.String(), map[string]any{"title": "x"}).ExpectError(400, "invalid_input")
	admin.Patch("/api/archive-items/"+it.ID.String(), map[string]any{"row_version": 0}).ExpectError(400, "invalid_input")
	same := Decode[itemView](admin.Patch("/api/archive-items/"+it.ID.String(), map[string]any{"row_version": it.RowVersion, "title": " 版本 "}).Expect(200))
	if same.RowVersion != it.RowVersion {
		t.Fatalf("没有变化也加了版本：%d", same.RowVersion)
	}
	admin.Delete("/api/archive-items/"+it.ID.String()+"?row_version=9").ExpectError(409, "conflict")

	// 解锁只要 catalog（用户 2026-10-19）：归类员也能解锁；解锁后是未锁定、封存。
	lk := newItem("locked")
	c.GrantProject(c.pid, c.PersonaID("cataloger"))
	got := Decode[itemView](c.As("cataloger").Post("/api/archive-items/"+lk.ID.String()+"/unlock",
		map[string]any{"row_version": lk.RowVersion}).Expect(200))
	if got.Status != "draft" || got.SourceStatus != "sealed" || got.LockedAt != nil {
		t.Fatalf("归类员解锁：%+v", got)
	}
}

// TestItemLock：锁定的前置条件（不在修改中、必填在锁定时查、档号不空、全部页通过）、锁定时间和审计、批量锁定。
func TestItemLock(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "LK")
	_, _, year, _, org := c.mkChain()
	admin := c.admin
	msgOf := func(r *Response) string { return errOf(r).Message }

	// 有页没审核通过：3 页（通过、待审、驳回）。
	f, _ := c.mkFolder(uuid.Nil, "有页待审", "approved", "pending", "rejected")
	pending := c.archiveOne(org.ID, f, "")
	if m := msgOf(c.act(pending.ID, "lock").ExpectError(409, "invalid_state")); !strings.Contains(m, "有 2 页还没审核通过（例如第 2 页）") {
		t.Fatalf("没审核通过：%s", m)
	}
	noNo := c.mkItem(year.ID, map[string]any{"title": "缺级"})
	if m := msgOf(c.act(noNo.ID, "lock").ExpectError(409, "invalid_state")); !strings.Contains(m, "没有档号") || !strings.Contains(m, "保管期限、机构") {
		t.Fatalf("没有档号：%s", m)
	}
	// 修改中不能锁定（409，先于著录检查）。
	mod := c.mkItem(org.ID, map[string]any{"title": "修改中"})
	c.unseal(mod.ID).Expect(200)
	if m := msgOf(c.act(mod.ID, "lock").ExpectError(409, "invalid_state")); !strings.Contains(m, "修改中") {
		t.Fatalf("修改中：%s", m)
	}
	// 必填在锁定时查：题名清空、必填著录项没填 → 400 details.fields；草稿时随便改。
	c.setSettings(map[string]any{"item_fields": []any{map[string]any{"key": "author", "label": "责任者", "type": "text", "required": true}}})
	req := c.mkItem(org.ID, map[string]any{"title": "必填"})
	c.patchItem(req.ID, map[string]any{"title": ""}).Expect(200)
	if e := errOf(c.act(req.ID, "lock").ExpectError(400, "invalid_input")); len(e.Details.Fields) != 2 ||
		e.Details.Fields[0].Key != "title" || e.Details.Fields[1].Key != "author" {
		t.Fatalf("必填：%+v", e)
	}
	c.patchItem(req.ID, map[string]any{"title": "必填", "extra_fields": map[string]any{"author": "张三"}}).Expect(200)
	c.act(req.ID, "lock").Expect(200)
	c.setSettings(map[string]any{})

	// 详情：页数、各审核状态的页数、页的文件名和文件夹路径、来源文件夹。
	d := c.item(pending.ID)
	if d.PageCount != 3 || d.PageCounts["pending"] != 1 || d.PageCounts["rejected"] != 1 || len(d.Pages) != 3 ||
		d.Pages[1].PageNo != 2 || d.Pages[1].ReviewStatus != "pending" || d.Pages[0].FolderPath != "LK 项目/有页待审" ||
		d.SourceFolderID != f || d.SourceFolderPath != "LK 项目/有页待审" || d.SourceStatus != "sealed" || d.Title != "有页待审" {
		t.Fatalf("详情：%+v", d)
	}

	// 关掉"锁定要求全部页通过"后可以锁定；锁定时间来自 lock 审计，审计带 archive_no、page_count。
	c.setSettings(map[string]any{"catalog": map[string]any{"lock_requires_approved": false}})
	lk := Decode[itemView](c.act(pending.ID, "lock").Expect(200))
	if lk.Status != "locked" || lk.LockedAt == nil || lk.ArchiveNo == nil {
		t.Fatalf("锁定：%+v", lk)
	}
	detail := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'lock'`, pending.ID)
	if detail["page_count"] != float64(3) || detail["archive_no"] != *lk.ArchiveNo || detail["lock_requires_approved"] != false {
		t.Fatalf("lock 审计：%v", detail)
	}
	ul := Decode[itemView](c.act(pending.ID, "unlock").Expect(200))
	if ul.Status != "draft" || ul.LockedAt != nil {
		t.Fatalf("解锁：%+v", ul)
	}
	c.setSettings(map[string]any{})

	// 批量锁定：不存在的 404（details.ids）；有一个不满足就整体不做（409，details.ids 列全部）；已锁定的跳过。
	ok1 := c.mkItem(org.ID, map[string]any{"title": "可以锁定 1"})
	ok2 := c.mkItem(org.ID, map[string]any{"title": "可以锁定 2"})
	lockPath := "/api/projects/" + c.pid.String() + "/archive-items:lock"
	ghost := uuid.New()
	e := errOf(admin.Post(lockPath, map[string]any{"item_ids": []uuid.UUID{ok1.ID, ghost}}).ExpectError(404, "not_found"))
	if len(e.Details.IDs) != 1 || e.Details.IDs[0] != ghost {
		t.Fatalf("404 details：%+v", e)
	}
	e = errOf(admin.Post(lockPath, map[string]any{"item_ids": []uuid.UUID{ok1.ID, mod.ID, noNo.ID, ok2.ID}}).ExpectError(409, "invalid_state"))
	if len(e.Details.IDs) != 2 || !slices.Contains(e.Details.IDs, mod.ID) || !slices.Contains(e.Details.IDs, noNo.ID) ||
		!strings.Contains(e.Message, "有 2 个条目不能锁定（1 个修改中（要先重新归档），1 个没有档号）") {
		t.Fatalf("409：%+v", e)
	}
	if c.item(ok1.ID).Status != "draft" {
		t.Fatal("整体不做：ok1 被锁定了")
	}
	c.act(ok1.ID, "lock").Expect(200)
	s := openStream(t, admin, c.pid)
	var res struct {
		Locked  int         `json:"locked"`
		Skipped int         `json:"skipped"`
		ItemIDs []uuid.UUID `json:"item_ids"`
	}
	admin.Post(lockPath, map[string]any{"item_ids": []uuid.UUID{ok2.ID, ok1.ID, ok2.ID}}).Expect(200).JSON(&res)
	if res.Locked != 1 || res.Skipped != 1 || len(res.ItemIDs) != 1 || res.ItemIDs[0] != ok2.ID || c.item(ok2.ID).Status != "locked" {
		t.Fatalf("批量锁定：%+v", res)
	}
	// 锁定改了来源文件夹显示的归档状态：先 folders.changed（来源文件夹）再 catalog.changed。
	if ev := s.next(t); ev["type"] != "folders.changed" || !slices.Equal(idsOf(ev["folder_ids"]), strIDs(ok2.SourceFolderID)) {
		t.Fatalf("锁定的事件：%v", ev)
	}
	if ev := s.next(t); ev["type"] != "catalog.changed" {
		t.Fatalf("锁定的事件：%v", ev)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action = 'lock' AND detail->>'batch' = 'true'`, ok2.ID); n != 1 {
		t.Fatalf("批量锁定的审计：%d", n)
	}
	admin.Post(lockPath, map[string]any{"item_ids": []uuid.UUID{}}).ExpectError(400, "invalid_input")
	many := make([]uuid.UUID, 501)
	for i := range many {
		many[i] = uuid.New()
	}
	admin.Post(lockPath, map[string]any{"item_ids": many}).ExpectError(400, "invalid_input")
}
