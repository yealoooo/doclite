package catalog

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是归类的接口（归类树、条目、著录、锁定；M9-1 的整个文件夹归档和封存；归类信息、看板）：
//
// 能进项目即可：
//
//	GET    /api/projects/{pid}/catalog-settings                         → 200 SettingsResponse
//	POST   /api/projects/{pid}/catalog-placements  {file_ids}           → 200 Placements（0–500 个）
//	GET    /api/files/{fid}/catalog                                     → 200 FileCatalog
//
// dashboard：
//
//	GET    /api/projects/{pid}/catalog-summary?folder_id=               → 200 Summary
//
// catalog：
//
//	GET    /api/projects/{pid}/archive-categories?parent_id=            → 200 {parent, items}
//	POST   /api/projects/{pid}/archive-categories  {parent_id?, category_type, category_code, category_name} → 201 Category
//	PATCH  /api/projects/{pid}/archive-categories/{id}  {category_name?, category_code?, category_type?, parent_id?}
//	                                                                    → 200 UpdatedCategory
//	DELETE /api/projects/{pid}/archive-categories/{id}                  → 204
//	GET    /api/archive-categories/{id}/items?limit=&cursor=            → 200 ItemList
//	POST   /api/archive-categories/{id}/items:from-folders  {folder_ids, titles?, dry_run?}
//	                                                                    → 200 ArchiveResult（预览）/ 201（归档）
//	GET    /api/archive-items/{id}                                      → 200 Item
//	PATCH  /api/archive-items/{id}  {row_version, title?, extra_fields?, category_id?, item_no?} → 200 Item
//	DELETE /api/archive-items/{id}?row_version=                         → 204（撤销归档，页一并移出）
//	POST   /api/archive-items/{id}/lock | unlock  {row_version}         → 200 Item
//	POST   /api/archive-items/{id}/unseal  {row_version, reason?}       → 200 Item（解除封存）
//	GET    /api/archive-items/{id}/reseal-preview                       → 200 ResealPreview
//	POST   /api/archive-items/{id}/reseal  {row_version}                → 200 Item（重新归档）
//	POST   /api/projects/{pid}/archive-items:lock  {item_ids}           → 200 LockResult
//	POST   /api/projects/{pid}/archive-items:renumber  {category_id?}   → 200 RenumberResult（可以没有请求体）
//	GET    /api/projects/{pid}/archive-items:modifying                  → 200 ModifyingList（修改中的件，≤ 500 件）
//
// 归类的全部操作（含解锁）只要 catalog + 能进项目（用户 2026-10-19）。
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	inProject := auth.PathProject("pid")
	r.Handle("GET /api/projects/{pid}/catalog-settings", g.InProject(g.Login(), inProject), h.settings)
	r.Handle("POST /api/projects/{pid}/catalog-placements", g.InProject(g.Login(), inProject), h.placements)
	r.Handle("GET /api/files/{fid}/catalog", g.InProject(g.Login(), h.fileProject), h.fileCatalog)
	r.Handle("GET /api/projects/{pid}/catalog-summary", g.InProject(g.AnyMenu(auth.MenuDashboard), inProject), h.summary)

	cat := g.AnyMenu(auth.MenuCatalog)
	r.Handle("GET /api/projects/{pid}/archive-categories", g.InProject(cat, inProject), h.listCategories)
	r.Handle("POST /api/projects/{pid}/archive-categories", g.InProject(cat, inProject), h.createCategory)
	r.Handle("PATCH /api/projects/{pid}/archive-categories/{id}", g.InProject(cat, inProject), h.updateCategory)
	r.Handle("DELETE /api/projects/{pid}/archive-categories/{id}", g.InProject(cat, inProject), h.deleteCategory)

	inCategory := g.InProject(cat, h.categoryProject)
	r.Handle("GET /api/archive-categories/{id}/items", inCategory, h.listItems)
	r.Handle("POST /api/archive-categories/{id}/items:from-folders", inCategory, h.archiveFolders)

	inItem := g.InProject(cat, h.itemProject)
	r.Handle("GET /api/archive-items/{id}", inItem, h.getItem)
	r.Handle("PATCH /api/archive-items/{id}", inItem, h.updateItem)
	r.Handle("DELETE /api/archive-items/{id}", inItem, h.deleteItem)
	r.Handle("POST /api/archive-items/{id}/lock", inItem, h.action(ActionLock))
	r.Handle("POST /api/archive-items/{id}/unlock", inItem, h.action(ActionUnlock))
	r.Handle("POST /api/archive-items/{id}/unseal", inItem, h.unseal)
	r.Handle("GET /api/archive-items/{id}/reseal-preview", inItem, h.resealPreview)
	r.Handle("POST /api/archive-items/{id}/reseal", inItem, h.reseal)

	r.Handle("POST /api/projects/{pid}/archive-items:lock", g.InProject(cat, inProject), h.lockItems)
	r.Handle("POST /api/projects/{pid}/archive-items:renumber", g.InProject(cat, inProject), h.renumber)
	r.Handle("GET /api/projects/{pid}/archive-items:modifying", g.InProject(cat, inProject), h.modifying)
}

func (h *Handler) categoryProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfCategory(r.Context(), id)
}

func (h *Handler) itemProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfItem(r.Context(), id)
}

