package upload_test

// 连真 server 跑一遍上传队列（设置 DOCLITE_IT_SERVER 才跑，例如 http://127.0.0.1:29081）：
//
//	DOCLITE_IT_SERVER=http://127.0.0.1:29081 go test ./internal/upload/ -run Integration -v
//
// 账号默认 demo_scanner / demo123456（DOCLITE_IT_USERNAME、DOCLITE_IT_PASSWORD），项目默认第一个能进的
// （DOCLITE_IT_PROJECT 指定项目编码）。在项目根下建一个"M6-2 集成 <时间>"文件夹，传完后删掉（软删除）。
// 样本取 server/testdata/samples，每次运行往里加一段注释（图片不变、内容各次不同，不会判为重复）。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"doclite/client/internal/proxy"
	"doclite/client/internal/upload"
)

const samples = "../../../server/testdata/samples"

// tagged 往样本里加一段这次运行特有的注释：JPEG 加 COM 段、PNG 在 IEND 前加 tEXt 块、TIFF 在末尾追加。
func tagged(t *testing.T, name, tag string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(samples, name))
	if err != nil {
		t.Fatal(err)
	}
	text := []byte(tag + " " + name)
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8}):
		i := 2
		for i+4 <= len(data) && data[i] == 0xFF && data[i+1] >= 0xE0 && data[i+1] <= 0xEF {
			i += 2 + int(binary.BigEndian.Uint16(data[i+2:]))
		}
		seg := []byte{0xFF, 0xFE, 0, 0}
		binary.BigEndian.PutUint16(seg[2:], uint16(len(text)+2))
		return slices.Concat(data[:i], seg, text, data[i:])
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		body := append([]byte("tEXtComment\x00"), text...)
		chunk := binary.BigEndian.AppendUint32(nil, uint32(len(body)-4))
		chunk = append(chunk, body...)
		chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
		at := len(data) - 12
		return slices.Concat(data[:at], chunk, data[at:])
	}
	return slices.Concat(data, text)
}

// recorder 记下队列发的事件。
type recorder struct {
	mu       sync.Mutex
	progress map[int64]int64 // 项 → 最后一次的 sent
	auth     int
}

func (r *recorder) ItemChanged(*upload.Item) {}
func (r *recorder) ItemRemoved(int64)        {}
func (r *recorder) Progress(id, sent, size int64) {
	r.mu.Lock()
	r.progress[id] = sent
	r.mu.Unlock()
}
func (r *recorder) StateChanged(upload.QueueStatus)   {}
func (r *recorder) AuthRequired()                     { r.mu.Lock(); r.auth++; r.mu.Unlock() }
func (r *recorder) UpgradeRequired(json.RawMessage)   {}
func (r *recorder) HotfolderChanged(upload.HotStatus) {}

func fetch(t *testing.T, p *proxy.Proxy, method, path string, body any, out any) int {
	t.Helper()
	req := &proxy.Request{Method: method, Path: path, Headers: map[string]string{"accept": "application/json"}}
	if body != nil {
		b, _ := json.Marshal(body)
		s := string(b)
		req.Body = &s
		req.Headers["content-type"] = "application/json"
	}
	resp, err := p.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("%s %s：%v", method, path, err)
	}
	if out != nil && resp.Status < 300 {
		if err := json.Unmarshal([]byte(resp.Body), out); err != nil {
			t.Fatalf("%s %s 的响应：%v", method, path, err)
		}
	}
	return resp.Status
}

func login(t *testing.T, p *proxy.Proxy) {
	t.Helper()
	user, pass := os.Getenv("DOCLITE_IT_USERNAME"), os.Getenv("DOCLITE_IT_PASSWORD")
	if user == "" {
		user, pass = "demo_scanner", "demo123456"
	}
	if st := fetch(t, p, http.MethodPost, proxy.LoginPath, map[string]string{"username": user, "password": pass}, nil); st != 200 {
		t.Fatalf("登录失败：HTTP %d", st)
	}
}

