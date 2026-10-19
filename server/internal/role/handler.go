package role

import (
	"net/http"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是角色与权限的接口（菜单码 system.roles；角色列表 system.roles 或 system.users 都能调，用户页选角色要用）：
//
//	GET    /api/system/roles             → 200 [Role]
//	POST   /api/system/roles             {role_code, role_name, description?, menu_codes?} → 201 Role
//	GET    /api/system/roles/{id}        → 200 Role
//	PATCH  /api/system/roles/{id}        {role_name?, description?}（description 传空字符串表示清空）→ 200 Role
//	DELETE /api/system/roles/{id}        → 204
//	PUT    /api/system/roles/{id}/menus  {menu_codes} → 200 Role
//	GET    /api/system/menus             → 200 [Menu]（全部菜单，含分组，按 sort_no）
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	manage := g.AnyMenu(auth.MenuSystemRoles)
	r.Handle("GET /api/system/roles", g.AnyMenu(auth.MenuSystemRoles, auth.MenuSystemUsers), h.list)
	r.Handle("POST /api/system/roles", manage, h.create)
	r.Handle("GET /api/system/roles/{id}", manage, h.get)
	r.Handle("PATCH /api/system/roles/{id}", manage, h.update)
	r.Handle("DELETE /api/system/roles/{id}", manage, h.delete)
	r.Handle("PUT /api/system/roles/{id}/menus", manage, h.setMenus)
	r.Handle("GET /api/system/menus", manage, h.menus)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	roles, err := h.svc.List(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, roles)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	role, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, role)
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	role, err := h.svc.Create(r.Context(), auth.Current(r.Context()).UserID, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, role)
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
	role, err := h.svc.Update(r.Context(), auth.Current(r.Context()).UserID, id, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, role)
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

type setMenusRequest struct {
	MenuCodes *[]string `json:"menu_codes"`
}

func (h *Handler) setMenus(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req setMenusRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.MenuCodes == nil {
		return httpx.Invalid("缺少 menu_codes（角色的全部菜单，整体替换）")
	}
	role, err := h.svc.SetMenus(r.Context(), auth.Current(r.Context()).UserID, id, *req.MenuCodes)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, role)
	return nil
}

func (h *Handler) menus(w http.ResponseWriter, r *http.Request) error {
	menus, err := h.svc.AllMenus(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, menus)
	return nil
}
