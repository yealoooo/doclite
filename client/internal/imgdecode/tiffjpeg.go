package imgdecode

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
)

// JPEG 压缩的 TIFF（TIFF 技术说明 2）。认的范围和 server 导出（server/internal/export/bundle/tiffjpeg.go）一致，
// 另外支持分平面的 RGB、CMYK：
//
//   - 新式（Compression=7）：每个条带、分块是一段 JPEG。有 JPEGTables 时把表拼进去：SOI + 表（去掉表的 SOI、EOI）
//     + 条带（去掉条带的 SOI），末尾补一个 EOI（条带本来就有 EOI 时，多出来的那个不会被读到）。逐个交给 image/jpeg，
//     贴到整张图的位置上，贴完就丢掉——同时只有一个条带的解码结果；条带之间、解码时每读 4 KB 看一次 ctx。
//   - 旧式（Compression=6）：只认 JPEGInterchangeFormat 指向一段完整 JPEG（有 SOF、SOS）的写法，整段当一个块；
//     定位不到完整 JPEG 的不支持。
//   - 颜色按 TIFF 的 Photometric 算，不按 image/jpeg 的猜测（JPEG 里没有 JFIF、Adobe 标记时它按分量 ID 猜）：
//     从解出的图里取回各分量的原值再换算。YCbCr（6）转成 RGB；RGB（2）原样——libtiff 写的 RGB 没有 Adobe 标记，
//     分量 ID 也不一定是 'R','G','B'，这时 image/jpeg 当成 YCbCr、给出 Y、Cb、Cr 三个平面，其实就是 R、G、B；
//     灰度（1；0 是反相的）；CMYK（5）：没有 Adobe 标记时 image/jpeg 不解 4 个分量的 JPEG，补一个"未变换"的
//     Adobe 标记让它解，它会按 Adobe 的习惯把各值反相，再反相回来（TIFF 的 CMYK 0 是没有墨）；本来就带 Adobe
//     标记的按 image/jpeg 的结果。
//   - 分平面（PlanarConfiguration=2）：每个平面的条带是单分量的 JPEG，填进对应的通道；YCbCr 分平面的不支持。
//   - 块解出来比应有的大（最后一个条带按 RowsPerStrip 写满、边缘的分块补齐）就裁掉；比应有的小算文件坏了；
//     比标称的块大出一个 MCU（16）以上也算坏了（防止几 KB 的块声明一个巨大的尺寸，见 decodeBlock）。
//   - 只有一个块、解出来的类型能直接用（灰度、YCbCr、RGB、CMYK）时不再复制，省一份整张图的内存。
//
// 不支持：每个样本不是 8 位、调色板、带透明度或额外通道、其他颜色空间；JPEG 本身是无损、算术编码、12 位的由
// image/jpeg 报不支持。

var (
	soi = []byte{0xFF, 0xD8}
	eoi = []byte{0xFF, 0xD9}
	// adobeSegment 是补的 APP14：Adobe、版本 100、两个 flags、transform 0（未变换）。
	adobeSegment = []byte{0xFF, 0xEE, 0x00, 0x0E, 'A', 'd', 'o', 'b', 'e', 0x00, 0x64, 0, 0, 0, 0, 0}
)

type jpegLayout struct {
	size           int64
	width, height  int
	photometric    int
	colors, planes int
	old, tiled     bool
	blockW, blockH int // 条带：宽 = 图宽，高 = RowsPerStrip；分块：分块的宽高；旧式：整张
	across, down   int // 每个平面横向、纵向的块数
	offsets        []uint32
	counts         []uint32
	tables         []byte // JPEGTables 的主体（去掉了 SOI、EOI）
}

