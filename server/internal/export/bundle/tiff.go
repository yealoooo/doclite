package bundle

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// TIFF 的 IFD 读取。shared/imagehdr 的 IFD 读取没有导出，而且只读上传要的几个标签；导出要压缩方式、条带、预测器、
// FillOrder、调色板、纵向分辨率这些，这些细节只有导出用，所以在这个包里自己读，不扩展 shared（imagehdr 仍用来
// 认格式、宽高、方向，和上传时一致）。
//
// 文件头里的个数、长度都不可信：读之前按结构的范围检查，数组按算出来应有的个数读（有上限），不按声明的长度分配内存。

// 标签。
const (
	tagImageWidth      = 256
	tagImageLength     = 257
	tagBitsPerSample   = 258
	tagCompression     = 259
	tagPhotometric     = 262
	tagFillOrder       = 266
	tagStripOffsets    = 273
	tagSamplesPerPixel = 277
	tagRowsPerStrip    = 278
	tagStripByteCounts = 279
	tagXResolution     = 282
	tagYResolution     = 283
	tagPlanarConfig    = 284
	tagT4Options       = 292
	tagT6Options       = 293
	tagResolutionUnit  = 296
	tagPredictor       = 317
	tagColorMap        = 320
	tagTileWidth       = 322
	tagTileLength      = 323
	tagTileOffsets     = 324
	tagTileByteCounts  = 325
	tagInkSet          = 332
	tagExtraSamples    = 338
	tagSampleFormat    = 339
	tagJPEGTables      = 347
	tagJPEGIF          = 513 // 旧式 JPEG：JPEGInterchangeFormat
	tagJPEGIFLength    = 514
)

const (
	maxIFDEntries = 4096
	maxBlocks     = 1 << 18 // 条带、分块最多这么多个
)

type tiffEntry struct {
	typ   uint16
	count uint32
	val   [4]byte
}

type tiffIFD map[uint16]tiffEntry

// tiffReader 是一段 TIFF 结构 [base, end)，偏移都相对于 base（整个 TIFF 文件时 base 为 0；JPEG 的 EXIF 在段里）。
type tiffReader struct {
	ra        io.ReaderAt
	base, end int64
	bo        binary.ByteOrder
	first     uint32
}

func openTIFF(ra io.ReaderAt, base, end int64) (*tiffReader, error) {
	t := &tiffReader{ra: ra, base: base, end: end}
	h, err := t.bytes(0, 8)
	if err != nil {
		return nil, err
	}
	switch string(h[:4]) {
	case "II*\x00":
		t.bo = binary.LittleEndian
	case "MM\x00*":
		t.bo = binary.BigEndian
	default:
		return nil, corruptf("TIFF 文件头不对")
	}
	t.first = t.bo.Uint32(h[4:8])
	return t, nil
}

// bytes 读结构内偏移 off 处的 n 个字节，不能超出结构的范围。
func (t *tiffReader) bytes(off int64, n int64) ([]byte, error) {
	if off < 0 || n < 0 || n > t.end-t.base-off {
		return nil, corruptf("TIFF 里偏移 %d 处的 %d 个字节超出了文件范围", off, n)
	}
	b := make([]byte, n)
	return b, readFull(t.ra, b, t.base+off)
}

func (t *tiffReader) readIFD() (tiffIFD, error) {
	if t.first < 8 {
		return nil, corruptf("TIFF 第一个 IFD 的偏移 %d 不对", t.first)
	}
	b, err := t.bytes(int64(t.first), 2)
	if err != nil {
		return nil, err
	}
	count := int64(t.bo.Uint16(b))
	if count > maxIFDEntries {
		return nil, corruptf("TIFF 的 IFD 有 %d 个条目，太多", count)
	}
	raw, err := t.bytes(int64(t.first)+2, count*12)
	if err != nil {
		return nil, err
	}
	d := tiffIFD{}
	for i := int64(0); i < count; i++ {
		e := raw[i*12 : i*12+12]
		var ent tiffEntry
		ent.typ = t.bo.Uint16(e[2:4])
		ent.count = t.bo.Uint32(e[4:8])
		copy(ent.val[:], e[8:12])
		tag := t.bo.Uint16(e[0:2])
		if _, dup := d[tag]; !dup {
			d[tag] = ent
		}
	}
	return d, nil
}