func TestIntegrationUpload(t *testing.T) {
	server := os.Getenv("DOCLITE_IT_SERVER")
	if server == "" {
		t.Skip("没有设置 DOCLITE_IT_SERVER（连真 server 的集成测试）")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	p := proxy.New("0.1.0", proxy.OpenJar("", server), log)
	login(t, p)

	var projects []struct {
		ID           string `json:"id"`
		ProjectCode  string `json:"project_code"`
		Status       string `json:"status"`
		RootFolderID string `json:"root_folder_id"`
	}
	fetch(t, p, http.MethodGet, "/api/projects", nil, &projects)
	idx := slices.IndexFunc(projects, func(pr struct {
		ID           string `json:"id"`
		ProjectCode  string `json:"project_code"`
		Status       string `json:"status"`
		RootFolderID string `json:"root_folder_id"`
	}) bool {
		if code := os.Getenv("DOCLITE_IT_PROJECT"); code != "" {
			return pr.ProjectCode == code
		}
		return pr.Status == "active"
	})
	if idx < 0 {
		t.Fatal("这个账号没有能进的项目")
	}
	project := projects[idx]
	var target struct {
		ID string `json:"id"`
	}
	stamp := time.Now().Format("0102-150405.000")
	if st := fetch(t, p, http.MethodPost, "/api/folders", map[string]any{"parent_id": project.RootFolderID,
		"folder_name": "M6-2 集成 " + stamp}, &target); st != 201 {
		t.Fatalf("建测试文件夹：HTTP %d", st)
	}
	t.Cleanup(func() { fetch(t, p, http.MethodDelete, "/api/folders/"+target.ID, nil, nil) })

	// 本机文件：根目录一张（方向 6），"盒 001" 四张（自然顺序 IMG_1、IMG_2、IMG_3、IMG_10；IMG_3 是 JPEG 压缩的
	// TIFF，生成不了封面），"盒 002" 两张内容相同（b 判为重复）。
	dir := t.TempDir()
	tag := "m6-2-it " + stamp
	files := []struct{ rel, name, sample string }{
		{"", "封面.jpg", "jpeg-exif6.jpg"},
		{"扫描/盒 001", "IMG_10.png", "png-alpha.png"},
		{"扫描/盒 001", "IMG_2.tif", "tiff-g4-1bit.tif"},
		{"扫描/盒 001", "IMG_3.tif", "tiff-jpeg-ycbcr.tif"},
		{"扫描/盒 001", "IMG_1.jpg", "jpeg-rgb.jpg"},
		{"扫描/盒 002", "a.jpg", "jpeg-gray.jpg"},
		{"扫描/盒 002", "b.jpg", "jpeg-gray.jpg"},
	}
	var local []upload.LocalFile
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.rel), f.name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, tagged(t, f.sample, tag), 0o644); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(path)
		local = append(local, upload.LocalFile{Path: path, RelDir: f.rel, Size: st.Size(), MtimeNS: st.ModTime().UnixNano()})
	}

	store, err := upload.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec := &recorder{progress: map[int64]int64{}}
	e := upload.NewEngine(store, upload.NewClient(p), log, rec)
	added, skipped, err := e.Enqueue(context.Background(), local, target.ID)
	if err != nil || len(added) != len(files) || len(skipped) != 0 {
		t.Fatalf("加入队列：%v %v %v", added, skipped, err)
	}
	e.Start()
	defer e.Stop(5 * time.Second)

	waitDone := func(n int) upload.Stats {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for {
			st := e.Status(context.Background())
			if st.Stats.Done+st.Stats.Duplicate+st.Stats.Failed >= n && st.Stats.Uploading == 0 {
				return st.Stats
			}
			if time.Now().After(deadline) {
				t.Fatalf("两分钟还没传完：%+v", st)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	stats := waitDone(len(files))
	if stats.Done != 6 || stats.Duplicate != 1 || stats.Failed != 0 {
		items, _ := e.Recent(context.Background(), 100, nil)
		for _, it := range items {
			t.Logf("%s/%s %s %v %v", it.RelDir, it.FileName, it.Status, deref(it.ErrorCode), deref(it.ErrorMessage))
		}
		t.Fatalf("结果不对：%+v", stats)
	}

	items, _ := e.Recent(context.Background(), 100, nil)
	byName := map[string]*upload.Item{}
	for _, it := range items {
		byName[it.FileName] = it
	}
	if it := byName["b.jpg"]; it.Status != upload.StatusDuplicate || it.DuplicateOf == nil {
		t.Errorf("b.jpg 应该判为内容重复：%s %s", it.Status, it.DuplicateOf)
	}
	if it := byName["IMG_3.tif"]; deref(it.CoverStatus) != upload.CoverNotGenerated {
		t.Errorf("JPEG 压缩的 TIFF 应该不带封面：%v", deref(it.CoverStatus))
	}
	var fileIDs []string
	for name, it := range byName {
		if it.Status != upload.StatusDone {
			continue
		}
		if it.FileID == nil || it.FolderID == nil {
			t.Fatalf("%s 没有 file_id、folder_id", name)
		}
		if name != "IMG_3.tif" && deref(it.CoverStatus) != upload.CoverIncluded {
			t.Errorf("%s 应该带封面：%v %v", name, deref(it.CoverStatus), deref(it.CoverReason))
		}
		if rec.progress[it.ID] != it.Size {
			t.Errorf("%s 上传进度的最后一次应该是传完（%d），实际 %d", name, it.Size, rec.progress[it.ID])
		}
		fileIDs = append(fileIDs, *it.FileID)
	}

	// 子文件夹逐层建好，盒 001 里按自然顺序入库（sort_no 的先后）。
	var top struct {
		Folders []struct {
			ID         string `json:"id"`
			FolderName string `json:"folder_name"`
		} `json:"folders"`
		Files []struct {
			FileName string `json:"file_name"`
		} `json:"files"`
	}
	fetch(t, p, http.MethodGet, "/api/folders/"+target.ID+"/children", nil, &top)
	if len(top.Folders) != 1 || top.Folders[0].FolderName != "扫描" || len(top.Files) != 1 || top.Files[0].FileName != "封面.jpg" {
		t.Fatalf("目标文件夹里应该是子文件夹「扫描」和「封面.jpg」：%+v", top)
	}
	box := *byName["IMG_1.jpg"].FolderID
	var children struct {
		Files []struct {
			FileName string `json:"file_name"`
			SortNo   int    `json:"sort_no"`
		} `json:"files"`
	}
	fetch(t, p, http.MethodGet, "/api/folders/"+box+"/children", nil, &children)
	var order []string
	for _, f := range children.Files {
		order = append(order, f.FileName)
	}
	if want := []string{"IMG_1.jpg", "IMG_2.tif", "IMG_3.tif", "IMG_10.png"}; !slices.Equal(order, want) {
		t.Errorf("盒 001 的入库顺序 %v，应为 %v", order, want)
	}

	// 封面：batch 里除了 IMG_3 都有封面地址。
	var batch struct {
		Items []struct {
			FileID   string  `json:"file_id"`
			CoverURL *string `json:"cover_url"`
			WidthPx  int     `json:"width_px"`
			HeightPx int     `json:"height_px"`
		} `json:"items"`
	}
	fetch(t, p, http.MethodPost, "/api/files/batch", map[string]any{"file_ids": fileIDs}, &batch)
	covers := 0
	for _, it := range batch.Items {
		if it.CoverURL != nil {
			covers++
		}
	}
	if covers != len(fileIDs)-1 {
		t.Errorf("有封面的应该是 %d 个，实际 %d", len(fileIDs)-1, covers)
	}

	// 没变化的不重传。
	_, skipped, _ = e.Enqueue(context.Background(), local, target.ID)
	if len(skipped) != len(local) {
		t.Errorf("没变化的应该都跳过：%v", skipped)
	}

	// 会话失效（退出登录）：队列暂停；重新登录后自动继续。
	fetch(t, p, http.MethodPost, proxy.LogoutPath, nil, nil)
	extra := filepath.Join(dir, "扫描", "盒 001", "IMG_11.jpg")
	os.WriteFile(extra, tagged(t, "jpeg-progressive.jpg", tag), 0o644)
	st, _ := os.Stat(extra)
	if _, _, err := e.Enqueue(context.Background(), []upload.LocalFile{{Path: extra, RelDir: "扫描/盒 001", Size: st.Size(),
		MtimeNS: st.ModTime().UnixNano()}}, target.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.Status(context.Background()).Paused == nil {
		if time.Now().After(deadline) {
			t.Fatal("没有会话时队列应该暂停")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ps := e.Status(context.Background()).Paused; ps.Reason != upload.PauseAuth || rec.auth == 0 {
		t.Fatalf("暂停的原因应该是 auth_required：%+v（事件 %d 次）", ps, rec.auth)
	}
	login(t, p)
	e.LoginSucceeded()
	stats = waitDone(len(files) + 1)
	if stats.Done != 7 {
		t.Fatalf("重新登录后应该接着传完：%+v", stats)
	}
	fmt.Printf("集成测试：项目 %s，%d 个文件入库、1 个重复，盒 001 顺序 %v\n", project.ProjectCode, stats.Done, order)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
