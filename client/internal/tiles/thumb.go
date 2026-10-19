package tiles

import (
	"context"
	"image"

	"doclite/client/internal/imgdecode"
)

// Shrink 把解码出来的图（文件里存的方向）缩小到转正后宽高仍不小于 minW×minH 的最小的 1/2ⁿ，再按方向转正、合成到
// 白底（封面用，internal/upload：先这样缩到目标的 2–4 倍，再做一次插值缩放）。缩小只读一遍原图、按 2ⁿ×2ⁿ 的方块
// 取平均（和逐级减半一样的思路，边上不满一块的按实际有的像素平均），不另存一份原尺寸的像素。灰度图返回 *image.Gray，
// 其余 *image.RGBA。
func Shrink(ctx context.Context, img image.Image, orientation, minW, minH int) (image.Image, error) {
	ch := 3
	switch img.(type) {
	case *image.Gray, *image.Gray16:
		ch = 1
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := imgdecode.OrientedSize(orientation, w, h)
	shift := 0
	for ow>>(shift+1) >= minW && oh>>(shift+1) >= minH {
		shift++
	}
	f := 1 << shift
	sw, sh := (w+f-1)/f, (h+f-1)/f
	small := make([]byte, sw*sh*ch)
	read := rowReader(img, ch)
	row := make([]byte, w*ch)
	acc := make([]uint32, sw*ch)
	for oy := 0; oy < sh; oy++ {
		if ctx.Err() != nil {
			return nil, timeoutError()
		}
		clear(acc)
		y0, y1 := oy*f, min(h, oy*f+f)
		for y := y0; y < y1; y++ {
			read(y, row)
			for x := 0; x < w; x++ {
				o := (x >> shift) * ch
				for c := 0; c < ch; c++ {
					acc[o+c] += uint32(row[x*ch+c])
				}
			}
		}
		for ox := 0; ox < sw; ox++ {
			n := uint32((y1 - y0) * (min(w, ox*f+f) - ox*f))
			for c := 0; c < ch; c++ {
				small[(oy*sw+ox)*ch+c] = byte((acc[ox*ch+c] + n/2) / n)
			}
		}
	}
	lv, err := placeRows(ctx, sw, sh, ch, orientation, func(y int, dst []byte) {
		copy(dst, small[y*sw*ch:(y+1)*sw*ch])
	})
	if err != nil {
		return nil, err
	}
	return lv.image(), nil
}

// image 把一级换成 image.Image：1 个通道是 *image.Gray（共用像素），3 个通道是 *image.RGBA。
func (l *level) image() image.Image {
	r := image.Rect(0, 0, l.w, l.h)
	if l.ch == 1 {
		return &image.Gray{Pix: l.pix, Stride: l.w, Rect: r}
	}
	out := image.NewRGBA(r)
	for i, j := 0, 0; i < len(l.pix); i, j = i+3, j+4 {
		out.Pix[j], out.Pix[j+1], out.Pix[j+2], out.Pix[j+3] = l.pix[i], l.pix[i+1], l.pix[i+2], 255
	}
	return out
}
