package bundle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// 出错的分类：某一页的问题带档号、页号、file_id、snapshot_id，不重试；读源、写出失败可以重试；
// 出错、取消时临时目录都清空（generate 里查）。Inspect 只看文件头，能查出的和正式导出报一样的类别。
func TestErrors(t *testing.T) {
	flip := func(b []byte, i int) []byte {
		b = bytes.Clone(b)
		b[i] ^= 0xFF
		return b
	}
	gray := sample(t, "jpeg-gray.jpg")
	oldJPEGWithoutIF := buildTIFF(make([]byte, 108), []tiffEntryT{
		{256, 4, []uint32{64}}, {257, 4, []uint32{48}}, {258, 3, []uint32{8}}, {259, 3, []uint32{6}},
		{262, 3, []uint32{1}}, {273, 4, []uint32{8}}, {277, 3, []uint32{1}}, {278, 4, []uint32{48}}, {279, 4, []uint32{100}},
	})
	png1 := sample(t, "png-1bit.png")
	idat := bytes.Index(png1, []byte("IDAT"))

	cases := []struct {
		name     string
		declared []byte // 快照记的
		served   []byte // 数据源给的（nil 同 declared）
		mime     string
		format   Format
		opts     Options
		src      func(*memSource) PageSource
		w        io.Writer
		kind     Kind
		inspect  Kind // Inspect 应报的类别，空串表示查不出来（返回 nil）
	}{
		{name: "sha256 不对", declared: gray, served: flip(gray, 5000), mime: "image/jpeg", kind: KindIntegrity},
		{name: "比快照记的长", declared: gray, served: append(bytes.Clone(gray), 0), mime: "image/jpeg", kind: KindIntegrity},
		{name: "数据源给的短", declared: gray, served: gray[:len(gray)-100], mime: "image/jpeg", kind: KindSource},
		{name: "数据源打不开", declared: gray, mime: "image/jpeg", kind: KindSource, inspect: KindSource,
			src: func(m *memSource) PageSource { m.openErr = errors.New("连接被重置"); return m }},
		{name: "魔数和 mime 不一致", declared: png1, mime: "image/jpeg", kind: KindIntegrity, inspect: KindIntegrity},
		{name: "原图包也查魔数", declared: png1, mime: "image/tiff", format: FormatImages, kind: KindIntegrity, inspect: KindIntegrity},
		{name: "JPEG 压缩又分平面", declared: fixture(t, "tiff-jpeg-planar.tif"), mime: "image/tiff", kind: KindUnsupported, inspect: KindUnsupported},
		{name: "旧式 JPEG 没有完整的 JPEG", declared: oldJPEGWithoutIF, mime: "image/tiff", kind: KindUnsupported, inspect: KindUnsupported},
		{name: "多页 TIFF", declared: sample(t, "tiff-multipage-2p.tif"), mime: "image/tiff", kind: KindUnsupported, inspect: KindUnsupported},
		{name: "PNG 块的 CRC 不对", declared: flip(png1, idat+100), mime: "image/png", kind: KindCorrupt},
		{name: "截断（IFD 没了）", declared: sample(t, "tiff-lzw-rgb.tif")[:500000], mime: "image/tiff", kind: KindCorrupt, inspect: KindCorrupt},
		{name: "转换超过内存上限", declared: sample(t, "png-alpha.png"), mime: "image/png", opts: Options{ConvertMemoryLimit: 512 << 10},
			kind: KindTooLarge, inspect: KindTooLarge},
		{name: "写出失败", declared: gray, mime: "image/jpeg", w: &failWriter{after: 100000}, kind: KindOutput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.format == "" {
				c.format = FormatPDF
			}
			ms := newMemSource()
			ok := ms.addSample(t, 1, "tiff-g4-1bit.tif")
			bad := ms.add(2, c.declared, c.served, c.mime)
			var src PageSource = ms
			if c.src != nil {
				src = c.src(ms)
			}
			item := testItem("A-2025-0001", ok, bad)
			job := &Job{Format: c.format, Items: []Item{testItem("A-2025-0000", ms.addSample(t, 1, "jpeg-gray.jpg")), item}}
			if c.src != nil { // 打不开时第一件就失败
				job.Items = job.Items[1:]
				item.Pages = item.Pages[1:]
				job.Items[0] = item
			}
			opts := c.opts
			opts.TmpDir = t.TempDir()
			var err error
			if c.w != nil {
				_, err = Generate(context.Background(), job, src, c.w, opts, nil)
				assertEmptyDir(t, opts.TmpDir)
			} else {
				_, _, err = generate(t, job, src, opts)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("错误不是 *Error：%v", err)
			}
			if e.Kind != c.kind {
				t.Fatalf("类别是 %s，应该是 %s：%v", e.Kind, c.kind, err)
			}
			wantRetry := c.kind == KindSource || c.kind == KindOutput
			if e.Retryable() != wantRetry || isRetryable(err) != wantRetry {
				t.Fatalf("Retryable 是 %v", e.Retryable())
			}
			if c.kind != KindOutput {
				pageIdx := len(job.Items[len(job.Items)-1].Pages) - 1
				if e.ArchiveNo != "A-2025-0001" || e.PageNo != 2 || e.FileID != bad.FileID || e.SnapshotID != bad.SnapshotID ||
					e.ItemIndex != len(job.Items)-1 || e.PageIndex != pageIdx {
					t.Fatalf("没指出是哪一页：%+v", e)
				}
				t.Logf("%v", err)
			}

			ierr := Inspect(context.Background(), src, c.format, item, bad, c.opts)
			if kindOf(ierr) != c.inspect {
				t.Fatalf("Inspect 的类别是 %q，应该是 %q：%v", kindOf(ierr), c.inspect, ierr)
			}
			if ierr != nil {
				var ie *Error
				errors.As(ierr, &ie)
				if ie.PageNo != 2 || ie.SnapshotID != bad.SnapshotID {
					t.Fatalf("Inspect 没指出是哪一页：%+v", ie)
				}
			}
			if err := Inspect(context.Background(), ms, c.format, item, ok, c.opts); c.src == nil && err != nil {
				t.Fatalf("没问题的页 Inspect 报错：%v", err)
			}
		})
	}

	t.Run("取消", func(t *testing.T) {
		ms := newMemSource()
		ctx, cancel := context.WithCancel(context.Background())
		src := &cancelSource{memSource: ms, cancelAt: 2, cancel: cancel}
		job := &Job{Format: FormatPDF, Items: []Item{testItem("A-1", ms.addSample(t, 1, "jpeg-gray.jpg"), ms.addSample(t, 2, "jpeg-rgb.jpg"))}}
		tmp := t.TempDir()
		_, err := Generate(ctx, job, src, io.Discard, Options{TmpDir: tmp}, nil)
		assertEmptyDir(t, tmp)
		if kindOf(err) != KindCanceled || !errors.Is(err, context.Canceled) {
			t.Fatalf("应该是取消：%v", err)
		}
	})

	t.Run("输入不对", func(t *testing.T) {
		ms := newMemSource()
		p := ms.addSample(t, 1, "jpeg-gray.jpg")
		unknownMIME := p
		unknownMIME.MIME = "image/gif"
		for name, job := range map[string]*Job{
			"没有条目":     {Format: FormatPDF},
			"格式不认识":    {Format: "ofd", Items: []Item{testItem("A", p)}},
			"没有档号":     {Format: FormatPDF, Items: []Item{testItem(" ", p)}},
			"没有页":      {Format: FormatPDF, Items: []Item{testItem("A")}},
			"页号不递增":    {Format: FormatPDF, Items: []Item{testItem("A", p, p)}},
			"mime 不认识": {Format: FormatPDF, Items: []Item{testItem("A", unknownMIME)}},
			"著录项个数不对":  {Format: FormatPDF, FieldNames: []string{"年度"}, Items: []Item{testItem("A", p)}},
		} {
			_, _, err := generate(t, job, ms, Options{})
			if kindOf(err) != KindInvalidInput || isRetryable(err) {
				t.Errorf("%s：应该是 invalid_input，却是 %v", name, err)
			}
		}
	})
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if len(p) > f.after {
		n := f.after
		f.after = 0
		return n, errors.New("上传中断")
	}
	f.after -= len(p)
	return len(p), nil
}

// cancelSource 在打开第 cancelAt 页时取消 ctx（模拟执行中途取消）。
type cancelSource struct {
	*memSource
	cancelAt int
	cancel   func()
}

func (c *cancelSource) Open(ctx context.Context, p Page) (io.ReadCloser, error) {
	if p.PageNo == c.cancelAt {
		c.cancel()
	}
	return c.memSource.Open(ctx, p)
}

// kindOf 返回 err 的类别；不是 *Error 时返回空串。
func kindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// isRetryable：err 是可以重试的 *Error。
func isRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable()
}