func (d *ifd) jpegLayout(comp int) (*jpegLayout, error) {
	var err error
	get := func(tag uint16, def uint32) int {
		if err != nil {
			return 0
		}
		var v uint32
		v, err = d.uint(tag, def)
		return int(min(v, 1<<30))
	}
	l := &jpegLayout{size: d.size}
	l.width, l.height = get(tagImageWidth, 0), get(tagImageLength, 0)
	spp := get(tagSamplesPerPixel, 1)
	defPhoto := uint32(1)
	if spp >= 3 {
		defPhoto = 2
	}
	l.photometric = get(tagPhotometric, defPhoto)
	planar := get(tagPlanarConfig, 1)
	inkSet := get(tagInkSet, 1)
	if err != nil {
		return nil, err
	}
	if l.width == 0 || l.height == 0 {
		return nil, corruptf("TIFF 的宽高是 0")
	}
	bits, ok, err := d.uints(tagBitsPerSample, 64)
	if err != nil {
		return nil, err
	}
	if !ok {
		bits = []uint32{1}
	}
	for _, b := range bits {
		if b != 8 {
			return nil, unsupportedf("不支持每个样本 %d 位的 JPEG 压缩 TIFF", b)
		}
	}
	switch l.photometric {
	case 0, 1:
		l.colors = 1
	case 2, 6:
		l.colors = 3
	case 5:
		if inkSet != 1 {
			return nil, unsupportedf("不支持 CMYK 以外的分色 JPEG 压缩 TIFF")
		}
		l.colors = 4
	case 3:
		return nil, unsupportedf("不支持 JPEG 压缩的调色板 TIFF")
	default:
		return nil, unsupportedf("不支持这种颜色的 JPEG 压缩 TIFF（Photometric=%d）", l.photometric)
	}
	if spp != l.colors {
		return nil, unsupportedf("不支持带透明度或额外通道的 JPEG 压缩 TIFF（每像素 %d 个样本）", spp)
	}
	l.planes = 1
	if planar == 2 && l.colors > 1 {
		if l.photometric == 6 || comp == compOJPEG {
			return nil, unsupportedf("不支持分平面存的 YCbCr 或旧式 JPEG 压缩 TIFF")
		}
		l.planes = l.colors
	}

	if comp == compOJPEG {
		off, err1 := d.uint(tagJPEGIF, 0)
		n, err2 := d.uint(tagJPEGIFLength, 0)
		if err1 != nil || err2 != nil || off == 0 || n < 4 || int64(off)+int64(n) > d.size {
			return nil, unsupportedf("不支持这种旧式 JPEG 压缩的 TIFF（没有指向完整 JPEG 的 JPEGInterchangeFormat）")
		}
		l.old = true
		l.blockW, l.blockH, l.across, l.down = l.width, l.height, 1, 1
		l.offsets, l.counts = []uint32{off}, []uint32{n}
		return l, nil
	}

	offTag, cntTag := uint16(tagStripOffsets), uint16(tagStripByteCounts)
	if _, tiled := d.entries[tagTileWidth]; tiled {
		l.tiled = true
		l.blockW, l.blockH = get(tagTileWidth, 0), get(tagTileLength, 0)
		offTag, cntTag = tagTileOffsets, tagTileByteCounts
	} else {
		l.blockW = l.width
		l.blockH = get(tagRowsPerStrip, uint32(l.height))
		if l.blockH > l.height {
			l.blockH = l.height
		}
	}
	if err != nil {
		return nil, err
	}
	if l.blockW == 0 || l.blockH == 0 {
		return nil, corruptf("TIFF 的条带或分块尺寸是 0")
	}
	l.across = (l.width + l.blockW - 1) / l.blockW
	l.down = (l.height + l.blockH - 1) / l.blockH
	n := int64(l.across) * int64(l.down) * int64(l.planes)
	if n > maxBlocks {
		return nil, unsupportedf("TIFF 的条带或分块太多（%d 个）", n)
	}
	offsets, ok, err := d.uints(offTag, maxBlocks)
	if err != nil {
		return nil, err
	}
	if !ok || int64(len(offsets)) < n {
		return nil, corruptf("TIFF 的条带表不完整（应有 %d 项）", n)
	}
	counts, ok, err := d.uints(cntTag, maxBlocks)
	if err != nil {
		return nil, err
	}
	if !ok && n == 1 && int64(offsets[0]) < d.size {
		// 只有一个条带又没写长度：到文件末尾。
		counts, ok = []uint32{uint32(min(d.size-int64(offsets[0]), 1<<32-1))}, true
	}
	if !ok || int64(len(counts)) < n {
		return nil, corruptf("TIFF 的条带长度表不完整（应有 %d 项）", n)
	}
	l.offsets, l.counts = offsets[:n], counts[:n]

	tables, err := d.bytes(tagJPEGTables, maxTables)
	if err != nil {
		return nil, err
	}
	if tables != nil {
		body, ok := bytes.CutPrefix(tables, soi)
		if !ok {
			return nil, corruptf("TIFF 的 JPEGTables 开头不是 SOI")
		}
		l.tables = bytes.TrimSuffix(body, eoi)
	}
	return l, nil
}

