package project

import (
	"net/http"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是项目的接口：
//
//	GET /api/projects  → 200 [MyProject]（登录即可）
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	r.Handle("GET /api/projects", g.Login(), h.listMine)
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) error {
	list, err := h.svc.ListMine(r.Context(), auth.Current(r.Context()).UserID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, list)
	return nil
}
