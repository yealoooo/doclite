package apitest

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

type statusCounts struct {
	Unassigned int `json:"unassigned"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	Total      int `json:"total"`
}

type reviewSummary struct {
	FolderID     uuid.UUID    `json:"folder_id"`
	FolderName   string       `json:"folder_name"`
	ParentID     *uuid.UUID   `json:"parent_id"`
	Counts       statusCounts `json:"counts"`
	DirectCounts statusCounts `json:"direct_counts"`
	Children     []struct {
		FolderID   uuid.UUID    `json:"folder_id"`
		FolderName string       `json:"folder_name"`
		Counts     statusCounts `json:"counts"`
	} `json:"children"`
}

type openAssignments struct {
	Total     int `json:"total"`
	Reviewers []struct {
		ReviewerID uuid.UUID `json:"reviewer_id"`
		Open       int       `json:"open"`
		CanReview  bool      `json:"can_review"`
		Problems   []string  `json:"problems"`
	} `json:"reviewers"`
	Items []struct {
		AssignmentID      uuid.UUID `json:"assignment_id"`
		FileID            uuid.UUID `json:"file_id"`
		FolderPath        string    `json:"folder_path"`
		ReviewerID        uuid.UUID `json:"reviewer_id"`
		ReviewerCanReview bool      `json:"reviewer_can_review"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// TestReviewAssign：分配（按文件夹、按文件、跳过、404、审核人不合格）、单个和批量改派、读接口（可以分配的人、
// 状态计数、open 分配和翻页、一个文件的全部分配）、日志和事件。
func TestReviewAssign(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "AS")
	sub := mkdir(e.admin, e.box.ID, "子")
	box2 := mkdir(e.admin, e.p.RootFolderID, "盒 2")
	a := e.upload(e.box.ID, "a.jpg")
	b := e.upload(e.box.ID, "b.jpg")
	c := e.upload(sub.ID, "c.jpg")
	d := e.upload(box2.ID, "d.jpg")
	other := e.CreateProject("AS2")
	o := uploadOK(t, e.admin, other.RootFolderID, "o.jpg", "as-o")
	stream := openStream(t, e.mgr, e.p.ID)

	// 审核人不合格 400：不存在、停用、没有 review 菜单、进不了项目。参数不对 400；文件夹不在这个项目里 404。
	disabled := e.CreateUser("rv_off", "reviewer")
	e.GrantProject(e.p.ID, e.mgrID, e.rv1ID, e.rv2ID, e.scanID, disabled)
	e.admin.Post("/api/system/users/"+disabled.String()+"/disable", nil).Expect(200)
	outsider := e.CreateUser("rv_out", "reviewer")
	for _, who := range []uuid.UUID{uuid.New(), disabled, e.scanID, outsider} {
		e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": who, "folder_id": e.box.ID}).ExpectError(400, "invalid_input")
	}
	e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID}).ExpectError(400, "invalid_input")
	e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "folder_id": e.box.ID,
		"file_ids": []uuid.UUID{a.FileID}}).ExpectError(400, "invalid_input")
	e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "file_ids": []uuid.UUID{}}).
		ExpectError(400, "invalid_input")
	e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "folder_id": other.RootFolderID}).
		ExpectError(404, "not_found")

	// 按文件有不在的（不存在、别的项目）整体 404，details.ids；什么都没改。
	ghost := uuid.New()
	res := e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID,
		"file_ids": []uuid.UUID{a.FileID, ghost, o.FileID}}).ExpectError(404, "not_found")
	if ids := detailIDs(res); len(ids) != 2 || ids[0] != ghost || ids[1] != o.FileID {
		t.Fatalf("details.ids：%s", res.Body)
	}
	if st, _ := e.status(a.FileID); st != "unassigned" {
		t.Fatal("404 却分配了")
	}

	// 按文件夹：子树全部待分配（a、b、子/c），盒 2 不动；再分一次全部跳过。管理员（admin）也能分给自己。
	r := Decode[assignResult](e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "folder_id": e.box.ID}).Expect(200))
	if r.Assigned != 3 || r.Skipped != 0 || !slices.Equal(r.FileIDs, []uuid.UUID{a.FileID, b.FileID, c.FileID}) {
		t.Fatalf("按文件夹分配：%+v", r)
	}
	if ev := stream.next(t); ev["type"] != "files.changed" || !hasIDs(ev["file_ids"], a.FileID, b.FileID, c.FileID) ||
		!hasIDs(ev["folder_ids"], e.box.ID, sub.ID) {
		t.Fatalf("事件：%v", ev)
	}
	r = Decode[assignResult](e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "folder_id": e.box.ID}).Expect(200))
	if r.Assigned != 0 || r.Skipped != 3 || len(r.FileIDs) != 0 {
		t.Fatalf("再分一次：%+v", r)
	}
	d1 := e.lastLog(a.FileID, "assign_review")
	if d1["reviewer"] != "rv1" || d1["reviewer_id"] != e.rv1ID.String() || d1["file_name"] != "a.jpg" ||
		d1["folder_path"] != "AS 项目/盒 1" || d1["version_no"] != float64(1) || d1["review_round"] != float64(1) {
		t.Fatalf("assign_review 日志：%v", d1)
	}
	// 按文件：重复的算一个，不是待分配的跳过。
	r = e.assign(e.rv1ID, d.FileID, a.FileID, d.FileID)
	if r.Assigned != 1 || r.Skipped != 1 || r.FileIDs[0] != d.FileID {
		t.Fatalf("按文件分配：%+v", r)
	}
	stream.next(t)

	// 读接口：可以分配的人（含负责人自己、admin；不含停用的、没授权的、扫描员），状态计数。
	rv := Decode[struct {
		Reviewers []struct {
			ID   uuid.UUID `json:"id"`
			Open int       `json:"open"`
		} `json:"reviewers"`
	}](e.mgr.Get(e.url("/reviewers")).Expect(200))
	got := map[uuid.UUID]int{}
	for _, x := range rv.Reviewers {
		got[x.ID] = x.Open
	}
	adminID := e.UserID("admin")
	if len(got) != 4 || got[e.rv1ID] != 4 || got[e.rv2ID] != 0 || got[e.mgrID] != 0 || got[adminID] != 0 {
		t.Fatalf("可以分配的人：%+v", rv.Reviewers)
	}
	sm := Decode[reviewSummary](e.mgr.Get(e.url("/review-summary")).Expect(200))
	if sm.FolderID != e.p.RootFolderID || sm.ParentID != nil || sm.Counts.Pending != 4 || sm.Counts.Total != 4 ||
		sm.DirectCounts.Total != 0 || len(sm.Children) != 2 || sm.Children[0].FolderID != e.box.ID || sm.Children[0].Counts.Pending != 3 ||
		sm.Children[1].Counts.Pending != 1 {
		t.Fatalf("状态计数（根）：%+v", sm)
	}
	sm = Decode[reviewSummary](e.mgr.Get(e.url("/review-summary?folder_id=" + e.box.ID.String())).Expect(200))
	if sm.Counts.Total != 3 || sm.DirectCounts.Pending != 2 || len(sm.Children) != 1 || sm.Children[0].Counts.Pending != 1 {
		t.Fatalf("状态计数（盒 1）：%+v", sm)
	}
	e.mgr.Get(e.url("/review-summary?folder_id="+other.RootFolderID.String())).ExpectError(404, "not_found")
	e.mgr.Get(e.url("/review-summary?folder_id=x")).ExpectError(400, "invalid_input")

	// 单个改派：分配记录来自 open 分配列表；改给同一个人 changed:false；旧行再改派 409；审核人不合格 400；不存在 404。
	oa := Decode[openAssignments](e.mgr.Get(e.url("/review-assignments?folder_id=" + e.box.ID.String())).Expect(200))
	if oa.Total != 3 || len(oa.Items) != 3 || oa.Items[0].FileID != a.FileID || oa.Items[0].FolderPath != "AS 项目/盒 1" ||
		len(oa.Reviewers) != 1 || oa.Reviewers[0].Open != 3 || !oa.Reviewers[0].CanReview {
		t.Fatalf("open 分配：%+v", oa)
	}
	old := oa.Items[0].AssignmentID
	ro := Decode[struct {
		AssignmentID   uuid.UUID `json:"assignment_id"`
		ReviewerID     uuid.UUID `json:"reviewer_id"`
		FromReviewerID uuid.UUID `json:"from_reviewer_id"`
		Changed        bool      `json:"changed"`
	}](e.mgr.Post("/api/review-assignments/"+old.String()+"/reassign", map[string]any{"reviewer_id": e.rv2ID}).Expect(200))
	if !ro.Changed || ro.AssignmentID == old || ro.ReviewerID != e.rv2ID || ro.FromReviewerID != e.rv1ID || e.openReviewer(a.FileID) != e.rv2ID {
		t.Fatalf("单个改派：%+v", ro)
	}
	if ev := stream.next(t); !hasIDs(ev["file_ids"], a.FileID) {
		t.Fatalf("改派的事件：%v", ev)
	}
	if lg := e.lastLog(a.FileID, "reassign_review"); lg["from"] != "rv1" || lg["to"] != "rv2" || lg["to_id"] != e.rv2ID.String() {
		t.Fatalf("reassign_review 日志：%v", lg)
	}
	same := Decode[struct {
		AssignmentID uuid.UUID `json:"assignment_id"`
		Changed      bool      `json:"changed"`
	}](e.mgr.Post("/api/review-assignments/"+ro.AssignmentID.String()+"/reassign", map[string]any{"reviewer_id": e.rv2ID}).Expect(200))
	if same.Changed || same.AssignmentID != ro.AssignmentID {
		t.Fatalf("改给同一个人：%+v", same)
	}
	e.mgr.Post("/api/review-assignments/"+old.String()+"/reassign", map[string]any{"reviewer_id": e.rv1ID}).ExpectError(409, "invalid_state")
	e.mgr.Post("/api/review-assignments/"+ro.AssignmentID.String()+"/reassign", map[string]any{"reviewer_id": outsider}).
		ExpectError(400, "invalid_input")
	e.mgr.Post("/api/review-assignments/"+uuid.NewString()+"/reassign", map[string]any{"reviewer_id": e.rv1ID}).ExpectError(404, "not_found")

	// 批量改派（按文件）：a 已经是 rv2 的跳过；有不在的 404。按子树：rv1 在盒 1 子树下的（b、c）全部给 rv2；原审核人等于新的 400。
	rm := Decode[reassignResult](e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID,
		"file_ids": []uuid.UUID{a.FileID, d.FileID}}).Expect(200))
	if rm.Reassigned != 1 || rm.Skipped != 1 || !slices.Equal(rm.FileIDs, []uuid.UUID{d.FileID}) || e.openReviewer(d.FileID) != e.rv2ID {
		t.Fatalf("批量改派：%+v", rm)
	}
	res = e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID,
		"file_ids": []uuid.UUID{b.FileID, ghost}}).ExpectError(404, "not_found")
	if ids := detailIDs(res); len(ids) != 1 || ids[0] != ghost {
		t.Fatalf("details.ids：%s", res.Body)
	}
	e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID, "from_reviewer_id": e.rv2ID,
		"folder_id": e.box.ID}).ExpectError(400, "invalid_input")
	e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID, "from_reviewer_id": e.rv1ID}).
		ExpectError(400, "invalid_input")
	var many reassignResult
	e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID, "from_reviewer_id": e.rv1ID,
		"folder_id": e.box.ID}).Expect(200).JSON(&many)
	if many.Reassigned != 2 || many.Skipped != 0 || !slices.Equal(many.FileIDs, []uuid.UUID{b.FileID, c.FileID}) {
		t.Fatalf("按子树改派：%+v", many)
	}

	// open 分配：审核人停用后标出不能再审；按审核人筛选；翻页（limit=1，cursor 原样传回）。
	e.admin.Post("/api/system/users/"+e.rv2ID.String()+"/disable", nil).Expect(200)
	oa = Decode[openAssignments](e.mgr.Get(e.url("/review-assignments?reviewer_id=" + e.rv2ID.String() + "&limit=1")).Expect(200))
	if oa.Total != 4 || len(oa.Items) != 1 || oa.Items[0].ReviewerCanReview || oa.NextCursor == nil || len(oa.Reviewers) != 1 ||
		oa.Reviewers[0].CanReview || !slices.Equal(oa.Reviewers[0].Problems, []string{"disabled"}) {
		t.Fatalf("open 分配（停用）：%+v", oa)
	}
	var seen []uuid.UUID
	seen = append(seen, oa.Items[0].FileID)
	for cur := oa.NextCursor; cur != nil; {
		page := Decode[openAssignments](e.mgr.Get(e.url("/review-assignments?reviewer_id=" + e.rv2ID.String() + "&limit=1&cursor=" + *cur)).Expect(200))
		for _, it := range page.Items {
			seen = append(seen, it.FileID)
		}
		cur = page.NextCursor
	}
	if !slices.Equal(seen, []uuid.UUID{a.FileID, b.FileID, c.FileID, d.FileID}) {
		t.Fatalf("翻页：%v", seen)
	}
	e.mgr.Get(e.url("/review-assignments?cursor=bad")).ExpectError(400, "invalid_input")
	e.mgr.Get(e.url("/review-assignments?limit=2001")).ExpectError(400, "invalid_input")
	e.admin.Post("/api/system/users/"+e.rv2ID.String()+"/enable", nil).Expect(200)

	// 一个文件的全部分配：含已取消的，按分配的先后。
	fr := Decode[struct {
		ReviewStatus string `json:"review_status"`
		VersionNo    int    `json:"version_no"`
		Reviews      []struct {
			ReviewerName string  `json:"reviewer_name"`
			Status       string  `json:"status"`
			Result       *string `json:"result"`
		} `json:"reviews"`
	}](e.mgr.Get("/api/files/" + a.FileID.String() + "/reviews").Expect(200))
	if fr.ReviewStatus != "pending" || fr.VersionNo != 1 || len(fr.Reviews) != 2 || fr.Reviews[0].Status != "cancelled" ||
		fr.Reviews[0].ReviewerName != "rv1" || fr.Reviews[1].Status != "open" || fr.Reviews[1].Result != nil {
		t.Fatalf("一个文件的分配：%+v", fr)
	}

	// 按文件夹超过 1 万个待分配 400（直接造数据：子树里插 10001 个待分配的文件）。
	big := mkdir(e.admin, e.p.RootFolderID, "大")
	e.Exec(`WITH f AS (SELECT gen_random_uuid() AS id, g FROM generate_series(1, 10001) g),
ins AS (INSERT INTO files (id) SELECT id FROM f)
INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, created_by)
SELECT gen_random_uuid(), $1, $2, f.id, 'x' || f.g || '.jpg', f.g, $3 FROM f`, e.p.ID, big.ID, e.scanID)
	e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "folder_id": big.ID}).ExpectError(400, "invalid_input")
	if st, _ := e.status(a.FileID); st != "pending" {
		t.Fatal("状态变了")
	}
}

type reassignResult struct {
	Reassigned int         `json:"reassigned"`
	Skipped    int         `json:"skipped"`
	FileIDs    []uuid.UUID `json:"file_ids"`
}
