package review

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// Handler 是审核的接口（requirements 5.8、5.6、第 8 节）。fid 是 file_id，pid 是项目 id，review-assignments/{id} 是
// file_reviews 的 id。
//
// review_assign：
//
//	POST /api/projects/{pid}/review-assignments            {reviewer_id, folder_id | file_ids} → 200 AssignResult
//	POST /api/review-assignments/{id}/reassign             {reviewer_id} → 200 ReassignOneResult
//	POST /api/projects/{pid}/review-assignments:reassign   {to_reviewer_id, file_ids | from_reviewer_id + folder_id}
//	                                                       → 200 ReassignResult
//	GET  /api/projects/{pid}/reviewers                     → 200 ProjectReviewers
//	GET  /api/projects/{pid}/review-summary?folder_id=     → 200 Summary
//	GET  /api/projects/{pid}/review-assignments?folder_id=&reviewer_id=&limit=&cursor= → 200 OpenAssignments
//
// review：
//
//	GET  /api/me/reviews?project_id=&folder_id=&state=&low_dpi=&limit=&cursor= → 200 MyReviews
//	POST /api/files/{fid}/review                           {result, reject_reasons?, comment?, snapshot_id?} → 200 ReviewedFile
//	POST /api/projects/{pid}/reviews:approve               {file_ids} → 200 ApproveManyResult
//
// review 或 review_assign：
//
//	POST /api/files/{fid}/reopen                           （没有请求体）→ 200 ReopenResult
//	GET  /api/files/{fid}/reviews                          → 200 FileReviews
//
// scan 或 files：
//
//	POST /api/files/{fid}/rescan                           multipart（同上传）→ 201 RescanResponse
//	GET  /api/projects/{pid}/rescans?folder_id=&limit=&cursor= → 200 RescanList
//
// 能进项目即可：
//
//	GET  /api/projects/{pid}/review-settings               → 200 Settings
type Handler struct {
	svc *Service
}

// NewHandler 建 Handler。
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes 注册路由。
func (h *Handler) Routes(r *httpx.Router, g *auth.Gate) {
	assign := g.AnyMenu(auth.MenuReviewAssign)
	review := g.AnyMenu(auth.MenuReview)
	either := g.AnyMenu(auth.MenuReview, auth.MenuReviewAssign)
	scanOrFiles := g.AnyMenu(auth.MenuScan, auth.MenuFiles)
	pid := auth.PathProject("pid")

	r.Handle("POST /api/projects/{pid}/review-assignments", g.InProject(assign, pid), h.assign)
	r.Handle("POST /api/review-assignments/{id}/reassign", g.InProject(assign, h.assignmentProject), h.reassignOne)
	r.Handle("POST /api/projects/{pid}/review-assignments:reassign", g.InProject(assign, pid), h.reassignMany)
	r.Handle("GET /api/projects/{pid}/reviewers", g.InProject(assign, pid), h.reviewers)
	r.Handle("GET /api/projects/{pid}/review-summary", g.InProject(assign, pid), h.summary)
	r.Handle("GET /api/projects/{pid}/review-assignments", g.InProject(assign, pid), h.openAssignments)

	r.Handle("GET /api/me/reviews", review, h.myReviews)
	r.Handle("POST /api/files/{fid}/review", g.InProject(review, h.fileProject), h.review)
	r.Handle("POST /api/projects/{pid}/reviews:approve", g.InProject(review, pid), h.approveMany)

	r.Handle("POST /api/files/{fid}/reopen", g.InProject(either, h.fileProject), h.reopen)
	r.Handle("GET /api/files/{fid}/reviews", g.InProject(either, h.fileProject), h.fileReviews)

	// 重扫：最外层是 UploadBody（出错时先读完请求体、空闲超时），鉴权和项目授权的错误也会先读完再返回。
	r.Handle("POST /api/files/{fid}/rescan", httpx.Chain(h.svc.files.UploadBody(), g.InProject(scanOrFiles, h.fileProject)), h.rescan)
	r.Handle("GET /api/projects/{pid}/rescans", g.InProject(scanOrFiles, pid), h.rescans)

	r.Handle("GET /api/projects/{pid}/review-settings", g.InProject(g.Login(), pid), h.settings)
}

func (h *Handler) fileProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.files.ProjectOfFile(r.Context(), id)
}

func (h *Handler) assignmentProject(r *http.Request) (uuid.UUID, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return uuid.Nil, err
	}
	return h.svc.ProjectOfAssignment(r.Context(), id)
}

