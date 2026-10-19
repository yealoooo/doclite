package project

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
)

// Handler 是项目的接口：
//
//	GET    /api/projects                              → 200 [MyProject]（登录即可）
//	GET    /api/projects/{pid}/stream                 → SSE（能进项目即可，requirements 第 7 节）
//
// system.projects：
//
//	GET    /api/system/projects?keyword=&status=&page=&page_size= → 200 Page<Item>（新建的在前）
//	POST   /api/system/projects  {project_code, project_name, description?} → 201 Detail
//	GET    /api/system/projects/access-candidates     → 200 Candidates
//	GET    /api/system/projects/{id}                  → 200 Detail
//	PATCH  /api/system/projects/{id}  {project_name?, description?, status?, settings?, expected_updated_at?} → 200 Detail
//	DELETE /api/system/projects/{id}                  → 204
//	GET    /api/projects/{pid}/access                 → 200 Access
//	PUT    /api/projects/{pid}/access  {user_ids, role_ids} → 200 Access（整体替换）
type Handler struct {
	svc    *Service
	stream events.StreamOptions
}

// NewHandler 建 Handler。stream 只用 KeepAlive、RecheckEvery（0 用默认的 15 秒保活、60 秒复核；测试里调短）。
func NewHandler(svc *Service, stream events.StreamOptions) *Handler {
	return &Handler{svc: svc, stream: stream}
}

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	r.Handle("GET /api/projects", g.Login(), h.listMine)
	r.Handle("GET /api/projects/{pid}/stream", g.InProject(g.Login(), auth.PathProject("pid")), h.streamEvents)

	m := g.AnyMenu(auth.MenuSystemProjects)
	r.Handle("GET /api/system/projects", m, h.list)
	r.Handle("POST /api/system/projects", m, h.create)
	r.Handle("GET /api/system/projects/access-candidates", m, h.candidates)
	r.Handle("GET /api/system/projects/{id}", m, h.get)
	r.Handle("PATCH /api/system/projects/{id}", m, h.update)
	r.Handle("DELETE /api/system/projects/{id}", m, h.delete)
	r.Handle("GET /api/projects/{pid}/access", m, h.getAccess)
	r.Handle("PUT /api/projects/{pid}/access", m, h.setAccess)
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) error {
	list, err := h.svc.ListMine(r.Context(), auth.Current(r.Context()).UserID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, list)
	return nil
}

// streamEvents 是 `GET /api/projects/{pid}/stream`：15 秒保活，60 秒复核会话和授权（失效即断），会话到期时断开。
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) error {
	pid := auth.ProjectID(r.Context())
	p := auth.Current(r.Context())
	token := auth.SessionToken(r)
	opts := h.stream
	opts.Recheck = func(ctx context.Context) error { return h.svc.auth.RecheckProject(ctx, token, pid) }
	opts.Until, opts.Log = p.SessionExpiresAt, httpx.LoggerFrom(r.Context())
	events.Serve(w, r, h.svc.events, pid, opts)
	return nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	result, err := h.svc.List(r.Context(), q.Get("keyword"), q.Get("status"), page)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, result)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	d, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	d, err := h.svc.Create(r.Context(), auth.Current(r.Context()).UserID, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, d)
	return nil
}

type updateRequest struct {
	ProjectName *string         `json:"project_name"`
	Description *string         `json:"description"`
	Status      *string         `json:"status"`
	Settings    json.RawMessage `json:"settings"`
	// ExpectedUpdatedAt 是 RFC 3339 时间（原样传回读到的 updated_at）；null 或不传表示不检查。
	ExpectedUpdatedAt *string `json:"expected_updated_at"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	in := UpdateInput{ProjectName: req.ProjectName, Description: req.Description, Status: req.Status}
	if len(req.Settings) > 0 && string(req.Settings) != "null" {
		in.Settings = req.Settings
	}
	if req.ExpectedUpdatedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *req.ExpectedUpdatedAt)
		if err != nil {
			return httpx.Invalid("expected_updated_at 不是合法的时间（RFC 3339）")
		}
		in.ExpectedUpdatedAt = &t
	}
	d, err := h.svc.Update(r.Context(), auth.Current(r.Context()).UserID, id, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := h.svc.Delete(r.Context(), auth.Current(r.Context()).UserID, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) getAccess(w http.ResponseWriter, r *http.Request) error {
	pid, err := httpx.PathUUID(r, "pid")
	if err != nil {
		return err
	}
	a, err := h.svc.GetAccess(r.Context(), pid)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, a)
	return nil
}

type setAccessRequest struct {
	UserIDs *[]uuid.UUID `json:"user_ids"`
	RoleIDs *[]uuid.UUID `json:"role_ids"`
}

func (h *Handler) setAccess(w http.ResponseWriter, r *http.Request) error {
	pid, err := httpx.PathUUID(r, "pid")
	if err != nil {
		return err
	}
	var req setAccessRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.UserIDs == nil || req.RoleIDs == nil {
		return httpx.Invalid("缺少 user_ids 或 role_ids（项目授权的全部用户和角色，整体替换）")
	}
	a, err := h.svc.SetAccess(r.Context(), auth.Current(r.Context()).UserID, pid, *req.UserIDs, *req.RoleIDs)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, a)
	return nil
}

func (h *Handler) candidates(w http.ResponseWriter, r *http.Request) error {
	c, err := h.svc.AccessCandidates(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}
