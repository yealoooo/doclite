package file

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是文件的接口（id 是文件夹 id，fid 是 file_id）：
//
// scan 或 files：
//
//	POST /api/folders/{id}/files        multipart（file_name、content_sha256?、cover?、file）
//	                                    → 201 IngestResponse；200 之前已上传过（断网重试）
//	POST /api/folders/{id}/files:check  {file_name, sha256, size} → 200 CheckResponse（预检，给壳用）
//
// 登录即可（只给能进的项目里的文件）：
//
//	POST /api/files/batch               {file_ids} → 200 BatchResponse（≤ 500 个；空列表返回空结果）
//	GET  /api/files/{fid}/snapshots     → 200 SnapshotsResponse（进不了项目也是 404）
//
// files：
//
//	GET  /api/files/{fid}/logs          → 200 LogsResponse（新的在前；文件已删除 404）
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	scanOrFiles := g.AnyMenu(auth.MenuScan, auth.MenuFiles)
	// 上传：最外层是 UploadBody（出错时先读完请求体、空闲超时），鉴权和项目授权的错误也会先读完再返回。
	r.Handle("POST /api/folders/{id}/files", httpx.Chain(h.svc.UploadBody(), g.InProject(scanOrFiles, h.folderProject)), h.upload)
	r.Handle("POST /api/folders/{id}/files:check", g.InProject(scanOrFiles, h.folderProject), h.check)
	r.Handle("POST /api/files/batch", g.Login(), h.batch)
	r.Handle("GET /api/files/{fid}/snapshots", g.Login(), h.snapshots)
	r.Handle("GET /api/files/{fid}/logs", g.InProject(g.AnyMenu(auth.MenuFiles), h.fileProject), h.logs)
}

func (h *Handler) folderProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfFolder(r.Context(), id)
}

func (h *Handler) fileProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfFile(r.Context(), id)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) error {
	folderID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	u, err := h.svc.Receive(r)
	if err != nil {
		return err
	}
	defer u.Close()
	// 收完请求体之后，"传 S3 → 写库 → 发事件"不受客户端断开影响。
	ctx := context.WithoutCancel(r.Context())
	res, created, err := h.svc.Ingest(ctx, auth.ProjectID(ctx), folderID, auth.Current(ctx).UserID, u)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, res)
	return nil
}

func (h *Handler) check(w http.ResponseWriter, r *http.Request) error {
	folderID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req CheckRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	res, err := h.svc.Check(r.Context(), auth.ProjectID(r.Context()), folderID, req)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

type batchRequest struct {
	FileIDs []uuid.UUID `json:"file_ids"`
}

func (h *Handler) batch(w http.ResponseWriter, r *http.Request) error {
	var req batchRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		return err
	}
	res, err := h.svc.Batch(r.Context(), auth.Current(r.Context()).UserID, req.FileIDs)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) snapshots(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	res, err := h.svc.Snapshots(r.Context(), auth.Current(r.Context()).UserID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) logs(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	res, err := h.svc.Logs(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}
