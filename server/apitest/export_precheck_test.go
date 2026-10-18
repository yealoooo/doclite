package apitest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 预检、建任务的前提（M5-2，requirements 5.11"范围""预检"）。文件直接写库（没有快照），只测读和 409，不真正建任务。

// precheckResult 是预检的响应。
type precheckResult struct {
	ScopeType         string      `json:"scope_type"`
	ScopeIDs          []uuid.UUID `json:"scope_ids"`
	ResubmitOf        *uuid.UUID  `json:"resubmit_of"`
	ExportFormat      string      `json:"export_format"`
	OnlyApproved      bool        `json:"only_approved"`
	RequireLockedItem bool        `json:"require_locked_item"`
	ItemCount         int         `json:"item_count"`
	PageCount         int         `json:"page_count"`
	ReadyItemCount    int         `json:"ready_item_count"`
	ReadyPageCount    int         `json:"ready_page_count"`
	NotReadyCount     int         `json:"not_ready_count"`
	NotReadyPageCount int         `json:"not_ready_page_count"`
	ReasonCounts      struct {
		SourceModifying  int `json:"source_modifying"`
		NotLocked        int `json:"not_locked"`
		NoArchiveNo      int `json:"no_archive_no"`
		NoPages          int `json:"no_pages"`
		PagesNotApproved int `json:"pages_not_approved"`
	} `json:"reason_counts"`
	NotReady []struct {
		ItemID        uuid.UUID `json:"item_id"`
		CategoryNames []string  `json:"category_names"`
		ArchiveNo     *string   `json:"archive_no"`
		Status        string    `json:"status"`
		PageCount     int       `json:"page_count"`
		Reasons       []struct {
			Code       string  `json:"code"`
			Message    string  `json:"message"`
			ItemStatus *string `json:"item_status"`
			Count      *int    `json:"count"`
			Pages      []struct {
				PageNo       int       `json:"page_no"`
				FileID       uuid.UUID `json:"file_id"`
				FileName     string    `json:"file_name"`
				ReviewStatus string    `json:"review_status"`
			} `json:"pages"`
		} `json:"reasons"`
	} `json:"not_ready"`
	NotReadyTruncated bool    `json:"not_ready_truncated"`
	MaxPageCount      int     `json:"max_page_count"`
	PageLimitExceeded bool    `json:"page_limit_exceeded"`
	PageLimitMessage  *string `json:"page_limit_message"`
}

// reasonList 是不就绪条目的"条目 → 原因码"（按导出顺序）。
func (p precheckResult) reasonList() []string {
	var out []string
	for _, it := range p.NotReady {
		var codes []string
		for _, r := range it.Reasons {
			codes = append(codes, r.Code)
		}
		out = append(out, it.ItemID.String()[:8]+":"+strings.Join(codes, ","))
	}
	return out
}