func (l *jpegLayout) blockName(i int) string {
	switch {
	case l.old:
		return "旧式 JPEG 压缩的 TIFF 里的 JPEG"
	case l.tiled:
		return fmt.Sprintf("TIFF 第 %d 个分块", i+1)
	}
	return fmt.Sprintf("TIFF 第 %d 个条带", i+1)
}

// decode 逐个块解码、贴进整张图。
func (l *jpegLayout) decode(ctx context.Context, ra io.ReaderAt) (image.Image, error) {
	var dst image.Image
	var buf []byte
	perPlane := l.across * l.down
	for i := range l.offsets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		plane, b := i/perPlane, i%perPlane
		x0, y0 := (b%l.across)*l.blockW, (b/l.across)*l.blockH
		w, h := min(l.blockW, l.width-x0), min(l.blockH, l.height-y0)
		stream, inverted, err := l.stream(ra, i, &buf)
		if err != nil {
			return nil, err
		}
		img, err := l.decodeBlock(ctx, i, stream, w, h)
		if err != nil {
			return nil, err
		}
		if len(l.offsets) == 1 {
			if out := l.direct(img, w, h, inverted); out != nil {
				return out, nil
			}
		}
		if dst == nil {
			dst = l.newTarget()
		}
		if !l.paste(dst, img, x0, y0, w, h, plane, inverted) {
			return nil, corruptf("%s解出来是 %T，和 TIFF 写的颜色（Photometric=%d、%d 个样本、%d 个平面）对不上",
				l.blockName(i), img, l.photometric, l.colors, l.planes)
		}
	}
	return dst, nil
}

// stream 拼出第 i 块完整的 JPEG（见文件开头），放在 *buf 里（各块复用）。inverted：补了 Adobe 标记，
// 解出的 CMYK 是反相的。
func (l *jpegLayout) stream(ra io.ReaderAt, i int, buf *[]byte) (stream []byte, inverted bool, err error) {
	off, n := int64(l.offsets[i]), int64(l.counts[i])
	if n < 4 || off+n > l.size {
		return nil, false, corruptf("%s的数据不完整（偏移 %d、长度 %d）", l.blockName(i), off, n)
	}
	var sig [2]byte
	if err := readFull(ra, sig[:], off); err != nil {
		return nil, false, err
	}
	if !bytes.Equal(sig[:], soi) {
		if l.old {
			return nil, false, unsupportedf("不支持这种旧式 JPEG 压缩的 TIFF（JPEGInterchangeFormat 指向的不是完整的 JPEG）")
		}
		return nil, false, corruptf("%s不是 JPEG（开头不是 SOI）", l.blockName(i))
	}
	// 布局：[SOI][补的 APP14][表][条带去掉 SOI][EOI]；不补 APP14 时 SOI 紧挨着表，从 head−2 开始。
	head := len(soi) + len(adobeSegment)
	need := head + len(l.tables) + int(n) - 2 + len(eoi)
	if cap(*buf) < need {
		*buf = make([]byte, need)
	}
	b := (*buf)[:need]
	p := head + copy(b[head:], l.tables)
	if err := readFull(ra, b[p:p+int(n)-2], off+2); err != nil {
		return nil, false, err
	}
	copy(b[need-2:], eoi)
	start := head - 2
	if l.old || (l.colors == 4 && l.planes == 1) {
		adobe, sof, sos := scanMarkers(b[head:])
		if l.old && (!sof || !sos) {
			return nil, false, unsupportedf("不支持这种旧式 JPEG 压缩的 TIFF（JPEGInterchangeFormat 指向的不是完整的 JPEG）")
		}
		if l.colors == 4 && l.planes == 1 && !adobe {
			copy(b[len(soi):], adobeSegment)
			start, inverted = 0, true
		}
	}
	copy(b[start:], soi)
	return b[start:], inverted, nil
}

