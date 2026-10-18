package apitest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// 归类的测试辅助。条目只能由整个文件夹归档产生（M9-1）：mkItem 建一个新文件夹（放在项目根文件夹下）、造文件、
// 归档；改页走"解除封存 → 改文件夹 → 重新归档"（addPage）。

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
	ID               uuid.UUID      `json:"id"`
	ProjectID        uuid.UUID      `json:"project_id"`
	CategoryID       uuid.UUID      `json:"category_id"`
	CategoryPath     string         `json:"category_path"`
	ItemNo           int            `json:"item_no"`
	ArchiveNo        *string        `json:"archive_no"`
	ArchiveNoReason  *itemReason    `json:"archive_no_reason"`
	Title            string         `json:"title"`
	ExtraFields      map[string]any `json:"extra_fields"`
	FieldIssues      []fieldIssue   `json:"field_issues"`
	Status           string         `json:"status"`
	SourceFolderID   uuid.UUID      `json:"source_folder_id"`
	SourceFolderPath string         `json:"source_folder_path"`
	SourceStatus     string         `json:"source_status"`
	UnsealedBy       *uuid.UUID     `json:"unsealed_by"`
	UnsealedByName   *string        `json:"unsealed_by_name"`
	UnsealReason     *string        `json:"unseal_reason"`
	RowVersion       int            `json:"row_version"`
	PageCount        int            `json:"page_count"`
	PageCounts       map[string]int `json:"page_counts"`
	LockedAt         *string        `json:"locked_at"`
	Pages            []struct {
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
	n       int // 文件夹名的序号
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

// archive 归档（POST items:from-folders）。
func (c *catalogEnv) archive(cat uuid.UUID, body map[string]any) *Response {
	c.t.Helper()
	return c.admin.Post("/api/archive-categories/"+cat.String()+"/items:from-folders", body)
}

// archiveResult 是归档的响应。
type archiveResult struct {
	CategoryID uuid.UUID `json:"category_id"`
	DryRun     bool      `json:"dry_run"`
	Items      []struct {
		FolderID   uuid.UUID  `json:"folder_id"`
		FolderName string     `json:"folder_name"`
		FolderPath string     `json:"folder_path"`
		Title      string     `json:"title"`
		PageCount  int        `json:"page_count"`
		ItemNo     int        `json:"item_no"`
		ArchiveNo  *string    `json:"archive_no"`
		ItemID     *uuid.UUID `json:"item_id"`
	} `json:"items"`
	Skipped []struct {
		FolderID       uuid.UUID `json:"folder_id"`
		FolderName     string    `json:"folder_name"`
		Reason         string    `json:"reason"`
		Message        string    `json:"message"`
		PageCount      int       `json:"page_count"`
		SubfolderCount int       `json:"subfolder_count"`
		Item           *struct {
			ItemID        uuid.UUID `json:"item_id"`
			ItemNo        int       `json:"item_no"`
			CategoryNames []string  `json:"category_names"`
		} `json:"item"`
	} `json:"skipped"`
	LooseFiles []struct {
		FolderID   uuid.UUID `json:"folder_id"`
		FolderName string    `json:"folder_name"`
		FolderPath string    `json:"folder_path"`
		FileCount  int       `json:"file_count"`
	} `json:"loose_files"`
}

// mkFolder 在 parent（uuid.Nil 是项目根文件夹）下建文件夹，造 statuses 个文件（直接写库），返回文件夹和文件。
func (c *catalogEnv) mkFolder(parent uuid.UUID, name string, statuses ...string) (uuid.UUID, []uuid.UUID) {
	c.t.Helper()
	if parent == uuid.Nil {
		parent = c.folder
	}
	f := mkdir(c.admin, parent, name).ID
	files := make([]uuid.UUID, len(statuses))
	for i, st := range statuses {
		files[i] = c.mkFile(f, st)
	}
	return f, files
}

// archiveOne 把一个文件夹归档到节点 cat（title 为空用文件夹名），返回条目详情。
func (c *catalogEnv) archiveOne(cat, folder uuid.UUID, title string) itemView {
	c.t.Helper()
	body := map[string]any{"folder_ids": []uuid.UUID{folder}}
	if title != "" {
		body["titles"] = map[string]any{folder.String(): title}
	}
	res := Decode[archiveResult](c.archive(cat, body).Expect(http.StatusCreated))
	if len(res.Items) != 1 || res.Items[0].ItemID == nil {
		c.t.Fatalf("归档：%+v", res)
	}
	return c.item(*res.Items[0].ItemID)
}

// mkItem 建一件：新文件夹（"件 n"）里一个通过的文件（body 带 file_ids 时是把这些文件移进新文件夹），归档到 cat；
// body 的 title 走 titles，extra_fields 归档后再 PATCH。
func (c *catalogEnv) mkItem(cat uuid.UUID, body map[string]any) itemView {
	c.t.Helper()
	c.n++
	name := fmt.Sprintf("件 %03d", c.n)
	var folder uuid.UUID
	if ids, ok := body["file_ids"].([]uuid.UUID); ok {
		folder, _ = c.mkFolder(uuid.Nil, name)
		c.moveFiles(folder, ids...)
	} else {
		folder, _ = c.mkFolder(uuid.Nil, name, "approved")
	}
	title, _ := body["title"].(string)
	it := c.archiveOne(cat, folder, title)
	if f, ok := body["extra_fields"]; ok {
		it = Decode[itemView](c.patchItem(it.ID, map[string]any{"extra_fields": f}).Expect(http.StatusOK))
	}
	return it
}

// moveFiles 把这些文件（file_id）移到 folder（folder-files:move），按原文件夹的顺序排到末尾。
func (c *catalogEnv) moveFiles(folder uuid.UUID, fileIDs ...uuid.UUID) {
	c.t.Helper()
	var ffIDs []uuid.UUID
	for _, fid := range fileIDs {
		ffIDs = append(ffIDs, QueryValue[uuid.UUID](c.Env, `SELECT id FROM folder_files WHERE file_id = $1`, fid))
	}
	c.admin.Post("/api/folder-files:move", map[string]any{"ids": ffIDs, "folder_id": folder}).Expect(http.StatusOK)
}

// unseal、reseal 用条目现在的 row_version 解除封存、重新归档。
func (c *catalogEnv) unseal(id uuid.UUID) *Response {
	c.t.Helper()
	return c.admin.Post("/api/archive-items/"+id.String()+"/unseal", map[string]any{"row_version": c.item(id).RowVersion})
}

func (c *catalogEnv) reseal(id uuid.UUID) *Response {
	c.t.Helper()
	return c.admin.Post("/api/archive-items/"+id.String()+"/reseal", map[string]any{"row_version": c.item(id).RowVersion})
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

// pageIDs 是条目详情里的页（按页号）。
func (v itemView) pageIDs() []uuid.UUID {
	out := []uuid.UUID{}
	for _, p := range v.Pages {
		out = append(out, p.FileID)
	}
	return out
}

func errOf(r *Response) errorBody { return Decode[errorBody](r) }