func (h *Handler) fileProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfFile(r.Context(), id)
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) error {
	out, err := h.svc.Settings(r.Context(), auth.ProjectID(r.Context()))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// ---------------------------------------------------------------------------
// 归类节点
// ---------------------------------------------------------------------------

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) error {
	parentID, err := httpx.QueryUUID(r, "parent_id")
	if err != nil {
		return err
	}
	out, err := h.svc.ListCategories(r.Context(), auth.ProjectID(r.Context()), parentID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) error {
	var in CategoryInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	out, err := h.svc.CreateCategory(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
	return nil
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in CategoryPatch
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	out, err := h.svc.UpdateCategory(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), id,
		in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteCategory(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---------------------------------------------------------------------------
// 条目
// ---------------------------------------------------------------------------

func (h *Handler) listItems(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	limit, err := httpx.QueryLimit(r, DefaultItemLimit, MaxItemLimit)
	if err != nil {
		return err
	}
	out, err := h.svc.ListItems(r.Context(), auth.ProjectID(r.Context()), id, limit, strings.TrimSpace(r.URL.Query().Get("cursor")))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) archiveFolders(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in ArchiveInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	out, err := h.svc.ArchiveFolders(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), id,
		in)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if in.DryRun {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, out)
	return nil
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	out, err := h.svc.GetItem(r.Context(), auth.ProjectID(r.Context()), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type updateItemRequest struct {
	RowVersion  *int           `json:"row_version"`
	Title       *string        `json:"title"`
	ExtraFields map[string]any `json:"extra_fields"`
	CategoryID  *uuid.UUID     `json:"category_id"`
	ItemNo      *int           `json:"item_no"`
}

var errMissingRowVersion = httpx.Invalid("缺少 row_version（用读到的条目里的 row_version）")

func (h *Handler) updateItem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req updateItemRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.RowVersion == nil {
		return errMissingRowVersion
	}
	out, err := h.svc.UpdateItem(r.Context(), auth.Current(r.Context()).UserID, id, ItemPatch{
		RowVersion: *req.RowVersion, Title: req.Title, Fields: req.ExtraFields, CategoryID: req.CategoryID, ItemNo: req.ItemNo,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var rv *int
	if v := strings.TrimSpace(r.URL.Query().Get("row_version")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return httpx.Invalid("row_version 要是正整数（用读到的条目里的 row_version）")
		}
		rv = &n
	}
	if err := h.svc.DeleteItem(r.Context(), auth.Current(r.Context()).UserID, id, rv); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type rowVersionRequest struct {
	RowVersion *int `json:"row_version"`
}

func (h *Handler) action(action string) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := httpx.PathUUID(r, "id")
		if err != nil {
			return err
		}
		var req rowVersionRequest
		if err := httpx.ReadJSON(r, &req); err != nil {
			return err
		}
		if req.RowVersion == nil {
			return errMissingRowVersion
		}
		out, err := h.svc.ItemAction(r.Context(), auth.Current(r.Context()).UserID, id, action, *req.RowVersion)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, out)
		return nil
	}
}

type lockItemsRequest struct {
	ItemIDs []uuid.UUID `json:"item_ids"`
}

func (h *Handler) lockItems(w http.ResponseWriter, r *http.Request) error {
	var req lockItemsRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	out, err := h.svc.LockItems(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), req.ItemIDs)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type renumberRequest struct {
	CategoryID *uuid.UUID `json:"category_id"`
}

func (h *Handler) renumber(w http.ResponseWriter, r *http.Request) error {
	var req renumberRequest
	if _, err := httpx.ReadOptionalJSON(r, &req); err != nil {
		return err
	}
	out, err := h.svc.Renumber(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), req.CategoryID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type unsealRequest struct {
	RowVersion *int    `json:"row_version"`
	Reason     *string `json:"reason"`
}

func (h *Handler) unseal(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req unsealRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.RowVersion == nil {
		return errMissingRowVersion
	}
	out, err := h.svc.Unseal(r.Context(), auth.Current(r.Context()).UserID, id, *req.RowVersion, req.Reason)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) modifying(w http.ResponseWriter, r *http.Request) error {
	out, err := h.svc.Modifying(r.Context(), auth.ProjectID(r.Context()))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) resealPreview(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	out, err := h.svc.ResealPreview(r.Context(), auth.ProjectID(r.Context()), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) reseal(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req rowVersionRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.RowVersion == nil {
		return errMissingRowVersion
	}
	out, err := h.svc.Reseal(r.Context(), auth.Current(r.Context()).UserID, id, *req.RowVersion)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// ---------------------------------------------------------------------------
// 归类信息、看板
// ---------------------------------------------------------------------------

type placementsRequest struct {
	FileIDs *[]uuid.UUID `json:"file_ids"`
}

func (h *Handler) placements(w http.ResponseWriter, r *http.Request) error {
	var req placementsRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.FileIDs == nil {
		return httpx.Invalid("缺少 file_ids")
	}
	out, err := h.svc.Placements(r.Context(), auth.ProjectID(r.Context()), *req.FileIDs)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) fileCatalog(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	out, err := h.svc.FileCatalog(r.Context(), auth.ProjectID(r.Context()), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) error {
	folderID, err := httpx.QueryUUID(r, "folder_id")
	if err != nil {
		return err
	}
	out, err := h.svc.Summary(r.Context(), auth.ProjectID(r.Context()), folderID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}
