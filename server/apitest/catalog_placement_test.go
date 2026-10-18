package apitest

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

// 归类的测试共用的：文件日志、409 file_cataloged 的错误体、事件里的 id。

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
	b := c.mkFile(uuid.Nil, "pending")
	gone := c.mkFile(uuid.Nil, "approved")
	c.Exec(`UPDATE folder_files SET deleted_at = now() WHERE file_id = $1`, gone)
	other := c.CreateProject("PY")
	foreign := c.mkFileIn(other.ID, other.RootFolderID, "approved")
	fa, files := c.mkFolder(uuid.Nil, "甲", "approved", "approved")
	a := files[1]
	it := c.archiveOne(org.ID, fa, "条目甲")
	// 直接在根节点下的条目（归档不能放到根节点下，归档后移过去）。
	ft, tops := c.mkFolder(uuid.Nil, "顶层", "approved")
	top := tops[0]
	topItem := c.archiveOne(org.ID, ft, "顶层")
	c.patchItem(topItem.ID, map[string]any{"category_id": c.rootID}).Expect(200)

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
