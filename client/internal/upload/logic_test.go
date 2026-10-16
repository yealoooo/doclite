package upload

import (
	"bytes"
	"io"
	"mime/multipart"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// 自然顺序：同一子文件夹按文件名的自然顺序串行上传（IMG_2 在 IMG_10 前）。
func TestNaturalKeyOrder(t *testing.T) {
	want := []string{
		"IMG_1.jpg",
		"img_2.jpg",
		"IMG_02b.jpg",
		"IMG_9.jpg",
		"IMG_10.jpg",
		"IMG_10a.jpg",
		"IMG_100.jpg",
		"page 3 of 12.tif",
		"page 3 of 100.tif",
		"x.jpg",
		"x1.jpg",
		"扫描 2.png",
		"扫描 11.png",
	}
	got := slices.Clone(want)
	slices.Reverse(got)
	sort.SliceStable(got, func(i, j int) bool { return bytes.Compare(NaturalKey(got[i]), NaturalKey(got[j])) < 0 })
	if !slices.Equal(got, want) {
		t.Fatalf("自然顺序不对：\n得到 %q\n应为 %q", got, want)
	}
	// 只差大小写、前导零的按原文定先后（排序是确定的），但不相等。
	for _, pair := range [][2]string{{"IMG_2.jpg", "img_2.jpg"}, {"IMG_02.jpg", "IMG_2.jpg"}} {
		if bytes.Equal(NaturalKey(pair[0]), NaturalKey(pair[1])) {
			t.Errorf("%q 和 %q 的键不该相同", pair[0], pair[1])
		}
	}
}

// 忽略规则：隐藏、系统、临时文件；只收图片扩展名。
func TestIgnoreRules(t *testing.T) {
	for _, name := range []string{".DS_Store", ".hidden.jpg", "~$doc.jpg", "scan.TMP", "Thumbs.db", "desktop.ini", ""} {
		if !IgnoredName(name, false) {
			t.Errorf("%q 应该忽略", name)
		}
	}
	for _, name := range []string{"$RECYCLE.BIN", "System Volume Information", ".git"} {
		if !IgnoredName(name, true) {
			t.Errorf("目录 %q 应该忽略", name)
		}
	}
	for _, name := range []string{"IMG_1.jpg", "盒 001", "a~$b.jpg", "tmp.jpg", "$RECYCLE.BIN.jpg"} {
		if IgnoredName(name, false) {
			t.Errorf("%q 不该忽略", name)
		}
	}
	for name, want := range map[string]bool{"a.JPG": true, "a.jpeg": true, "a.Tif": true, "a.tiff": true, "a.png": true,
		"a.pdf": false, "a.jpg.part": false, "jpg": false} {
		if SupportedImage(name) != want {
			t.Errorf("SupportedImage(%q) 应为 %v", name, want)
		}
	}
}

type fakeProbe struct{ exclusive, tail bool }

func (p fakeProbe) Exclusive() bool { return p.exclusive }
func (p fakeProbe) Tail() bool      { return p.tail }

// 写完判定：大小和修改时间 2 秒不变 → 能独占打开 → 文件尾完整；60 秒还不完整记失败，一直被占用只提示。
func TestJudge(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	ok := fakeProbe{exclusive: true, tail: true}
	s, v := Judge(nil, 100, 1, t0, ok)
	if v != KeepWaiting {
		t.Fatalf("第一次看到应该等：%v", v)
	}
	if _, v = Judge(s, 100, 1, t0.Add(1900*time.Millisecond), ok); v != KeepWaiting {
		t.Fatalf("不到 2 秒应该等：%v", v)
	}
	// 还在写：大小变了，重新计时。
	s, v = Judge(s, 200, 2, t0.Add(1900*time.Millisecond), ok)
	if v != KeepWaiting || !s.Since.Equal(t0.Add(1900*time.Millisecond)) {
		t.Fatalf("变了应该重新计时：%v %v", v, s.Since)
	}
	if _, v = Judge(s, 200, 2, t0.Add(3*time.Second), ok); v != KeepWaiting {
		t.Fatalf("重新计时后不到 2 秒应该等：%v", v)
	}
	if _, v = Judge(s, 200, 2, t0.Add(4*time.Second), ok); v != Ready {
		t.Fatalf("稳定 2 秒、能独占、尾部完整应该就绪：%v", v)
	}
	// 尾部不完整：60 秒内等，之后失败。
	bad := fakeProbe{exclusive: true, tail: false}
	if _, v = Judge(s, 200, 2, t0.Add(30*time.Second), bad); v != KeepWaiting {
		t.Fatalf("尾部不完整、不到 60 秒应该等：%v", v)
	}
	if _, v = Judge(s, 200, 2, t0.Add(62*time.Second), bad); v != Incomplete {
		t.Fatalf("尾部不完整超过 60 秒应该失败：%v", v)
	}
	// 一直被占用：60 秒后只提示，不判失败；能打开后照常就绪。
	locked := fakeProbe{exclusive: false, tail: true}
	if _, v = Judge(s, 200, 2, t0.Add(30*time.Second), locked); v != KeepWaiting {
		t.Fatalf("被占用、不到 60 秒应该等：%v", v)
	}
	if _, v = Judge(s, 200, 2, t0.Add(70*time.Second), locked); v != Locked {
		t.Fatalf("被占用超过 60 秒应该提示：%v", v)
	}
	if _, v = Judge(s, 200, 2, t0.Add(71*time.Second), ok); v != Ready {
		t.Fatalf("不再被占用后应该就绪：%v", v)
	}
}

// 文件尾：JPEG 有 FF D9、PNG 有 IEND（允许后面跟 4 KiB 以内的多余字节），TIFF 不查，只写了开头几个字节的不完整。
func TestTailComplete(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{1}, 10000)...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 10000)...)
	iend := []byte{0, 0, 0, 0, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"jpeg 完整", append(slices.Clone(jpeg), 0xFF, 0xD9), true},
		{"jpeg 后面有填充", append(append(slices.Clone(jpeg), 0xFF, 0xD9), make([]byte, 4000)...), true},
		{"jpeg 没写完", jpeg, false},
		{"jpeg EOI 后面太多", append(append(slices.Clone(jpeg), 0xFF, 0xD9), make([]byte, 5000)...), false},
		{"png 完整", append(slices.Clone(png), iend...), true},
		{"png 没写完", png, false},
		{"tiff 不查", []byte("II*\x00abc"), true},
		{"空文件", nil, false},
		{"只有 png 开头", []byte("\x89PN"), false},
		{"只有 jpeg 开头", []byte{0xFF, 0xD8}, false},
	}
	for _, c := range cases {
		got, err := TailComplete(bytes.NewReader(c.data), int64(len(c.data)))
		if err != nil || got != c.want {
			t.Errorf("%s：得到 %v（%v），应为 %v", c.name, got, err, c.want)
		}
	}
}

