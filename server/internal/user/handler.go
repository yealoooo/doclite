package user

import (
	"net/http"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是用户管理的接口（菜单码 system.users）：
//
//	GET  /api/system/users?keyword=&page=&page_size=   → 200 Page<User>（按登录名排序）
//	POST /api/system/users  {username, display_name, password, role_ids?} → 201 User
//	GET  /api/system/users/{id}                        → 200 User
//	PATCH /api/system/users/{id}  {username?, display_name?} → 200 User
//	POST /api/system/users/{id}/disable                → 200 User
//	POST /api/system/users/{id}/enable                 → 200 User
//	POST /api/system/users/{id}/reset-password {password} → 204
//	PUT  /api/system/users/{id}/roles  {role_ids}      → 200 User
//
// 没有删除接口：不用的账号停用。
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	users := g.AnyMenu(auth.MenuSystemUsers)
	r.Handle("GET /api/system/users", users, h.list)
	r.Handle("POST /api/system/users", users, h.create)
	r.Handle("GET /api/system/users/{id}", users, h.get)
	r.Handle("PATCH /api/system/users/{id}", users, h.update)
	r.Handle("POST /api/system/users/{id}/disable", users, h.disable)
	r.Handle("POST /api/system/users/{id}/enable", users, h.enable)
	r.Handle("POST /api/system/users/{id}/reset-password", users, h.resetPassword)
	r.Handle("PUT /api/system/users/{id}/roles", users, h.setRoles)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r)
	if err != nil {
		return err
	}
	result, err := h.svc.List(r.Context(), r.URL.Query().Get("keyword"), page)
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
	u, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, u)
	return nil
}

type createRequest struct {
	Username    string      `json:"username"`
	DisplayName string      `json:"display_name"`
	Password    string      `json:"password"`
	RoleIDs     []uuid.UUID `json:"role_ids"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var req createRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	u, err := h.svc.Create(r.Context(), auth.Current(r.Context()).UserID, CreateInput{
		Username: req.Username, DisplayName: req.DisplayName, Password: req.Password, RoleIDs: req.RoleIDs,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, u)
	return nil
}

type updateRequest struct {
	Username    *string `json:"username"`
	DisplayName *string `json:"display_name"`
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
	u, err := h.svc.Update(r.Context(), auth.Current(r.Context()).UserID, id, UpdateInput{
		Username: req.Username, DisplayName: req.DisplayName,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, u)
	return nil
}

func (h *Handler) disable(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	u, err := h.svc.Disable(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, u)
	return nil
}

func (h *Handler) enable(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	u, err := h.svc.Enable(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, u)
	return nil
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req resetPasswordRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if err := h.svc.ResetPassword(r.Context(), auth.Current(r.Context()).UserID, id, req.Password); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type setRolesRequest struct {
	RoleIDs *[]uuid.UUID `json:"role_ids"`
}

func (h *Handler) setRoles(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req setRolesRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.RoleIDs == nil {
		return httpx.Invalid("缺少 role_ids（用户的全部角色，整体替换）")
	}
	u, err := h.svc.SetRoles(r.Context(), auth.Current(r.Context()).UserID, id, *req.RoleIDs)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, u)
	return nil
}
