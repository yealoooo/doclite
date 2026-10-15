package catalog

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是归类的接口（M4-1；挂接、归类信息、看板在 M4-2）：
//
// 能进项目即可：
//
//	GET    /api/projects/{pid}/catalog-settings                         → 200 SettingsResponse
//
// catalog：
//
//	GET    /api/projects/{pid}/archive-categories?parent_id=            → 200 {parent, items}
//	POST   /api/projects/{pid}/archive-categories  {parent_id?, category_type, category_code, category_name} → 201 Category
//	PATCH  /api/projects/{pid}/archive-categories/{id}  {category_name?, category_code?, category_type?, parent_id?}
//	                                                                    → 200 UpdatedCategory
//	DELETE /api/projects/{pid}/archive-categories/{id}                  → 204
//	GET    /api/archive-categories/{id}/items?limit=&cursor=            → 200 ItemList
//	POST   /api/archive-categories/{id}/items  {title?, extra_fields?}  → 201 Item（可以没有请求体）
//	GET    /api/archive-items/{id}                                      → 200 Item
//	PATCH  /api/archive-items/{id}  {row_version, title?, extra_fields?, category_id?, item_no?} → 200 Item
//	DELETE /api/archive-items/{id}?row_version=                         → 204
//	POST   /api/archive-items/{id}/mark-done | back-to-draft | lock  {row_version} → 200 Item
//	POST   /api/projects/{pid}/archive-items:lock  {item_ids}           → 200 LockResult
//	POST   /api/projects/{pid}/archive-items:renumber  {category_id?}   → 200 RenumberResult（可以没有请求体）
//
// catalog 和 review_assign：
//
//	POST   /api/archive-items/{id}/unlock  {row_version}                → 200 Item
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	inProject := auth.PathProject("pid")
	r.Handle("GET /api/projects/{pid}/catalog-settings", g.InProject(g.Login(), inProject), h.settings)

	cat := g.AnyMenu(auth.MenuCatalog)
	r.Handle("GET /api/projects/{pid}/archive-categories", g.InProject(cat, inProject), h.listCategories)
	r.Handle("POST /api/projects/{pid}/archive-categories", g.InProject(cat, inProject), h.createCategory)
	r.Handle("PATCH /api/projects/{pid}/archive-categories/{id}", g.InProject(cat, inProject), h.updateCategory)
	r.Handle("DELETE /api/projects/{pid}/archive-categories/{id}", g.InProject(cat, inProject), h.deleteCategory)

	inCategory := g.InProject(cat, h.categoryProject)
	r.Handle("GET /api/archive-categories/{id}/items", inCategory, h.listItems)
	r.Handle("POST /api/archive-categories/{id}/items", inCategory, h.createItem)

	inItem := g.InProject(cat, h.itemProject)
	r.Handle("GET /api/archive-items/{id}", inItem, h.getItem)
	r.Handle("PATCH /api/archive-items/{id}", inItem, h.updateItem)
	r.Handle("DELETE /api/archive-items/{id}", inItem, h.deleteItem)
	r.Handle("POST /api/archive-items/{id}/mark-done", inItem, h.action(ActionMarkDone))
	r.Handle("POST /api/archive-items/{id}/back-to-draft", inItem, h.action(ActionBackToDraft))
	r.Handle("POST /api/archive-items/{id}/lock", inItem, h.action(ActionLock))
	// 解锁要同时有 catalog 和 review_assign（用户决定 Q5）。
	r.Handle("POST /api/archive-items/{id}/unlock",
		g.InProject(g.AllMenus(auth.MenuCatalog, auth.MenuReviewAssign), h.itemProject), h.action(ActionUnlock))

	r.Handle("POST /api/projects/{pid}/archive-items:lock", g.InProject(cat, inProject), h.lockItems)
	r.Handle("POST /api/projects/{pid}/archive-items:renumber", g.InProject(cat, inProject), h.renumber)
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
	var parentID *uuid.UUID
	if v := strings.TrimSpace(r.URL.Query().Get("parent_id")); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return httpx.Invalid("parent_id 不是合法的 UUID")
		}
		parentID = &id
	}
	out, err := h.svc.ListCategories(r.Context(), auth.ProjectID(r.Context()), parentID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createCategoryRequest struct {
	ParentID     *uuid.UUID `json:"parent_id"`
	CategoryType string     `json:"category_type"`
	CategoryCode string     `json:"category_code"`
	CategoryName string     `json:"category_name"`
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) error {
	var req createCategoryRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	out, err := h.svc.CreateCategory(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), CategoryInput{
		ParentID: req.ParentID, Type: req.CategoryType, Code: req.CategoryCode, Name: req.CategoryName,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
	return nil
}

type updateCategoryRequest struct {
	CategoryName *string    `json:"category_name"`
	CategoryCode *string    `json:"category_code"`
	CategoryType *string    `json:"category_type"`
	ParentID     *uuid.UUID `json:"parent_id"`
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req updateCategoryRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	out, err := h.svc.UpdateCategory(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), id,
		CategoryPatch{Name: req.CategoryName, Code: req.CategoryCode, Type: req.CategoryType, ParentID: req.ParentID})
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
	q := r.URL.Query()
	limit := DefaultItemLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxItemLimit {
			return httpx.Invalid("limit 要在 1 到 %d 之间", MaxItemLimit)
		}
		limit = n
	}
	out, err := h.svc.ListItems(r.Context(), auth.ProjectID(r.Context()), id, limit, strings.TrimSpace(q.Get("cursor")))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createItemRequest struct {
	Title       *string        `json:"title"`
	ExtraFields map[string]any `json:"extra_fields"`
	FileIDs     []uuid.UUID    `json:"file_ids"`
}

func (h *Handler) createItem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req createItemRequest
	if _, err := httpx.ReadOptionalJSON(r, &req); err != nil {
		return err
	}
	if len(req.FileIDs) > 0 {
		// TODO(M4-2)：带 file_ids 新建并挂上。
		return httpx.Invalid("新建条目时挂接文件（file_ids）还没有实现")
	}
	out, err := h.svc.CreateItem(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), id,
		ItemInput{Title: req.Title, Fields: req.ExtraFields})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
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
