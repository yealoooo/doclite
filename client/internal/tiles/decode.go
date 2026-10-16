package tiles

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"

	"doclite/client/internal/imagehdr"
	"doclite/client/internal/imgdecode"
)

// 错误码（imagecache 把它们作为 422 的 code 交给页面，见 web/src/lib/viewer/load-original.ts 的 shellMessage）。
const (
	CodeTooLarge          = "too_large"          // 超出内存或输出的上限
	CodeTimeout           = "timeout"            // 切得太久
	CodeUnsupportedFormat = "unsupported_format" // 不是 JPEG、PNG、经典 TIFF
	CodeUnsupported       = "unsupported"        // 格式认得，但这种写法解不了（例如多页 TIFF、12 位的 JPEG）
	CodeCorrupt           = "corrupt"            // 文件坏了
	CodeIO                = "io"                 // 读写本机文件出错（不是文件本身的问题）
)

// Error 是切块失败。Code 见上；除了 io，其余都是这个文件本身的问题，再试也一样。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Permanent：文件本身的问题（再试也一样），调用方可以记住、不再反复尝试。
func (e *Error) Permanent() bool { return e.Code != CodeIO }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// decodeError 把 imgdecode 的错误换成切块的错误码（取消就是超时：切块的 ctx 只有超时）。
func decodeError(err error) *Error {
	var de *imgdecode.Error
	if !errors.As(err, &de) {
		return fail(CodeIO, "%v", err)
	}
	switch de.Kind {
	case imgdecode.KindCanceled:
		return timeoutError()
	case imgdecode.KindUnsupportedFormat:
		return fail(CodeUnsupportedFormat, "%s", de.Msg)
	case imgdecode.KindUnsupported:
		return fail(CodeUnsupported, "%s", de.Msg)
	case imgdecode.KindTooLarge:
		return fail(CodeTooLarge, "%s", de.Msg)
	case imgdecode.KindIO:
		return fail(CodeIO, "%s", de.Msg)
	}
	return fail(CodeCorrupt, "%s", de.Msg)
}

// Mode 是块的颜色：gray 灰度（JPEG）、rgb 彩色（JPEG）、bilevel 1 位黑白（PNG）。
type Mode string

const (
	ModeGray    Mode = "gray"
	ModeRGB     Mode = "rgb"
	ModeBilevel Mode = "bilevel"
)

// Plan 是读了文件头之后的切块计划：转正后的宽高、块的颜色和格式、整张解码大约要的内存。
type Plan struct {
	src  *imgdecode.Source
	info imagehdr.Info
	// pngDepth、pngColorType：PNG 的 IHDR（估算内存用）。
	pngDepth, pngColorType int

	// Width、Height 是转正后的宽高（最高一级的尺寸）。
	Width, Height int
	Mode          Mode
	// Format 是块的扩展名：jpg 或 png。
	Format string
	// Estimate 是整张解码、转正、建金字塔时内存的峰值估算（字节）。
	Estimate int64
}

// NewPlan 读文件头（internal/imagehdr，和 server 的那份同步的规则），算出切块计划。
func NewPlan(path string) (*Plan, error) {
	src, err := imgdecode.Open(path, 0)
	if err != nil {
		return nil, decodeError(err)
	}
	info := src.Info
	p := &Plan{src: src, info: info, Width: info.Width, Height: info.Height}
	switch info.ColorMode {
	case imagehdr.ColorBW:
		p.Mode, p.Format = ModeBilevel, "png"
	case imagehdr.ColorGray:
		p.Mode, p.Format = ModeGray, "jpg"
	default:
		p.Mode, p.Format = ModeRGB, "jpg"
	}
	if info.Format == imagehdr.PNG {
		if f, err := os.Open(path); err == nil {
			var ihdr [26]byte
			if _, err := f.ReadAt(ihdr[:], 0); err == nil {
				p.pngDepth, p.pngColorType = int(ihdr[24]), int(ihdr[25])
			}
			f.Close()
		}
	}
	p.Estimate = p.estimate()
	return p, nil
}

