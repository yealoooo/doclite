package apitest

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (c *catalogEnv) renumber(categoryID *uuid.UUID) (res struct {
	CategoryID         uuid.UUID `json:"category_id"`
	Renumbered         int       `json:"renumbered"`
	SkippedLocked      int       `json:"skipped_locked"`
	ArchiveNoConflicts int       `json:"archive_no_conflicts"`
}) {
	c.t.Helper()
	body := map[string]any{}
	if categoryID != nil {
		body["category_id"] = categoryID
	}
	c.admin.Post("/api/projects/"+c.pid.String()+"/archive-items:renumber", body).Expect(http.StatusOK).JSON(&res)
	return
}

func (c *catalogEnv) patchCat(id uuid.UUID, body map[string]any) *Response {
	c.t.Helper()
	return c.admin.Patch(c.catPath()+"/"+id.String(), body)
}

// TestArchiveNoGeneration：按模板和祖先链生成档号、件号 max+1（算上已删除的）、改件号、移动条目、改节点编码和类型
// 时重算（锁定的不动、挡住）、按新模板重新生成。
func TestArchiveNoGeneration(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "AN")
	_, _, _, ret, org := c.mkChain()
	rsc := c.mkCat(&ret, "org", "RSC", "人事处")
	const prefix = "Z109-WS·2025-Y-"

	a := c.mkItem(org.ID, map[string]any{"title": "一"})
	if str(a.ArchiveNo) != prefix+"BGS-0001" || a.ItemNo != 1 || a.ArchiveNoReason != nil || a.Status != "draft" || a.RowVersion != 1 ||
		a.CategoryPath != "AN 项目/全宗/文书/2025 年/永久/办公室" {
		t.Fatalf("新建：%+v", a)
	}
	b := c.mkItem(org.ID, nil)
	c.admin.Delete("/api/archive-items/" + b.ID.String()).Expect(http.StatusNoContent)
	d := c.mkItem(org.ID, map[string]any{"title": "三"})
	if d.ItemNo != 3 || str(d.ArchiveNo) != prefix+"BGS-0003" {
		t.Fatalf("件号不回收：%d %s", d.ItemNo, str(d.ArchiveNo))
	}
	// 显式改件号：被已删除的占着、被未删除的占着都 409；空着的可以，档号跟着变。
	if m := errOf(c.patchItem(d.ID, map[string]any{"item_no": 2}).ExpectError(409, "conflict")).Message; !strings.Contains(m, "以前用过") {
		t.Fatalf("已删除的件号：%s", m)
	}
	if m := errOf(c.patchItem(d.ID, map[string]any{"item_no": 1}).ExpectError(409, "conflict")).Message; !strings.Contains(m, "已有第 1 件") {
		t.Fatalf("已占的件号：%s", m)
	}
	c.patchItem(d.ID, map[string]any{"item_no": 0}).ExpectError(400, "invalid_input")
	d = Decode[itemView](c.patchItem(d.ID, map[string]any{"item_no": 7}).Expect(200))
	if d.ItemNo != 7 || str(d.ArchiveNo) != prefix+"BGS-0007" || d.RowVersion != 2 {
		t.Fatalf("改件号：%+v", d)
	}
	// 移到别的节点：件号取目标的 max+1，档号重算；审计 move 带名称路径。
	d = Decode[itemView](c.patchItem(d.ID, map[string]any{"category_id": rsc.ID}).Expect(200))
	if d.CategoryID != rsc.ID || d.ItemNo != 1 || str(d.ArchiveNo) != prefix+"RSC-0001" {
		t.Fatalf("移动：%+v", d)
	}
	mv := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'move'`, d.ID)
	if mv["after"].(map[string]any)["category_path"] != "AN 项目/全宗/文书/2025 年/永久/人事处" || mv["before"].(map[string]any)["item_no"] != float64(7) {
		t.Fatalf("move 审计：%v", mv)
	}
	c.patchItem(d.ID, map[string]any{"category_id": uuid.New()}).ExpectError(404, "not_found")
	// 同一链上有多层同类型：取离条目最近的。
	sub := c.mkCat(&org, "org", "KS", "科室")
	if e := c.mkItem(sub.ID, nil); str(e.ArchiveNo) != prefix+"KS-0001" {
		t.Fatalf("最近的同类型节点：%s", str(e.ArchiveNo))
	}

	// 改节点编码：子树里未锁定的条目重算（row_version + 1，renumber 审计带 cause）。
	up := Decode[catNode](c.patchCat(org.ID, map[string]any{"category_code": "BG2"}).Expect(200))
	if up.Renumbered != 1 || up.ArchiveNoConflicts != 0 {
		t.Fatalf("改编码：%+v", up)
	}
	if a2 := c.item(a.ID); str(a2.ArchiveNo) != prefix+"BG2-0001" || a2.RowVersion != 2 {
		t.Fatalf("改编码后：%s v%d", str(a2.ArchiveNo), a2.RowVersion)
	}
	rn := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'renumber'`, a.ID)
	if rn["cause"].(map[string]any)["category_id"] != org.ID.String() || rn["before"].(map[string]any)["archive_no"] != prefix+"BGS-0001" {
		t.Fatalf("renumber 审计：%v", rn)
	}
	// 改类型：人事处改成自定义 → 缺"机构"，档号留空；改回来恢复。
	up = Decode[catNode](c.patchCat(rsc.ID, map[string]any{"category_type": "custom"}).Expect(200))
	if up.Renumbered != 1 || c.item(d.ID).ArchiveNo != nil {
		t.Fatalf("改类型：%+v", up)
	}
	c.patchCat(rsc.ID, map[string]any{"category_type": "org"}).Expect(200)
	if str(c.item(d.ID).ArchiveNo) != prefix+"RSC-0001" {
		t.Fatal("改回类型后档号没有恢复")
	}

	// 锁定的条目：子树里有它时移动、改编码、改类型都 409 item_locked，什么都不改。
	c.act(a.ID, "lock").Expect(200)
	c.patchCat(org.ID, map[string]any{"category_code": "BG3"}).ExpectError(409, "item_locked")
	c.patchCat(ret.ID, map[string]any{"category_type": "custom"}).ExpectError(409, "item_locked")
	c.patchCat(org.ID, map[string]any{"parent_id": c.rootID}).ExpectError(409, "item_locked")
	c.patchCat(org.ID, map[string]any{"category_name": "改名可以"}).Expect(200)

	// 改模板不自动重算；按新模板重新生成：只改未锁定的，重复执行不变。
	c.setSettings(map[string]any{"archive_no": map[string]any{"template": "{fonds}.{org}.{item_no:03}"}})
	if str(c.item(d.ID).ArchiveNo) != prefix+"RSC-0001" {
		t.Fatal("改模板不应自动重算")
	}
	if r := c.item(d.ID); r.ArchiveNo == nil || r.ArchiveNoReason != nil {
		t.Fatalf("有档号时没有原因：%+v", r.ArchiveNoReason)
	}
	res := c.renumber(&rsc.ID)
	if res.CategoryID != rsc.ID || res.Renumbered != 1 || res.SkippedLocked != 0 || str(c.item(d.ID).ArchiveNo) != "Z109.RSC.001" {
		t.Fatalf("按节点重新生成：%+v", res)
	}
	res = c.renumber(nil)
	if res.CategoryID != c.rootID || res.Renumbered != 1 || res.SkippedLocked != 1 || str(c.item(a.ID).ArchiveNo) != prefix+"BG2-0001" {
		t.Fatalf("整个项目重新生成：%+v", res)
	}
	n := len(auditActions(c.Env, c.rootID))
	if res = c.renumber(nil); res.Renumbered != 0 || len(auditActions(c.Env, c.rootID)) != n {
		t.Fatalf("重复执行：%+v", res)
	}
}

