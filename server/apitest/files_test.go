package apitest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/file"
)

// TestPermsFiles 是上传、预检、图片信息、快照、日志的菜单权限。
func TestPermsFiles(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                          admin  manager  scanner  reviewer  cataloger  scan  files  review_assign  none
	POST   /api/folders/{id}/files              Y      Y        Y        Y         Y         Y     Y        -           -
	POST   /api/folders/{id}/files:check        Y      Y        Y        Y         Y         Y     Y        -           -
	POST   /api/files/batch                     Y      Y        Y        Y         Y         Y     Y        Y           Y
	GET    /api/files/{fid}/snapshots           Y      Y        Y        Y         Y         Y     Y        Y           Y
	GET    /api/files/{fid}/logs                Y      Y        Y        Y         Y         -     Y        -           -
	`)
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type imageItem struct {
	FileID        uuid.UUID `json:"file_id"`
	ProjectID     uuid.UUID `json:"project_id"`
	SnapshotID    uuid.UUID `json:"snapshot_id"`
	VersionNo     int       `json:"version_no"`
	WidthPx       *int      `json:"width_px"`
	HeightPx      *int      `json:"height_px"`
	DPI           *int      `json:"dpi"`
	ColorMode     *string   `json:"color_mode"`
	FileSize      int64     `json:"file_size"`
	MimeType      string    `json:"mime_type"`
	ContentSHA256 string    `json:"content_sha256"`
	CoverURL      *string   `json:"cover_url"`
	OriginalURL   *string   `json:"original_url"`
	URLExpiresAt  *string   `json:"url_expires_at"`
}

type batchResponse struct {
	Items   []imageItem `json:"items"`
	Missing []uuid.UUID `json:"missing"`
}

// TestUpload：JPEG（带封面和 sha）、方向 6、PNG、TIFF 入库，快照和对象，自动改名，断网重试 200，内容重复 409，
// 别的项目不算重复，DPI 提示，batch、snapshots，事件；临时文件用完删掉。
func TestUpload(t *testing.T) {
	t.Parallel()
	env, tmp := newUploadEnv(t)
	admin := env.Admin()
	p := env.CreateProject("UP")
	f1 := mkdir(admin, p.RootFolderID, "盒 1")
	f2 := mkdir(admin, p.RootFolderID, "盒 2")
	stream := openStream(t, admin, p.ID)
	prefix := QueryValue[string](env, `SELECT path_prefix FROM storage_profiles WHERE is_default`)

	jpg := unique(sampleBytes(t, "jpeg-rgb.jpg"), "u1")
	cover := coverFor(t, 2480, 3508)
	up := Decode[ingestResponse](admin.UploadParts(f1.ID, textPart("file_name", "IMG_0001.jpg"), textPart("content_sha256", shaHex(jpg)),
		filePart("cover", cover), filePart("file", jpg)).Expect(201))
	if up.FileName != "IMG_0001.jpg" || up.Renamed || up.SortNo != 1 || up.VersionNo != 1 || *up.WidthPx != 2480 ||
		*up.HeightPx != 3508 || *up.DPI != 300 || *up.ColorMode != "color" || up.FileSize != int64(len(jpg)) || !up.HasCover ||
		len(up.Warnings) != 0 {
		t.Fatalf("上传：%+v", up)
	}
	if ev := stream.next(t); ev["type"] != "files.changed" || !hasIDs(ev["folder_ids"], f1.ID) || !hasIDs(ev["file_ids"], up.FileID) {
		t.Fatalf("事件：%v", ev)
	}
	base := prefix + "UP/" + up.FileID.String() + "/v1"
	if got := QueryValue[string](env, `SELECT concat_ws('|', s.snapshot_type, s.storage_key, s.cover_key, s.mime_type, s.upload_name,
  encode(s.content_sha256, 'hex'), ff.review_status, ff.review_round)
FROM file_snapshots s JOIN folder_files ff ON ff.file_id = s.file_id WHERE s.id = $1`, up.SnapshotID); got !=
		strings.Join([]string{"original", base + ".jpg", base + ".cover.jpg", "image/jpeg", "IMG_0001.jpg", shaHex(jpg), "unassigned", "1"}, "|") {
		t.Fatalf("快照：%s", got)
	}
	if got := QueryValue[string](env, `SELECT detail::text FROM file_logs WHERE file_id = $1 AND action = 'upload'`, up.FileID); !strings.Contains(got,
		`"folder_path": "UP 项目/盒 1"`) || !strings.Contains(got, `"upload_name": "IMG_0001.jpg"`) {
		t.Fatalf("upload 日志：%s", got)
	}

	// batch：图片地址能下载，原图的 sha 对得上；对象带 Cache-Control 和原始文件名。
	var br batchResponse
	admin.Post("/api/files/batch", map[string]any{"file_ids": []uuid.UUID{up.FileID, up.FileID, uuid.Nil}}).Expect(200).JSON(&br)
	if len(br.Items) != 1 || len(br.Missing) != 1 || br.Items[0].ContentSHA256 != shaHex(jpg) || br.Items[0].SnapshotID != up.SnapshotID ||
		br.Items[0].ProjectID != p.ID || br.Items[0].CoverURL == nil || br.Items[0].OriginalURL == nil || br.Items[0].URLExpiresAt == nil {
		t.Fatalf("batch：%+v", br)
	}
	res, err := testHTTP.Get(*br.Items[0].OriginalURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(body, jpg) || !strings.HasPrefix(res.Header.Get("Cache-Control"), "private, max-age=") ||
		res.Header.Get("X-Amz-Meta-Original-Name") != url.PathEscape("IMG_0001.jpg") || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("原图：%d %v", res.StatusCode, res.Header)
	}
	if code, data := httpGet(t, *br.Items[0].CoverURL); code != 200 || !bytes.Equal(data, cover) {
		t.Fatalf("封面：%d", code)
	}

	// 方向 6 的 JPEG：存转正后的宽高，竖的封面合规。PNG（低 DPI，不带封面）、TIFF（G4 黑白）。
	ex := Decode[ingestResponse](admin.Upload(f1.ID, "exif6.jpg", unique(sampleBytes(t, "jpeg-exif6.jpg"), "u2"), coverFor(t, 1240, 1754)).Expect(201))
	if *ex.WidthPx != 1240 || *ex.HeightPx != 1754 || !ex.HasCover || ex.Warnings[0] != "low_dpi" {
		t.Fatalf("方向 6：%+v", ex)
	}
	pngUp := Decode[ingestResponse](admin.Upload(f1.ID, "alpha.png", unique(sampleBytes(t, "png-alpha.png"), "u3"), nil).Expect(201))
	if pngUp.HasCover || *pngUp.ColorMode != "color" || len(pngUp.Warnings) != 1 || pngUp.Warnings[0] != "low_dpi" {
		t.Fatalf("PNG：%+v", pngUp)
	}
	tif := Decode[ingestResponse](admin.Upload(f1.ID, "g4.tif", unique(sampleBytes(t, "tiff-g4-1bit.tif"), "u4"), nil).Expect(201))
	if *tif.ColorMode != "bw" || *tif.DPI != 300 ||
		QueryValue[string](env, `SELECT storage_key || ' ' || mime_type FROM file_snapshots WHERE id = $1`, tif.SnapshotID) !=
			prefix+"UP/"+tif.FileID.String()+"/v1.tif image/tiff" {
		t.Fatalf("TIFF：%+v", tif)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 60, 80))); err != nil {
		t.Fatal(err)
	}
	unknown := Decode[ingestResponse](admin.Upload(f1.ID, "no-dpi.png", buf.Bytes(), nil).Expect(201))
	if unknown.DPI != nil || len(unknown.Warnings) != 1 || unknown.Warnings[0] != "dpi_unknown" {
		t.Fatalf("DPI 未知：%+v", unknown)
	}

	// 同名自动改名，排到末尾。
	again := Decode[ingestResponse](admin.Upload(f1.ID, "img_0001.JPG", unique(sampleBytes(t, "jpeg-rgb.jpg"), "u5"), nil).Expect(201))
	if again.FileName != "img_0001 (1).JPG" || !again.Renamed || again.SortNo != 6 {
		t.Fatalf("重名：%+v", again)
	}

	// 断网重试：同一文件夹里 upload_name 和 sha 都相同 → 200 已有文件（被人改了名也认得出），不写库、不发事件。
	retry := Decode[ingestResponse](admin.Upload(f1.ID, "IMG_0001.jpg", jpg, nil).Expect(200))
	if retry.FileID != up.FileID || retry.Renamed || !retry.HasCover {
		t.Fatalf("断网重试：%+v", retry)
	}
	admin.Patch("/api/folder-files/"+up.FolderFileID.String(), map[string]any{"file_name": "第一页.jpg"}).Expect(200)
	retry = Decode[ingestResponse](admin.Upload(f1.ID, "IMG_0001.jpg", jpg, nil).Expect(200))
	if retry.FileID != up.FileID || retry.FileName != "第一页.jpg" || !retry.Renamed {
		t.Fatalf("改名后断网重试：%+v", retry)
	}

	// 内容重复：同项目别的文件夹 409（details 是已有文件的位置）；别的项目不算。
	dup := admin.Upload(f2.ID, "copy.jpg", jpg, nil).ExpectError(409, "duplicate_content")
	var dd struct {
		Details file.DuplicateDetails `json:"details"`
	}
	dup.JSON(&dd)
	if dd.Details.FileID != up.FileID || dd.Details.FolderPath != "UP 项目/盒 1" || dd.Details.FileName != "第一页.jpg" {
		t.Fatalf("duplicate_content：%s", dup.Body)
	}
	other := env.CreateProject("UP2")
	op := Decode[ingestResponse](admin.Upload(other.RootFolderID, "IMG_0001.jpg", jpg, nil).Expect(201))
	if n := QueryValue[int](env, `SELECT count(*) FROM folder_files WHERE project_id = $1`, p.ID); n != 6 {
		t.Fatalf("项目里应有 6 个文件，实际 %d", n)
	}

	// snapshots：全部版本，带存储名、key、创建人、图片地址。
	snaps := Decode[struct {
		FileID    uuid.UUID `json:"file_id"`
		Snapshots []struct {
			imageItem
			SnapshotType       string  `json:"snapshot_type"`
			UploadName         *string `json:"upload_name"`
			CreatedByName      *string `json:"created_by_name"`
			StorageProfileName string  `json:"storage_profile_name"`
			StorageKey         string  `json:"storage_key"`
		} `json:"snapshots"`
	}](admin.Get("/api/files/" + up.FileID.String() + "/snapshots").Expect(200))
	if len(snaps.Snapshots) != 1 || snaps.Snapshots[0].SnapshotType != "original" || snaps.Snapshots[0].StorageKey != base+".jpg" ||
		snaps.Snapshots[0].StorageProfileName != "SeaweedFS（随系统部署）" || *snaps.Snapshots[0].CreatedByName != "系统管理员" ||
		*snaps.Snapshots[0].UploadName != "IMG_0001.jpg" || snaps.Snapshots[0].CoverURL == nil {
		t.Fatalf("snapshots：%+v", snaps)
	}

	// 只给能进的项目：别的项目、已删除、不存在的放 missing；snapshots 进不了项目也是 404。
	scanner := env.As("scanner")
	env.GrantProject(p.ID, env.PersonaID("scanner"))
	admin.Delete("/api/folder-files/" + pngUp.FolderFileID.String()).Expect(204)
	scanner.Post("/api/files/batch", map[string]any{"file_ids": []uuid.UUID{op.FileID, up.FileID, pngUp.FileID, uuid.New()}}).
		Expect(200).JSON(&br)
	if len(br.Items) != 1 || br.Items[0].FileID != up.FileID || len(br.Missing) != 3 || br.Missing[0] != op.FileID {
		t.Fatalf("batch 过滤：%+v", br)
	}
	scanner.Get("/api/files/"+op.FileID.String()+"/snapshots").ExpectError(404, "not_found")
	scanner.Get("/api/files/"+pngUp.FileID.String()+"/snapshots").ExpectError(404, "not_found")
	scanner.Post("/api/files/batch", map[string]any{"file_ids": []uuid.UUID{}}).Expect(200)
	many := make([]uuid.UUID, 501)
	for i := range many {
		many[i] = uuid.New()
	}
	scanner.Post("/api/files/batch", map[string]any{"file_ids": many}).ExpectError(400, "invalid_input")

	if left := uploadTmpFiles(t, tmp); len(left) != 0 {
		t.Fatalf("临时文件没删：%v", left)
	}
}

// TestUploadErrors：413、415、invalid_image、checksum_mismatch、invalid_cover、invalid_input（字段、名字）、404；
// 原图还没发完就出错（包括鉴权、找不到文件夹）时客户端照样收到完整的错误响应；失败后什么都没写，临时文件删掉。
func TestUploadErrors(t *testing.T) {
	t.Parallel()
	env, tmp := newUploadEnv(t, func(o *file.Options) { o.MaxUploadBytes = 1 << 20 })
	admin := env.Admin()
	p := env.CreateProject("UE")
	f := mkdir(admin, p.RootFolderID, "盒")
	jpg := unique(sampleBytes(t, "jpeg-gray.jpg"), "e1") // 1654×2339
	cover := coverFor(t, 1654, 2339)
	name := textPart("file_name", "a.jpg")

	big := append(append([]byte{}, jpg[:4]...), make([]byte, (1<<20)+1)...)
	admin.Upload(f.ID, "big.jpg", big, nil).ExpectError(413, "payload_too_large")
	admin.Upload(f.ID, "a.heic", append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 100)...), nil).ExpectError(415, "unsupported_media_type")
	admin.Upload(f.ID, "a.jpg", jpg[:20], nil).ExpectError(400, "invalid_image")
	admin.Upload(f.ID, "a.tif", sampleBytes(t, "tiff-multipage-2p.tif"), nil).ExpectError(400, "invalid_image")
	admin.UploadParts(f.ID, name, textPart("content_sha256", strings.Repeat("0", 64)), filePart("file", jpg)).
		ExpectError(400, "checksum_mismatch")

	// 封面：不是 JPEG、横竖不一致、长边超过 400、长宽比不对、超过 256 KiB。
	var pngCover bytes.Buffer
	_ = png.Encode(&pngCover, image.NewGray(image.Rect(0, 0, 283, 400)))
	for _, c := range [][]byte{pngCover.Bytes(), coverJPEG(t, 400, 283), coverJPEG(t, 354, 500), coverJPEG(t, 250, 400),
		append(coverJPEG(t, 283, 400), make([]byte, 256<<10)...)} {
		admin.Upload(f.ID, "a.jpg", jpg, c).ExpectError(400, "invalid_cover")
	}

	// 字段：顺序、缺、重复、多余、不认识；名字不合法；不是 multipart。
	for _, parts := range [][]part{
		{filePart("file", jpg), name},
		{name},
		{name, name, filePart("file", jpg)},
		{name, filePart("file", jpg), textPart("extra", "x")},
		{name, textPart("folder", "x"), filePart("file", jpg)},
		{name, filePart("cover", cover), textPart("content_sha256", shaHex(jpg)), filePart("file", jpg)},
		{textPart("file_name", "CON.jpg"), filePart("file", jpg)},
		{textPart("file_name", strings.Repeat("长", 2000)), filePart("file", jpg)},
		{name, textPart("content_sha256", strings.ToUpper(shaHex(jpg))), filePart("file", jpg)},
	} {
		admin.UploadParts(f.ID, parts...).ExpectError(400, "invalid_input")
	}
	admin.Post("/api/folders/"+f.ID.String()+"/files", map[string]any{"file_name": "a.jpg"}).ExpectError(400, "invalid_input")

	// 原图有 2.5 MiB 还没发完就出错：名字不合法（处理函数里）、文件夹已删除（项目授权的反查里）、没登录（鉴权里），
	// 客户端都收到完整的错误响应，不是"连接被重置"。
	huge := append(append([]byte{}, jpg...), make([]byte, 5<<19)...)
	admin.Upload(f.ID, "nul", huge, nil).ExpectError(400, "invalid_input")
	gone := mkdir(admin, p.RootFolderID, "已删除")
	admin.Delete("/api/folders/" + gone.ID.String()).Expect(204)
	admin.Upload(gone.ID, "a.jpg", huge, nil).ExpectError(404, "not_found")
	env.Anon().Upload(f.ID, "a.jpg", huge, nil).ExpectError(401, "unauthorized")

	if n := QueryValue[int](env, `SELECT count(*) FROM files`); n != 0 {
		t.Fatalf("失败的上传写了 %d 个文件", n)
	}
	if keys := s3Keys(t, env); len(keys) != 0 {
		t.Fatalf("失败的上传留下了对象：%v", keys)
	}
	if left := uploadTmpFiles(t, tmp); len(left) != 0 {
		t.Fatalf("临时文件没删：%v", left)
	}
	// 最后正常传一个，证明前面的失败没影响。
	admin.Upload(f.ID, "a.jpg", jpg, cover).Expect(201)
}

// TestUploadCheck：预检的三种结果和实际上传一致；参数错误。
func TestUploadCheck(t *testing.T) {
	t.Parallel()
	env, _ := newUploadEnv(t, func(o *file.Options) { o.MaxUploadBytes = 1 << 20 })
	admin := env.Admin()
	p := env.CreateProject("UC")
	f1 := mkdir(admin, p.RootFolderID, "盒 1")
	f2 := mkdir(admin, p.RootFolderID, "盒 2")
	data := unique(sampleBytes(t, "jpeg-progressive.jpg"), "c1")
	up := Decode[ingestResponse](admin.Upload(f1.ID, "x.jpg", data, nil).Expect(201))

	type checkResponse struct {
		Duplicate       *file.DuplicateDetails `json:"duplicate"`
		AlreadyUploaded bool                   `json:"already_uploaded"`
		FinalName       string                 `json:"final_name"`
	}
	check := func(folder uuid.UUID, name string, sha string, size int) *Response {
		return admin.Post("/api/folders/"+folder.String()+"/files:check", map[string]any{"file_name": name, "sha256": sha, "size": size})
	}
	r := Decode[checkResponse](check(f1.ID, "x.jpg", shaHex(data), len(data)).Expect(200))
	if !r.AlreadyUploaded || r.Duplicate != nil || r.FinalName != "x.jpg" {
		t.Fatalf("已上传过：%+v", r)
	}
	admin.Upload(f1.ID, "x.jpg", data, nil).Expect(200)

	r = Decode[checkResponse](check(f2.ID, "y.jpg", shaHex(data), len(data)).Expect(200))
	if r.AlreadyUploaded || r.Duplicate == nil || r.Duplicate.FileID != up.FileID || r.Duplicate.FolderPath != "UC 项目/盒 1" ||
		r.FinalName != "y.jpg" {
		t.Fatalf("内容重复：%+v", r)
	}
	admin.Upload(f2.ID, "y.jpg", data, nil).ExpectError(409, "duplicate_content")

	other := unique(sampleBytes(t, "jpeg-progressive.jpg"), "c2")
	r = Decode[checkResponse](check(f1.ID, "X.JPG", shaHex(other), len(other)).Expect(200))
	if r.AlreadyUploaded || r.Duplicate != nil || r.FinalName != "X (1).JPG" {
		t.Fatalf("新文件：%+v", r)
	}
	if got := Decode[ingestResponse](admin.Upload(f1.ID, "X.JPG", other, nil).Expect(201)); got.FileName != r.FinalName {
		t.Fatalf("实际名字 %s，预检 %s", got.FileName, r.FinalName)
	}

	check(f1.ID, "x.jpg", strings.ToUpper(shaHex(data)), 10).ExpectError(400, "invalid_input")
	check(f1.ID, "aux.jpg", shaHex(data), 10).ExpectError(400, "invalid_input")
	check(f1.ID, "x.jpg", shaHex(data), (1<<20)+1).ExpectError(413, "payload_too_large")
	check(uuid.New(), "x.jpg", shaHex(data), 10).ExpectError(404, "not_found")
}

// TestUploadSameContentConcurrently：同内容同时传进同一项目的不同文件夹，只入库一个，其余 409 duplicate_content。
func TestUploadSameContentConcurrently(t *testing.T) {
	t.Parallel()
	env, _ := newUploadEnv(t)
	admin := env.Admin()
	p := env.CreateProject("UX")
	const n = 8
	for round := range 3 {
		data := unique(sampleBytes(t, "jpeg-progressive.jpg"), "round-"+string(rune('a'+round)))
		folders := make([]uuid.UUID, n)
		for i := range folders {
			folders[i] = mkdir(admin, p.RootFolderID, "r"+string(rune('a'+round))+"-"+string(rune('0'+i))).ID
		}
		type result struct {
			status int
			code   string
		}
		results := make([]result, n)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body, ct := multipartBody(t, textPart("file_name", "page.jpg"), filePart("file", data))
				req, _ := http.NewRequest("POST", admin.base+"/api/folders/"+folders[i].String()+"/files", bytes.NewReader(body))
				req.Header.Set("Content-Type", ct)
				<-start
				res, err := admin.hc.Do(req)
				if err != nil {
					results[i] = result{status: -1, code: err.Error()}
					return
				}
				defer res.Body.Close()
				var e struct{ Code string }
				_ = json.NewDecoder(res.Body).Decode(&e)
				results[i] = result{status: res.StatusCode, code: e.Code}
			}()
		}
		close(start)
		wg.Wait()
		created := 0
		for _, r := range results {
			switch {
			case r.status == 201:
				created++
			case r.status == 409 && r.code == "duplicate_content":
			default:
				t.Fatalf("第 %d 轮：意外的结果 %+v", round, r)
			}
		}
		if created != 1 {
			t.Fatalf("第 %d 轮：%d 个入库，应只有 1 个：%+v", round, created, results)
		}
		if got := QueryValue[int](env, `SELECT count(*) FROM file_snapshots WHERE content_sha256 = $1`, sha256Of(data)); got != 1 {
			t.Fatalf("第 %d 轮：库里有 %d 个相同内容的快照", round, got)
		}
	}
}

func sha256Of(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// TestUploadIdleTimeout：请求体超过空闲时间收不到数据时 400 invalid_input，临时文件删掉。
func TestUploadIdleTimeout(t *testing.T) {
	t.Parallel()
	env, tmp := newUploadEnv(t, func(o *file.Options) { o.IdleTimeout = 300 * time.Millisecond })
	admin := env.Admin()
	p := env.CreateProject("UI")
	body, ct := multipartBody(t, textPart("file_name", "a.jpg"), filePart("file", sampleBytes(t, "jpeg-progressive.jpg")))
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(body[:len(body)/2]) // 发一半就停住
		time.Sleep(3 * time.Second)
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", admin.base+"/api/folders/"+p.RootFolderID.String()+"/files", pr)
	req.Header.Set("Content-Type", ct)
	req.ContentLength = int64(len(body))
	start := time.Now()
	res, err := admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var e struct{ Code string }
	_ = json.NewDecoder(res.Body).Decode(&e)
	if res.StatusCode != 400 || e.Code != "invalid_input" || time.Since(start) > 2*time.Second {
		t.Fatalf("空闲超时：%d %s，用了 %s", res.StatusCode, e.Code, time.Since(start))
	}
	if left := uploadTmpFiles(t, tmp); len(left) != 0 {
		t.Fatalf("临时文件没删：%v", left)
	}
}