// uints 读整数数组（BYTE、SHORT、LONG），最多 maxCount 个。
func (t *tiffReader) uints(d tiffIFD, tag uint16, maxCount int) ([]uint32, bool, error) {
	e, ok := d[tag]
	if !ok {
		return nil, false, nil
	}
	var size int64
	switch e.typ {
	case 1:
		size = 1
	case 3:
		size = 2
	case 4:
		size = 4
	default:
		return nil, true, corruptf("TIFF 标签 %d 的类型 %d 不是整数", tag, e.typ)
	}
	if e.count == 0 || int64(e.count) > int64(maxCount) {
		return nil, true, corruptf("TIFF 标签 %d 有 %d 个值，不对", tag, e.count)
	}
	total := int64(e.count) * size
	var b []byte
	if total <= 4 {
		b = e.val[:total]
	} else {
		var err error
		if b, err = t.bytes(int64(t.bo.Uint32(e.val[:])), total); err != nil {
			return nil, true, err
		}
	}
	out := make([]uint32, e.count)
	for i := range out {
		switch size {
		case 1:
			out[i] = uint32(b[i])
		case 2:
			out[i] = uint32(t.bo.Uint16(b[2*i:]))
		default:
			out[i] = t.bo.Uint32(b[4*i:])
		}
	}
	return out, true, nil
}

// raw 读字节数组（BYTE、UNDEFINED），最多 maxCount 个；没有时返回 nil。
func (t *tiffReader) raw(d tiffIFD, tag uint16, maxCount int) ([]byte, error) {
	e, ok := d[tag]
	if !ok {
		return nil, nil
	}
	if (e.typ != 1 && e.typ != 7) || e.count == 0 || int64(e.count) > int64(maxCount) {
		return nil, corruptf("TIFF 标签 %d 的类型或长度不对", tag)
	}
	if e.count <= 4 {
		return append([]byte(nil), e.val[:e.count]...), nil
	}
	return t.bytes(int64(t.bo.Uint32(e.val[:])), int64(e.count))
}

// uint 读整数标签的第一个值；没有时返回 def。
func (t *tiffReader) uint(d tiffIFD, tag uint16, def uint32) (uint32, error) {
	v, ok, err := t.uints(d, tag, 1<<16)
	if err != nil || !ok {
		return def, err
	}
	return v[0], nil
}

// rational 读 RATIONAL（有的软件写成 SHORT、LONG，按"值 / 1"）。读不出来返回 0。
func (t *tiffReader) rational(d tiffIFD, tag uint16) float64 {
	e, ok := d[tag]
	if !ok || e.count == 0 {
		return 0
	}
	if e.typ == 5 {
		b, err := t.bytes(int64(t.bo.Uint32(e.val[:])), 8)
		if err != nil {
			return 0
		}
		num, den := t.bo.Uint32(b[0:4]), t.bo.Uint32(b[4:8])
		if den == 0 {
			return 0
		}
		return float64(num) / float64(den)
	}
	v, err := t.uint(d, tag, 0)
	if err != nil {
		return 0
	}
	return float64(v)
}

// resolution 是横纵 DPI：XResolution、YResolution 和 ResolutionUnit（没写按英寸，厘米 × 2.54，"无"算未知）。
func (t *tiffReader) resolution(d tiffIFD) (x, y float64) {
	x, y = t.rational(d, tagXResolution), t.rational(d, tagYResolution)
	unit, err := t.uint(d, tagResolutionUnit, 2)
	if err != nil {
		unit = 2
	}
	switch unit {
	case 2:
		return x, y
	case 3:
		return x * 2.54, y * 2.54
	}
	return 0, 0
}

// ---------------------------------------------------------------------------
// 布局

// 压缩方式。
const (
	compNone     = 1
	compMH       = 2 // CCITT 修改的哈夫曼（RLE）
	compG3       = 3
	compG4       = 4
	compLZW      = 5
	compOJPEG    = 6
	compJPEG     = 7
	compDeflate  = 8
	compPackBits = 32773
	compDeflate2 = 32946
)

