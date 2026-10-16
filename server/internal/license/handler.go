package license

import (
	"net/http"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是许可证的接口（菜单码 system.license）：
//
//	GET /api/system/license            → 200 View
//	PUT /api/system/license {license}  → 200 View（license 是 .lic 文件的全文）
//
// 导入的错误：400 `invalid_input`（没给内容）、`invalid_license`（不是许可证文件、签名验不过；details.reason 是
// format 或 signature）、`license_mismatch`（install_id 不符；details 带两边的 install_id）、`license_file_expired`
// （已经过期；details.expires_at）；开发构建 409 `invalid_state`。
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	m := g.AnyMenu(auth.MenuSystemLicense)
	r.Handle("GET /api/system/license", m, h.get)
	r.Handle("PUT /api/system/license", m, h.put)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.Get(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

type importRequest struct {
	License string `json:"license"`
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) error {
	var req importRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	v, err := h.svc.Import(r.Context(), auth.Current(r.Context()), req.License)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}
