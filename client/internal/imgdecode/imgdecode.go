// Package imgdecode 把本机的一张图片（JPEG、PNG、经典 TIFF）整张解码成 image.Image，封面（internal/upload）和
// 切块（internal/tiles）共用这一份：
//
//   - [Open] 读文件头（shared/imagehdr，和 server 同一份规则：格式、宽高、方向；多页 TIFF 拒绝）；
//   - [Source.Decode] 整张解码：JPEG 用 image/jpeg，PNG 用 image/png；TIFF 先看压缩方式，JPEG 压缩的（7，以及能
//     定位到完整 JPEG 的旧式 6）由这个包按条带或分块交给 image/jpeg 再拼成整张（tiffjpeg.go），其余交给
//     golang.org/x/image/tiff；
//   - 解出来的是文件里存的方向（没转正），宽高和文件头一致；方向的换算见 orient.go。
//
// 不缩放、不合成背景（调用方的事）；整张解码要多少内存由调用方按文件头估算、决定解不解。
package imgdecode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"

	"golang.org/x/image/tiff"

	"doclite/shared/imagehdr"
)

// Kind 是解不了的原因（tiles 原样当作 422 的 code 交给页面，见 web/src/lib/viewer/load-original.ts）。
type Kind string

const (
	KindUnsupportedFormat Kind = "unsupported_format" // 不是 JPEG、PNG、经典 TIFF
	KindUnsupported       Kind = "unsupported"        // 格式认得，但这种写法解不了（多页 TIFF、12 位 JPEG……）
	KindCorrupt           Kind = "corrupt"            // 文件坏了
	KindTooLarge          Kind = "too_large"          // 尺寸超过上限
	KindIO                Kind = "io"                 // 读本机文件出错（不是文件本身的问题）
	KindCanceled          Kind = "canceled"           // ctx 到期或取消
)

// Error 是 Open、Decode 返回的错误。Error() 是给人看的中文说明。
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errorf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Source 是读过文件头的一张本机图片。
type Source struct {
	Path string
	Size int64
	// Info 是文件头（宽高是转正后的；Info.StoredSize() 是文件里存的）。
	Info imagehdr.Info
}

// Open 读文件头。maxPixels 是像素总数上限（同 imagehdr.Parse，<= 0 不限）。
func Open(path string, maxPixels int64) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errorf(KindIO, "打不开文件：%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, errorf(KindIO, "读不了文件：%v", err)
	}
	info, err := imagehdr.Parse(f, st.Size(), maxPixels)
	if err != nil {
		var he *imagehdr.Error
		if !errors.As(err, &he) {
			return nil, errorf(KindIO, "读文件出错：%v", err)
		}
		kind := KindCorrupt
		switch {
		case errors.Is(err, imagehdr.ErrUnsupported):
			kind = KindUnsupportedFormat
		case errors.Is(err, imagehdr.ErrMultiPage):
			kind = KindUnsupported
		case errors.Is(err, imagehdr.ErrTooLarge):
			kind = KindTooLarge
		}
		return nil, &Error{Kind: kind, Msg: he.Msg}
	}
	return &Source{Path: path, Size: st.Size(), Info: info}, nil
}

// Decode 整张解码，返回文件里存的方向的图（宽高 = Info.StoredSize()）。ctx 到期时尽快停下（读文件、解每个条带
// 之前都看一眼），返回 KindCanceled。解码器碰到畸形文件 panic 时返回 KindCorrupt，不把壳带崩。
func (s *Source) Decode(ctx context.Context) (img image.Image, err error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, errorf(KindIO, "打不开文件：%v", err)
	}
	defer f.Close()
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, errorf(KindCorrupt, "解码出错：%v", r)
		}
	}()
	r := &ctxFile{ctx: ctx, f: f}
	switch s.Info.Format {
	case imagehdr.JPEG:
		img, err = jpeg.Decode(bufio.NewReaderSize(r, 256<<10))
	case imagehdr.PNG:
		img, err = png.Decode(bufio.NewReaderSize(r, 256<<10))
	case imagehdr.TIFF:
		img, err = decodeTIFF(ctx, r, s.Size)
	default:
		return nil, errorf(KindUnsupportedFormat, "不支持的格式 %s", s.Info.Format)
	}
	if err != nil {
		return nil, classify(ctx, err)
	}
	sw, sh := s.Info.StoredSize()
	if b := img.Bounds(); b.Dx() != sw || b.Dy() != sh {
		return nil, errorf(KindCorrupt, "解出来是 %d×%d，文件头写的是 %d×%d", b.Dx(), b.Dy(), sw, sh)
	}
	return img, nil
}

// classify 把解码器的错误归类。
func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errorf(KindCanceled, "解码超时或被取消")
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var ju jpeg.UnsupportedError
	var pu png.UnsupportedError
	var tu tiff.UnsupportedError
	if errors.As(err, &ju) || errors.As(err, &pu) || errors.As(err, &tu) {
		return errorf(KindUnsupported, "解不了这种写法：%v", err)
	}
	return errorf(KindCorrupt, "解码失败：%v", err)
}

// ctxFile：读文件之前先看 ctx（到期后解码器在下一次读时就出错停下）。同时是 io.Reader 和 io.ReaderAt
// （x/image/tiff 见到 ReaderAt 就按偏移读，不把整个文件读进内存）。
type ctxFile struct {
	ctx context.Context
	f   *os.File
	off int64
}

func (c *ctxFile) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.f.ReadAt(b, c.off)
	c.off += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (c *ctxFile) ReadAt(b []byte, off int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.f.ReadAt(b, off)
}
