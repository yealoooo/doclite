package apitest

import (
	"bytes"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type rescanList struct {
	Total int `json:"total"`
	Items []struct {
		FileID       uuid.UUID `json:"file_id"`
		FolderName   string    `json:"folder_name"`
		FolderPath   string    `json:"folder_path"`
		FolderRemark *string   `json:"folder_remark"`
		FileName     string    `json:"file_name"`
		ReviewRound  int       `json:"review_round"`
		VersionNo    int       `json:"version_no"`
		LastReject   *struct {
			ReviewerID   uuid.UUID `json:"reviewer_id"`
			ReviewerName string    `json:"reviewer_name"`
			Reasons      []string  `json:"reasons"`
			Comment      *string   `json:"comment"`
			SnapshotID   uuid.UUID `json:"snapshot_id"`
			VersionNo    int       `json:"version_no"`
		} `json:"last_reject"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// TestRescan：待重扫列表；重扫（不是已驳回 409、和别的文件或自己当前版本相同 409、和自己更早的版本相同放行、
// 追加 rescan 快照、轮次 +1、分回最近一次驳回它的人、那人不能再审时待分配）；同一文件的重扫串行。
func TestRescan(t *testing.T) {
	t.Parallel()
	e := newReviewEnv(t, "RS")
	e.admin.Patch("/api/folders/"+e.box.ID.String(), map[string]any{"remark": "A 架 3 层"}).Expect(200)
	other := e.upload(e.box.ID, "other.jpg")
	f := e.fileIn("pending", "f.jpg")
	review(e.rv1, f.FileID, map[string]any{"result": "rejected", "reject_reasons": []string{"blur", "other"}, "comment": "看不清"}).Expect(200)
	g := e.fileIn("rejected", "g.jpg")

	// 待重扫：已驳回的，带文件夹备注和最近一次驳回；翻页。
	rl := Decode[rescanList](e.scan.Get(e.url("/rescans?limit=1")).Expect(200))
	if rl.Total != 2 || len(rl.Items) != 1 || rl.NextCursor == nil || rl.Items[0].FileID != f.FileID || rl.Items[0].FolderName != "盒 1" ||
		rl.Items[0].FolderPath != "RS 项目/盒 1" || rl.Items[0].FolderRemark == nil || *rl.Items[0].FolderRemark != "A 架 3 层" ||
		rl.Items[0].LastReject == nil || rl.Items[0].LastReject.ReviewerName != "rv1" || *rl.Items[0].LastReject.Comment != "看不清" ||
		len(rl.Items[0].LastReject.Reasons) != 2 || rl.Items[0].LastReject.VersionNo != 1 || rl.Items[0].LastReject.SnapshotID != f.SnapshotID {
		t.Fatalf("待重扫：%+v", rl)
	}
	rl = Decode[rescanList](e.scan.Get(e.url("/rescans?limit=1&cursor=" + *rl.NextCursor)).Expect(200))
	if len(rl.Items) != 1 || rl.Items[0].FileID != g.FileID || rl.NextCursor != nil {
		t.Fatalf("待重扫第 2 页：%+v", rl)
	}

	// 不是已驳回 409 invalid_state；和别的文件、自己现在的版本内容相同 409 duplicate_content；文件不存在 404。
	fresh := func(tag string) []byte { return unique(sampleBytes(t, "jpeg-progressive.jpg"), "rs-"+tag) }
	rescan(e.scan, other.FileID, "x.jpg", fresh("x")).ExpectError(409, "invalid_state")
	otherBytes := unique(sampleBytes(t, "tiff-g4-1bit.tif"), "dup-src")
	dup := Decode[ingestResponse](e.scan.Upload(e.box.ID, "dup.tif", otherBytes, nil).Expect(201))
	res := rescan(e.scan, f.FileID, "f.tif", otherBytes).ExpectError(409, "duplicate_content")
	if Decode[struct {
		Details struct {
			FileID uuid.UUID `json:"file_id"`
		} `json:"details"`
	}](res).Details.FileID != dup.FileID {
		t.Fatalf("重复的 details：%s", res.Body)
	}
	res = rescan(e.scan, g.FileID, "g.tif", downloadSnapshot(t, e, g.FileID, 1)).ExpectError(409, "duplicate_content")
	if !bytes.Contains(res.Body, []byte(g.FileID.String())) {
		t.Fatalf("和自己现在的版本相同，details 要指向自己：%s", res.Body)
	}
	rescan(e.scan, uuid.New(), "n.jpg", fresh("n")).ExpectError(404, "not_found")

	// 成功：v2 rescan 快照、轮次 2、待审核、分回 rv1；对象 key 是 v2；日志；事件。
	stream := openStream(t, e.scan, e.p.ID)
	newBytes := fresh("f-v2")
	r := Decode[rescanResponse](rescan(e.scan, f.FileID, "IMG_重扫.jpg", newBytes).Expect(201))
	if r.FileID != f.FileID || r.VersionNo != 2 || r.ReviewRound != 2 || r.ReviewStatus != "pending" || r.ReviewerID == nil ||
		*r.ReviewerID != e.rv1ID || r.FileName != "f.jpg" || r.Renamed || r.FolderID != e.box.ID || r.FolderFileID != f.FolderFileID {
		t.Fatalf("重扫：%+v", r)
	}
	prefix := QueryValue[string](e.Env, `SELECT path_prefix FROM storage_profiles WHERE is_default`)
	if got := QueryValue[string](e.Env, `SELECT snapshot_type || ' ' || storage_key || ' ' || upload_name FROM file_snapshots WHERE id = $1`,
		r.SnapshotID); got != "rescan "+prefix+"RS/"+f.FileID.String()+"/v2.jpg IMG_重扫.jpg" {
		t.Fatalf("快照：%s", got)
	}
	if lg := e.lastLog(f.FileID, "rescan"); lg["version_no"] != float64(2) || lg["review_round"] != float64(2) ||
		lg["review_status"] != "pending" || lg["reviewer"] != "rv1" || lg["upload_name"] != "IMG_重扫.jpg" || lg["folder_path"] != "RS 项目/盒 1" {
		t.Fatalf("rescan 日志：%v", lg)
	}
	if ev := stream.next(t); ev["type"] != "files.changed" || !hasIDs(ev["file_ids"], f.FileID) {
		t.Fatalf("事件：%v", ev)
	}
	// 断网重试不适用：再传一次同样的 → 已经不是已驳回 409 invalid_state。
	rescan(e.scan, f.FileID, "IMG_重扫.jpg", newBytes).ExpectError(409, "invalid_state")

	// 第 2 轮再驳回，用 v1 的内容重扫（和自己更早的版本相同）放行 → v3、第 3 轮。和自己现在的版本相同 409。
	reject(e.rv1, f.FileID).Expect(200)
	rescan(e.scan, f.FileID, "again.jpg", newBytes).ExpectError(409, "duplicate_content")
	f1Bytes := downloadSnapshot(t, e, f.FileID, 1)
	r = Decode[rescanResponse](rescan(e.scan, f.FileID, "back-to-v1.tif", f1Bytes).Expect(201))
	if r.VersionNo != 3 || r.ReviewRound != 3 {
		t.Fatalf("回到 v1 的内容：%+v", r)
	}

	// 最近一次驳回它的人不能再审（没有 review 菜单）→ 待分配，日志记 skipped_reviewer。
	h := e.fileIn("pending", "h.jpg")
	reject(e.rv1, h.FileID).Expect(200)
	e.admin.Put("/api/system/users/"+e.rv1ID.String()+"/roles", map[string]any{"role_ids": []uuid.UUID{e.RoleID("scanner")}}).Expect(200)
	r = Decode[rescanResponse](rescan(e.scan, h.FileID, "h2.jpg", fresh("h2")).Expect(201))
	if r.ReviewStatus != "unassigned" || r.ReviewerID != nil || r.ReviewRound != 2 {
		t.Fatalf("不能再审的人：%+v", r)
	}
	if lg := e.lastLog(h.FileID, "rescan"); lg["skipped_reviewer"] != "rv1" || lg["reviewer"] != nil || lg["review_status"] != "unassigned" {
		t.Fatalf("skipped_reviewer：%v", lg)
	}

	// 同一个文件的重扫串行：同时传两个不同的重扫件，一个 201、一个 409 invalid_state；v2 的对象是成功的那个。
	k := e.upload(e.box.ID, "k.jpg")
	e.assign(e.rv2ID, k.FileID)
	reject(e.rv2, k.FileID).Expect(200)
	bodies := [][]byte{fresh("k-a"), fresh("k-b")}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = rescan(e.scan, k.FileID, "k.jpg", bodies[i]).Status
		}()
	}
	wg.Wait()
	if !(codes[0] == 201 && codes[1] == 409 || codes[0] == 409 && codes[1] == 201) {
		t.Fatalf("同时重扫：%v", codes)
	}
	won := bodies[0]
	if codes[1] == 201 {
		won = bodies[1]
	}
	if got := downloadSnapshot(t, e, k.FileID, 2); !bytes.Equal(got, won) {
		t.Fatal("v2 的对象被另一个重扫覆盖了")
	}
	if QueryValue[int](e.Env, `SELECT count(*) FROM file_snapshots WHERE file_id = $1`, k.FileID) != 2 {
		t.Fatal("快照数不对")
	}
}

// downloadSnapshot 经 snapshots 接口的预签名地址下载文件第 v 版的原图。
func downloadSnapshot(t *testing.T, e *reviewEnv, fileID uuid.UUID, v int) []byte {
	t.Helper()
	snaps := Decode[struct {
		Snapshots []struct {
			VersionNo   int     `json:"version_no"`
			OriginalURL *string `json:"original_url"`
		} `json:"snapshots"`
	}](e.admin.Get("/api/files/" + fileID.String() + "/snapshots").Expect(200))
	for _, s := range snaps.Snapshots {
		if s.VersionNo == v {
			code, data := httpGet(t, *s.OriginalURL)
			if code != 200 {
				t.Fatalf("下载 v%d：%d", v, code)
			}
			return data
		}
	}
	t.Fatalf("没有 v%d", v)
	return nil
}
