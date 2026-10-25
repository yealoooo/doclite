package folder

import (
	"net/http"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是文件目录的接口（requirements 5.5、第 8 节）。folders/{id} 是文件夹 id，folder-files/{id} 是 folder_files 的 id。
//
// scan 或 files（读、列、建文件夹；热文件夹要用）：
//
//	GET    /api/folders/{id}            → 200 FolderItem（本身和祖先链）
//	GET    /api/folders/{id}/children   → 200 Children
//	POST   /api/folders  {parent_id, folder_name, remark?, if_exists?: "return"} → 201 FolderItem；同名且 if_exists 时 200
//
// files（整理）：
//
//	PATCH  /api/folders/{id}  {folder_name?, remark?, parent_id?} → 200 FolderItem
//	DELETE /api/folders/{id}                                       → 204
//	PATCH  /api/folder-files/{id}  {file_name?, folder_id?}        → 200 FolderFileUpdated
//	POST   /api/folder-files/{id}/copy  {folder_id?}（可以没有请求体）→ 201 FolderFileCopied
//	DELETE /api/folder-files/{id}                                  → 204
//	POST   /api/folder-files:move  {ids, folder_id}                → 200 MoveResult
//	POST   /api/folder-files:delete  {ids}                         → 204
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	read := g.AnyMenu(auth.MenuScan, auth.MenuFiles)
	files := g.AnyMenu(auth.MenuFiles)
	r.Handle("GET /api/folders/{id}", g.InProject(read, h.folderProject), h.get)
	r.Handle("GET /api/folders/{id}/children", g.InProject(read, h.folderProject), h.children)
	r.Handle("POST /api/folders", read, h.create)
	r.Handle("PATCH /api/folders/{id}", g.InProject(files, h.folderProject), h.update)
	r.Handle("DELETE /api/folders/{id}", g.InProject(files, h.folderProject), h.delete)
	r.Handle("PATCH /api/folder-files/{id}", g.InProject(files, h.folderFileProject), h.updateFolderFile)
	r.Handle("POST /api/folder-files/{id}/copy", g.InProject(files, h.folderFileProject), h.copyFolderFile)
	r.Handle("DELETE /api/folder-files/{id}", g.InProject(files, h.folderFileProject), h.deleteFolderFile)
	r.Handle("POST /api/folder-files:move", files, h.moveFolderFiles)
	r.Handle("POST /api/folder-files:delete", files, h.deleteFolderFiles)
}

func (h *Handler) folderProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOf(r.Context(), id)
}

func (h *Handler) folderFileProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfFolderFile(r.Context(), id)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	item, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, item)
	return nil
}

func (h *Handler) children(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	c, err := h.svc.Children(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	item, created, err := h.svc.Create(r.Context(), auth.Current(r.Context()).UserID, in)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, item)
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in UpdateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	item, err := h.svc.Update(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), id,
		in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, item)
	return nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := h.svc.Delete(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) updateFolderFile(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in FolderFileInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.UpdateFolderFile(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), id,
		in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) copyFolderFile(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in FolderFileInput
	if err := httpx.ReadOptionalJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.CopyFolderFile(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), id, in.FolderID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
	return nil
}

func (h *Handler) deleteFolderFile(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := h.svc.DeleteFolderFile(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type bulkRequest struct {
	IDs      []uuid.UUID `json:"ids"`
	FolderID uuid.UUID   `json:"folder_id"`
}

func (h *Handler) moveFolderFiles(w http.ResponseWriter, r *http.Request) error {
	var req bulkRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.FolderID == uuid.Nil {
		return httpx.Invalid("缺少 folder_id")
	}
	ctx := r.Context()
	uid := auth.Current(ctx).UserID
	scope, err := h.svc.ResolveBulk(ctx, uid, req.IDs)
	if err != nil {
		return err
	}
	res, err := h.svc.MoveFolderFiles(ctx, uid, scope, req.FolderID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) deleteFolderFiles(w http.ResponseWriter, r *http.Request) error {
	var req bulkRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	uid := auth.Current(ctx).UserID
	scope, err := h.svc.ResolveBulk(ctx, uid, req.IDs)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteFolderFiles(ctx, uid, scope); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