func (h *Handler) assign(w http.ResponseWriter, r *http.Request) error {
	var in AssignInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.Assign(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) reassignOne(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		ReviewerID *uuid.UUID `json:"reviewer_id"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.ReassignOne(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), id, in.ReviewerID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) reassignMany(w http.ResponseWriter, r *http.Request) error {
	var in ReassignManyInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.ReassignMany(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) reviewers(w http.ResponseWriter, r *http.Request) error {
	res, err := h.svc.Reviewers(r.Context(), auth.ProjectID(r.Context()))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) error {
	folderID, err := ParseOptionalUUID("folder_id", query(r, "folder_id"))
	if err != nil {
		return err
	}
	res, err := h.svc.Summary(r.Context(), auth.ProjectID(r.Context()), folderID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

// listParams 解析列表共同的 folder_id、limit、cursor（项目检查之后才解析：进不了项目的人带错参数也是 403）。
func listParams(r *http.Request) (ListParams, error) {
	var p ListParams
	var err error
	if p.FolderID, err = ParseOptionalUUID("folder_id", query(r, "folder_id")); err != nil {
		return p, err
	}
	if p.Limit, err = ParseLimit(query(r, "limit")); err != nil {
		return p, err
	}
	p.After, err = ParseCursor(query(r, "cursor"))
	return p, err
}

// query 是查询参数（去掉首尾空白；空串当作没传）。
func query(r *http.Request, name string) string { return strings.TrimSpace(r.URL.Query().Get(name)) }

func (h *Handler) openAssignments(w http.ResponseWriter, r *http.Request) error {
	p, err := listParams(r)
	if err != nil {
		return err
	}
	reviewer, err := ParseOptionalUUID("reviewer_id", query(r, "reviewer_id"))
	if err != nil {
		return err
	}
	res, err := h.svc.OpenAssignments(r.Context(), auth.ProjectID(r.Context()), p, reviewer)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) myReviews(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	raw := query(r, "project_id")
	if raw == "" {
		return httpx.Invalid("缺少 project_id（我的审核跟随当前项目）")
	}
	pid, err := uuid.Parse(raw)
	if err != nil {
		return httpx.Invalid("参数 project_id 不是合法的 UUID")
	}
	user := auth.Current(ctx).UserID
	if err := h.svc.auth.CheckProjectAccess(ctx, user, pid); err != nil {
		return err
	}
	q := MyReviewsQuery{ProjectID: pid, State: "all"}
	if q.ListParams, err = listParams(r); err != nil {
		return err
	}
	if v := query(r, "state"); v != "" {
		if !slices.Contains(MyReviewStates, v) {
			return httpx.Invalid("state 只能是 all、open、approved、rejected")
		}
		q.State = v
	}
	switch query(r, "low_dpi") {
	case "", "false", "0":
	case "true", "1":
		q.LowDPI = true
	default:
		return httpx.Invalid("low_dpi 只能是 true 或 false")
	}
	res, err := h.svc.MyReviews(ctx, user, q)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) review(w http.ResponseWriter, r *http.Request) error {
	fid, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	var in ReviewInput
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.Review(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), fid, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) approveMany(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		FileIDs []uuid.UUID `json:"file_ids"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.ApproveMany(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), in.FileIDs)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) reopen(w http.ResponseWriter, r *http.Request) error {
	fid, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	ctx := r.Context()
	res, err := h.svc.Reopen(ctx, auth.Current(ctx), auth.ProjectID(ctx), fid)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) fileReviews(w http.ResponseWriter, r *http.Request) error {
	fid, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	res, err := h.svc.FileReviews(r.Context(), auth.ProjectID(r.Context()), fid)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) rescan(w http.ResponseWriter, r *http.Request) error {
	fid, err := httpx.PathUUID(r, "fid")
	if err != nil {
		return err
	}
	u, err := h.svc.files.Receive(r)
	if err != nil {
		return err
	}
	defer u.Close()
	// 收完请求体之后，"传 S3 → 写库 → 发事件"不受客户端断开影响（重扫的进程内锁也跟着走到提交之后）。
	ctx := context.WithoutCancel(r.Context())
	res, err := h.svc.Rescan(ctx, auth.Current(ctx).UserID, auth.ProjectID(ctx), fid, u)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
	return nil
}

func (h *Handler) rescans(w http.ResponseWriter, r *http.Request) error {
	p, err := listParams(r)
	if err != nil {
		return err
	}
	res, err := h.svc.Rescans(r.Context(), auth.ProjectID(r.Context()), p)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) error {
	res, err := h.svc.Settings(r.Context(), auth.ProjectID(r.Context()))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}
