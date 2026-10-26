package bundle

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"

	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"

	"doclite/server/internal/platform/imagehdr"
)

// BMP、GIF、WebP（M11-7，用户 2026-10-26）：PDF 都不能原样放，一律转换：整张解码（golang.org/x/image/bmp、标准库
// image/gif、golang.org/x/image/webp，都是纯 Go），按 Flate 写进 PDF。像素不变：调色板仍是索引（Indexed，位深按
// 调色板的项数取 1、2、4、8），24、32 位和 WebP 是 8 位 RGB（有损 WebP 解出来的 YCbCr 按标准公式转成 RGB）；
// 有透明度时和 PNG 一样另写一个 SMask（8 位）。
//
// 只看文件头就判为"不支持"的写法和上传时（platform/imagehdr 的 bmp.go、gif.go、webp.go）一致：BMP 按 x/image/bmp 的
// DecodeConfig；GIF 第一帧没有占满画布；WebP 的透明度标记和数据对不上。多帧 GIF、动画 WebP 在 imagehdr.Parse 就是
// ErrMultiPage。

func analyzeDecoded(ra io.ReaderAt, size int64, format imagehdr.Format, info imagehdr.Info) (*pagePlan, error) {
	w, h := info.StoredSize()
	plan := &pagePlan{width: w, height: h}
	px := int64(w) * int64(h)
	var memory int64
	var reason string
	switch format {
	case imagehdr.BMP:
		cfg, err := bmp.DecodeConfig(io.NewSectionReader(ra, 0, size))
		if err != nil {
			var ie *ioError
			switch {
			case errors.As(err, &ie):
				return nil, err
			case errors.Is(err, bmp.ErrUnsupported):
				return nil, unsupportedf("这种写法的 BMP 转换不了")
			}
			return nil, corruptf("BMP 文件头读不出来：%v", err)
		}
		// 横纵的像素/米在文件头第 38、42 字节。
		b := make([]byte, 8)
		if err := readFull(ra, b, 38); err == nil {
			le := binary.LittleEndian
			plan.dpiX = float64(int32(le.Uint32(b[0:4]))) * 0.0254
			plan.dpiY = float64(int32(le.Uint32(b[4:8]))) * 0.0254
		}
		memory, reason = px*4, "BMP"
		if _, ok := cfg.ColorModel.(color.Palette); ok {
			memory = px
		}
	case imagehdr.GIF:
		if err := gifFirstFrameCovers(ra, w, h); err != nil {
			return nil, err
		}
		memory, reason = px, "GIF"
	case imagehdr.WebP:
		lossless, err := webpCheck(ra, size)
		if err != nil {
			return nil, err
		}
		// 有损：YCbCr（4:2:0）+ 透明度；无损：解码器内部每像素 4 字节 + 解出来的 NRGBA。
		memory, reason = px*3, "WebP"
		if lossless {
			memory = px * 8
		}
	}
	plan.convert = &conversion{
		reason: reason,
		memory: memory,
		decode: func(ctx context.Context) (*raster, error) {
			r := bufio.NewReaderSize(io.NewSectionReader(ra, 0, size), 64<<10)
			var img image.Image
			var err error
			switch format {
			case imagehdr.BMP:
				img, err = bmp.Decode(r)
			case imagehdr.GIF:
				img, err = gif.Decode(r)
			case imagehdr.WebP:
				img, err = webp.Decode(r)
			}
			if err != nil {
				var ie *ioError
				if errors.As(err, &ie) {
					return nil, err
				}
				return nil, corruptf("%s 解不开：%v", reason, err)
			}
			return imageRaster(img, w, h, reason)
		},
	}
	return plan, nil
}

// gifFirstFrameCovers 找到第一帧的描述，核对它占满整个画布（Go 解出来的只是那一块）。
func gifFirstFrameCovers(ra io.ReaderAt, w, h int) error {
	le := binary.LittleEndian
	b := make([]byte, 13)
	if err := readFull(ra, b, 0); err != nil {
		return err
	}
	pos := int64(13)
	if b[10]&0x80 != 0 {
		pos += 3 << (b[10]&7 + 1)
	}
	one := make([]byte, 1)
	for range 4096 {
		if err := readFull(ra, one, pos); err != nil {
			return err
		}
		pos++
		switch one[0] {
		case 0x21: // 扩展块：标签，然后是数据子块（应用扩展、文字扩展的固定头也是按子块的格式写的）
			pos++
			for {
				if err := readFull(ra, one, pos); err != nil {
					return err
				}
				pos += 1 + int64(one[0])
				if one[0] == 0 {
					break
				}
			}
		case 0x2C:
			d := make([]byte, 8)
			if err := readFull(ra, d, pos); err != nil {
				return err
			}
			if le.Uint16(d[0:2]) != 0 || le.Uint16(d[2:4]) != 0 || int(le.Uint16(d[4:6])) != w || int(le.Uint16(d[6:8])) != h {
				return unsupportedf("GIF 的第一帧没有占满画布，转换不了")
			}
			return nil
		default:
			return corruptf("GIF 里认不出的块 0x%02X", one[0])
		}
	}
	return corruptf("GIF 第一帧之前的扩展块太多")
}

