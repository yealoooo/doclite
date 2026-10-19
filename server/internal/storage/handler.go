package storage

import (
	"net/http"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是存储配置的接口（菜单码 system.storage）：
//
//	GET    /api/system/storage-profiles                 → 200 [Profile]（默认的在最前，其余按创建时间）
//	POST   /api/system/storage-profiles                 {profile_name, endpoint, public_endpoint?, bucket, region?,
//	                                                     path_prefix?, access_key, secret_key, force_path_style?,
//	                                                     url_expire_hours?, remark?} → 201 Profile
//	GET    /api/system/storage-profiles/{id}            → 200 Profile
//	PATCH  /api/system/storage-profiles/{id}            {同上都可选, status?} → 200 Profile
//	DELETE /api/system/storage-profiles/{id}            → 204
//	POST   /api/system/storage-profiles/test            {连接参数} → 200 TestReport（连接失败也是 200）
//	POST   /api/system/storage-profiles/{id}/test       {未保存的修改，可以没有请求体} → 200 TestReport
//	POST   /api/system/storage-profiles/{id}/set-default（没有请求体）→ 200 Profile
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	m := g.AnyMenu(auth.MenuSystemStorage)
	r.Handle("GET /api/system/storage-profiles", m, h.list)
	r.Handle("POST /api/system/storage-profiles", m, h.create)
	r.Handle("POST /api/system/storage-profiles/test", m, h.testNew)
	r.Handle("GET /api/system/storage-profiles/{id}", m, h.get)
	r.Handle("PATCH /api/system/storage-profiles/{id}", m, h.update)
	r.Handle("DELETE /api/system/storage-profiles/{id}", m, h.delete)
	r.Handle("POST /api/system/storage-profiles/{id}/test", m, h.testSaved)
	r.Handle("POST /api/system/storage-profiles/{id}/set-default", m, h.setDefault)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	list, err := h.svc.List(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, list)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	p, err := h.svc.Create(r.Context(), auth.Current(r.Context()).UserID, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, p)
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
	p, err := h.svc.Update(r.Context(), auth.Current(r.Context()).UserID, id, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
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

func (h *Handler) setDefault(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p, err := h.svc.SetDefault(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

func (h *Handler) testNew(w http.ResponseWriter, r *http.Request) error {
	var in ConnectionInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	rep, err := h.svc.TestNew(r.Context(), in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
	return nil
}

func (h *Handler) testSaved(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in UpdateInput
	if _, err := httpx.ReadOptionalJSON(r, &in); err != nil {
		return err
	}
	rep, err := h.svc.TestSaved(r.Context(), id, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
	return nil
}
