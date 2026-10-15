package apitest

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// 审核测试的支撑：一个能上传的项目、两个审核员、一个负责人、一个扫描员（都授权了这个项目）和一个文件夹。

type reviewEnv struct {
	*Env
	t       *testing.T
	admin   *Client
	p       projectDetail
	mgr     *Client
	rv1     *Client
	rv2     *Client
	scan    *Client
	mgrID   uuid.UUID
	rv1ID   uuid.UUID
	rv2ID   uuid.UUID
	scanID  uuid.UUID
	box     folderItem
	counter int
}

func newReviewEnv(t *testing.T, code string) *reviewEnv {
	t.Helper()
	env, _ := newUploadEnv(t)
	e := &reviewEnv{Env: env, t: t, admin: env.Admin(), p: env.CreateProject(code)}
	e.mgrID = env.CreateUser("mgr", "manager")
	e.rv1ID = env.CreateUser("rv1", "reviewer")
	e.rv2ID = env.CreateUser("rv2", "reviewer")
	e.scanID = env.CreateUser("scan1", "scanner")
	env.GrantProject(e.p.ID, e.mgrID, e.rv1ID, e.rv2ID, e.scanID)
	e.mgr = env.Login("mgr", DefaultPassword)
	e.rv1 = env.Login("rv1", DefaultPassword)
	e.rv2 = env.Login("rv2", DefaultPassword)
	e.scan = env.Login("scan1", DefaultPassword)
	e.box = mkdir(e.admin, e.p.RootFolderID, "盒 1")
	return e
}

// upload 由扫描员往文件夹里上传一个内容唯一的 300 DPI 的 G4 TIFF（小，不算"DPI 不足"）。
func (e *reviewEnv) upload(folderID uuid.UUID, name string) ingestResponse {
	e.t.Helper()
	e.counter++
	data := unique(sampleBytes(e.t, "tiff-g4-1bit.tif"), e.p.ProjectCode+"-"+name+"-"+strconv.Itoa(e.counter))
	return Decode[ingestResponse](e.scan.Upload(folderID, name, data, nil).Expect(http.StatusCreated))
}

type assignResult struct {
	Assigned int         `json:"assigned"`
	Skipped  int         `json:"skipped"`
	FileIDs  []uuid.UUID `json:"file_ids"`
}

func (e *reviewEnv) url(path string) string { return "/api/projects/" + e.p.ID.String() + path }

// assign 由负责人按文件分配给 reviewer，要求 200。
func (e *reviewEnv) assign(reviewer uuid.UUID, fileIDs ...uuid.UUID) assignResult {
	e.t.Helper()
	return Decode[assignResult](e.mgr.Post(e.url("/review-assignments"),
		map[string]any{"reviewer_id": reviewer, "file_ids": fileIDs}).Expect(http.StatusOK))
}

// reviewed 是审核的响应。
type reviewedFile struct {
	FileID       uuid.UUID `json:"file_id"`
	FolderID     uuid.UUID `json:"folder_id"`
	AssignmentID uuid.UUID `json:"assignment_id"`
	Result       string    `json:"result"`
	ReviewStatus string    `json:"review_status"`
	SnapshotID   uuid.UUID `json:"snapshot_id"`
	VersionNo    int       `json:"version_no"`
	ReviewRound  int       `json:"review_round"`
	ReviewedAt   string    `json:"reviewed_at"`
}

// review 发审核请求（不检查结果）。
func review(c *Client, fileID uuid.UUID, body map[string]any) *Response {
	c.t.Helper()
	return c.Post("/api/files/"+fileID.String()+"/review", body)
}

func approve(c *Client, fileID uuid.UUID) *Response {
	c.t.Helper()
	return review(c, fileID, map[string]any{"result": "approved"})
}

func reject(c *Client, fileID uuid.UUID, reasons ...string) *Response {
	c.t.Helper()
	if reasons == nil {
		reasons = []string{"blur"}
	}
	return review(c, fileID, map[string]any{"result": "rejected", "reject_reasons": reasons})
}

// status 是文件现在的审核状态和轮次。
func (e *reviewEnv) status(fileID uuid.UUID) (string, int) {
	e.t.Helper()
	var s string
	var r int
	if err := e.Pool.QueryRow(e.t.Context(), `SELECT review_status, review_round FROM folder_files WHERE file_id = $1`, fileID).
		Scan(&s, &r); err != nil {
		e.t.Fatal(err)
	}
	return s, r
}

// openReviewer 是文件 open 的那一行的审核人；没有为 uuid.Nil。
func (e *reviewEnv) openReviewer(fileID uuid.UUID) uuid.UUID {
	e.t.Helper()
	return QueryValue[uuid.UUID](e.Env, `SELECT COALESCE((SELECT reviewer_id FROM file_reviews WHERE file_id = $1 AND status = 'open'),
  '00000000-0000-0000-0000-000000000000'::uuid)`, fileID)
}

// lastLog 是文件最近一条 action 日志的 detail。
func (e *reviewEnv) lastLog(fileID uuid.UUID, action string) map[string]any {
	e.t.Helper()
	return QueryValue[map[string]any](e.Env, `SELECT detail FROM file_logs WHERE file_id = $1 AND action = $2 ORDER BY id DESC LIMIT 1`,
		fileID, action)
}

// rescanResponse 是重扫的响应。
type rescanResponse struct {
	ingestResponse
	FolderID     uuid.UUID  `json:"folder_id"`
	ReviewStatus string     `json:"review_status"`
	ReviewRound  int        `json:"review_round"`
	ReviewerID   *uuid.UUID `json:"reviewer_id"`
}

// rescan 上传重扫件（file_name、file）。
func rescan(c *Client, fileID uuid.UUID, name string, data []byte) *Response {
	c.t.Helper()
	body, ct := multipartBody(c.t, textPart("file_name", name), filePart("file", data))
	return c.PostRaw("/api/files/"+fileID.String()+"/rescan", ct, body)
}

// fileIn 建一个处在 state 的文件（审核人 rv1）。
func (e *reviewEnv) fileIn(state, name string) ingestResponse {
	e.t.Helper()
	f := e.upload(e.box.ID, name)
	if state == "unassigned" {
		return f
	}
	e.assign(e.rv1ID, f.FileID)
	switch state {
	case "approved":
		approve(e.rv1, f.FileID).Expect(http.StatusOK)
	case "rejected":
		reject(e.rv1, f.FileID).Expect(http.StatusOK)
	}
	return f
}

type idsDetails struct {
	Details struct {
		IDs []uuid.UUID `json:"ids"`
	} `json:"details"`
}

// detailIDs 是错误体的 details.ids。
func detailIDs(r *Response) []uuid.UUID { return Decode[idsDetails](r).Details.IDs }