// channels 是金字塔每像素的字节数。
func (p *Plan) channels() int {
	if p.Mode == ModeRGB {
		return 3
	}
	return 1
}

// estimate 是进程内存峰值的估算：同时存活的数据 ×2 + 余量。同时存活的是解码出来的图 + max（TIFF 读一个条带的
// 缓冲，转正后的那一份）+ 下一级；×2 是 Go 的垃圾回收留的余量（堆涨到存活数据的两倍才回收）和解码器读条带时
// 缓冲逐步变大留下的旧缓冲。实测 200 MB 的无压缩 RGB TIFF（7016×9921）进程峰值约 950 MB，估算约 990 MB。
// 解码出来的每像素字节数按 Go 的解码器：JPEG 灰度 1、彩色 YCbCr 最多 3；PNG 灰度 1（16 位 2）、调色板 1、
// 其余 RGBA 4（16 位 8）；TIFF 黑白和灰度 1、彩色 RGBA 4（JPEG 压缩的 TIFF 也是：整张 RGBA 或 CMYK，加上一个条带的
// 解码结果——最多是整张 YCbCr 每像素 3 字节，记在 raw 里；只有一个条带的 YCbCr、灰度直接用解码结果，更省）。
func (p *Plan) estimate() int64 {
	px := int64(p.info.Width) * int64(p.info.Height)
	var decoded, raw int64
	switch p.info.Format {
	case imagehdr.JPEG:
		decoded = px * 3
		if p.info.ColorMode == imagehdr.ColorGray {
			decoded = px
		}
	case imagehdr.PNG:
		bytes := int64(max(1, p.pngDepth/8))
		switch p.pngColorType {
		case 0:
			decoded = px * bytes
		case 3:
			decoded = px
		default:
			decoded = px * 4 * bytes
		}
	case imagehdr.TIFF:
		switch p.info.ColorMode {
		case imagehdr.ColorBW:
			decoded, raw = px, px/8
		case imagehdr.ColorGray:
			decoded, raw = px, px
		default:
			decoded, raw = px*4, px*3
		}
	}
	level0 := px * int64(p.channels())
	return 2*(decoded+max(raw, level0)+level0/4) + 16<<20
}

// decode 整张解码（imgdecode，文件里存的方向）。
func (p *Plan) decode(ctx context.Context) (image.Image, error) {
	img, err := p.src.Decode(ctx)
	if err != nil {
		return nil, decodeError(err)
	}
	return img, nil
}

func timeoutError() *Error { return fail(CodeTimeout, "切块超时") }

// toLevel 把解码出来的图转成金字塔的最高一级：按方向转正，合成到白底，灰度和黑白 1 个通道、彩色 3 个通道。
// 方向是 1、本来就是紧凑的灰度图时直接用它的像素，不再复制一份。
func toLevel(ctx context.Context, img image.Image, ch, orientation int) (*level, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := imgdecode.OrientedSize(orientation, w, h)
	if orientation < 1 || orientation > 8 {
		orientation = 1
	}
	if g, ok := img.(*image.Gray); ok && ch == 1 && orientation == 1 && g.Stride == w && len(g.Pix) >= w*h {
		return &level{w: w, h: h, ch: 1, pix: g.Pix[:w*h]}, nil
	}
	out := make([]byte, ow*oh*ch)
	row := make([]byte, w*ch)
	read := rowReader(img, ch)
	for y := 0; y < h; y++ {
		if y%256 == 0 && ctx.Err() != nil {
			return nil, timeoutError()
		}
		read(y, row)
		if orientation == 1 {
			copy(out[y*w*ch:], row)
			continue
		}
		p, step := imgdecode.RowPlacement(orientation, w, h, ow, y)
		for x := 0; x < w; x++ {
			copy(out[p*ch:p*ch+ch], row[x*ch:x*ch+ch])
			p += step
		}
	}
	return &level{w: ow, h: oh, ch: ch, pix: out}, nil
}

