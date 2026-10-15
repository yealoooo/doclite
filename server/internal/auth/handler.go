package auth

import (
	"net/http"

	"doclite/server/internal/platform/httpx"
)

// Handler 是会话的接口：
//
//	POST /api/auth/login   {username, password} → 200 Me，下发会话 Cookie
//	POST /api/auth/logout  → 204，删 Cookie（没登录、会话已失效也是 204）
//	GET  /api/me           → 200 Me（登录即可）
//	GET  /api/me/menus     → 200 [MyMenu]（登录即可）
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *Gate) {
	r.Handle("POST /api/auth/login", g.Public(), h.login)
	// 退出不要求登录：会话已经失效时也要能删掉浏览器里的 Cookie（返回 204）。
	r.Handle("POST /api/auth/logout", g.Public(), h.logout)
	r.Handle("GET /api/me", g.Login(), h.me)
	r.Handle("GET /api/me/menus", g.Login(), h.myMenus)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	ci, err := ParseClientInfo(r)
	if err != nil {
		return err
	}
	var req loginRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	res, err := h.svc.Login(r.Context(), req.Username, req.Password, ci)
	if err != nil {
		return err
	}
	me, err := h.svc.GetMe(r.Context(), res.UserID)
	if err != nil {
		return err
	}
	setSessionCookie(w, res.Token, h.svc.opts.SessionTTL, h.svc.opts.CookieSecure)
	httpx.WriteJSON(w, http.StatusOK, me)
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Logout(r.Context(), SessionToken(r)); err != nil {
		return err
	}
	clearSessionCookie(w, h.svc.opts.CookieSecure)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	me, err := h.svc.GetMe(r.Context(), Current(r.Context()).UserID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, me)
	return nil
}

// myMenu 是 `GET /api/me/menus` 的一项（不含分组本身）。
type myMenu struct {
	MenuCode   string  `json:"menu_code"`
	ParentCode *string `json:"parent_code"`
	MenuName   string  `json:"menu_name"`
	RoutePath  *string `json:"route_path"`
	SortNo     int     `json:"sort_no"`
}

func (h *Handler) myMenus(w http.ResponseWriter, r *http.Request) error {
	menus, err := h.svc.MyMenus(r.Context(), Current(r.Context()).UserID)
	if err != nil {
		return err
	}
	out := make([]myMenu, 0, len(menus))
	for _, m := range menus {
		if m.MenuType != "menu" {
			continue
		}
		out = append(out, myMenu{MenuCode: m.MenuCode, ParentCode: m.ParentCode, MenuName: m.MenuName, RoutePath: m.RoutePath, SortNo: m.SortNo})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}