// tiffLayout 是第一个 IFD 里导出要用的东西。
type tiffLayout struct {
	t                                           *tiffReader
	width, height                               int
	bits, spp, colors                           int
	compression, photometric, planar, predictor int
	fillOrder                                   int
	extra                                       []uint32
	colormap                                    []uint32
	tiled                                       bool
	blockW, blockH                              int // 条带：宽 = 图宽，高 = RowsPerStrip；分块：分块的宽高
	across, down                                int // 横向、纵向的块数
	offsets, counts                             []uint32
	t4, t6                                      uint32
	dpiX, dpiY                                  float64
	jpegTables                                  []byte // JPEG 压缩（7）的 JPEGTables
	ojpegOff, ojpegLen                          int64  // 旧式 JPEG（6）：JPEGInterchangeFormat 指向的完整 JPEG
}

// planes 是平面数：分平面存（PlanarConfiguration=2）且不止一个样本时每个样本一个平面。
func (l *tiffLayout) planes() int {
	if l.planar == 2 && l.spp > 1 {
		return l.spp
	}
	return 1
}

// planeSamples 是一个平面里每个像素的样本数。
func (l *tiffLayout) planeSamples() int {
	if l.planes() > 1 {
		return 1
	}
	return l.spp
}

// rowBytes 是一个平面里 w 个像素一行的字节数（行末补齐到整字节）。
func (l *tiffLayout) rowBytes(w int) int64 {
	return (int64(w)*int64(l.planeSamples())*int64(l.bits) + 7) / 8
}

// blockRows 是第 by 排块（条带）的行数：分块总是整块，条带最后一个可能短一些。
func (l *tiffLayout) blockRows(by int) int {
	if l.tiled {
		return l.blockH
	}
	return min(l.blockH, l.height-by*l.blockH)
}

