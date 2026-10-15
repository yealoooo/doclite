package apitest

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 挂接、页序、删除条目时一并移出（M4-2，requirements 5.9"挂接"）。

// fileLog 是 file_logs 的一行。
type fileLog struct {
	Action string
	Detail map[string]any
}

func fileLogs(e *Env, fileID uuid.UUID) []fileLog {
	e.t.Helper()
	rows, err := e.Pool.Query(e.t.Context(), `SELECT action, detail FROM file_logs WHERE file_id = $1 ORDER BY id`, fileID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []fileLog
	for rows.Next() {
		var l fileLog
		if err := rows.Scan(&l.Action, &l.Detail); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

// catalogedError 是 409 file_cataloged 的错误体。
type catalogedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details struct {
		IDs   []uuid.UUID `json:"ids"`
		Items []struct {
			FileID        uuid.UUID `json:"file_id"`
			ItemID        uuid.UUID `json:"item_id"`
			CategoryID    uuid.UUID `json:"category_id"`
			CategoryNames []string  `json:"category_names"`
			ItemNo        int       `json:"item_no"`
			ArchiveNo     *string   `json:"archive_no"`
			Title         string    `json:"title"`
			PageNo        int       `json:"page_no"`
			ItemStatus    string    `json:"item_status"`
		} `json:"items"`
	} `json:"details"`
}

func idsOf(evField any) []string {
	var out []string
	list, _ := evField.([]any)
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

func strIDs(ids ...uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// TestCatalogPages：PUT 整体替换页（挂上、移出、只换顺序、没有变化），日志、reorder 审计、事件，主要错误码。
func TestCatalogPages(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "PG")
	_, _, _, _, org := c.mkChain()
	admin := c.admin
	a, b, cc, d := c.mkFile(uuid.Nil, "approved"), c.mkFile(uuid.Nil, "pending"), c.mkFile(uuid.Nil, "unassigned"), c.mkFile(uuid.Nil, "rejected")
	it := c.mkItem(org.ID, map[string]any{"title": "挂接"})
	s := openStream(t, admin, c.pid)
	path := filesPath(it.ID)

	// 挂到空条目：页号 1..n，row_version + 1；先 files.changed 再 catalog.changed；每页一条 link。
	got := Decode[itemView](admin.Put(path, map[string]any{"row_version": it.RowVersion, "file_ids": []uuid.UUID{a, b, cc}}).Expect(200))
	if !slices.Equal(got.pageIDs(), []uuid.UUID{a, b, cc}) || got.Pages[2].PageNo != 3 || got.RowVersion != it.RowVersion+1 ||
		got.PageCount != 3 || got.PageCounts["pending"] != 1 || got.Pages[0].FolderPath != "PG 项目" {
		t.Fatalf("挂上：%+v", got)
	}
	ev := s.next(t)
	if ev["type"] != "files.changed" || !slices.Equal(idsOf(ev["folder_ids"]), strIDs(c.folder)) ||
		!slices.Equal(idsOf(ev["file_ids"]), strIDs(a, b, cc)) {
		t.Fatalf("files.changed：%v", ev)
	}
	ev = s.next(t)
	if ev["type"] != "catalog.changed" || !slices.Equal(idsOf(ev["category_ids"]), strIDs(org.ID)) ||
		!slices.Equal(idsOf(ev["item_ids"]), strIDs(it.ID)) {
		t.Fatalf("catalog.changed：%v", ev)
	}
	logs := fileLogs(c.Env, b)
	if len(logs) != 1 || logs[0].Action != "link" {
		t.Fatalf("link 日志：%+v", logs)
	}
	dl := logs[0].Detail
	if dl["item_id"] != it.ID.String() || dl["item_no"] != float64(1) || dl["archive_no"] != "Z109-WS·2025-Y-BGS-0001" ||
		dl["title"] != "挂接" || dl["page_no"] != float64(2) || !strings.HasPrefix(dl["file_name"].(string), "IMG-") ||
		dl["folder_path"] != "PG 项目" || dl["version_no"] != float64(0) || dl["review_status"] != "pending" {
		t.Fatalf("link 的 detail：%v", dl)
	}

	// 和现在完全一样：200，row_version 不变，不写日志、不发事件。
	rv := got.RowVersion
	same := Decode[itemView](admin.Put(path, map[string]any{"row_version": rv, "file_ids": []uuid.UUID{a, b, cc}}).Expect(200))
	if same.RowVersion != rv || len(fileLogs(c.Env, a)) != 1 {
		t.Fatalf("没有变化：v%d", same.RowVersion)
	}

	// 只换顺序（把第 3 页拖到最前）：只发 catalog.changed；一条 reorder 审计，只记挪动过的那一页。
	got = Decode[itemView](admin.Put(path, map[string]any{"row_version": rv, "file_ids": []uuid.UUID{cc, a, b}}).Expect(200))
	if !slices.Equal(got.pageIDs(), []uuid.UUID{cc, a, b}) || got.RowVersion != rv+1 || len(fileLogs(c.Env, cc)) != 1 {
		t.Fatalf("换顺序：%+v", got)
	}
	if ev = s.next(t); ev["type"] != "catalog.changed" {
		t.Fatalf("只换顺序应当只发 catalog.changed：%v", ev)
	}
	reorder := QueryValue[map[string]any](c.Env, `SELECT detail FROM audit_logs WHERE target_id = $1 AND action = 'reorder'`, it.ID)
	moved, _ := reorder["moved"].([]any)
	if reorder["page_count"] != float64(3) || reorder["linked"] != float64(0) || reorder["unlinked"] != float64(0) || len(moved) != 1 {
		t.Fatalf("reorder 审计：%v", reorder)
	}
	if m := moved[0].(map[string]any); m["file_id"] != cc.String() || m["from_page"] != float64(3) || m["to_page"] != float64(1) ||
		m["file_name"] == "" {
		t.Fatalf("reorder 的 moved：%v", m)
	}

	// 移出一页、挂上一页（顺延不算重排，不写 reorder）：先 unlink 后 link；files.changed 只带这两个文件。
	got = Decode[itemView](c.putPages(it.ID, cc, a, d).Expect(200))
	if !slices.Equal(got.pageIDs(), []uuid.UUID{cc, a, d}) {
		t.Fatalf("移出、挂上：%v", got.pageIDs())
	}
	if ev = s.next(t); ev["type"] != "files.changed" || !slices.Equal(idsOf(ev["file_ids"]), strIDs(b, d)) {
		t.Fatalf("files.changed：%v", ev)
	}
	s.next(t)
	if l := fileLogs(c.Env, b); len(l) != 2 || l[1].Action != "unlink" || l[1].Detail["page_no"] != float64(3) || l[1].Detail["item_deleted"] != nil {
		t.Fatalf("unlink 日志：%+v", l)
	}
	if l := fileLogs(c.Env, d); len(l) != 1 || l[0].Action != "link" || l[0].Detail["page_no"] != float64(3) {
		t.Fatalf("link 日志：%+v", l)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action = 'reorder'`, it.ID); n != 1 {
		t.Fatalf("顺延不算重排：%d 条 reorder", n)
	}

	// 错误码。
	rv = got.RowVersion
	put := func(body map[string]any) *Response { return admin.Put(path, body) }
	put(map[string]any{"file_ids": []uuid.UUID{}}).ExpectError(400, "invalid_input")
	put(map[string]any{"row_version": 0, "file_ids": []uuid.UUID{}}).ExpectError(400, "invalid_input")
	put(map[string]any{"row_version": rv}).ExpectError(400, "invalid_input")
	if m := errOf(put(map[string]any{"row_version": rv, "file_ids": []uuid.UUID{a, d, a}}).ExpectError(400, "invalid_input")).Message; !strings.Contains(m, "第 1 项和第 3 项") {
		t.Fatalf("重复：%s", m)
	}
	many := make([]uuid.UUID, 2001)
	for i := range many {
		many[i] = uuid.New()
	}
	put(map[string]any{"row_version": rv, "file_ids": many}).ExpectError(400, "invalid_input")
	put(map[string]any{"row_version": rv + 5, "file_ids": []uuid.UUID{a}}).ExpectError(409, "conflict")
	admin.Put(filesPath(uuid.New()), map[string]any{"row_version": 1, "file_ids": []uuid.UUID{}}).ExpectError(404, "not_found")

	// 要挂的文件：不存在、已删除、不在这个项目里 404（details.ids 按请求里的顺序）。
	ghost := uuid.New()
	gone := c.mkFile(uuid.Nil, "approved")
	c.Exec(`UPDATE folder_files SET deleted_at = now() WHERE file_id = $1`, gone)
	other := c.CreateProject("PX")
	foreign := c.mkFileIn(other.ID, other.RootFolderID, "approved")
	e := errOf(put(map[string]any{"row_version": rv, "file_ids": []uuid.UUID{cc, foreign, ghost, a, gone}}).ExpectError(404, "not_found"))
	if !slices.Equal(e.Details.IDs, []uuid.UUID{foreign, ghost, gone}) {
		t.Fatalf("404 details：%+v", e)
	}

	// 已经挂在别的条目里 409 file_cataloged：details 写明在哪（条目、件号、档号、题名、节点名称链、第几页、状态）。
	it2 := c.mkItem(org.ID, map[string]any{"title": "别的条目"})
	e2 := c.mkFile(uuid.Nil, "approved")
	c.putPages(it2.ID, e2, b).Expect(200)
	ce := Decode[catalogedError](put(map[string]any{"row_version": rv, "file_ids": []uuid.UUID{b, cc, a, d}}).ExpectError(409, "file_cataloged"))
	if !slices.Equal(ce.Details.IDs, []uuid.UUID{b}) || len(ce.Details.Items) != 1 {
		t.Fatalf("file_cataloged：%+v", ce)
	}
	if x := ce.Details.Items[0]; x.FileID != b || x.ItemID != it2.ID || x.ItemNo != 2 || str(x.ArchiveNo) != "Z109-WS·2025-Y-BGS-0002" ||
		x.Title != "别的条目" || x.PageNo != 2 || x.ItemStatus != "draft" || x.CategoryID != org.ID ||
		!slices.Equal(x.CategoryNames, []string{"全宗", "文书", "2025 年", "永久", "办公室"}) || !strings.Contains(ce.Message, "第 2 页") {
		t.Fatalf("file_cataloged 的 items：%+v %s", x, ce.Message)
	}
	// 同时有不存在的和已在别处的：先 404。
	put(map[string]any{"row_version": rv, "file_ids": []uuid.UUID{b, ghost}}).ExpectError(404, "not_found")
	if now := c.item(it.ID); now.RowVersion != rv || !slices.Equal(now.pageIDs(), []uuid.UUID{cc, a, d}) {
		t.Fatalf("出错后条目变了：v%d %v", now.RowVersion, now.pageIDs())
	}

	// 从原条目移出后可以挂过来；定稿的条目改页序 409 item_locked（没有变化、版本过期也一样，状态先于版本）。
	c.putPages(it2.ID, e2).Expect(200)
	got = Decode[itemView](c.putPages(it.ID, cc, a, d, b).Expect(200))
	c.act(it2.ID, "mark-done").Expect(200)
	c.act(it2.ID, "lock").Expect(200)
	locked := c.item(it2.ID)
	admin.Put(filesPath(it2.ID), map[string]any{"row_version": locked.RowVersion, "file_ids": []uuid.UUID{e2}}).ExpectError(409, "item_locked")
	admin.Put(filesPath(it2.ID), map[string]any{"row_version": 99, "file_ids": []uuid.UUID{}}).ExpectError(409, "item_locked")
	// 挂的是定稿条目里的文件：409 file_cataloged，item_status 是 locked（界面不给"从原条目移出"）。
	ce = Decode[catalogedError](c.putPages(it.ID, cc, a, d, b, e2).ExpectError(409, "file_cataloged"))
	if ce.Details.Items[0].ItemStatus != "locked" {
		t.Fatalf("定稿条目里的文件：%+v", ce.Details.Items)
	}

	// 已归类的文件不能删除（文件目录的 409 file_cataloged），移出后可以删。
	ffID := QueryValue[uuid.UUID](c.Env, `SELECT id FROM folder_files WHERE file_id = $1`, b)
	admin.Delete("/api/folder-files/"+ffID.String()).ExpectError(409, "file_cataloged")
	c.putPages(it.ID, cc, a, d).Expect(200)
	admin.Delete("/api/folder-files/" + ffID.String()).Expect(204)
	_ = got
}

// TestCatalogItemPagesCreateDelete：新建条目带 file_ids（失败整体回滚、件号不占）、删除条目时一并移出。
func TestCatalogItemPagesCreateDelete(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "CD")
	_, _, _, _, org := c.mkChain()
	admin := c.admin
	a, b, x := c.mkFile(uuid.Nil, "approved"), c.mkFile(uuid.Nil, "approved"), c.mkFile(uuid.Nil, "approved")
	holder := c.mkItem(org.ID, map[string]any{"title": "占着 x"})
	c.putPages(holder.ID, x).Expect(200)
	s := openStream(t, admin, c.pid)

	// 带 file_ids 新建：按顺序挂上，row_version 仍是 1；create 审计带 page_count；先 files.changed 再 catalog.changed。
	itemsPath := "/api/archive-categories/" + org.ID.String() + "/items"
	it := Decode[itemView](admin.Post(itemsPath, map[string]any{"title": "带页新建", "file_ids": []uuid.UUID{b, a}}).Expect(201))
	if !slices.Equal(it.pageIDs(), []uuid.UUID{b, a}) || it.RowVersion != 1 || it.ItemNo != 2 {
		t.Fatalf("带页新建：%+v", it)
	}
	if ev := s.next(t); ev["type"] != "files.changed" || !slices.Equal(idsOf(ev["file_ids"]), strIDs(b, a)) {
		t.Fatalf("files.changed：%v", ev)
	}
	if ev := s.next(t); ev["type"] != "catalog.changed" || !slices.Equal(idsOf(ev["item_ids"]), strIDs(it.ID)) {
		t.Fatalf("catalog.changed：%v", ev)
	}
	created := QueryValue[map[string]any](c.Env, `SELECT detail->'after' FROM audit_logs WHERE target_id = $1 AND action = 'create'`, it.ID)
	if created["page_count"] != float64(2) {
		t.Fatalf("create 审计：%v", created)
	}
	if l := fileLogs(c.Env, a); len(l) != 1 || l[0].Action != "link" || l[0].Detail["page_no"] != float64(2) || l[0].Detail["item_no"] != float64(2) {
		t.Fatalf("link 日志：%+v", l)
	}

	// 失败整体回滚：已在别处 409、不存在 404、重复 400，条目都不建、件号不占。
	y := c.mkFile(uuid.Nil, "approved")
	admin.Post(itemsPath, map[string]any{"title": "失败", "file_ids": []uuid.UUID{y, x}}).ExpectError(409, "file_cataloged")
	admin.Post(itemsPath, map[string]any{"title": "失败", "file_ids": []uuid.UUID{y, uuid.New()}}).ExpectError(404, "not_found")
	admin.Post(itemsPath, map[string]any{"title": "失败", "file_ids": []uuid.UUID{y, y}}).ExpectError(400, "invalid_input")
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM archive_items WHERE title = '失败'`); n != 0 {
		t.Fatalf("失败的新建留下了 %d 个条目", n)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM file_logs WHERE file_id = $1`, y); n != 0 {
		t.Fatalf("失败的新建留下了日志")
	}
	next := c.mkItem(org.ID, map[string]any{"title": "下一个"})
	if next.ItemNo != 3 {
		t.Fatalf("失败的新建占了件号：%d", next.ItemNo)
	}
	s.next(t) // 下一个的 catalog.changed

	// 删除有页的条目：一并移出（unlink 带 item_deleted），文件本身不动；delete 审计带 page_count；先 files.changed。
	admin.Delete("/api/archive-items/" + it.ID.String() + "?row_version=" + strconv.Itoa(it.RowVersion)).Expect(204)
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM archive_item_files WHERE archive_item_id = $1`, it.ID); n != 0 {
		t.Fatalf("删除后还有 %d 页", n)
	}
	if l := fileLogs(c.Env, b); len(l) != 2 || l[1].Action != "unlink" || l[1].Detail["item_deleted"] != true || l[1].Detail["page_no"] != float64(1) {
		t.Fatalf("unlink 日志：%+v", l)
	}
	if ev := s.next(t); ev["type"] != "files.changed" || !slices.Equal(idsOf(ev["file_ids"]), strIDs(b, a)) {
		t.Fatalf("files.changed：%v", ev)
	}
	if ev := s.next(t); ev["type"] != "catalog.changed" {
		t.Fatalf("catalog.changed：%v", ev)
	}
	del := QueryValue[map[string]any](c.Env, `SELECT detail->'before' FROM audit_logs WHERE target_id = $1 AND action = 'delete'`, it.ID)
	if del["page_count"] != float64(2) {
		t.Fatalf("delete 审计：%v", del)
	}
	// 移出的文件可以挂到别的条目；件号不回收。
	c.putPages(next.ID, a, b).Expect(200)
	if n := Decode[itemView](admin.Post(itemsPath, map[string]any{}).Expect(201)).ItemNo; n != 4 {
		t.Fatalf("件号回收了：%d", n)
	}
	// 不变量：已删除的条目没有挂接行；页号是连续的 1..n。
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM archive_item_files af JOIN archive_items a ON a.id = af.archive_item_id
WHERE a.deleted_at IS NOT NULL`); n != 0 {
		t.Fatalf("已删除的条目有 %d 个挂接行", n)
	}
	if n := QueryValue[int](c.Env, `SELECT count(*) FROM (SELECT archive_item_id FROM archive_item_files GROUP BY 1
HAVING min(page_no) <> 1 OR max(page_no) <> count(*)) t`); n != 0 {
		t.Fatalf("有 %d 个条目的页号不连续", n)
	}
}

// placementView 是 placements 的一项。
type placementView struct {
	FileID    uuid.UUID `json:"file_id"`
	Placement *struct {
		ItemID        uuid.UUID `json:"item_id"`
		ItemNo        int       `json:"item_no"`
		ArchiveNo     *string   `json:"archive_no"`
		Title         string    `json:"title"`
		PageNo        int       `json:"page_no"`
		ItemStatus    string    `json:"item_status"`
		CategoryID    uuid.UUID `json:"category_id"`
		CategoryNames []string  `json:"category_names"`
		CategoryPath  string    `json:"category_path"`
	} `json:"placement"`
}

// TestCatalogPlacements：一批文件各挂在哪（placements）、一个文件挂在哪（files/{fid}/catalog）；能进项目就能看。
func TestCatalogPlacements(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "PL")
	_, _, _, _, org := c.mkChain()
	admin := c.admin
	a, b, top := c.mkFile(uuid.Nil, "approved"), c.mkFile(uuid.Nil, "pending"), c.mkFile(uuid.Nil, "approved")
	gone := c.mkFile(uuid.Nil, "approved")
	c.Exec(`UPDATE folder_files SET deleted_at = now() WHERE file_id = $1`, gone)
	other := c.CreateProject("PY")
	foreign := c.mkFileIn(other.ID, other.RootFolderID, "approved")
	it := c.mkItem(org.ID, map[string]any{"title": "条目甲"})
	c.putPages(it.ID, c.mkFile(uuid.Nil, "approved"), a).Expect(200)
	topItem := c.mkItem(c.rootID, map[string]any{"title": "顶层"})
	c.putPages(topItem.ID, top).Expect(200)

	path := "/api/projects/" + c.pid.String() + "/catalog-placements"
	ghost := uuid.New()
	var res struct {
		Items   []placementView `json:"items"`
		Missing []uuid.UUID     `json:"missing"`
	}
	admin.Post(path, map[string]any{"file_ids": []uuid.UUID{a, ghost, b, foreign, a, gone, top}}).Expect(200).JSON(&res)
	if len(res.Items) != 3 || res.Items[0].FileID != a || res.Items[1].FileID != b || res.Items[2].FileID != top ||
		!slices.Equal(res.Missing, []uuid.UUID{ghost, foreign, gone}) {
		t.Fatalf("placements：%+v", res)
	}
	p := res.Items[0].Placement
	if p == nil || p.ItemID != it.ID || p.ItemNo != 1 || str(p.ArchiveNo) != "Z109-WS·2025-Y-BGS-0001" || p.Title != "条目甲" ||
		p.PageNo != 2 || p.ItemStatus != "draft" || p.CategoryID != org.ID ||
		!slices.Equal(p.CategoryNames, []string{"全宗", "文书", "2025 年", "永久", "办公室"}) || p.CategoryPath != "全宗/文书/2025 年/永久/办公室" {
		t.Fatalf("placement：%+v", p)
	}
	if res.Items[1].Placement != nil {
		t.Fatalf("没挂接的应为 null：%+v", res.Items[1].Placement)
	}
	// 直接在根节点下的条目：名称链为空，档号缺级为 null。
	if tp := res.Items[2].Placement; tp == nil || tp.CategoryNames == nil || len(tp.CategoryNames) != 0 || tp.CategoryPath != "" || tp.ArchiveNo != nil {
		t.Fatalf("根节点下：%+v", tp)
	}
	admin.Post(path, map[string]any{"file_ids": []uuid.UUID{}}).Expect(200).JSON(&res)
	if len(res.Items) != 0 || res.Missing == nil || len(res.Missing) != 0 {
		t.Fatalf("空列表：%+v", res)
	}
	admin.Post(path, map[string]any{}).ExpectError(400, "invalid_input")
	many := make([]uuid.UUID, 501)
	for i := range many {
		many[i] = a // 重复的也按个数算
	}
	admin.Post(path, map[string]any{"file_ids": many}).ExpectError(400, "invalid_input")

	// 一个文件：挂在哪（没挂接为 null）；已删除、不存在 404。
	one := func(fid uuid.UUID) *Response { return admin.Get("/api/files/" + fid.String() + "/catalog") }
	var raw struct {
		FileID    uuid.UUID `json:"file_id"`
		ProjectID uuid.UUID `json:"project_id"`
		Placement *struct {
			ItemID uuid.UUID `json:"item_id"`
			PageNo int       `json:"page_no"`
		} `json:"placement"`
	}
	one(a).Expect(200).JSON(&raw)
	if raw.FileID != a || raw.ProjectID != c.pid || raw.Placement == nil || raw.Placement.ItemID != it.ID || raw.Placement.PageNo != 2 {
		t.Fatalf("一个文件：%+v", raw)
	}
	raw.Placement = nil
	one(b).Expect(200).JSON(&raw)
	if raw.FileID != b || raw.Placement != nil {
		t.Fatalf("没挂接：%+v", raw)
	}
	one(gone).ExpectError(404, "not_found")
	one(ghost).ExpectError(404, "not_found")

	// 只要能进项目（审核员没有 catalog 菜单也能看）；进不了 403。
	c.GrantProject(c.pid, c.PersonaID("reviewer"))
	rev := c.As("reviewer")
	rev.Get("/api/files/" + a.String() + "/catalog").Expect(200)
	rev.Post(path, map[string]any{"file_ids": []uuid.UUID{a}}).Expect(200)
	rev.Get("/api/files/"+foreign.String()+"/catalog").ExpectError(403, "forbidden")
	rev.Post("/api/projects/"+other.ID.String()+"/catalog-placements", map[string]any{"file_ids": []uuid.UUID{}}).ExpectError(403, "forbidden")
}