// scanMarkers 从 SOI 之后的第一个标记起往后看，到 SOS、EOI 或数据不够为止：有没有 Adobe 标记、SOF、SOS。
func scanMarkers(b []byte) (adobe, sof, sos bool) {
	for p := 0; p+4 <= len(b); {
		if b[p] != 0xFF {
			return
		}
		m := b[p+1]
		switch {
		case m == 0xFF: // 填充
			p++
			continue
		case m == 0xDA:
			return adobe, sof, true
		case m == 0xD9:
			return
		case m >= 0xD0 && m <= 0xD7 || m == 0x01: // RSTn、TEM 没有长度
			p += 2
			continue
		}
		n := int(b[p+2])<<8 | int(b[p+3])
		if n < 2 {
			return
		}
		if m == 0xEE && p+9 <= len(b) && string(b[p+4:p+9]) == "Adobe" {
			adobe = true
		}
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			sof = true
		}
		p += 2 + n
	}
	return
}

// decodeBlock 解一块：先看 SOF 里的尺寸，不比应有的 w×h 小，也不比标称的块大出一个 MCU（16）以上——分块比图
// 还大时按图的边长算，至少 4096（几 KB 的文件写一个巨大的分块尺寸，不能让它按那个尺寸分配内存）；再整块解码。
func (l *jpegLayout) decodeBlock(ctx context.Context, i int, stream []byte, w, h int) (image.Image, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(stream))
	if err != nil {
		return nil, fmt.Errorf("%s：%w", l.blockName(i), err)
	}
	maxW, maxH := min(l.blockW, max(l.width, 4096))+16, min(l.blockH, max(l.height, 4096))+16
	if cfg.Width < w || cfg.Height < h || cfg.Width > maxW || cfg.Height > maxH {
		return nil, corruptf("%s是 %d×%d，应为 %d×%d", l.blockName(i), cfg.Width, cfg.Height, w, h)
	}
	img, err := jpeg.Decode(&ctxReader{ctx: ctx, r: bytes.NewReader(stream)})
	if err != nil {
		return nil, fmt.Errorf("%s：%w", l.blockName(i), err)
	}
	return img, nil
}

// direct：只有一个块时，解出来的图能直接用就不再复制（裁到图的宽高）；返回 nil 表示要复制。
func (l *jpegLayout) direct(img image.Image, w, h int, inverted bool) image.Image {
	r := image.Rect(0, 0, w, h)
	switch m := img.(type) {
	case *image.Gray:
		if l.colors == 1 {
			if l.photometric == 0 {
				invert(m.Pix)
			}
			return m.SubImage(r)
		}
	case *image.YCbCr:
		if l.photometric == 6 {
			return m.SubImage(r)
		}
	case *image.RGBA:
		if l.photometric == 2 {
			return m.SubImage(r)
		}
	case *image.CMYK:
		if l.colors == 4 {
			if inverted {
				invert(m.Pix)
			}
			return m.SubImage(r)
		}
	}
	return nil
}

