package storage

import (
	"net/http"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// MigrateHandler 是存储迁移的接口（菜单码 system.storage，和存储配置一样）：
//
//	POST /api/system/storage-profiles/{id}/migrate         {target_profile_id} → 201 Migration（源 = 这个存储）
//	GET  /api/system/storage-profiles/{id}/migrate         → 200 Migration（这个存储最近一次迁移，带 remaining、failures）；
//	                                                         没有迁移记录 404
//	POST /api/system/storage-profiles/{id}/migrate/cancel  （没有请求体）→ 200 Migration
type MigrateHandler struct {
	m *Migrator
}

// NewMigrateHandler 建 MigrateHandler。
func NewMigrateHandler(m *Migrator) *MigrateHandler { return &MigrateHandler{m: m} }

// Routes 注册路由。
func (h *MigrateHandler) Routes(r *httpx.Router, g *auth.Gate) {
	m := g.AnyMenu(auth.MenuSystemStorage)
	r.Handle("POST /api/system/storage-profiles/{id}/migrate", m, h.start)
	r.Handle("GET /api/system/storage-profiles/{id}/migrate", m, h.get)
	r.Handle("POST /api/system/storage-profiles/{id}/migrate/cancel", m, h.cancel)
}

type migrateRequest struct {
	TargetProfileID *string `json:"target_profile_id"`
}

func (h *MigrateHandler) start(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req migrateRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	target := uuid.Nil
	if req.TargetProfileID != nil {
		if target, err = uuid.Parse(*req.TargetProfileID); err != nil {
			return httpx.Invalid("target_profile_id 不是合法的 id")
		}
	}
	mg, err := h.m.Start(r.Context(), auth.Current(r.Context()).UserID, id, target)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, mg)
	return nil
}

func (h *MigrateHandler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	mg, err := h.m.Latest(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, mg)
	return nil
}

func (h *MigrateHandler) cancel(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	mg, err := h.m.Cancel(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, mg)
	return nil
}