// 重试间隔：1 秒起翻倍，封顶 5 分钟。
func TestRetryDelay(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := RetryDelay(i + 1); got != w {
			t.Errorf("第 %d 次：%v，应为 %v", i+1, got, w)
		}
	}
	if RetryDelay(1000) != MaxRetryDelay || RetryDelay(0) != time.Second {
		t.Error("边界不对")
	}
}

// multipart：字段按序各一次 file_name、content_sha256、cover（可选）、file；Content-Length 和实际字节数一致；
// 文件比说好的短报 ErrFileChanged。
func TestMultipartBody(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	file := bytes.Repeat([]byte("0123456789"), 10000)
	for _, cover := range [][]byte{[]byte("\xFF\xD8cover\xFF\xD9"), nil} {
		b, err := NewBody(`盒 "001"\IMG_1.jpg`, sha, cover, int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(b.Reader(bytes.NewReader(file)))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) != b.Length {
			t.Fatalf("Content-Length %d，实际 %d", b.Length, len(data))
		}
		_, params, _ := strings.Cut(b.ContentType, "boundary=")
		mr := multipart.NewReader(bytes.NewReader(data), params)
		var names []string
		values := map[string][]byte{}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			v, _ := io.ReadAll(p)
			names = append(names, p.FormName())
			values[p.FormName()] = v
		}
		want := []string{"file_name", "content_sha256", "cover", "file"}
		if cover == nil {
			want = []string{"file_name", "content_sha256", "file"}
		}
		if !slices.Equal(names, want) {
			t.Fatalf("字段顺序 %v，应为 %v", names, want)
		}
		if string(values["file_name"]) != `盒 "001"\IMG_1.jpg` || string(values["content_sha256"]) != sha ||
			!bytes.Equal(values["cover"], cover) || !bytes.Equal(values["file"], file) {
			t.Fatal("字段内容不对")
		}
	}
	b, _ := NewBody("a.jpg", sha, nil, 100)
	if _, err := io.ReadAll(b.Reader(bytes.NewReader(make([]byte, 60)))); err != ErrFileChanged {
		t.Fatalf("文件变短了应该报 ErrFileChanged，实际 %v", err)
	}
}

// M7-1：许可证到期、没有有效许可证（403）和会话失效一样暂停整个队列，不算文件失败；别的 403 照旧永久失败。
func TestClassifyLicenseBlocked(t *testing.T) {
	e := &Engine{}
	for _, code := range []string{"license_expired", "license_missing"} {
		r := e.classifyAPI(&Item{}, &APIError{Status: 403, Code: code, Message: "x"})
		if r.kind != resPause || r.pause != PauseAuth || r.message == "" {
			t.Fatalf("%s：%+v", code, r)
		}
	}
	if r := e.classifyAPI(&Item{}, &APIError{Status: 403, Code: "forbidden", Message: "x"}); r.kind != resFailed {
		t.Fatalf("forbidden：%+v", r)
	}
}