// TestArchiveNoConflict：撞号——自动生成、重算留空不报错，只有显式改件号 409；重算分档先到先得；互换档号不撞唯一索引。
func TestArchiveNoConflict(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "CF")
	_, _, _, ret, _ := c.mkChain()
	org := c.mkCat(&ret, "org", "TMP", "临时机构")
	c1 := c.mkCat(&org, "custom", "C1", "补充 1")
	const x = "Z109-WS·2025-Y-TMP-0001"

	// A 在机构下第 1 件；B 在它下面的自定义节点里也是第 1 件（模板没有自定义这一级）→ 撞号，档号留空、不报错。
	a := c.mkItem(org.ID, map[string]any{"title": "A"})
	b := c.mkItem(c1.ID, map[string]any{"title": "B"})
	if str(a.ArchiveNo) != x || b.ArchiveNo != nil || b.ArchiveNoReason == nil || b.ArchiveNoReason.Code != "conflict" ||
		str(b.ArchiveNoReason.ArchiveNo) != x || *b.ArchiveNoReason.ConflictItemID != a.ID ||
		*b.ArchiveNoReason.ConflictCategoryID != org.ID || *b.ArchiveNoReason.ConflictItemNo != 1 {
		t.Fatalf("撞号：A %s，B %+v", str(a.ArchiveNo), b.ArchiveNoReason)
	}
	cr := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'archive'`, b.ID)
	if cr["after"].(map[string]any)["archive_no_conflict"].(map[string]any)["item_id"] != a.ID.String() {
		t.Fatalf("archive 审计：%v", cr)
	}

	// A 改件号 9 → B 变成"未生成"；C2 里新建的 C 拿到这个号。
	c.patchItem(a.ID, map[string]any{"item_no": 9}).Expect(200)
	if r := c.item(b.ID).ArchiveNoReason; r == nil || r.Code != "not_generated" || str(r.ArchiveNo) != x {
		t.Fatalf("未生成：%+v", r)
	}
	c2 := c.mkCat(&org, "custom", "C2", "补充 2")
	cc := c.mkItem(c2.ID, map[string]any{"title": "C"})
	if str(cc.ArchiveNo) != x {
		t.Fatalf("C 应拿到 %s：%s", x, str(cc.ArchiveNo))
	}
	// 重算分档：C 有号不用换（第 ① 档）先于原来没号的 B（第 ③ 档），即使 B 的 id 更小。
	res := c.renumber(nil)
	if res.Renumbered != 0 || res.ArchiveNoConflicts != 1 || c.item(b.ID).ArchiveNo != nil || str(c.item(cc.ID).ArchiveNo) != x {
		t.Fatalf("分档：%+v", res)
	}
	// 显式改件号撞号 409 conflict，什么都不改。
	m := errOf(c.patchItem(a.ID, map[string]any{"item_no": 1}).ExpectError(409, "conflict")).Message
	if !strings.Contains(m, x) || !strings.Contains(m, "请换一个件号") || c.item(a.ID).ItemNo != 9 {
		t.Fatalf("改件号撞号：%s", m)
	}
	// 删掉 C 之后重新生成，B 补上。
	c.admin.Delete("/api/archive-items/" + cc.ID.String()).Expect(204)
	if res = c.renumber(nil); res.Renumbered != 1 || res.ArchiveNoConflicts != 0 || str(c.item(b.ID).ArchiveNo) != x {
		t.Fatalf("补上：%+v", res)
	}
	// 新建撞 B → 留空；改它所在节点的编码（重算）→ 仍撞号，响应 archive_no_conflicts = 1。
	c3 := c.mkCat(&org, "custom", "C3", "补充 3")
	e := c.mkItem(c3.ID, map[string]any{"title": "E"})
	if e.ArchiveNo != nil {
		t.Fatal("E 应撞号")
	}
	up := Decode[catNode](c.patchCat(c3.ID, map[string]any{"category_code": "C4"}).Expect(200))
	if up.Renumbered != 0 || up.ArchiveNoConflicts != 1 {
		t.Fatalf("节点修改的撞号计数：%+v", up)
	}
	// 只移动、不带件号时撞号也留空，不报错：C1 里 F 是第 2 件；A 移进 C3（第 2 件）撞 F。
	f := c.mkItem(c1.ID, map[string]any{"title": "F"})
	moved := Decode[itemView](c.patchItem(a.ID, map[string]any{"category_id": c3.ID}).Expect(200))
	if moved.ItemNo != 2 || moved.ArchiveNo != nil || moved.ArchiveNoReason.Code != "conflict" || *moved.ArchiveNoReason.ConflictItemID != f.ID {
		t.Fatalf("移动撞号：%+v", moved)
	}

	// 互换：两个条目的档号在重新生成时正好对调，先置空再写，不撞唯一索引。
	s := newCatalogEnv(t, "SW")
	s.setSettings(map[string]any{"archive_no": map[string]any{"template": "{fonds}-{year}-{item_no}"}})
	f11 := s.mkCat(nil, "fonds", "11", "全宗 11")
	y22 := s.mkCat(&f11, "year", "22", "年度 22")
	f22 := s.mkCat(nil, "fonds", "22", "全宗 22")
	y11 := s.mkCat(&f22, "year", "11", "年度 11")
	p := s.mkItem(y22.ID, nil)
	q := s.mkItem(y11.ID, nil)
	if str(p.ArchiveNo) != "11-22-1" || str(q.ArchiveNo) != "22-11-1" {
		t.Fatalf("互换前：%s %s", str(p.ArchiveNo), str(q.ArchiveNo))
	}
	s.setSettings(map[string]any{"archive_no": map[string]any{"template": "{year}-{fonds}-{item_no}"}})
	if res := s.renumber(nil); res.Renumbered != 2 || res.ArchiveNoConflicts != 0 ||
		str(s.item(p.ID).ArchiveNo) != "22-11-1" || str(s.item(q.ID).ArchiveNo) != "11-22-1" {
		t.Fatalf("互换：%+v", res)
	}
}

// TestArchiveNoIncomplete：缺级档号留空（能归档不能锁定）、补齐上级后移进去有档号；模板解析不了时的处理。
func TestArchiveNoIncomplete(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "IC")
	fonds := c.mkCat(nil, "fonds", "Z1", "全宗")
	ws := c.mkCat(&fonds, "category", "WS", "文书")

	it := c.mkItem(ws.ID, map[string]any{"title": "缺级"})
	r := it.ArchiveNoReason
	if it.ArchiveNo != nil || r == nil || r.Code != "incomplete" || !slices.Equal(r.Missing, []string{"year", "retention", "org"}) ||
		!strings.Contains(r.Message, "年度、保管期限、机构") {
		t.Fatalf("缺级：%+v", r)
	}
	if m := errOf(c.act(it.ID, "lock").ExpectError(409, "invalid_state")).Message; !strings.Contains(m, "没有档号") {
		t.Fatalf("缺级不能锁定：%s", m)
	}
	// 直接放在根节点下的条目（归档后移过去）：缺全部五级。
	top := c.mkItem(ws.ID, nil)
	top = Decode[itemView](c.patchItem(top.ID, map[string]any{"category_id": c.rootID}).Expect(200))
	if top.ArchiveNoReason == nil || len(top.ArchiveNoReason.Missing) != 5 {
		t.Fatalf("根节点下：%+v", top.ArchiveNoReason)
	}
	// 补齐上级，移进去就有档号，可以锁定。
	year := c.mkCat(&ws, "year", "2025", "2025 年")
	ret := c.mkCat(&year, "retention", "Y", "永久")
	org := c.mkCat(&ret, "org", "BGS", "办公室")
	it = Decode[itemView](c.patchItem(it.ID, map[string]any{"category_id": org.ID}).Expect(200))
	if str(it.ArchiveNo) != "Z1-WS·2025-Y-BGS-0001" || it.ArchiveNoReason != nil || it.Status != "draft" {
		t.Fatalf("补齐后：%+v", it)
	}
	c.act(it.ID, "lock").Expect(200)
	kj := c.mkCat(&fonds, "category", "KJ", "科技")
	c.mkItem(kj.ID, nil)
	pending, _ := c.mkFolder(uuid.Nil, "待归档", "approved")

	// 模板解析不了（以前写入的设置，直接写库）：catalog-settings 给原因，编码按兜底的分隔符校验；归档、重新生成、
	// 改编码（要重算）409 invalid_state；只改名可以；详情的原因是 template_invalid。
	c.Exec(`UPDATE projects SET settings = '{"archive_no": {"template": "{fonds}-{bad}"}}' WHERE id = $1`, c.pid)
	var st struct {
		Error     *string  `json:"archive_no_template_error"`
		Levels    []string `json:"archive_no_levels"`
		Forbidden []string `json:"category_code_forbidden_chars"`
	}
	c.admin.Get("/api/projects/" + c.pid.String() + "/catalog-settings").Expect(200).JSON(&st)
	if st.Error == nil || len(st.Levels) != 0 || !slices.Contains(st.Forbidden, "_") || !slices.Contains(st.Forbidden, ".") {
		t.Fatalf("模板解析不了的设置：%+v", st)
	}
	c.archive(ws.ID, map[string]any{"folder_ids": []uuid.UUID{pending}}).ExpectError(409, "invalid_state")
	c.admin.Post("/api/projects/"+c.pid.String()+"/archive-items:renumber", nil).ExpectError(409, "invalid_state")
	c.patchCat(kj.ID, map[string]any{"category_code": "KJ2"}).ExpectError(409, "invalid_state")
	c.patchCat(ws.ID, map[string]any{"category_name": "文书档案"}).Expect(200)
	if r := c.item(top.ID).ArchiveNoReason; r == nil || r.Code != "template_invalid" {
		t.Fatalf("模板解析不了的原因：%+v", r)
	}
}
