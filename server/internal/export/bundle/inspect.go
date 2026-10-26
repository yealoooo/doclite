package bundle

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Inspect 只看文件头检查一页（不读整张原图）：[Generate] 在某一页失败后，用它把剩下的页都查一遍，一次列出所有
// 有问题的页。和正式导出用同一套判断（同样的解析、原样嵌入 / 转换 / 不支持、同样的内存上限），只是不写 PDF、
// 按需用 PageSource.ReadAt 读几段（按 64 KiB 的块读、缓存）。没问题返回 nil，有问题返回和 Generate 一样的 *[Error]。
//
// 查不出来、正式导出时照样会报的：大小或 sha256 对不上；PNG 的块 CRC、zlib 数据坏了（只看最后 12 字节有没有
// IEND）；PackBits 条带不规整（正式导出时会转换）；转换时数据坏了。
func Inspect(ctx context.Context, src PageSource, format Format, item Item, page Page, opts Options) error {
	opts = opts.withDefaults()
	wrap := func(err error) error {
		return classify(ctx, err, KindCorrupt).atPage(-1, &item, -1, &page)
	}
	if formatOfMIME(page.MIME) == "" {
		return invalidInput("mime %q 不认识", page.MIME).atPage(-1, &item, -1, &page)
	}
	if page.Size <= 0 {
		return invalidInput("文件大小 %d 不对", page.Size).atPage(-1, &item, -1, &page)
	}
	ra := &rangeReader{ctx: ctx, src: src, page: page, blocks: map[int64][]byte{}}
	if format != FormatPDF {
		head := make([]byte, min(page.Size, 12))
		if err := readFull(ra, head, 0); err != nil {
			return wrap(err)
		}
		if _, err := checkMagic(head, page.MIME); err != nil {
			return wrap(err)
		}
		return nil
	}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = corruptf("处理这一页时出错：%v", r)
			}
		}()
		_, err = analyze(ra, &page, opts, true)
		return err
	}()
	if err != nil {
		return wrap(err)
	}
	return nil
}

// rangeReader 是按块读、缓存的 io.ReaderAt，数据来自 PageSource.ReadAt。读失败包成 KindSource。
type rangeReader struct {
	ctx    context.Context
	src    PageSource
	page   Page
	blocks map[int64][]byte
}

const (
	rangeBlock     = 64 << 10
	maxRangeBlocks = 64 // 缓存最多 4 MiB
)

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= r.page.Size {
			return n, io.EOF
		}
		b, err := r.block(pos / rangeBlock)
		if err != nil {
			return n, err
		}
		in := pos % rangeBlock
		if in >= int64(len(b)) {
			return n, io.EOF
		}
		n += copy(p[n:], b[in:])
	}
	return n, nil
}

func (r *rangeReader) block(i int64) ([]byte, error) {
	if b, ok := r.blocks[i]; ok {
		return b, nil
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	off := i * rangeBlock
	b := make([]byte, min(rangeBlock, r.page.Size-off))
	n, err := r.src.ReadAt(r.ctx, r.page, b, off)
	if n < len(b) {
		if err == nil || errors.Is(err, io.EOF) {
			err = fmt.Errorf("只读到第 %d 字节，快照记的是 %d 字节", off+int64(n), r.page.Size)
		}
		return nil, sourceErr("读原图失败", err)
	}
	if len(r.blocks) >= maxRangeBlocks {
		for k := range r.blocks {
			delete(r.blocks, k)
			break
		}
	}
	r.blocks[i] = b
	return b, nil
}
