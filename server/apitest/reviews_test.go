package apitest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestPermsReviews 是审核、重扫接口的菜单权限。
func TestPermsReviews(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                                      admin manager scanner reviewer cataloger review review_assign scan files none
	POST   /api/projects/{pid}/review-assignments            Y     Y       -       -        -        -        Y          -    -    -
	POST   /api/review-assignments/{id}/reassign             Y     Y       -       -        -        -        Y          -    -    -
	POST   /api/projects/{pid}/review-assignments:reassign   Y     Y       -       -        -        -        Y          -    -    -
	GET    /api/projects/{pid}/reviewers                     Y     Y       -       -        -        -        Y          -    -    -
	GET    /api/projects/{pid}/review-summary                Y     Y       -       -        -        -        Y          -    -    -
	GET    /api/projects/{pid}/review-assignments            Y     Y       -       -        -        -        Y          -    -    -
	GET    /api/me/reviews                                   Y     Y       -       Y        -        Y        -          -    -    -
	POST   /api/files/{fid}/review                           Y     Y       -       Y        -        Y        -          -    -    -
	POST   /api/projects/{pid}/reviews:approve               Y     Y       -       Y        -        Y        -          -    -    -
	POST   /api/files/{fid}/reopen                           Y     Y       -       Y        -        Y        Y          -    -    -
	GET    /api/files/{fid}/reviews                          Y     Y       -       Y        -        Y        Y          -    -    -
	POST   /api/files/{fid}/rescan                           Y     Y       Y       Y        Y        -        -          Y    Y    -
	GET    /api/projects/{pid}/rescans                       Y     Y       Y       Y        Y        -        -          Y    Y    -
	GET    /api/projects/{pid}/review-settings               Y     Y       Y       Y        Y        Y        Y          Y    Y    Y
	`)
}

// TestReviewStateTable：requirements 5.8 状态流转表的每一格（4 种状态 × 7 个动作），加上重扫、撤回通过时原审核人不能
// 再审（→ 待分配）的两格。每格一个新文件，审核人 rv1。
func TestReviewStateTable(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "ST")
	type outcome struct {
		status   string // 动作之后文件的审核状态；"deleted" 是已删除
		code     string // 期望的错误码（为空表示成功）
		reviewer string // 之后 open 的那一行是谁："rv1"、"rv2"、""（没有）
		round    int
	}
	type action func(f ingestResponse) *Response
	actions := map[string]action{
		"assign": func(f ingestResponse) *Response {
			return e.mgr.Post(e.url("/review-assignments"), map[string]any{"reviewer_id": e.rv1ID, "file_ids": []uuid.UUID{f.FileID}})
		},
		"reassign": func(f ingestResponse) *Response {
			return e.mgr.Post(e.url("/review-assignments:reassign"), map[string]any{"to_reviewer_id": e.rv2ID,
				"file_ids": []uuid.UUID{f.FileID}})
		},
		"approve": func(f ingestResponse) *Response { return approve(e.rv1, f.FileID) },
		"reject":  func(f ingestResponse) *Response { return reject(e.rv1, f.FileID) },
		"rescan": func(f ingestResponse) *Response {
			return rescan(e.scan, f.FileID, "rescan.jpg", unique(sampleBytes(t, "jpeg-progressive.jpg"), "rs-"+f.FileID.String()))
		},
		"reopen": func(f ingestResponse) *Response { return e.rv1.Post("/api/files/"+f.FileID.String()+"/reopen", nil) },
		"delete": func(f ingestResponse) *Response { return e.mgr.Delete("/api/folder-files/" + f.FolderFileID.String()) },
	}
	cases := []struct {
		state, action string
		want          outcome
		before        func()
		after         func()
	}{
		{"unassigned", "assign", outcome{status: "pending", reviewer: "rv1", round: 1}, nil, nil},
		{"unassigned", "reassign", outcome{status: "unassigned", round: 1}, nil, nil}, // 跳过
		{"unassigned", "approve", outcome{status: "unassigned", code: "invalid_state", round: 1}, nil, nil},
		{"unassigned", "reject", outcome{status: "unassigned", code: "invalid_state", round: 1}, nil, nil},
		{"unassigned", "rescan", outcome{status: "unassigned", code: "invalid_state", round: 1}, nil, nil},
		{"unassigned", "reopen", outcome{status: "unassigned", code: "invalid_state", round: 1}, nil, nil},
		{"unassigned", "delete", outcome{status: "deleted", round: 1}, nil, nil},

		{"pending", "assign", outcome{status: "pending", reviewer: "rv1", round: 1}, nil, nil}, // 跳过
		{"pending", "reassign", outcome{status: "pending", reviewer: "rv2", round: 1}, nil, nil},
		{"pending", "approve", outcome{status: "approved", round: 1}, nil, nil},
		{"pending", "reject", outcome{status: "rejected", round: 1}, nil, nil},
		{"pending", "rescan", outcome{status: "pending", code: "invalid_state", reviewer: "rv1", round: 1}, nil, nil},
		{"pending", "reopen", outcome{status: "pending", code: "invalid_state", reviewer: "rv1", round: 1}, nil, nil},
		{"pending", "delete", outcome{status: "deleted", round: 1}, nil, nil}, // open 的那一行取消

		{"approved", "assign", outcome{status: "approved", round: 1}, nil, nil},   // 跳过
		{"approved", "reassign", outcome{status: "approved", round: 1}, nil, nil}, // 跳过
		{"approved", "approve", outcome{status: "approved", code: "invalid_state", round: 1}, nil, nil},
		{"approved", "reject", outcome{status: "approved", code: "invalid_state", round: 1}, nil, nil},
		{"approved", "rescan", outcome{status: "approved", code: "invalid_state", round: 1}, nil, nil},
		{"approved", "reopen", outcome{status: "pending", reviewer: "rv1", round: 1}, nil, nil},
		{"approved", "delete", outcome{status: "deleted", round: 1}, nil, nil},

		{"rejected", "assign", outcome{status: "rejected", round: 1}, nil, nil},   // 跳过
		{"rejected", "reassign", outcome{status: "rejected", round: 1}, nil, nil}, // 跳过
		{"rejected", "approve", outcome{status: "rejected", code: "invalid_state", round: 1}, nil, nil},
		{"rejected", "reject", outcome{status: "rejected", code: "invalid_state", round: 1}, nil, nil},
		{"rejected", "rescan", outcome{status: "pending", reviewer: "rv1", round: 2}, nil, nil},
		{"rejected", "reopen", outcome{status: "rejected", code: "invalid_state", round: 1}, nil, nil},
		{"rejected", "delete", outcome{status: "deleted", round: 1}, nil, nil},

		// 原审核人不能再审：重扫、撤回通过都改为待分配（rv1 失去项目授权；之后恢复）。
		{"rejected", "rescan", outcome{status: "unassigned", round: 2},
			func() { e.GrantProject(e.p.ID, e.mgrID, e.rv2ID, e.scanID) },
			func() { e.GrantProject(e.p.ID, e.mgrID, e.rv1ID, e.rv2ID, e.scanID) }},
		{"approved", "reopen-by-mgr", outcome{status: "unassigned", round: 1},
			func() { e.Exec(`UPDATE users SET status = 'disabled' WHERE id = $1`, e.rv1ID) },
			func() { e.Exec(`UPDATE users SET status = 'active' WHERE id = $1`, e.rv1ID) }},
	}
	actions["reopen-by-mgr"] = func(f ingestResponse) *Response { return e.mgr.Post("/api/files/"+f.FileID.String()+"/reopen", nil) }

	who := map[uuid.UUID]string{e.rv1ID: "rv1", e.rv2ID: "rv2", uuid.Nil: ""}
	for i, c := range cases {
		f := e.fileIn(c.state, c.state+"-"+c.action+".jpg")
		if c.before != nil {
			c.before()
		}
		res := actions[c.action](f)
		if c.after != nil {
			c.after()
		}
		if c.want.code != "" {
			res.ExpectError(http.StatusConflict, c.want.code)
		} else if res.Status >= 300 {
			t.Fatalf("#%d %s + %s：%d %s", i, c.state, c.action, res.Status, res.Body)
		}
		if c.want.status == "deleted" {
			if QueryValue[bool](e.Env, `SELECT deleted_at IS NOT NULL FROM folder_files WHERE file_id = $1`, f.FileID) != true ||
				QueryValue[int](e.Env, `SELECT count(*) FROM file_reviews WHERE file_id = $1 AND status = 'open'`, f.FileID) != 0 {
				t.Fatalf("#%d %s + %s：没有删除或没有取消分配", i, c.state, c.action)
			}
			continue
		}
		st, round := e.status(f.FileID)
		if st != c.want.status || round != c.want.round || who[e.openReviewer(f.FileID)] != c.want.reviewer {
			t.Fatalf("#%d %s + %s：状态 %s 轮次 %d 审核人 %q，期望 %+v", i, c.state, c.action, st, round,
				who[e.openReviewer(f.FileID)], c.want)
		}
		if n := QueryValue[int](e.Env, `SELECT count(*) FROM file_reviews WHERE file_id = $1 AND status = 'open'`, f.FileID); (n == 1) !=
			(c.want.status == "pending") {
			t.Fatalf("#%d %s + %s：open 的行数 %d", i, c.state, c.action, n)
		}
	}
}