// luma 是 8 位 RGB 的亮度（和 Go 的 color.GrayModel 同一个公式）。
func luma(r, g, b uint32) byte {
	return byte((19595*r + 38470*g + 7471*b + 1<<15) >> 16)
}

// rowReader 返回一个按行读像素的函数：把存的第 y 行（从 0 起）写成 ch 个通道（1 灰度、3 RGB），透明合成到白底。
// 常见的几种图有快速路径，其余走 At()。
func rowReader(img image.Image, ch int) func(y int, dst []byte) {
	b := img.Bounds()
	w := b.Dx()
	put := func(dst []byte, x int, r, g, bl uint32) {
		if ch == 3 {
			dst[x*3], dst[x*3+1], dst[x*3+2] = byte(r), byte(g), byte(bl)
		} else {
			dst[x] = luma(r, g, bl)
		}
	}
	switch m := img.(type) {
	case *image.Gray:
		return func(y int, dst []byte) {
			off := m.PixOffset(b.Min.X, b.Min.Y+y)
			src := m.Pix[off : off+w]
			if ch == 1 {
				copy(dst, src)
				return
			}
			for x, v := range src {
				dst[x*3], dst[x*3+1], dst[x*3+2] = v, v, v
			}
		}
	case *image.Gray16:
		return func(y int, dst []byte) {
			src := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				v := src[x*2]
				if ch == 1 {
					dst[x] = v
				} else {
					dst[x*3], dst[x*3+1], dst[x*3+2] = v, v, v
				}
			}
		}
	case *image.YCbCr:
		return func(y int, dst []byte) {
			for x := 0; x < w; x++ {
				yi := m.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := m.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
				put(dst, x, uint32(r), uint32(g), uint32(bl))
			}
		}
	case *image.RGBA:
		// 预乘过 alpha：c + (255 − a)。
		return func(y int, dst []byte) {
			src := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				s := src[x*4 : x*4+4]
				bg := 255 - uint32(s[3])
				put(dst, x, min(255, uint32(s[0])+bg), min(255, uint32(s[1])+bg), min(255, uint32(s[2])+bg))
			}
		}
	case *image.NRGBA:
		// 没有预乘：c × a / 255 + (255 − a)，四舍五入。
		return func(y int, dst []byte) {
			src := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				s := src[x*4 : x*4+4]
				a := uint32(s[3])
				if a == 255 {
					put(dst, x, uint32(s[0]), uint32(s[1]), uint32(s[2]))
					continue
				}
				bg := 255 * (255 - a)
				put(dst, x, (uint32(s[0])*a+bg+127)/255, (uint32(s[1])*a+bg+127)/255, (uint32(s[2])*a+bg+127)/255)
			}
		}
	case *image.Paletted:
		var lut [256][3]uint32
		for i, c := range m.Palette {
			if i >= len(lut) {
				break
			}
			lut[i] = onWhite(c)
		}
		return func(y int, dst []byte) {
			off := m.PixOffset(b.Min.X, b.Min.Y+y)
			src := m.Pix[off : off+w]
			for x, idx := range src {
				c := lut[idx]
				put(dst, x, c[0], c[1], c[2])
			}
		}
	case *image.CMYK:
		return func(y int, dst []byte) {
			src := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				s := src[x*4 : x*4+4]
				r, g, bl := color.CMYKToRGB(s[0], s[1], s[2], s[3])
				put(dst, x, uint32(r), uint32(g), uint32(bl))
			}
		}
	}
	return func(y int, dst []byte) {
		for x := 0; x < w; x++ {
			c := onWhite(img.At(b.Min.X+x, b.Min.Y+y))
			put(dst, x, c[0], c[1], c[2])
		}
	}
}

// onWhite 把任意颜色合成到白底，返回 8 位 RGB。
func onWhite(c color.Color) [3]uint32 {
	if c == nil {
		return [3]uint32{}
	}
	r, g, b, a := c.RGBA() // 16 位，预乘过 alpha
	bg := 0xffff - a
	return [3]uint32{min(255, (r+bg)>>8), min(255, (g+bg)>>8), min(255, (b+bg)>>8)}
}
