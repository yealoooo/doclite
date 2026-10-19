package imagecache

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doclite/client/internal/proxy"
)

const (
	fid  = "0190a5b2-0000-7000-8000-00000000000a"
	sid  = "0190a5b2-0000-7000-8000-00000000000b"
	pidA = "0190a5b2-0000-7000-8000-0000000000a1"
	pidB = "0190a5b2-0000-7000-8000-0000000000b1"
)

func TestParsePath(t *testing.T) {
	ok := map[string]Request{
		"/shell/img/cover/" + fid + "/" + sid:    {Op: OpCover, FileID: fid, SnapshotID: sid},
		"/shell/img/original/" + fid + "/" + sid: {Op: OpOriginal, FileID: fid, SnapshotID: sid},
		"/shell/img/dzi/" + fid + "/" + sid:      {Op: OpDZI, FileID: fid, SnapshotID: sid},
		"/shell/img/tile/" + fid + "/" + sid + "/12/3_10.jpg": {Op: OpTile, FileID: fid, SnapshotID: sid,
			Level: 12, X: 3, Y: 10, Ext: "jpg"},
		"/shell/img/tile/" + fid + "/" + sid + "/0/0_0.png": {Op: OpTile, FileID: fid, SnapshotID: sid, Ext: "png"},
		"/shell/img/test/probe.png":                         {Op: OpTest},
		// 大写的 id 转成小写。
		"/shell/img/cover/0190A5B2-0000-7000-8000-00000000000A/" + sid: {Op: OpCover, FileID: fid, SnapshotID: sid},
	}
	for p, want := range ok {
		got, valid := ParsePath(p)
		if !valid || got != want {
			t.Errorf("ParsePath(%q) = %+v, %v，应为 %+v", p, got, valid, want)
		}
	}
	bad := []string{
		"/shell/img/",
		"/shell/img/cover/" + fid,
		"/shell/img/cover/" + fid + "/" + sid + "/",
		"/shell/img/cover/" + fid + "/" + sid + "/x",
		"/shell/img/thumb/" + fid + "/" + sid,
		"/shell/img/cover/" + fid + "/not-a-uuid",
		"/shell/img/cover/../" + sid,
		"/shell/img/cover/" + fid + "x/" + sid,
		"/shell/img/tile/" + fid + "/" + sid + "/12/3_10.gif",
		"/shell/img/tile/" + fid + "/" + sid + "/12/3-10.jpg",
		"/shell/img/tile/" + fid + "/" + sid + "/-1/0_0.jpg",
		"/shell/img/tile/" + fid + "/" + sid + "/63/0_0.jpg",
		"/shell/img/tile/" + fid + "/" + sid + "/1/0_0",
		"/shell/img/tile/" + fid + "/" + sid + "/1/+1_0.jpg",
		"/shell/img/tile/" + fid + "/" + sid + "/1/0_0.jpg/x",
		"/shell/img/test/other.png",
		"/img/cover/" + fid + "/" + sid,
	}
	for _, p := range bad {
		if r, valid := ParsePath(p); valid {
			t.Errorf("ParsePath(%q) 应该不认，得到 %+v", p, r)
		}
	}
}

func batchBody(items ...string) []byte {
	return []byte(`{"items":[` + strings.Join(items, ",") + `],"missing":[]}`)
}

func item(fileID, snapID, cover string) string {
	c := "null"
	if cover != "" {
		c = `"` + cover + `"`
	}
	return `{"file_id":"` + fileID + `","project_id":"` + pidA + `","snapshot_id":"` + snapID +
		`","file_size":10,"mime_type":"image/jpeg","content_sha256":"AB","cover_url":` + c +
		`,"original_url":"http://s3/o"}`
}

func TestSources(t *testing.T) {
	var s Sources
	const server = "http://srv"
	s.Observe(server, "POST", batchPath, 200, batchBody(item(fid, sid, "http://s3/c1")))
	src, ok := s.Lookup(server, fid, sid)
	if !ok || src.CoverURL != "http://s3/c1" || src.OriginalURL != "http://s3/o" || src.ProjectID != pidA ||
		src.SHA256 != "ab" || src.FileSize != 10 {
		t.Fatalf("batch 没记对：%+v %v", src, ok)
	}
	// file_id 对不上不算。
	if _, ok := s.Lookup(server, pidB, sid); ok {
		t.Error("file_id 对不上也查到了")
	}
	// 最近一次出现的为准（这次没有封面）。
	s.Observe(server, "POST", batchPath, 200, batchBody(item(fid, sid, "")))
	if src, _ := s.Lookup(server, fid, sid); src.CoverURL != "" {
		t.Errorf("应以最近一次为准，封面地址还是 %q", src.CoverURL)
	}
	// 非 200、别的接口不记。
	other := "0190a5b2-0000-7000-8000-0000000000cc"
	s.Observe(server, "POST", batchPath, 500, batchBody(item(fid, other, "http://x")))
	s.Observe(server, "GET", "/api/files/"+fid, 200, batchBody(item(fid, other, "http://x")))
	if _, ok := s.Lookup(server, fid, other); ok {
		t.Error("非 200、别的接口的响应也记了")
	}
	// snapshots。
	snaps := `{"file_id":"` + fid + `","project_id":"` + pidA + `","snapshots":[{"snapshot_id":"` + other +
		`","file_size":5,"mime_type":"image/png","content_sha256":"cd","cover_url":"http://c","original_url":"http://o"}]}`
	s.Observe(server, "GET", "/api/files/"+fid+"/snapshots", 200, []byte(snaps))
	if src, ok := s.Lookup(server, fid, other); !ok || src.MimeType != "image/png" {
		t.Errorf("snapshots 没记对：%+v %v", src, ok)
	}
	// 登录失败不清空，登录成功清空。
	s.Observe(server, "POST", proxy.LoginPath, 401, nil)
	if len(s.m) != 2 {
		t.Errorf("登录失败不该清空，还剩 %d", len(s.m))
	}
	s.Observe(server, "POST", proxy.LoginPath, 200, nil)
	if len(s.m) != 0 {
		t.Error("登录成功应清空")
	}
	// 退出登录清空。
	s.Observe(server, "POST", batchPath, 200, batchBody(item(fid, sid, "http://c")))
	s.Observe(server, "POST", proxy.LogoutPath, 204, nil)
	if len(s.m) != 0 {
		t.Error("退出登录应清空")
	}
	// 换服务器清空。
	s.Observe(server, "POST", batchPath, 200, batchBody(item(fid, sid, "http://c")))
	if _, ok := s.Lookup("http://other", fid, sid); ok || len(s.m) != 0 {
		t.Error("换了服务器还能查到")
	}
}