// webpCheck 看 WebP 是无损还是有损，并核对透明度的写法 x/image/webp 解得了：VP8X 标了透明时有损图像前要有 ALPH，
// 没标时不能有；ALPH 不能配无损图像，压缩方式只能是 0、1。
func webpCheck(ra io.ReaderAt, size int64) (lossless bool, err error) {
	le := binary.LittleEndian
	head := make([]byte, 8)
	pos := int64(12)
	var alphaFlag, alph bool
	for range 1024 {
		if err := readFull(ra, head, pos); err != nil {
			return false, err
		}
		n := int64(le.Uint32(head[4:8]))
		data := pos + 8
		pos = data + n + n&1
		switch string(head[0:4]) {
		case "VP8X":
			f := make([]byte, 1)
			if err := readFull(ra, f, data); err != nil {
				return false, err
			}
			alphaFlag = f[0]&0x10 != 0
		case "ALPH":
			m := make([]byte, 1)
			if err := readFull(ra, m, data); err != nil {
				return false, err
			}
			if !alphaFlag || m[0]&3 > 1 {
				return false, unsupportedf("WebP 的透明度写法转换不了")
			}
			alph = true
		case "VP8 ":
			if alphaFlag != alph {
				return false, unsupportedf("WebP 的透明度写法转换不了")
			}
			return false, nil
		case "VP8L":
			if alph {
				return false, unsupportedf("WebP 的透明度写法转换不了")
			}
			return true, nil
		}
		if pos >= size {
			break
		}
	}
	return false, corruptf("WebP 里没有图像数据")
}

// imageRaster 把解码器给的图按原样转成 raster：调色板 → Indexed，其余 → 8 位 RGB；有透明（不是全不透明）时带 8 位
// 的透明度。
func imageRaster(img image.Image, w, h int, name string) (*raster, error) {
	if b := img.Bounds(); b != image.Rect(0, 0, w, h) {
		return nil, corruptf("%s 解出来的尺寸不对", name)
	}
	opaque := true
	if o, ok := img.(interface{ Opaque() bool }); ok {
		opaque = o.Opaque()
	}
	r := &raster{w: w, h: h, bpc: 8}
	if !opaque {
		r.alphaBPC = 8
	}
	if p, ok := img.(*image.Paletted); ok {
		lookup := make([]byte, 3*len(p.Palette))
		alphas := make([]uint32, len(p.Palette))
		for i, c := range p.Palette {
			n := color.NRGBAModel.Convert(c).(color.NRGBA)
			lookup[3*i], lookup[3*i+1], lookup[3*i+2] = n.R, n.G, n.B
			alphas[i] = uint32(n.A)
		}
		r.cs = indexed(lookup)
		for r.bpc > 1 && len(p.Palette) <= 1<<(r.bpc/2) {
			r.bpc /= 2
		}
		r.fill = func(y int, colorOut, alphaOut []uint32) {
			row := p.Pix[y*p.Stride : y*p.Stride+w]
			for x, idx := range row {
				if colorOut != nil {
					colorOut[x] = uint32(idx)
				}
				if alphaOut != nil {
					alphaOut[x] = alphas[idx]
				}
			}
		}
		return r, nil
	}
	r.cs = csRGB
	switch m := img.(type) {
	case *image.RGBA, *image.NRGBA:
		// RGBA 是预乘过的（x/image/bmp 只给不透明的 24 位用），带透明时 SMask 加 /Matte。
		var pix []byte
		var stride int
		if v, ok := m.(*image.RGBA); ok {
			pix, stride, r.matte = v.Pix, v.Stride, true
		} else {
			v := m.(*image.NRGBA)
			pix, stride = v.Pix, v.Stride
		}
		r.fill = func(y int, colorOut, alphaOut []uint32) {
			row := pix[y*stride:]
			for x := range w {
				px := row[4*x : 4*x+4]
				if colorOut != nil {
					colorOut[3*x], colorOut[3*x+1], colorOut[3*x+2] = uint32(px[0]), uint32(px[1]), uint32(px[2])
				}
				if alphaOut != nil {
					alphaOut[x] = uint32(px[3])
				}
			}
		}
	case *image.YCbCr, *image.NYCbCrA:
		var yc *image.YCbCr
		var a []byte
		var aStride int
		if v, ok := m.(*image.NYCbCrA); ok {
			yc, a, aStride = &v.YCbCr, v.A, v.AStride
		} else {
			yc = m.(*image.YCbCr)
		}
		r.fill = func(y int, colorOut, alphaOut []uint32) {
			for x := range w {
				if colorOut != nil {
					yi, ci := yc.YOffset(x, y), yc.COffset(x, y)
					cr, cg, cb := color.YCbCrToRGB(yc.Y[yi], yc.Cb[ci], yc.Cr[ci])
					colorOut[3*x], colorOut[3*x+1], colorOut[3*x+2] = uint32(cr), uint32(cg), uint32(cb)
				}
				if alphaOut != nil {
					alphaOut[x] = uint32(a[y*aStride+x])
				}
			}
		}
	default:
		return nil, corruptf("%s 解出来的类型不对", name)
	}
	return r, nil
}