// newTarget 是整张图：灰度 Gray，CMYK 是 CMYK（TIFF 的值），RGB、YCbCr 转成 RGBA。
func (l *jpegLayout) newTarget() image.Image {
	r := image.Rect(0, 0, l.width, l.height)
	switch l.colors {
	case 1:
		return image.NewGray(r)
	case 4:
		return image.NewCMYK(r)
	}
	return image.NewRGBA(r)
}

// paste 把解出的块的左上 w×h 贴到 (x0, y0)。块的类型和 TIFF 写的颜色对不上时返回 false。
func (l *jpegLayout) paste(dst, src image.Image, x0, y0, w, h, plane int, inverted bool) bool {
	if l.planes > 1 {
		// 分平面：单分量的块填进第 plane 个通道（RGBA 的 alpha 在填第一个平面时写 255）。
		g, ok := src.(*image.Gray)
		if !ok {
			return false
		}
		var pix []byte
		var stride int
		switch d := dst.(type) {
		case *image.RGBA:
			pix, stride = d.Pix, d.Stride
		case *image.CMYK:
			pix, stride = d.Pix, d.Stride
		}
		alpha := l.colors == 3 && plane == 0
		for y := 0; y < h; y++ {
			o := (y0+y)*stride + x0*4
			for x, v := range g.Pix[y*g.Stride : y*g.Stride+w] {
				pix[o+x*4+plane] = v
				if alpha {
					pix[o+x*4+3] = 255
				}
			}
		}
		return true
	}
	switch d := dst.(type) {
	case *image.Gray:
		g, ok := src.(*image.Gray)
		if !ok {
			return false
		}
		for y := 0; y < h; y++ {
			row := d.Pix[(y0+y)*d.Stride+x0:][:w]
			copy(row, g.Pix[y*g.Stride:])
			if l.photometric == 0 {
				invert(row)
			}
		}
	case *image.CMYK:
		s, ok := src.(*image.CMYK)
		if !ok {
			return false
		}
		for y := 0; y < h; y++ {
			row := d.Pix[(y0+y)*d.Stride+x0*4:][:w*4]
			copy(row, s.Pix[y*s.Stride:])
			if inverted {
				invert(row)
			}
		}
	case *image.RGBA:
		ycc := l.photometric == 6
		switch s := src.(type) {
		case *image.YCbCr:
			for y := 0; y < h; y++ {
				o := (y0+y)*d.Stride + x0*4
				for x := 0; x < w; x++ {
					ci := s.COffset(x, y)
					putRGB(d.Pix[o+x*4:], s.Y[s.YOffset(x, y)], s.Cb[ci], s.Cr[ci], ycc)
				}
			}
		case *image.RGBA:
			for y := 0; y < h; y++ {
				o := (y0+y)*d.Stride + x0*4
				so := y * s.Stride
				for x := 0; x < w; x++ {
					putRGB(d.Pix[o+x*4:], s.Pix[so+x*4], s.Pix[so+x*4+1], s.Pix[so+x*4+2], ycc)
				}
			}
		default:
			return false
		}
	}
	return true
}

// putRGB 写一个 RGBA 像素：三个分量的原值 a、b、c，ycc 时是 Y、Cb、Cr（换算成 RGB），否则就是 R、G、B。
func putRGB(p []byte, a, b, c byte, ycc bool) {
	if ycc {
		a, b, c = color.YCbCrToRGB(a, b, c)
	}
	p[0], p[1], p[2], p[3] = a, b, c, 255
}

func invert(p []byte) {
	for i, v := range p {
		p[i] = 255 - v
	}
}

// ctxReader：每次读之前看 ctx（image/jpeg 每 4 KB 读一次），解一个大条带时也能停下。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}
