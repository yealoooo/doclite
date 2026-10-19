package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"time"

	"golang.org/x/image/draw"

	"doclite/client/internal/imagehdr"
	"doclite/client/internal/imgdecode"
	"doclite/client/internal/tiles"
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
		// 编码器碰到意外时不应该把壳带崩（解码的 panic 由 imgdecode 兜住）。
		if r := recover(); r != nil {
			data, err = nil, fmt.Errorf("生成封面出错：%v", r)
		}
	}()
	src, err := imgdecode.Open(path, imagehdr.DefaultMaxPixels)
	if err != nil {
		return nil, err
	}
	sw, sh := src.Info.StoredSize()
	if int64(sw)*int64(sh)*4 > CoverMemoryBudget {
		return nil, fmt.Errorf("图片太大（%d×%d），超过生成封面的内存上限", sw, sh)
	}
	img, err := src.Decode(context.Background())
	if err != nil {
		return nil, err
	}
	return EncodeCover(img, src.Info.Orientation)
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
// 编成 JPEG。先按 2ⁿ 取平均缩到封面的 2–4 倍并转正（tiles.Shrink，和切块同一套读像素、转正的代码），再用 CatmullRom
// 缩到封面尺寸。
func EncodeCover(src image.Image, orientation int) ([]byte, error) {
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errors.New("图片是空的")
	}
	cw, ch := CoverSize(imgdecode.OrientedSize(orientation, b.Dx(), b.Dy()))
	small, err := tiles.Shrink(context.Background(), src, orientation, 2*cw, 2*ch)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.CatmullRom.Scale(out, out.Bounds(), small, small.Bounds(), draw.Src, nil)
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