func TestSourcesLimit(t *testing.T) {
	var s Sources
	var body []string
	ids := make([]string, maxSources+1)
	for i := range ids {
		ids[i] = "0190a5b2-0000-7000-8000-" + hex12(i)
		body = append(body, item(fid, ids[i], "http://c"))
	}
	s.Observe("", "POST", batchPath, 200, batchBody(body...))
	if n := len(s.m); n != maxSources+1-(maxSources+1)/10 {
		t.Fatalf("超出后应丢掉最早的 1/10，还剩 %d", n)
	}
	if _, ok := s.Lookup("", fid, ids[0]); ok {
		t.Error("最早的那条应被丢掉")
	}
	if _, ok := s.Lookup("", fid, ids[maxSources]); !ok {
		t.Error("最新的那条不该丢")
	}
}

func hex12(i int) string {
	const digits = "0123456789abcdef"
	b := []byte("000000000000")
	for j := len(b) - 1; i > 0; j-- {
		b[j] = digits[i%16]
		i /= 16
	}
	return string(b)
}

// 存入超出上限时按最近访问淘汰到 90%，刚存的不删；按项目清空；清空之后拿到的 token 才能存。
func TestStoreLRU(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := openStore(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	st.limit = 1000
	put := func(snap, project string, size int) {
		t.Helper()
		src := filepath.Join(st.tmpDir(), snap)
		if err := os.WriteFile(src, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		e := &entry{FileID: fid, SnapshotID: snap, ProjectID: project, Kind: KindCover, Size: int64(size)}
		if ok, err := st.put(e, src, st.token(project)); !ok || err != nil {
			t.Fatalf("存 %s：%v %v", snap, ok, err)
		}
	}
	put("s1", pidA, 400)
	put("s2", pidA, 300)
	put("s3", pidB, 200)
	// 访问时间：s1 最旧、s2 次之；再访问一次 s1，它就成了最新的。
	st.entries[entryKey("s1", KindCover)].Access = 1
	st.entries[entryKey("s2", KindCover)].Access = 10
	st.entries[entryKey("s3", KindCover)].Access = 50
	if _, _, ok := st.lookup(entryKey("s1", KindCover)); !ok {
		t.Fatal("s1 不在")
	}
	put("s4", pidB, 300) // 1200 > 1000 → 删到 900 以下：先删 s2（最旧）→ 900
	if st.has(entryKey("s2", KindCover)) || !st.has(entryKey("s1", KindCover)) || !st.has(entryKey("s4", KindCover)) {
		t.Errorf("淘汰不对：%v", st.entries)
	}
	if st.total != 900 {
		t.Errorf("占用 %d，应为 900", st.total)
	}
	if _, err := os.Stat(filepath.Join(dir, fid, "s2.cover.jpg")); !os.IsNotExist(err) {
		t.Error("被淘汰的文件还在")
	}
	// 按项目清空。
	tokA := st.token(pidA)
	rep := st.clear(pidB)
	if rep.RemovedFiles != 2 || rep.RemovedBytes != 500 || st.total != 400 {
		t.Errorf("清空项目 B：%+v，剩 %d", rep, st.total)
	}
	// 清空别的项目不影响 A 的在途存入；清空 A 之后，之前拿的 token 存不进。
	put("s5", pidA, 10)
	st.clear(pidA)
	src := filepath.Join(st.tmpDir(), "s6")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	if ok, _ := st.put(&entry{FileID: fid, SnapshotID: "s6", ProjectID: pidA, Kind: KindCover, Size: 1}, src, tokA); ok {
		t.Error("清空之后，清空前的 token 还能存")
	}
	// 索引写盘、重新打开后还在。
	put("s7", pidA, 100)
	st.close()
	st2, err := openStore(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.has(entryKey("s7", KindCover)) || st2.total != 100 {
		t.Errorf("重新打开后：%v 占用 %d", st2.entries, st2.total)
	}
	// 启动之前就有、又不在索引里的文件删掉（索引里的、启动以后新存的不碰）。
	orphan := filepath.Join(dir, fid, sid+".original.jpg")
	_ = os.WriteFile(orphan, []byte("x"), 0o600)
	st2.sweep(time.Now().Add(time.Second))
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("索引外的文件没删")
	}
	if _, err := os.Stat(filepath.Join(dir, fid, "s7.cover.jpg")); err != nil {
		t.Error("索引里的文件被删了")
	}
}
