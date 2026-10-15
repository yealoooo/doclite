package upload

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/tiff"

	"doclite/shared/imagehdr"
)

// 封面的规格（requirements 5.6"封面"，用户决定 2026-09-28；server 的校验在 server/internal/file/cover.go）：
// JPEG，长边 400（原图更小时不放大），质量约 75，按转正后的方向生成、不写 EXIF 方向，透明部分铺白，≤ 256 KiB。
const (
	CoverLongEdge = 400
	CoverQuality  = 75
	MaxCoverBytes = 256 << 10
	// CoverMemoryBudget：解码原图最多用这么多内存（按每像素 4 字节估），超过就不生成封面（超大图不撑爆壳）。
	CoverMemoryBudget = 512 << 20
)

// 封面的情况（队列项的 cover_status、重扫结果的 cover.status）。
const (
	CoverIncluded     = "included"
	CoverNotGenerated = "not_generated"
	CoverRejected     = "rejected"
)

// Cover 是生成封面的结果。Data 为 nil 表示没生成，Reason 是原因（给人看）。
type Cover struct {
	Data   []byte
	Reason string
	MS     int64
}

// coverSlot：同时只生成一张封面（解码大图的内存峰值只有一份）。
var coverSlot = make(chan struct{}, 1)

// MakeCover 给本机图片生成封面。生成不了（格式解不了、超过内存上限、文件读不了）时 Data 为 nil，不报错：
// 封面可以缺，不能因封面丢页。
func MakeCover(path string) Cover {
	coverSlot <- struct{}{}
	defer func() { <-coverSlot }()
	started := time.Now()
	data, err := makeCover(path)
	ms := time.Since(started).Milliseconds()
	if err != nil {
		return Cover{Reason: err.Error(), MS: ms}
	}
	return Cover{Data: data, MS: ms}
}

func makeCover(path string) (data []byte, err error) {
	defer func() {
		// 解码器碰到畸形文件时不应该把壳带崩。
		if r := recover(); r != nil {
			data, err = nil, fmt.Errorf("解码出错：%v", r)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读不了文件：%w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("读不了文件：%w", err)
	}
	info, err := imagehdr.Parse(f, st.Size(), imagehdr.DefaultMaxPixels)
	if err != nil {
		return nil, fmt.Errorf("文件头读不出来：%w", err)
	}
	sw, sh := info.StoredSize()
	if int64(sw)*int64(sh)*4 > CoverMemoryBudget {
		return nil, fmt.Errorf("图片太大（%d×%d），超过生成封面的内存上限", sw, sh)
	}
	src, err := decode(f, info.Format)
	if err != nil {
		return nil, err
	}
	if b := src.Bounds(); b.Dx() != sw || b.Dy() != sh {
		return nil, fmt.Errorf("解出的尺寸 %d×%d 和文件头的 %d×%d 不一致", b.Dx(), b.Dy(), sw, sh)
	}
	return EncodeCover(src, info.Orientation)
}

func decode(f *os.File, format imagehdr.Format) (image.Image, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var img image.Image
	var err error
	switch format {
	case imagehdr.JPEG:
		img, err = jpeg.Decode(bufio.NewReaderSize(f, 64<<10))
	case imagehdr.PNG:
		img, err = png.Decode(bufio.NewReaderSize(f, 64<<10))
	case imagehdr.TIFF:
		// *os.File 是 io.ReaderAt，tiff 按偏移读，不把整个文件读进内存。
		img, err = tiff.Decode(f)
	default:
		return nil, fmt.Errorf("不支持的格式 %s", format)
	}
	if err != nil {
		var ue tiff.UnsupportedError
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("这种 TIFF 解不了（%s）", string(ue))
		}
		return nil, fmt.Errorf("解码失败：%w", err)
	}
	return img, nil
}

// CoverSize 是转正后 w×h 的原图对应的封面尺寸：长边 min(400, 原图长边)，短边按比例四舍五入（至少 1）。
// 和 server 的校验一致（长宽比：短边和 round(原图短边 × 封面长边 ÷ 原图长边) 相差不超过 1）。
func CoverSize(w, h int) (int, int) {
	long, short := max(w, h), min(w, h)
	cl := min(CoverLongEdge, long)
	cs := max(1, int((int64(short)*int64(cl)*2+int64(long))/(2*int64(long))))
	if w >= h {
		return cl, cs
	}
	return cs, cl
}

// EncodeCover 把解码好的原图（文件里存的方向）缩小、按 orientation（EXIF / TIFF 的方向 1–8）转正、透明铺白，
// 编成 JPEG。先缩小再转正（转的是 400 像素的小图）。
func EncodeCover(src image.Image, orientation int) ([]byte, error) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil, errors.New("图片是空的")
	}
	// 转正后的尺寸和封面尺寸；方向 5–8 时宽高交换。
	ow, oh := sw, sh
	if orientation >= 5 && orientation <= 8 {
		ow, oh = sh, sw
	}
	cw, ch := CoverSize(ow, oh)
	tw, th := cw, ch
	if orientation >= 5 && orientation <= 8 {
		tw, th = ch, cw
	}
	small := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	op := draw.Over
	if o, ok := src.(interface{ Opaque() bool }); ok && o.Opaque() {
		op = draw.Src
	}
	draw.CatmullRom.Scale(small, small.Bounds(), src, b, op, nil)
	out := orient(small, orientation)
	for _, q := range []int{CoverQuality, 60, 45} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: q}); err != nil {
			return nil, fmt.Errorf("编码 JPEG 失败：%w", err)
		}
		if buf.Len() <= MaxCoverBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, errors.New("封面超过 256 KiB")
}

// orient 按 EXIF 方向把文件里存的图转正（1 不动；2–4 镜像、旋转 180°；5–8 宽高交换）。
func orient(src *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 2: // 水平镜像
				sx, sy = w-1-x, y
			case 3: // 旋转 180°
				sx, sy = w-1-x, h-1-y
			case 4: // 垂直镜像
				sx, sy = x, h-1-y
			case 5: // 转置
				sx, sy = y, x
			case 6: // 顺时针 90°
				sx, sy = y, h-1-x
			case 7: // 反转置
				sx, sy = w-1-y, h-1-x
			case 8: // 逆时针 90°
				sx, sy = w-1-y, x
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
