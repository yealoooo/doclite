package bundle

import (
	"bufio"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
)

// PNG：不隔行、没有 alpha 和 tRNS 的（灰度、RGB、调色板）原样嵌入：拼起来的各 IDAT 块就是一个 zlib 流，
// PDF 用 FlateDecode + PNG 预测器（Predictor 15）直接解；调色板 → Indexed。拷贝时核对每块的 CRC。
//
// 带 alpha（颜色类型 4、6）、有 tRNS、隔行的转换：用标准库 image/png 解码（它也核对 CRC 和 Adler-32），
// 颜色空间和位深保持原样（调色板仍是索引），透明度做成 SMask（类型 4、6 和颜色同位深，tRNS 8 位）。

const maxPNGChunks = 1 << 20

type pngInfo struct {
	width, height int
	depth         int
	colorType     byte
	interlaced    bool
	palette       []byte // PLTE 的内容
	trns          bool
	idat          []byteRange
}

func analyzePNG(ra io.ReaderAt, size int64, opts Options, inspect bool) (*pagePlan, error) {
	info, plan, err := readPNG(ra, size, inspect)
	if err != nil {
		return nil, err
	}
	reason := ""
	switch {
	case opts.forceConvert:
		reason = "测试要求转换"
	case info.interlaced:
		reason = "隔行"
	case info.colorType == 4 || info.colorType == 6:
		reason = "带透明度"
	case info.trns:
		reason = "有透明色（tRNS）"
	}
	if reason != "" {
		plan.convert = pngConversion(ra, size, info, reason)
		return plan, nil
	}
	img := imageObj{
		width:  info.width,
		height: info.height,
		bpc:    info.depth,
		filter: "FlateDecode",
		ranges: info.idat,
	}
	colors := 1
	switch info.colorType {
	case 0:
		img.cs = csGray
	case 2:
		img.cs, colors = csRGB, 3
	case 3:
		if len(info.palette) == 0 {
			return nil, corruptf("调色板 PNG 没有 PLTE 块")
		}
		img.cs = indexed(info.palette)
	}
	img.parms = predictorParms(colors, info.depth, info.width)
	plan.images = []imageObj{img}
	return plan, nil
}

// readPNG 读 IHDR，再逐块读到 IEND：PLTE、tRNS、pHYs、各 IDAT 的位置。inspect 时读到第一个 IDAT 就停，
// 只看文件最后 12 字节是不是 IEND。
func readPNG(ra io.ReaderAt, size int64, inspect bool) (*pngInfo, *pagePlan, error) {
	b := make([]byte, 8+8+13)
	if err := readFull(ra, b, 0); err != nil {
		return nil, nil, err
	}
	ihdr := b[16:]
	info := &pngInfo{
		width:      int(be32(ihdr[0:4])),
		height:     int(be32(ihdr[4:8])),
		depth:      int(ihdr[8]),
		colorType:  ihdr[9],
		interlaced: ihdr[12] == 1,
	}
	if ihdr[10] != 0 || ihdr[11] != 0 || ihdr[12] > 1 {
		return nil, nil, corruptf("PNG 的压缩、过滤或隔行方式不对")
	}
	plan := &pagePlan{width: info.width, height: info.height}
	pos := int64(8 + 8 + 13 + 4)
	head := make([]byte, 8)
	for range maxPNGChunks {
		if err := readFull(ra, head, pos); err != nil {
			return nil, nil, corruptf("PNG 没有 IEND，文件不完整")
		}
		n := int64(be32(head[0:4]))
		data := pos + 8
		if n > size-data-4 {
			return nil, nil, corruptf("PNG 的块超出了文件末尾，文件不完整")
		}
		switch string(head[4:8]) {
		case "PLTE":
			if n%3 != 0 || n > 768 {
				return nil, nil, corruptf("PNG 的 PLTE 块长度不对")
			}
			info.palette = make([]byte, n)
			if err := readFull(ra, info.palette, data); err != nil {
				return nil, nil, err
			}
		case "tRNS":
			info.trns = true
		case "pHYs":
			if n >= 9 {
				p := make([]byte, 9)
				if err := readFull(ra, p, data); err != nil {
					return nil, nil, err
				}
				if p[8] == 1 { // 单位是米
					plan.dpiX = float64(be32(p[0:4])) * 0.0254
					plan.dpiY = float64(be32(p[4:8])) * 0.0254
				}
			}
		case "IDAT":
			info.idat = append(info.idat, byteRange{off: data, n: n, pngCRC: true})
			if inspect {
				tail := make([]byte, 12)
				if err := readFull(ra, tail, size-12); err != nil {
					return nil, nil, err
				}
				if string(tail[4:8]) != "IEND" {
					return nil, nil, corruptf("PNG 末尾没有 IEND，文件不完整")
				}
				return info, plan, nil
			}
		case "IEND":
			if len(info.idat) == 0 {
				return nil, nil, corruptf("PNG 没有图像数据（IDAT）")
			}
			return info, plan, nil
		}
		pos = data + n + 4
	}
	return nil, nil, corruptf("PNG 的块太多")
}

