package apitest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// 归类（M4-1）的测试辅助。

// catNode 是归类节点的响应（修改时另有 renumbered、archive_no_conflicts）。
type catNode struct {
	ID                 uuid.UUID  `json:"id"`
	ProjectID          uuid.UUID  `json:"project_id"`
	ParentID           *uuid.UUID `json:"parent_id"`
	PathCode           string     `json:"path_code"`
	CategoryType       string     `json:"category_type"`
	CategoryCode       string     `json:"category_code"`
	CategoryName       string     `json:"category_name"`
	SortNo             int        `json:"sort_no"`
	ChildCount         int        `json:"child_count"`
	ItemCount          int        `json:"item_count"`
	Renumbered         int        `json:"renumbered"`
	ArchiveNoConflicts int        `json:"archive_no_conflicts"`
}

// itemReason 是 archive_no_reason。
type itemReason struct {
	Code               string     `json:"code"`
	Message            string     `json:"message"`
	Missing            []string   `json:"missing"`
	ArchiveNo          *string    `json:"archive_no"`
	ConflictItemID     *uuid.UUID `json:"conflict_item_id"`
	ConflictCategoryID *uuid.UUID `json:"conflict_category_id"`
	ConflictItemNo     *int       `json:"conflict_item_no"`
}

// itemView 是条目详情。
type itemView struct {
	ID              uuid.UUID      `json:"id"`
	ProjectID       uuid.UUID      `json:"project_id"`
	CategoryID      uuid.UUID      `json:"category_id"`
	CategoryPath    string         `json:"category_path"`
	ItemNo          int            `json:"item_no"`
	ArchiveNo       *string        `json:"archive_no"`
	ArchiveNoReason *itemReason    `json:"archive_no_reason"`
	Title           string         `json:"title"`
	ExtraFields     map[string]any `json:"extra_fields"`
	FieldIssues     []fieldIssue   `json:"field_issues"`
	Status          string         `json:"status"`
	RowVersion      int            `json:"row_version"`
	PageCount       int            `json:"page_count"`
	PageCounts      map[string]int `json:"page_counts"`
	LockedAt        *string        `json:"locked_at"`
	Pages           []struct {
		FileID       uuid.UUID `json:"file_id"`
		PageNo       int       `json:"page_no"`
		FileName     string    `json:"file_name"`
		FolderPath   string    `json:"folder_path"`
		ReviewStatus string    `json:"review_status"`
	} `json:"pages"`
}

type fieldIssue struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// errorBody 是错误响应体。
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details struct {
		IDs    []uuid.UUID  `json:"ids"`
		Fields []fieldIssue `json:"fields"`
	} `json:"details"`
}

func str(v *string) string {
	if v == nil {
		return "<nil>"
	}
	return *v
}

// catalogEnv 是一个项目和它的归类根。
type catalogEnv struct {
	*Env
	t       *testing.T
	pid     uuid.UUID
	rootID  uuid.UUID
	folder  uuid.UUID // 根文件夹（造页用）
	adminID uuid.UUID
	admin   *Client
}

func newCatalogEnv(t *testing.T, code string) *catalogEnv {
	t.Helper()
	env := NewEnv(t)
	d := env.CreateProject(code)
	return &catalogEnv{Env: env, t: t, pid: d.ID, rootID: d.CategoryRootID, folder: d.RootFolderID,
		adminID: env.UserID(AdminUsername), admin: env.Admin()}
}

func (c *catalogEnv) catPath() string {
	return "/api/projects/" + c.pid.String() + "/archive-categories"
}

// mkCat 建节点（parent 为 nil 时建在根节点下）。
func (c *catalogEnv) mkCat(parent *catNode, typ, code, name string) catNode {
	c.t.Helper()
	body := map[string]any{"category_type": typ, "category_code": code, "category_name": name}
	if parent != nil {
		body["parent_id"] = parent.ID
	}
	return Decode[catNode](c.admin.Post(c.catPath(), body).Expect(http.StatusCreated))
}

