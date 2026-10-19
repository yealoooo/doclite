package export

import (
	"net/http"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是导出的接口（requirements 5.11、第 8 节；都要 export 菜单 + 能进项目）：
//
//	POST /api/projects/{pid}/export-tasks:precheck  {scope_type?, scope_ids? | resubmit_of?, export_format, only_approved?}
//	                                                                          → 200 Precheck
//	POST /api/projects/{pid}/export-tasks  {…同上, only_ready?}              → 201 Task（不满足条件 409 invalid_state，details 是预检结果）
//	GET  /api/projects/{pid}/export-tasks?limit=&cursor=                     → 200 {items, next_cursor}
//	GET  /api/export-tasks/{id}                                              → 200 Task
//	GET  /api/export-tasks/{id}/files?limit=&cursor=                         → 200 TaskFiles
//	GET  /api/export-tasks/{id}/changes                                      → 200 Changes（不是 done 409）
//	GET  /api/export-tasks/{id}/download                                     → 200 Download（不是 done、过期 409；存储停用 409 storage_unavailable）
//	POST /api/export-tasks/{id}/cancel                                       → 200 Task（已结束 409）
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	menu := g.AnyMenu(auth.MenuExport)
	inProject := g.InProject(menu, auth.PathProject("pid"))
	r.Handle("POST /api/projects/{pid}/export-tasks:precheck", inProject, h.precheck)
	r.Handle("POST /api/projects/{pid}/export-tasks", inProject, h.create)
	r.Handle("GET /api/projects/{pid}/export-tasks", inProject, h.list)

	inTask := g.InProject(menu, h.taskProject)
	r.Handle("GET /api/export-tasks/{id}", inTask, h.get)
	r.Handle("GET /api/export-tasks/{id}/files", inTask, h.files)
	r.Handle("GET /api/export-tasks/{id}/changes", inTask, h.changes)
	r.Handle("GET /api/export-tasks/{id}/download", inTask, h.download)
	r.Handle("POST /api/export-tasks/{id}/cancel", inTask, h.cancel)
}

func (h *Handler) taskProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfTask(r.Context(), id)
}

func (h *Handler) precheck(w http.ResponseWriter, r *http.Request) error {
	var body RequestBody
	if err := httpx.ReadJSON(r, &body); err != nil {
		return err
	}
	out, err := h.svc.Precheck(r.Context(), auth.ProjectID(r.Context()), &body)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var body RequestBody
	if err := httpx.ReadJSON(r, &body); err != nil {
		return err
	}
	out, err := h.svc.Create(r.Context(), auth.Current(r.Context()).UserID, auth.ProjectID(r.Context()), &body)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
	return nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, err := httpx.QueryLimit(r, defaultTaskLimit, maxTaskLimit)
	if err != nil {
		return err
	}
	out, err := h.svc.List(r.Context(), auth.ProjectID(r.Context()), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	out, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) files(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	limit, err := httpx.QueryLimit(r, defaultFileLimit, maxFileLimit)
	if err != nil {
		return err
	}
	out, err := h.svc.Files(r.Context(), id, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) changes(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	out, err := h.svc.Changes(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	out, err := h.svc.Download(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	out, err := h.svc.Cancel(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}