func readTIFFLayout(ra io.ReaderAt, size int64) (*tiffLayout, error) {
	t, err := openTIFF(ra, 0, size)
	if err != nil {
		return nil, err
	}
	d, err := t.readIFD()
	if err != nil {
		return nil, err
	}
	l := &tiffLayout{t: t}
	get := func(tag uint16, def uint32) int {
		if err != nil {
			return 0
		}
		var v uint32
		v, err = t.uint(d, tag, def)
		return int(min(v, 1<<30))
	}
	l.width, l.height = get(tagImageWidth, 0), get(tagImageLength, 0)
	l.spp = get(tagSamplesPerPixel, 1)
	l.compression = get(tagCompression, compNone)
	defPhoto := uint32(1)
	switch {
	case l.compression == compMH || l.compression == compG3 || l.compression == compG4:
		defPhoto = 0
	case l.spp >= 3:
		defPhoto = 2
	}
	l.photometric = get(tagPhotometric, defPhoto)
	l.planar = get(tagPlanarConfig, 1)
	l.predictor = get(tagPredictor, 1)
	l.fillOrder = get(tagFillOrder, 1)
	t4, t6 := get(tagT4Options, 0), get(tagT6Options, 0)
	inkSet := get(tagInkSet, 1)
	if err != nil {
		return nil, err
	}
	l.t4, l.t6 = uint32(t4), uint32(t6)
	if l.width == 0 || l.height == 0 || l.spp == 0 {
		return nil, corruptf("TIFF 的宽高或样本数是 0")
	}
	bits, ok, err := t.uints(d, tagBitsPerSample, 64)
	if err != nil {
		return nil, err
	}
	if !ok {
		bits = []uint32{1}
	}
	for _, b := range bits {
		if b != bits[0] {
			return nil, unsupportedf("不支持各通道位数不同的 TIFF")
		}
	}
	l.bits = int(bits[0])
	switch l.bits {
	case 1, 2, 4, 8, 16:
	default:
		return nil, unsupportedf("不支持每个样本 %d 位的 TIFF", l.bits)
	}
	if formats, ok, err := t.uints(d, tagSampleFormat, 64); err != nil {
		return nil, err
	} else if ok {
		for _, f := range formats {
			if f != 1 {
				return nil, unsupportedf("不支持浮点或有符号样本的 TIFF")
			}
		}
	}
	if l.extra, _, err = t.uints(d, tagExtraSamples, 64); err != nil {
		return nil, err
	}

	switch l.compression {
	case compNone, compLZW, compDeflate, compDeflate2, compPackBits, compMH, compG3, compG4, compJPEG, compOJPEG:
	default:
		return nil, unsupportedf("不支持这种 TIFF 压缩方式（Compression=%d）", l.compression)
	}

	switch l.photometric {
	case 0, 1, 3:
		l.colors = 1
	case 2:
		l.colors = 3
	case 5:
		if inkSet != 1 {
			return nil, unsupportedf("不支持 CMYK 以外的分色 TIFF")
		}
		l.colors = 4
	case 6:
		if l.compression != compJPEG && l.compression != compOJPEG {
			return nil, unsupportedf("不支持 JPEG 压缩以外的 YCbCr TIFF")
		}
		l.colors = 3
	default:
		return nil, unsupportedf("不支持这种颜色的 TIFF（Photometric=%d）", l.photometric)
	}
	if l.spp < l.colors {
		return nil, corruptf("TIFF 的样本数 %d 少于颜色通道数 %d", l.spp, l.colors)
	}
	if l.photometric == 3 {
		if l.bits > 8 {
			return nil, unsupportedf("不支持 %d 位的调色板 TIFF", l.bits)
		}
		n := 3 << l.bits
		cm, ok, err := t.uints(d, tagColorMap, n)
		if err != nil {
			return nil, err
		}
		if !ok || len(cm) != n {
			return nil, corruptf("调色板 TIFF 的 ColorMap 不对")
		}
		l.colormap = cm
	}

	switch l.compression {
	case compJPEG:
		if l.jpegTables, err = t.raw(d, tagJPEGTables, 1<<20); err != nil {
			return nil, err
		}
	case compOJPEG:
		// 旧式 JPEG 只认 JPEGInterchangeFormat 指向完整 JPEG 的写法，不看条带表。
		off, err1 := t.uint(d, tagJPEGIF, 0)
		n, err2 := t.uint(d, tagJPEGIFLength, 0)
		if err1 != nil || err2 != nil || off == 0 || n < 4 || int64(off)+int64(n) > size {
			return nil, unsupportedf("不支持这种旧式 JPEG 压缩的 TIFF（没有指向完整 JPEG 的 JPEGInterchangeFormat）")
		}
		l.ojpegOff, l.ojpegLen = int64(off), int64(n)
		l.dpiX, l.dpiY = t.resolution(d)
		return l, nil
	}

	// 条带或分块。
	offTag, cntTag := uint16(tagStripOffsets), uint16(tagStripByteCounts)
	if _, ok := d[tagTileWidth]; ok {
		l.tiled = true
		l.blockW, l.blockH = get(tagTileWidth, 0), get(tagTileLength, 0)
		if err != nil {
			return nil, err
		}
		if l.blockW == 0 || l.blockH == 0 {
			return nil, corruptf("TIFF 的分块宽高是 0")
		}
		offTag, cntTag = tagTileOffsets, tagTileByteCounts
	} else {
		l.blockW = l.width
		l.blockH = get(tagRowsPerStrip, uint32(l.height))
		if err != nil {
			return nil, err
		}
		if l.blockH == 0 || l.blockH > l.height {
			l.blockH = l.height
		}
	}
	l.across = (l.width + l.blockW - 1) / l.blockW
	l.down = (l.height + l.blockH - 1) / l.blockH
	n := int64(l.across) * int64(l.down) * int64(l.planes())
	if n > maxBlocks {
		return nil, unsupportedf("TIFF 的条带或分块太多（%d 个）", n)
	}
	var okOff, okCnt bool
	if l.offsets, okOff, err = t.uints(d, offTag, maxBlocks); err != nil {
		return nil, err
	}
	if l.counts, okCnt, err = t.uints(d, cntTag, maxBlocks); err != nil {
		return nil, err
	}
	if !okOff || !okCnt || int64(len(l.offsets)) < n || int64(len(l.counts)) < n {
		return nil, corruptf("TIFF 的条带表不完整（应有 %d 个）", n)
	}
	l.offsets, l.counts = l.offsets[:n], l.counts[:n]
	for i := range l.offsets {
		if int64(l.offsets[i])+int64(l.counts[i]) > size {
			return nil, corruptf("TIFF 第 %d 个条带超出了文件末尾", i+1)
		}
	}
	l.dpiX, l.dpiY = t.resolution(d)
	return l, nil
}