// mkChain 建 全宗 Z109 / 门类 WS / 年度 2025 / 保管期限 Y / 机构 BGS，返回各级。
func (c *catalogEnv) mkChain() (fonds, cat, year, ret, org catNode) {
	c.t.Helper()
	fonds = c.mkCat(nil, "fonds", "Z109", "全宗")
	cat = c.mkCat(&fonds, "category", "WS", "文书")
	year = c.mkCat(&cat, "year", "2025", "2025 年")
	ret = c.mkCat(&year, "retention", "Y", "永久")
	org = c.mkCat(&ret, "org", "BGS", "办公室")
	return
}

func (c *catalogEnv) mkItem(cat uuid.UUID, body map[string]any) itemView {
	c.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	return Decode[itemView](c.admin.Post("/api/archive-categories/"+cat.String()+"/items", body).Expect(http.StatusCreated))
}

func (c *catalogEnv) item(id uuid.UUID) itemView {
	c.t.Helper()
	return Decode[itemView](c.admin.Get("/api/archive-items/" + id.String()).Expect(http.StatusOK))
}

func (c *catalogEnv) patchItem(id uuid.UUID, body map[string]any) *Response {
	c.t.Helper()
	if _, ok := body["row_version"]; !ok {
		body["row_version"] = c.item(id).RowVersion
	}
	return c.admin.Patch("/api/archive-items/"+id.String(), body)
}

func (c *catalogEnv) act(id uuid.UUID, action string) *Response {
	c.t.Helper()
	return c.admin.Post("/api/archive-items/"+id.String()+"/"+action, map[string]any{"row_version": c.item(id).RowVersion})
}

func (c *catalogEnv) setSettings(settings map[string]any) {
	c.t.Helper()
	c.admin.Patch("/api/system/projects/"+c.pid.String(), map[string]any{"settings": settings}).Expect(http.StatusOK)
}

// mkFileIn 造一个文件（files、folder_files 直接写库，没有快照、版本号按 0；上传要 S3，这里只要 file_id 和审核状态），
// 放在 folder 里，返回 file_id。
func (e *Env) mkFileIn(pid, folder uuid.UUID, reviewStatus string) uuid.UUID {
	e.t.Helper()
	fid := uuid.Must(uuid.NewV7())
	e.Exec(`INSERT INTO files (id) VALUES ($1)`, fid)
	e.Exec(`INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, review_status, created_by)
SELECT gen_random_uuid(), $1, $2, $3, $4,
  (SELECT coalesce(max(sort_no), 0) + 1 FROM folder_files WHERE folder_id = $2), $5, u.id
FROM users u WHERE u.username = 'admin'`, pid, folder, fid, "IMG-"+fid.String()[24:]+".jpg", reviewStatus)
	return fid
}

// mkFile 在这个项目的 folder（uuid.Nil 是根文件夹）里造一个文件。
func (c *catalogEnv) mkFile(folder uuid.UUID, reviewStatus string) uuid.UUID {
	c.t.Helper()
	if folder == uuid.Nil {
		folder = c.folder
	}
	return c.mkFileIn(c.pid, folder, reviewStatus)
}

func filesPath(itemID uuid.UUID) string { return "/api/archive-items/" + itemID.String() + "/files" }

// putPages 用条目现在的 row_version 整体替换页。
func (c *catalogEnv) putPages(itemID uuid.UUID, fileIDs ...uuid.UUID) *Response {
	c.t.Helper()
	if fileIDs == nil {
		fileIDs = []uuid.UUID{}
	}
	return c.admin.Put(filesPath(itemID), map[string]any{"row_version": c.item(itemID).RowVersion, "file_ids": fileIDs})
}

// pageIDs 是条目详情里的页（按页号）。
func (v itemView) pageIDs() []uuid.UUID {
	out := []uuid.UUID{}
	for _, p := range v.Pages {
		out = append(out, p.FileID)
	}
	return out
}

// addPage 给条目末尾挂一页：文件直接写库（审核状态 reviewStatus），挂接走 PUT /api/archive-items/{id}/files。
func (c *catalogEnv) addPage(itemID uuid.UUID, reviewStatus string) uuid.UUID {
	c.t.Helper()
	fid := c.mkFile(uuid.Nil, reviewStatus)
	c.putPages(itemID, append(c.item(itemID).pageIDs(), fid)...).Expect(http.StatusOK)
	return fid
}

func errOf(r *Response) errorBody { return Decode[errorBody](r) }