// pngConversion：image/png 解码要的内存按解出来的图像类型算（隔行时它先解各遍再拼，按两倍算）。
func pngConversion(ra io.ReaderAt, size int64, info *pngInfo, reason string) *conversion {
	bpp := int64(4) // NRGBA、RGBA
	switch {
	case info.colorType == 3, info.colorType == 0 && info.depth <= 8 && !info.trns:
		bpp = 1 // Paletted、Gray
	case info.colorType == 0 && !info.trns:
		bpp = 2 // Gray16
	case info.depth == 16:
		bpp = 8 // NRGBA64、RGBA64
	}
	mem := int64(info.width) * int64(info.height) * bpp
	if info.interlaced {
		mem *= 2
	}
	return &conversion{
		reason: reason,
		memory: mem,
		decode: func(ctx context.Context) (*raster, error) {
			img, err := png.Decode(bufio.NewReaderSize(io.NewSectionReader(ra, 0, size), 64<<10))
			if err != nil {
				var ie *ioError
				var unsupported png.UnsupportedError
				switch {
				case errors.As(err, &ie):
					return nil, err
				case errors.As(err, &unsupported):
					return nil, unsupportedf("PNG 解不开：%v", err)
				}
				return nil, corruptf("PNG 解不开：%v", err)
			}
			return pngRaster(img, info)
		},
	}
}

// pngRaster 按原图的颜色类型和位深取样本：灰度（类型 0、4）→ DeviceGray，RGB（2、6）→ DeviceRGB，
// 调色板（3）→ Indexed。image/png 把不到 8 位的灰度放大到 8 位（×255、×85、×17），这里缩回原来的位深。
func pngRaster(img image.Image, info *pngInfo) (*raster, error) {
	w, h := info.width, info.height
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		return nil, corruptf("PNG 解出来的尺寸不对")
	}
	r := &raster{w: w, h: h, bpc: info.depth}
	gray := info.colorType == 0 || info.colorType == 4
	shift := uint(0) // 8 位样本缩回原位深
	if gray && info.depth < 8 {
		shift = uint(8 - info.depth)
	}
	switch {
	case info.colorType == 3:
		p, ok := img.(*image.Paletted)
		if !ok {
			return nil, corruptf("PNG 解出来的类型不对")
		}
		lookup := make([]byte, 3*len(p.Palette))
		alphas := make([]uint32, len(p.Palette))
		for i, c := range p.Palette {
			n := color.NRGBAModel.Convert(c).(color.NRGBA)
			lookup[3*i], lookup[3*i+1], lookup[3*i+2] = n.R, n.G, n.B
			alphas[i] = uint32(n.A)
		}
		r.cs = indexed(lookup)
		if info.trns {
			r.alphaBPC = 8
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
	case gray:
		r.cs = csGray
	default:
		r.cs = csRGB
	}
	ncomp := r.cs.comps()
	switch {
	case info.colorType == 4 || info.colorType == 6:
		r.alphaBPC = info.depth
	case info.trns:
		r.alphaBPC = 8
	}

	switch m := img.(type) {
	case *image.Gray:
		r.fill = func(y int, colorOut, _ []uint32) {
			row := m.Pix[y*m.Stride:]
			for x := range w {
				colorOut[x] = uint32(row[x]) >> shift
			}
		}
	case *image.Gray16:
		r.fill = func(y int, colorOut, _ []uint32) {
			row := m.Pix[y*m.Stride:]
			for x := range w {
				colorOut[x] = uint32(be16(row[2*x:]))
			}
		}
	case *image.RGBA, *image.NRGBA:
		var pix []byte
		var stride int
		if v, ok := m.(*image.RGBA); ok {
			pix, stride = v.Pix, v.Stride
		} else {
			v := m.(*image.NRGBA)
			pix, stride = v.Pix, v.Stride
		}
		r.fill = func(y int, colorOut, alphaOut []uint32) {
			row := pix[y*stride:]
			for x := range w {
				px := row[4*x : 4*x+4]
				if colorOut != nil {
					for c := range ncomp {
						colorOut[x*ncomp+c] = uint32(px[c]) >> shift
					}
				}
				if alphaOut != nil {
					alphaOut[x] = uint32(px[3])
				}
			}
		}
	case *image.RGBA64, *image.NRGBA64:
		var pix []byte
		var stride int
		if v, ok := m.(*image.RGBA64); ok {
			pix, stride = v.Pix, v.Stride
		} else {
			v := m.(*image.NRGBA64)
			pix, stride = v.Pix, v.Stride
		}
		alpha8 := r.alphaBPC == 8 // tRNS：透明度只有全透明和不透明，写 8 位
		r.fill = func(y int, colorOut, alphaOut []uint32) {
			row := pix[y*stride:]
			for x := range w {
				px := row[8*x : 8*x+8]
				if colorOut != nil {
					for c := range ncomp {
						colorOut[x*ncomp+c] = uint32(be16(px[2*c:]))
					}
				}
				if alphaOut != nil {
					a := uint32(be16(px[6:]))
					if alpha8 {
						a >>= 8
					}
					alphaOut[x] = a
				}
			}
		}
	default:
		return nil, corruptf("PNG 解出来的类型不对")
	}
	return r, nil
}