// palette8 是能无损变成 8 位的调色板（每项都是 c × 257，或者所有值都不超过 255）的查找表；不能时返回 nil。
func (l *tiffLayout) palette8() []byte {
	n := len(l.colormap) / 3
	scaled, small := true, true
	for _, v := range l.colormap {
		scaled = scaled && v%257 == 0
		small = small && v <= 255
	}
	if !scaled && !small {
		return nil
	}
	out := make([]byte, 3*n)
	for i := range n {
		for c := range 3 {
			v := l.colormap[c*n+i]
			if scaled {
				v /= 257
			}
			out[3*i+c] = byte(v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 原样嵌入还是转换

// maxEmbedStrips：能转换的格式条带超过这么多个时转换成一张图（对象太多阅读器打不开）。
const maxEmbedStrips = 1024

func analyzeTIFF(ra io.ReaderAt, size int64, opts Options, inspect bool) (*pagePlan, error) {
	l, err := readTIFFLayout(ra, size)
	if err != nil {
		return nil, err
	}
	plan := &pagePlan{width: l.width, height: l.height, dpiX: l.dpiX, dpiY: l.dpiY}

	switch l.compression {
	case compMH, compG3, compG4:
		// CCITT 只能原样嵌入（转换不了），条带再多也照样拆。
		if l.bits != 1 || l.spp != 1 || l.photometric > 1 {
			return nil, corruptf("CCITT 压缩的 TIFF 必须是 1 位黑白")
		}
		if l.tiled {
			return nil, unsupportedf("不支持分块存的 CCITT TIFF")
		}
		if (l.compression == compG3 && l.t4&2 != 0) || (l.compression == compG4 && l.t6&2 != 0) {
			return nil, unsupportedf("不支持 CCITT 的非压缩模式")
		}
		plan.images = ccittImages(l)
		return plan, nil
	case compJPEG:
		// JPEG 压缩也只能原样嵌入（不解码），条带、分块再多也照样拆。
		plan.images, err = jpegTIFFImages(ra, l, inspect)
		return plan, err
	case compOJPEG:
		plan.images, err = oldJPEGImages(ra, l)
		return plan, err
	case compLZW:
		// 旧式 LZW（低位在前，开头是 00 01）PDF 和 Go 都解不了。
		if l.counts[0] >= 2 {
			b := make([]byte, 2)
			if err := readFull(ra, b, int64(l.offsets[0])); err != nil {
				return nil, err
			}
			if b[0] == 0 && b[1]&1 == 1 {
				return nil, unsupportedf("不支持旧式 LZW 压缩的 TIFF")
			}
		}
	}

	reason := ""
	switch {
	case opts.forceConvert:
		reason = "测试要求转换"
	case l.tiled:
		reason = "分块存"
	case l.planes() > 1:
		reason = "分平面存"
	case l.spp > l.colors:
		reason = "带透明度或额外通道"
	case l.bits == 16 && l.t.bo == binary.LittleEndian:
		reason = "16 位小端"
	case l.predictor == 2 && l.bits != 8:
		reason = "预测器用在 8 位以外"
	case l.predictor != 1 && l.predictor != 2:
		return nil, unsupportedf("不支持 TIFF 预测器 %d", l.predictor)
	case l.photometric == 3 && l.palette8() == nil:
		reason = "16 位调色板"
	case l.compression != compNone && len(l.offsets) > maxEmbedStrips:
		reason = "条带太多"
	case l.compression == compPackBits && !inspect:
		ok, err := packBitsEmbeddable(ra, l)
		if err != nil {
			return nil, err
		}
		if !ok {
			reason = "PackBits 条带不规整"
		}
	}
	if reason == "" {
		plan.images, err = tiffImages(l)
		return plan, err
	}
	plan.convert, err = tiffConversion(l, reason)
	return plan, err
}

// tiffColorSpace 是原样嵌入、转换都用的颜色空间和 Decode 数组（WhiteIsZero 时反相）。
func tiffColorSpace(l *tiffLayout) (colorSpace, string) {
	switch l.photometric {
	case 0:
		return csGray, "1 0"
	case 2:
		return csRGB, ""
	case 3:
		return indexed(l.palette8()), ""
	case 5:
		return csCMYK, ""
	}
	return csGray, ""
}

// tiffImages：无压缩的条带首尾相接拼成一个图像对象；LZW、Deflate、PackBits 每个条带一个。
func tiffImages(l *tiffLayout) ([]imageObj, error) {
	cs, decode := tiffColorSpace(l)
	base := imageObj{
		width:       l.width,
		cs:          cs,
		bpc:         l.bits,
		decode:      decode,
		reverseBits: l.fillOrder == 2,
	}
	rowBytes := l.rowBytes(l.width)
	if l.compression == compNone {
		img := base
		img.height = l.height
		for i := range l.offsets {
			need := int64(l.blockRows(i)) * rowBytes
			if int64(l.counts[i]) < need {
				return nil, corruptf("TIFF 第 %d 个条带的数据不够（%d 字节，应有 %d）", i+1, l.counts[i], need)
			}
			img.ranges = append(img.ranges, byteRange{off: int64(l.offsets[i]), n: need})
		}
		return []imageObj{img}, nil
	}
	var out []imageObj
	for i := range l.offsets {
		if l.counts[i] == 0 {
			return nil, corruptf("TIFF 第 %d 个条带是空的", i+1)
		}
		img := base
		img.row0 = i * l.blockH
		img.height = l.blockRows(i)
		img.ranges = []byteRange{{off: int64(l.offsets[i]), n: int64(l.counts[i])}}
		switch l.compression {
		case compLZW:
			img.filter = "LZWDecode"
		case compDeflate, compDeflate2:
			img.filter = "FlateDecode"
		case compPackBits:
			img.filter = "RunLengthDecode"
			img.trailer = []byte{128} // EOD
		}
		if l.predictor == 2 {
			img.parms = predictor2Parms(l)
		}
		out = append(out, img)
	}
	return out, nil
}

func predictor2Parms(l *tiffLayout) string {
	return fmt.Sprintf("/Predictor 2 /Colors %d /BitsPerComponent %d /Columns %d", l.spp, l.bits, l.width)
}

// ccittImages：每个条带一个 CCITTFaxDecode 的图像对象。
func ccittImages(l *tiffLayout) []imageObj {
	var out []imageObj
	for i := range l.offsets {
		rows := l.blockRows(i)
		k, align := 0, false
		switch l.compression {
		case compMH:
			align = true // 每行从整字节开始
		case compG3:
			if l.t4&1 != 0 {
				k = 10000 // 二维编码（每行前有一位标明一维还是二维）
			}
			align = l.t4&4 != 0 // EOL 前补齐到整字节
		case compG4:
			k = -1
		}
		parms := fmt.Sprintf("/K %d /Columns %d /Rows %d", k, l.width, rows)
		if l.photometric == 1 {
			parms += " /BlackIs1 true"
		}
		if align {
			parms += " /EncodedByteAlign true"
		}
		out = append(out, imageObj{
			width:       l.width,
			height:      rows,
			cs:          csGray,
			bpc:         1,
			filter:      "CCITTFaxDecode",
			parms:       parms,
			ranges:      []byteRange{{off: int64(l.offsets[i]), n: int64(l.counts[i])}},
			reverseBits: l.fillOrder == 2,
			row0:        i * l.blockH,
		})
	}
	return out
}

// packBitsEmbeddable 走一遍每个条带的控制字节：有 128（TIFF 里是空操作，PDF 的 RunLengthDecode 里是结束）、
// 解出来的长度不对时不能原样嵌入。
func packBitsEmbeddable(ra io.ReaderAt, l *tiffLayout) (bool, error) {
	rowBytes := l.rowBytes(l.width)
	for i := range l.offsets {
		br := bufio.NewReaderSize(io.NewSectionReader(ra, int64(l.offsets[i]), int64(l.counts[i])), 64<<10)
		var decoded int64
		for {
			c, err := br.ReadByte()
			if err == io.EOF {
				break
			}
			if err != nil {
				return false, err
			}
			n := int8(c)
			var skip int
			switch {
			case n == -128:
				return false, nil
			case n >= 0:
				skip, decoded = int(n)+1, decoded+int64(n)+1
			default:
				skip, decoded = 1, decoded+1-int64(n)
			}
			if d, err := br.Discard(skip); err != nil || d != skip {
				if err != nil && err != io.EOF {
					return false, err
				}
				return false, nil
			}
		}
		if decoded != int64(l.blockRows(i))*rowBytes {
			return false, nil
		}
	}
	return true, nil
}
