package audit

import (
	"net/http"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是审计日志的接口（菜单码 system.audit；只读）：
//
//	GET /api/system/audit-logs?from=&to=&operator_id=&project_id=&target_type=&action=&limit=&cursor=
//	                                              → 200 {items, next_cursor}（按 id 倒序，limit 默认 50、最大 200）
//	GET /api/system/audit-logs/filter-options     → 200 {users, projects}（筛选栏的选项，含停用的用户、已删除的项目）
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	m := g.AnyMenu(auth.MenuSystemAudit)
	r.Handle("GET /api/system/audit-logs", m, h.list)
	r.Handle("GET /api/system/audit-logs/filter-options", m, h.options)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	f, err := ParseFilter(r)
	if err != nil {
		return err
	}
	out, err := h.svc.List(r.Context(), f)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) options(w http.ResponseWriter, r *http.Request) error {
	out, err := h.svc.Options(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}