// TestExportPrecheck：就绪规则和原因码（表驱动）、范围（子树、条目、去重）、require_locked_item、参数 400、范围 404、
// 建任务的 409（details 是预检结果）。
func TestExportPrecheck(t *testing.T) {
	t.Parallel()
	c := newCatalogEnv(t, "EXP")
	_, _, _, _, org := c.mkChain()
	// 锁定不要求全部页通过（造"已锁定但有页没通过"的条目）。
	c.setSettings(map[string]any{"catalog": map[string]any{"lock_requires_approved": false}})
	lock := func(id uuid.UUID) {
		t.Helper()
		c.act(id, "lock").Expect(http.StatusOK)
	}
	// A：就绪（2 页通过，锁定）；B：锁定，第 2 页待审；C：未锁定；D：修改中，唯一的页的文件删掉了（导出不算它）；
	// E：在只有全宗的节点下（缺级，没有档号），1 页通过。
	fa, _ := c.mkFolder(uuid.Nil, "A", "approved", "approved")
	a := c.archiveOne(org.ID, fa, "")
	lock(a.ID)
	fb, bFiles := c.mkFolder(uuid.Nil, "B", "approved", "pending")
	bPending := bFiles[1]
	b := c.archiveOne(org.ID, fb, "")
	lock(b.ID)
	cc := c.mkItem(org.ID, map[string]any{"title": "C"})
	d := c.mkItem(org.ID, map[string]any{"title": "D"})
	c.unseal(d.ID).Expect(http.StatusOK)
	c.admin.Delete("/api/folder-files/" + QueryValue[uuid.UUID](c.Env, `SELECT id FROM folder_files WHERE file_id = $1`,
		d.Pages[0].FileID).String()).Expect(http.StatusNoContent)
	fonds2 := c.mkCat(nil, "fonds", "Z2", "全宗二")
	e := c.mkItem(fonds2.ID, map[string]any{"title": "E"})
	if e.ArchiveNo != nil {
		t.Fatalf("E 应该没有档号：%s", *e.ArchiveNo)
	}
	short := func(v itemView, codes string) string { return v.ID.String()[:8] + ":" + codes }
	const dReasons = "source_modifying,not_locked,no_pages"

	precheck := func(body map[string]any) precheckResult {
		t.Helper()
		return Decode[precheckResult](c.admin.Post("/api/projects/"+c.pid.String()+"/export-tasks:precheck", body).Expect(http.StatusOK))
	}
	cases := []struct {
		name                              string
		body                              map[string]any
		items, pages, ready, rpages       int
		mod, notLocked, noNo, noPages, na int
		reasons                           []string
		scopeIDs                          []uuid.UUID
	}{
		{name: "节点子树、只导出通过的（默认）", body: map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{org.ID}, "export_format": "pdf"},
			items: 4, pages: 5, ready: 1, rpages: 2, mod: 1, notLocked: 2, noPages: 1, na: 1,
			reasons: []string{short(b, "pages_not_approved"), short(cc, "not_locked"), short(d, dReasons)}},
		{name: "不勾只导出通过的", body: map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{org.ID}, "export_format": "images", "only_approved": false},
			items: 4, pages: 5, ready: 2, rpages: 4, mod: 1, notLocked: 2, noPages: 1,
			reasons: []string{short(cc, "not_locked"), short(d, dReasons)}},
		{name: "根节点（整个项目），子树重叠算一次", body: map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{c.rootID, org.ID}, "export_format": "pdf"},
			items: 5, pages: 6, ready: 1, rpages: 2, mod: 1, notLocked: 3, noNo: 1, noPages: 1, na: 1,
			reasons: []string{short(b, "pages_not_approved"), short(cc, "not_locked"), short(d, dReasons), short(e, "not_locked,no_archive_no")}},
		{name: "条目（去重，按导出顺序）", body: map[string]any{"scope_type": "item", "scope_ids": []uuid.UUID{d.ID, a.ID, a.ID}, "export_format": "pdf"},
			items: 2, pages: 2, ready: 1, rpages: 2, mod: 1, notLocked: 1, noPages: 1,
			reasons: []string{short(d, dReasons)}, scopeIDs: []uuid.UUID{d.ID, a.ID}},
	}
	for _, tc := range cases {
		p := precheck(tc.body)
		if p.ItemCount != tc.items || p.PageCount != tc.pages || p.ReadyItemCount != tc.ready || p.ReadyPageCount != tc.rpages ||
			p.NotReadyCount != tc.items-tc.ready || p.NotReadyPageCount != tc.pages-tc.rpages {
			t.Errorf("%s：件数页数 %+v", tc.name, p)
		}
		rc := p.ReasonCounts
		if rc.SourceModifying != tc.mod || rc.NotLocked != tc.notLocked || rc.NoArchiveNo != tc.noNo || rc.NoPages != tc.noPages ||
			rc.PagesNotApproved != tc.na {
			t.Errorf("%s：原因件数 %+v", tc.name, rc)
		}
		if got := strings.Join(p.reasonList(), " "); got != strings.Join(tc.reasons, " ") {
			t.Errorf("%s：不就绪的条目\n  %s\n期望\n  %s", tc.name, got, strings.Join(tc.reasons, " "))
		}
		if tc.scopeIDs != nil && !slicesEqual(p.ScopeIDs, tc.scopeIDs) {
			t.Errorf("%s：scope_ids %v", tc.name, p.ScopeIDs)
		}
		if !p.RequireLockedItem || p.MaxPageCount != 200000 || p.PageLimitExceeded || p.PageLimitMessage != nil || p.NotReadyTruncated {
			t.Errorf("%s：%+v", tc.name, p)
		}
	}

	// 原因的细节：not_locked 带现在的状态；pages_not_approved 带页数和前几页。
	p := precheck(cases[0].body)
	if r := p.NotReady[0].Reasons[0]; r.Count == nil || *r.Count != 1 || len(r.Pages) != 1 || r.Pages[0].PageNo != 2 ||
		r.Pages[0].FileID != bPending || r.Pages[0].ReviewStatus != "pending" || r.Message != "有 1 页不是审核通过（第 2 页：待审）" {
		t.Errorf("pages_not_approved：%+v", r)
	}
	if r := p.NotReady[1].Reasons[0]; r.ItemStatus == nil || *r.ItemStatus != "draft" || !strings.Contains(r.Message, "没有锁定") {
		t.Errorf("not_locked：%+v", r)
	}
	if r := p.NotReady[2].Reasons[0]; r.Code != "source_modifying" || !strings.Contains(r.Message, "重新归档") {
		t.Errorf("source_modifying：%+v", r)
	}
	if names := p.NotReady[0].CategoryNames; len(names) != 5 || names[4] != "办公室" {
		t.Errorf("category_names：%v", names)
	}

	// 建任务不满足条件：409 invalid_state，details 就是预检结果；什么都不写。
	tasksURL := "/api/projects/" + c.pid.String() + "/export-tasks"
	r := c.admin.Post(tasksURL, cases[0].body).ExpectError(http.StatusConflict, "invalid_state")
	var nr struct {
		Message string         `json:"message"`
		Details precheckResult `json:"details"`
	}
	r.JSON(&nr)
	if nr.Details.NotReadyCount != 3 || nr.Details.ReadyItemCount != 1 || !strings.Contains(nr.Message, "可以只导出就绪的 1 件") {
		t.Errorf("only_ready=false 有不就绪的：%s %+v", nr.Message, nr.Details)
	}
	c.admin.Post(tasksURL, map[string]any{"scope_type": "item", "scope_ids": []uuid.UUID{d.ID, cc.ID}, "export_format": "pdf", "only_ready": true}).
		ExpectError(http.StatusConflict, "invalid_state")
	empty := c.mkCat(&org, "custom", "EMPTY", "空节点")
	c.admin.Post(tasksURL, map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{empty.ID}, "export_format": "pdf"}).
		ExpectError(http.StatusConflict, "invalid_state")
	if n := QueryValue[int](c.Env, "SELECT count(*) FROM export_tasks"); n != 0 {
		t.Fatalf("409 不该写任务：%d", n)
	}

	// require_locked_item = false：没锁定的也能导出；修改中的照样不就绪（D 仍然没有页）。
	c.setSettings(map[string]any{"catalog": map[string]any{"lock_requires_approved": false}, "export": map[string]any{"require_locked_item": false}})
	p = precheck(cases[0].body)
	if p.RequireLockedItem || p.ReadyItemCount != 2 || strings.Join(p.reasonList(), " ") != short(b, "pages_not_approved")+" "+short(d, "source_modifying,no_pages") {
		t.Errorf("require_locked_item=false：%+v %v", p, p.reasonList())
	}

	// 参数 400。
	pre := "/api/projects/" + c.pid.String() + "/export-tasks:precheck"
	ids := []uuid.UUID{org.ID}
	for _, body := range []map[string]any{
		{"scope_type": "category", "scope_ids": ids, "resubmit_of": uuid.New(), "export_format": "pdf"},
		{"export_format": "pdf"},
		{"scope_type": "category", "scope_ids": []uuid.UUID{}, "export_format": "pdf"},
		{"scope_ids": ids, "export_format": "pdf"},
		{"scope_type": "folder", "scope_ids": ids, "export_format": "pdf"},
		{"scope_type": "category", "scope_ids": ids},
		{"scope_type": "category", "scope_ids": ids, "export_format": "pdf_ocr"},
		{"scope_type": "category", "scope_ids": ids, "export_format": "zip"},
		{"scope_type": "category", "resubmit_of": uuid.New(), "export_format": "pdf"},
		{"scope_type": "item", "scope_ids": make([]uuid.UUID, 5001), "export_format": "pdf"},
	} {
		c.admin.Post(pre, body).ExpectError(http.StatusBadRequest, "invalid_input")
	}
	// 范围里不存在、别的类型、已删除的 id：404，details.ids 按请求里的顺序。
	ghost := uuid.New()
	r = c.admin.Post(pre, map[string]any{"scope_type": "category", "scope_ids": []uuid.UUID{org.ID, ghost, a.ID}, "export_format": "pdf"}).
		ExpectError(http.StatusNotFound, "not_found")
	if got := errOf(r).Details.IDs; !slicesEqual(got, []uuid.UUID{ghost, a.ID}) {
		t.Errorf("details.ids：%v", got)
	}
	c.admin.Delete("/api/archive-items/" + d.ID.String()).Expect(http.StatusNoContent)
	r = c.admin.Post(pre, map[string]any{"scope_type": "item", "scope_ids": []uuid.UUID{a.ID, d.ID}, "export_format": "pdf"}).
		ExpectError(http.StatusNotFound, "not_found")
	if got := errOf(r).Details.IDs; !slicesEqual(got, []uuid.UUID{d.ID}) {
		t.Errorf("已删除的条目 details.ids：%v", got)
	}
	// 补交的任务不存在：404。
	c.admin.Post(pre, map[string]any{"resubmit_of": uuid.New(), "export_format": "pdf"}).ExpectError(http.StatusNotFound, "not_found")
}

func slicesEqual(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
