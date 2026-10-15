package apitest

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type myReviews struct {
	ProjectID       uuid.UUID `json:"project_id"`
	LowDPIThreshold int       `json:"low_dpi_threshold"`
	Counts          myCounts  `json:"counts"`
	Folders         []struct {
		FolderID   uuid.UUID `json:"folder_id"`
		FolderName string    `json:"folder_name"`
		FolderPath string    `json:"folder_path"`
		Counts     myCounts  `json:"counts"`
	} `json:"folders"`
	OtherProjectsOpen int `json:"other_projects_open"`
	OtherProjects     []struct {
		ProjectID uuid.UUID `json:"project_id"`
		Status    string    `json:"status"`
		Open      int       `json:"open"`
	} `json:"other_projects"`
	Items []struct {
		AssignmentID uuid.UUID `json:"assignment_id"`
		FileID       uuid.UUID `json:"file_id"`
		FolderPath   string    `json:"folder_path"`
		ReviewStatus string    `json:"review_status"`
		DPI          *int      `json:"dpi"`
		Status       string    `json:"status"`
		Result       *string   `json:"result"`
		IsCurrent    bool      `json:"is_current"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type myCounts struct {
	All      int `json:"all"`
	Open     int `json:"open"`
	Approved int `json:"approved"`
	Rejected int `json:"rejected"`
	LowDPI   int `json:"low_dpi"`
}

// TestReviewSubmit：审核设置（空列表按默认）、审核的检查顺序和驳回原因、批量通过（404 → 409 → 403）、撤回通过
// （只能撤自己的 / review_assign 能撤任何人的）、我的审核（计数、文件夹、is_current、筛选、翻页、其他项目）。
func TestReviewSubmit(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "RV")
	settings := func() []string {
		return Decode[struct {
			RejectReasons   []string `json:"reject_reasons"`
			RescanReassign  string   `json:"rescan_reassign"`
			CommentMaxChars int      `json:"comment_max_chars"`
		}](e.rv1.Get(e.url("/review-settings")).Expect(200)).RejectReasons
	}
	if got := settings(); !slices.Equal(got, []string{"blur", "skew", "cut", "stain", "missing", "dup", "orient", "other"}) {
		t.Fatalf("默认原因：%v", got)
	}
	e.Exec(`UPDATE projects SET settings = '{"review": {"reject_reasons": []}}' WHERE id = $1`, e.p.ID)
	if got := settings(); len(got) != 8 {
		t.Fatalf("空列表按默认：%v", got)
	}
	e.Exec(`UPDATE projects SET settings = '{"review": {"reject_reasons": ["模糊", "倾斜", "other"]}}' WHERE id = $1`, e.p.ID)
	if got := settings(); !slices.Equal(got, []string{"模糊", "倾斜", "other"}) {
		t.Fatalf("自定义原因：%v", got)
	}

	f1 := e.fileIn("pending", "f1.jpg")
	f2 := e.fileIn("pending", "f2.jpg")
	f3 := e.fileIn("pending", "f3.jpg")
	appr := e.fileIn("approved", "appr.jpg")
	stream := openStream(t, e.rv1, e.p.ID)

	// 检查顺序：参数 400 → 不是待审核 409 → 不是自己的 403 not_assignee（负责人、admin 也一样）→ 快照不是当前版本 409。
	review(e.rv1, f1.FileID, map[string]any{"result": "maybe"}).ExpectError(400, "invalid_input")
	review(e.rv1, f1.FileID, map[string]any{"result": "approved", "reject_reasons": []string{"模糊"}}).ExpectError(400, "invalid_input")
	reject(e.rv1, f1.FileID, "blur").ExpectError(400, "invalid_input")  // 不在项目列表里
	reject(e.rv1, f1.FileID, "Other").ExpectError(400, "invalid_input") // 区分大小写
	reject(e.rv1, f1.FileID).ExpectError(400, "invalid_input")
	review(e.rv1, f1.FileID, map[string]any{"result": "rejected", "reject_reasons": []string{}}).ExpectError(400, "invalid_input")
	reject(e.rv1, f1.FileID, "other").ExpectError(400, "invalid_input") // 选了其他要写说明
	review(e.rv1, f1.FileID, map[string]any{"result": "rejected", "reject_reasons": []string{"模糊", "模糊", "other", "other"},
		"comment": "x"}).ExpectError(400, "invalid_input") // 个数（重复的也算）超过列表长度
	review(e.rv1, f1.FileID, map[string]any{"result": "approved", "comment": strings.Repeat("长", 501)}).ExpectError(400, "invalid_input")
	review(e.rv1, f1.FileID, map[string]any{"result": "approved", "comment": "a\x07b"}).ExpectError(400, "invalid_input")
	approve(e.rv2, appr.FileID).ExpectError(409, "invalid_state")
	approve(e.rv2, f1.FileID).ExpectError(403, "not_assignee")
	approve(e.mgr, f1.FileID).ExpectError(403, "not_assignee")
	approve(e.admin, f1.FileID).ExpectError(403, "not_assignee")
	review(e.rv1, f1.FileID, map[string]any{"result": "approved", "snapshot_id": uuid.New()}).ExpectError(409, "conflict")
	approve(e.rv1, uuid.New()).ExpectError(404, "not_found")

	// 驳回：原因去重，选了其他带说明（去掉首尾空白，换行可以）；那一行 done，写所审快照；日志；事件。
	rj := Decode[reviewedFile](review(e.rv1, f1.FileID, map[string]any{"result": "rejected",
		"reject_reasons": []string{"other", "模糊", "other"}, "comment": "  第二行\n有折痕 ", "snapshot_id": f1.SnapshotID}).Expect(200))
	if rj.Result != "rejected" || rj.ReviewStatus != "rejected" || rj.SnapshotID != f1.SnapshotID || rj.VersionNo != 1 ||
		rj.ReviewRound != 1 || rj.FolderID != e.box.ID || rj.ReviewedAt == "" {
		t.Fatalf("驳回：%+v", rj)
	}
	if got := QueryValue[string](e.Env, `SELECT concat_ws('|', status, result, snapshot_id, array_to_string(reject_reasons, ','), comment)
FROM file_reviews WHERE id = $1`, rj.AssignmentID); got != "done|rejected|"+f1.SnapshotID.String()+"|other,模糊|第二行\n有折痕" {
		t.Fatalf("驳回的那一行：%q", got)
	}
	if lg := e.lastLog(f1.FileID, "reject"); lg["reviewer"] != "rv1" || lg["comment"] != "第二行\n有折痕" || lg["folder_path"] != "RV 项目/盒 1" ||
		len(lg["reasons"].([]any)) != 2 {
		t.Fatalf("reject 日志：%v", lg)
	}
	if ev := stream.next(t); ev["type"] != "files.changed" || !hasIDs(ev["file_ids"], f1.FileID) {
		t.Fatalf("事件：%v", ev)
	}
	// 通过可以带说明。
	ap := Decode[reviewedFile](review(e.rv1, f2.FileID, map[string]any{"result": "approved", "comment": "可以"}).Expect(200))
	if ap.ReviewStatus != "approved" || e.lastLog(f2.FileID, "approve")["comment"] != "可以" {
		t.Fatalf("通过：%+v", ap)
	}
	stream.next(t)

	// 批量通过：404 → 409 → 403，有一类就整体不做，details.ids；成功时一个事务、按位置排、日志带 batch。
	g1 := e.fileIn("pending", "g1.jpg")
	g2 := e.fileIn("pending", "g2.jpg")
	theirs := e.upload(e.box.ID, "theirs.jpg")
	e.assign(e.rv2ID, theirs.FileID)
	ghost := uuid.New()
	approveMany := func(ids ...uuid.UUID) *Response {
		return e.rv1.Post(e.url("/reviews:approve"), map[string]any{"file_ids": ids})
	}
	res := approveMany(g1.FileID, ghost, theirs.FileID, appr.FileID).ExpectError(404, "not_found")
	if ids := detailIDs(res); !slices.Equal(ids, []uuid.UUID{ghost}) {
		t.Fatalf("404 details：%s", res.Body)
	}
	res = approveMany(g1.FileID, theirs.FileID, appr.FileID).ExpectError(409, "invalid_state")
	if ids := detailIDs(res); !slices.Equal(ids, []uuid.UUID{appr.FileID}) {
		t.Fatalf("409 details：%s", res.Body)
	}
	res = approveMany(g1.FileID, theirs.FileID).ExpectError(403, "not_assignee")
	if ids := detailIDs(res); !slices.Equal(ids, []uuid.UUID{theirs.FileID}) {
		t.Fatalf("403 details：%s", res.Body)
	}
	approveMany().ExpectError(400, "invalid_input")
	if st, _ := e.status(g1.FileID); st != "pending" {
		t.Fatal("整体不做却通过了")
	}
	am := Decode[struct {
		Approved int            `json:"approved"`
		Items    []reviewedFile `json:"items"`
	}](approveMany(g2.FileID, g1.FileID, g1.FileID).Expect(200))
	if am.Approved != 2 || am.Items[0].FileID != g1.FileID || am.Items[1].FileID != g2.FileID || am.Items[0].ReviewedAt != am.Items[1].ReviewedAt {
		t.Fatalf("批量通过：%+v", am)
	}
	if e.lastLog(g1.FileID, "approve")["batch"] != true {
		t.Fatal("批量通过的日志要带 batch")
	}
	stream.next(t)

	// 撤回通过：不是已通过 409；只有 review 的只能撤自己通过的（rv2 撤 rv1 的 403）；rv1 撤自己的 → 待审核、重新分给 rv1；
	// 负责人（review_assign）能撤任何人的；轮次不变。
	reopen := func(c *Client, id uuid.UUID) *Response { return c.Post("/api/files/"+id.String()+"/reopen", nil) }
	reopen(e.rv1, f3.FileID).ExpectError(409, "invalid_state")
	reopen(e.rv2, g1.FileID).ExpectError(403, "not_assignee")
	rr := Decode[struct {
		ReviewStatus string     `json:"review_status"`
		ReviewRound  int        `json:"review_round"`
		AssignmentID *uuid.UUID `json:"assignment_id"`
		ReviewerID   *uuid.UUID `json:"reviewer_id"`
	}](reopen(e.rv1, g1.FileID).Expect(200))
	if rr.ReviewStatus != "pending" || rr.ReviewRound != 1 || rr.ReviewerID == nil || *rr.ReviewerID != e.rv1ID || rr.AssignmentID == nil {
		t.Fatalf("撤回通过：%+v", rr)
	}
	if lg := e.lastLog(g1.FileID, "reopen"); lg["by"] != "rv1" || lg["reviewer"] != "rv1" || lg["skipped_reviewer"] != nil {
		t.Fatalf("reopen 日志：%v", lg)
	}
	reopen(e.mgr, g2.FileID).Expect(200)
	// 原审核人失去项目授权：撤回后待分配，日志记 skipped_reviewer。
	e.GrantProject(e.p.ID, e.mgrID, e.rv2ID, e.scanID)
	rr2 := Decode[struct {
		ReviewStatus string     `json:"review_status"`
		ReviewerID   *uuid.UUID `json:"reviewer_id"`
	}](reopen(e.mgr, appr.FileID).Expect(200))
	if rr2.ReviewStatus != "unassigned" || rr2.ReviewerID != nil || e.lastLog(appr.FileID, "reopen")["skipped_reviewer"] != "rv1" {
		t.Fatalf("撤回给不能再审的人：%+v", rr2)
	}
	e.GrantProject(e.p.ID, e.mgrID, e.rv1ID, e.rv2ID, e.scanID)

	// 我的审核：每个文件取最近一次分给我、没取消的那一行。g2 撤回后改派给 rv2 → 我的那一行"通过"是历史（is_current false）。
	e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID, "file_ids": []uuid.UUID{g2.FileID}}).Expect(200)
	sub := mkdir(e.admin, e.box.ID, "子")
	low := Decode[ingestResponse](e.scan.Upload(sub.ID, "low.png", unique(sampleBytes(t, "png-alpha.png"), "rv-low"), nil).Expect(201))
	e.assign(e.rv1ID, low.FileID)
	// 别的项目里的待审（rv1 能进）。
	p2 := e.CreateProject("RV2")
	e.GrantProject(p2.ID, e.rv1ID, e.mgrID)
	x := uploadOK(t, e.admin, p2.RootFolderID, "x.jpg", "rv-x")
	e.admin.Post("/api/projects/"+p2.ID.String()+"/review-assignments", map[string]any{"reviewer_id": e.rv1ID,
		"file_ids": []uuid.UUID{x.FileID}}).Expect(200)

	my := Decode[myReviews](e.rv1.Get("/api/me/reviews?project_id=" + e.p.ID.String()).Expect(200))
	// 我的：f1 驳回、f2 通过、f3 待审、appr 通过（撤回后待分配，is_current）、g1 待审（撤回后重新分给我）、g2 通过（历史）、low 待审。
	if my.Counts != (myCounts{All: 7, Open: 3, Approved: 3, Rejected: 1, LowDPI: 1}) || my.LowDPIThreshold != 200 ||
		len(my.Folders) != 2 || my.Folders[0].FolderID != e.box.ID || my.Folders[0].Counts.All != 6 ||
		my.Folders[1].FolderPath != "RV 项目/盒 1/子" || my.Folders[1].FolderName != "子" ||
		my.OtherProjectsOpen != 1 || len(my.OtherProjects) != 1 || my.OtherProjects[0].ProjectID != p2.ID || my.NextCursor != nil {
		t.Fatalf("我的审核：%+v", my)
	}
	byFile := map[uuid.UUID]int{}
	for i, it := range my.Items {
		byFile[it.FileID] = i
	}
	if it := my.Items[byFile[g2.FileID]]; it.IsCurrent || it.Status != "done" || *it.Result != "approved" || it.ReviewStatus != "pending" {
		t.Fatalf("g2（改派走了）：%+v", it)
	}
	if it := my.Items[byFile[g1.FileID]]; !it.IsCurrent || it.Status != "open" || it.Result != nil {
		t.Fatalf("g1（撤回后重新分给我）：%+v", it)
	}
	if it := my.Items[byFile[appr.FileID]]; !it.IsCurrent || it.ReviewStatus != "unassigned" {
		t.Fatalf("appr：%+v", it)
	}
	// 筛选：state、low_dpi、folder_id（计数跟着范围变，不受 state 影响）；翻页。
	my = Decode[myReviews](e.rv1.Get("/api/me/reviews?project_id=" + e.p.ID.String() + "&state=open&low_dpi=true").Expect(200))
	if len(my.Items) != 1 || my.Items[0].FileID != low.FileID || my.Counts.All != 7 || *my.Items[0].DPI >= 200 {
		t.Fatalf("筛选：%+v", my)
	}
	my = Decode[myReviews](e.rv1.Get("/api/me/reviews?project_id=" + e.p.ID.String() + "&folder_id=" + sub.ID.String()).Expect(200))
	if my.Counts.All != 1 || len(my.Items) != 1 {
		t.Fatalf("子树：%+v", my)
	}
	var paged []uuid.UUID
	cursor := ""
	for {
		page := Decode[myReviews](e.rv1.Get("/api/me/reviews?project_id=" + e.p.ID.String() + "&limit=3&cursor=" + cursor).Expect(200))
		for _, it := range page.Items {
			paged = append(paged, it.FileID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(paged) != 7 || paged[6] != low.FileID {
		t.Fatalf("翻页：%v", paged)
	}
	// 参数：project_id 必填 400；进不了的项目 403；其余参数 400；state 不对 400。
	e.rv1.Get("/api/me/reviews").ExpectError(400, "invalid_input")
	e.rv1.Get("/api/me/reviews?project_id="+e.CreateProject("RV3").ID.String()+"&state=bad").ExpectError(403, "forbidden")
	e.rv1.Get("/api/me/reviews?project_id="+e.p.ID.String()+"&state=bad").ExpectError(400, "invalid_input")
	e.rv1.Get("/api/me/reviews?project_id="+e.p.ID.String()+"&folder_id="+p2.RootFolderID.String()).ExpectError(404, "not_found")
}
