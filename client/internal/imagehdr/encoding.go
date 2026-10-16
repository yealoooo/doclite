package imagehdr

import (
	"encoding/binary"
	"fmt"
)

// 上传时拒绝导出不了的罕见编码（requirements 第 11 节，总控建议、用户未反对 2026-10-16）：极少数写法导出 PDF 时既不能
// 原样嵌入、也转换不了，原来要到导出执行时才失败；现在上传时就按文件头拒绝（[ParseUpload] 返回
// [ErrUnsupportedEncoding]），扫描员当场知道要换个保存格式重扫。
//
// "导出不了"= server 导出 PDF（server/internal/export/bundle 的 analyze）只看文件头就会判为"不支持"的写法，这里的
// 判断和那边的代码一一对应（server 那份由 bundle 的 TestUploadCheckMatchesExport 核对两边对同一批文件的结论一致）。文件坏了
// （条带表不完整、偏移越界、JPEG 尺寸和 TIFF 对不上……）不在这里查，导出时照旧按"损坏"报；判断要用的标签读不出来、
// 字节越界时也不拒绝。
//
// JPEG：SOF 不是 C0（基线）、C1（扩展）、C2（渐进），即无损（C3）、分层（C5–C7）、算术编码（C9–CB、CD–CF）；精度
// 不是 8 位；分量数不是 1、3、4。PNG：都能原样嵌入或转换，不拒绝。TIFF（第一个 IFD）：
//
//   - 各通道位数不同；每个样本不是 1、2、4、8、16 位；浮点或有符号样本（SampleFormat 不是 1）；
//   - 压缩方式不是无压缩、CCITT（MH、G3、G4）、LZW、旧式 JPEG（6）、JPEG（7）、Deflate、PackBits（JPEG 2000、JBIG……）；
//   - 颜色不是黑白灰、RGB、调色板、CMYK（InkSet=1）、YCbCr（只在 JPEG 压缩时）；超过 8 位的调色板；
//   - 条带或分块超过 2^18 个；
//   - CCITT：分块存的、用了非压缩模式的；
//   - 旧式 JPEG（6）：没有 JPEGInterchangeFormat 指向一段完整的 JPEG；
//   - JPEG（6、7）：分平面、带透明度或额外通道、不是 8 位、调色板，或者里面的 JPEG 本身是上面说的那几种（新式看第一个
//     条带，拼上 JPEGTables）；
//   - 旧式 LZW（第一个条带以 00 01 开头）；
//   - 无压缩、LZW、Deflate、PackBits：预测器不是 1、2（浮点预测 3）；1、2、4 位的样本用了水平差分预测器，或者分平面存、
//     带额外通道（要转换，但转换只支持 8、16 位）；分块的一行不是整字节。

// encodingErr 是给扫描员看的提示（server 原样放进 400 invalid_image 的 message，网页显示"图片不合规：…"）。
func encodingErr(reason string) *Error {
	return newErr(ErrUnsupportedEncoding, "不支持的图片格式（%s），请换个保存格式重扫", reason)
}

func checkEncoding(h *header) error {
	switch h.format {
	case JPEG:
		if r := jpegEncoding(h.sof, h.bits, h.comps); r != "" {
			return encodingErr(r)
		}
	case TIFF:
		return checkTIFF(h)
	}
	return nil
}

// jpegEncoding：导出 PDF 时 DCTDecode 放不进、Go 也解不了的 JPEG 写法，返回说明；能导出时返回空串。
func jpegEncoding(sof byte, precision, comps int) string {
	switch {
	case sof == 0xC3:
		return "无损压缩的 JPEG"
	case sof >= 0xC5 && sof <= 0xC7:
		return "分层编码的 JPEG"
	case sof >= 0xC9 && sof <= 0xCF: // C9–CB、CD–CF（CC 是 DAC，不是 SOF，读 SOF 时已经排除）
		return "算术编码的 JPEG"
	case precision != 8:
		return fmt.Sprintf("%d 位精度的 JPEG", precision)
	case comps != 1 && comps != 3 && comps != 4:
		return fmt.Sprintf("%d 个颜色分量的 JPEG", comps)
	}
	return ""
}

// TIFF 的标签（imagehdr 读文件头时不用的那些）。
const (
	tagCompression     = 259
	tagStripOffsets    = 273
	tagRowsPerStrip    = 278
	tagStripByteCounts = 279
	tagPlanarConfig    = 284
	tagT4Options       = 292
	tagT6Options       = 293
	tagPredictor       = 317
	tagTileWidth       = 322
	tagTileLength      = 323
	tagTileOffsets     = 324
	tagTileByteCounts  = 325
	tagInkSet          = 332
	tagSampleFormat    = 339
	tagJPEGTables      = 347
	tagJPEGIF          = 513 // 旧式 JPEG：JPEGInterchangeFormat
	tagJPEGIFLength    = 514
)

// 压缩方式（导出认的那些）。
const (
	compNone     = 1
	compMH       = 2
	compG3       = 3
	compG4       = 4
	compLZW      = 5
	compOJPEG    = 6
	compJPEG     = 7
	compDeflate  = 8
	compPackBits = 32773
	compDeflate2 = 32946
)

// maxBlocks：条带、分块最多这么多个（和导出一致）。
const maxBlocks = 1 << 18

// maxJPEGTables：JPEGTables 最长 1 MiB（和导出一致）。
const maxJPEGTables = 1 << 20

var tiffCompressionNames = map[uint32]string{
	9:     "JBIG",
	10:    "JBIG",
	34661: "JBIG",
	34715: "JBIG2",
	33003: "JPEG 2000",
	33005: "JPEG 2000",
	34712: "JPEG 2000",
	34925: "LZMA",
	50000: "Zstandard",
	50001: "WebP",
	34887: "LERC",
	32766: "NeXT",
	32809: "ThunderScan",
	34676: "SGILog",
	34677: "SGILog24",
}

var tiffPhotometricNames = map[uint32]string{
	4:     "透明蒙版",
	8:     "CIELab",
	9:     "ICCLab",
	10:    "ITULab",
	32803: "CFA（相机原始数据）",
	32844: "LogL",
	32845: "LogLuv",
	34892: "LinearRaw",
}

// tiffValues 读标签的全部整数值（BYTE、SHORT、LONG），最多 maxCount 个。没有这个标签时 ok 为 false；类型、个数
// 不对、偏移越界是 ErrInvalid。
func (t *tiffBlock) tiffValues(d ifd, tag uint16, maxCount int) (v []uint32, ok bool, err error) {
	e, found := d.get(tag)
	if !found {
		return nil, false, nil
	}
	var size int64
	switch e.typ {
	case typeByte:
		size = 1
	case typeShort:
		size = 2
	case typeLong:
		size = 4
	default:
		return nil, true, invalidf("标签 %d 的类型是 %d，不是整数", tag, e.typ)
	}
	if e.count == 0 || int64(e.count) > int64(maxCount) {
		return nil, true, invalidf("标签 %d 有 %d 个值", tag, e.count)
	}
	total := int64(e.count) * size
	b := e.value
	if total > 4 {
		if b, err = t.bytes(int64(t.bo.Uint32(e.value)), int(total)); err != nil {
			return nil, true, err
		}
	}
	v = make([]uint32, e.count)
	for i := range v {
		switch size {
		case 1:
			v[i] = uint32(b[i])
		case 2:
			v[i] = uint32(t.bo.Uint16(b[2*i:]))
		default:
			v[i] = t.bo.Uint32(b[4*i:])
		}
	}
	return v, true, nil
}

// tiffReader 读判断要用的标签：标签读不出来（内容的问题）时记下 bad，整个判断放弃（不拒绝）；读文件本身出错记在 err。
type tiffReader struct {
	t   *tiffBlock
	d   ifd
	bad bool
	err error
}

func (r *tiffReader) note(err error) {
	if err == nil {
		return
	}
	if isHeaderErr(err) {
		r.bad = true
	} else if r.err == nil {
		r.err = err
	}
}

// first 是标签的第一个值，没有时是 def。
func (r *tiffReader) first(tag uint16, def uint32) uint32 {
	v, ok, err := r.t.reqUint(r.d, tag, def)
	r.note(err)
	if err != nil || !ok {
		return def
	}
	return v
}

// all 是标签的全部值（最多 64 个），没有时是 nil。
func (r *tiffReader) all(tag uint16) []uint32 {
	v, _, err := r.t.tiffValues(r.d, tag, 64)
	r.note(err)
	return v
}

func (r *tiffReader) has(tag uint16) bool {
	_, ok := r.d.get(tag)
	return ok
}

// checkTIFF 按导出（bundle 的 readTIFFLayout、analyzeTIFF、jpegTIFFCheck、tiffConversion）的顺序判断第一个 IFD。
func checkTIFF(h *header) error {
	reason, err := tiffEncoding(h)
	if err != nil {
		return err
	}
	if reason != "" {
		return encodingErr(reason)
	}
	return nil
}

func tiffEncoding(h *header) (string, error) {
	t := h.tiff
	r := &tiffReader{t: t, d: h.ifd0}
	spp := r.first(tagSamplesPerPix, 1)
	comp := r.first(tagCompression, compNone)
	defPhoto := uint32(1)
	switch {
	case comp == compMH || comp == compG3 || comp == compG4:
		defPhoto = 0
	case spp >= 3:
		defPhoto = 2
	}
	photo := r.first(tagPhotometric, defPhoto)
	planar := r.first(tagPlanarConfig, 1)
	predictor := r.first(tagPredictor, 1)
	t4, t6 := r.first(tagT4Options, 0), r.first(tagT6Options, 0)
	inkSet := r.first(tagInkSet, 1)
	bitsList := r.all(tagBitsPerSample)
	formats := r.all(tagSampleFormat)
	if r.err != nil || r.bad || spp == 0 {
		return "", r.err
	}

	// 样本。
	if bitsList == nil {
		bitsList = []uint32{1}
	}
	for _, b := range bitsList {
		if b != bitsList[0] {
			return "各通道位数不同的 TIFF", nil
		}
	}
	bits := bitsList[0]
	switch bits {
	case 1, 2, 4, 8, 16:
	default:
		return fmt.Sprintf("每个样本 %d 位的 TIFF", bits), nil
	}
	for _, f := range formats {
		if f != 1 {
			return "浮点或有符号样本的 TIFF", nil
		}
	}

	// 压缩方式、颜色。
	switch comp {
	case compNone, compMH, compG3, compG4, compLZW, compOJPEG, compJPEG, compDeflate, compPackBits, compDeflate2:
	default:
		if name, ok := tiffCompressionNames[comp]; ok {
			return name + " 压缩的 TIFF", nil
		}
		return fmt.Sprintf("压缩方式 %d 的 TIFF", comp), nil
	}
	var colors uint32
	switch photo {
	case 0, 1, 3:
		colors = 1
	case 2:
		colors = 3
	case 5:
		if inkSet != 1 {
			return "CMYK 以外的分色 TIFF", nil
		}
		colors = 4
	case 6:
		if comp != compJPEG && comp != compOJPEG {
			return "YCbCr 颜色、不是 JPEG 压缩的 TIFF", nil
		}
		colors = 3
	default:
		if name, ok := tiffPhotometricNames[photo]; ok {
			return name + " 颜色的 TIFF", nil
		}
		return fmt.Sprintf("颜色类型 %d 的 TIFF", photo), nil
	}
	if spp < colors {
		return "", nil // 样本数不够：文件坏了，导出时按损坏报
	}
	if photo == 3 && bits > 8 {
		return fmt.Sprintf("%d 位调色板的 TIFF", bits), nil
	}
	planes := uint32(1)
	if planar == 2 && spp > 1 {
		planes = spp
	}

	// JPEG 压缩（6、7）都要的条件（导出的 jpegTIFFCheck）。
	jpegCheck := func() string {
		switch {
		case planes > 1 || spp > colors:
			return "JPEG 压缩、分平面存或带透明度的 TIFF"
		case bits != 8:
			return fmt.Sprintf("JPEG 压缩、每个样本 %d 位的 TIFF", bits)
		case photo == 3:
			return "JPEG 压缩的调色板 TIFF"
		}
		return ""
	}

	if comp == compOJPEG {
		// 只认 JPEGInterchangeFormat 指向一段完整 JPEG 的写法（导出原样嵌入那一段）。
		const noIF = "旧式 JPEG 压缩、找不到完整 JPEG 的 TIFF"
		off, okOff, err := t.optUint(h.ifd0, tagJPEGIF)
		if err != nil {
			return "", err
		}
		n, okLen, err := t.optUint(h.ifd0, tagJPEGIFLength)
		if err != nil {
			return "", err
		}
		if !okOff || !okLen || off == 0 || n < 4 || int64(off)+int64(n) > t.s.size {
			return noIF, nil
		}
		if why := jpegCheck(); why != "" {
			return why, nil
		}
		soi, err := t.bytes(int64(off), 2)
		if err != nil {
			return "", ignoreHeaderErr(err)
		}
		if soi[0] != 0xFF || soi[1] != 0xD8 {
			return noIF, nil
		}
		return t.embeddedJPEG(int64(off)+2, int64(off)+int64(n))
	}

	// 条带或分块（导出 readTIFFLayout：先按块数设上限）。
	width, height := uint64(h.width), uint64(h.height)
	tiled := r.has(tagTileWidth)
	var blockW, blockH uint64
	if tiled {
		blockW, blockH = uint64(r.first(tagTileWidth, 0)), uint64(r.first(tagTileLength, 0))
	} else {
		blockW, blockH = width, uint64(r.first(tagRowsPerStrip, uint32(min(height, 1<<32-1))))
		if blockH == 0 || blockH > height {
			blockH = height
		}
	}
	if r.err != nil || r.bad || blockW == 0 || blockH == 0 {
		return "", r.err
	}
	across, down := (width+blockW-1)/blockW, (height+blockH-1)/blockH
	if across > maxBlocks || down > maxBlocks || across*down*uint64(planes) > maxBlocks {
		return "条带或分块太多的 TIFF", nil
	}
	offTag, cntTag := uint16(tagStripOffsets), uint16(tagStripByteCounts)
	if tiled {
		offTag, cntTag = tagTileOffsets, tagTileByteCounts
	}
	// firstBlock 是第一个条带（分块）的位置；读不出来、超出文件时 ok 为 false。
	firstBlock := func() (off, n int64, ok bool, err error) {
		o, okO, err := t.optUint(h.ifd0, offTag)
		if err != nil {
			return 0, 0, false, err
		}
		c, okC, err := t.optUint(h.ifd0, cntTag)
		if err != nil {
			return 0, 0, false, err
		}
		if !okO || !okC || int64(o)+int64(c) > t.s.size {
			return 0, 0, false, nil
		}
		return int64(o), int64(c), true, nil
	}

	switch comp {
	case compMH, compG3, compG4:
		if bits != 1 || spp != 1 || photo > 1 {
			return "", nil // 导出时按损坏报
		}
		if tiled {
			return "分块存的 CCITT 压缩 TIFF", nil
		}
		if (comp == compG3 && t4&2 != 0) || (comp == compG4 && t6&2 != 0) {
			return "CCITT 非压缩模式的 TIFF", nil
		}
		return "", nil
	case compJPEG:
		if why := jpegCheck(); why != "" {
			return why, nil
		}
		return t.jpegTIFFStrip(h.ifd0, firstBlock)
	case compLZW:
		// 旧式 LZW（低位在前，开头是 00 01）导出和 PDF 都解不了。
		off, n, ok, err := firstBlock()
		if err != nil {
			return "", err
		}
		if ok && n >= 2 {
			b, err := t.bytes(off, 2)
			if err != nil {
				return "", ignoreHeaderErr(err)
			}
			if b[0] == 0 && b[1]&1 == 1 {
				return "旧式 LZW 压缩的 TIFF", nil
			}
		}
	}

	// 无压缩、LZW、Deflate、PackBits：原样嵌入或转换。转换只支持 8、16 位的预测器和额外通道、整字节的分块。
	planeSamples := uint64(spp)
	if planes > 1 {
		planeSamples = 1
	}
	switch {
	case predictor != 1 && predictor != 2:
		return fmt.Sprintf("预测器 %d 的 TIFF", predictor), nil
	case predictor == 2 && bits < 8:
		return fmt.Sprintf("每个样本 %d 位又用了差分预测器的 TIFF", bits), nil
	case bits < 8 && (planes > 1 || spp > colors):
		return fmt.Sprintf("每个样本 %d 位、分平面存或带额外通道的 TIFF", bits), nil
	case tiled && (blockW*planeSamples*uint64(bits))%8 != 0:
		return "分块宽度不是整字节的 TIFF", nil
	}
	return "", nil
}

// jpegTIFFStrip 看 JPEG 压缩（7）的第一个条带：拼上 JPEGTables 之后是不是导出能嵌入的 JPEG。表和条带不完整时不判断。
func (t *tiffBlock) jpegTIFFStrip(d ifd, firstBlock func() (int64, int64, bool, error)) (string, error) {
	off, n, ok, err := firstBlock()
	if err != nil || !ok || n < 4 {
		return "", err
	}
	if b, err := t.bytes(off, 2); err != nil || b[0] != 0xFF || b[1] != 0xD8 {
		return "", ignoreHeaderErr(err)
	}
	// JPEGTables（BYTE 或 UNDEFINED）：SOI + DQT、DHT…… + EOI。SOF 一般在条带里，表里有时也照样认。
	if e, found := d.get(tagJPEGTables); found {
		if (e.typ != typeByte && e.typ != 7) || e.count < 4 || e.count > maxJPEGTables {
			return "", nil
		}
		tOff := int64(t.bo.Uint32(e.value))
		b, err := t.bytes(tOff, 2)
		if err != nil || b[0] != 0xFF || b[1] != 0xD8 {
			return "", ignoreHeaderErr(err)
		}
		marker, precision, comps, found, err := t.findSOF(tOff+2, tOff+int64(e.count))
		if err != nil || found {
			return jpegInTIFF(marker, precision, comps, found), err
		}
	}
	return t.embeddedJPEG(off+2, off+n)
}

// embeddedJPEG 看 TIFF 里嵌的一段 JPEG（[start, end)，start 在 SOI 之后）的 SOF。
func (t *tiffBlock) embeddedJPEG(start, end int64) (string, error) {
	marker, precision, comps, found, err := t.findSOF(start, end)
	return jpegInTIFF(marker, precision, comps, found), err
}

func jpegInTIFF(marker byte, precision, comps int, found bool) string {
	if !found {
		return ""
	}
	if r := jpegEncoding(marker, precision, comps); r != "" {
		return "JPEG 压缩的 TIFF，里面是" + r
	}
	return ""
}

// findSOF 从 start 起逐个读 JPEG 标记段，找第一个 SOF（不超过 end）。读到 SOS、EOI、读不下去时 found 为 false；
// 只有读文件本身出错才返回 err。
func (t *tiffBlock) findSOF(start, end int64) (marker byte, precision, comps int, found bool, err error) {
	be := binary.BigEndian
	pos := start
	for range maxJPEGMarkers {
		if pos+2 > end {
			return 0, 0, 0, false, nil
		}
		b, err := t.bytes(pos, 2)
		if err != nil {
			return 0, 0, 0, false, ignoreHeaderErr(err)
		}
		if b[0] != 0xFF {
			return 0, 0, 0, false, nil
		}
		m := b[1]
		switch {
		case m == 0xFF:
			pos++
			continue
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			pos += 2
			continue
		case m == 0x00 || m == 0xD8 || m == 0xD9 || m == 0xDA:
			return 0, 0, 0, false, nil
		}
		if pos+4 > end {
			return 0, 0, 0, false, nil
		}
		lb, err := t.bytes(pos+2, 2)
		if err != nil {
			return 0, 0, 0, false, ignoreHeaderErr(err)
		}
		length := int64(be.Uint16(lb))
		if length < 2 {
			return 0, 0, 0, false, nil
		}
		if isSOF(m) {
			if length < 8 || pos+10 > end {
				return 0, 0, 0, false, nil
			}
			p, err := t.bytes(pos+4, 6)
			if err != nil {
				return 0, 0, 0, false, ignoreHeaderErr(err)
			}
			return m, int(p[0]), int(p[5]), true, nil
		}
		pos += 2 + length
	}
	return 0, 0, 0, false, nil
}

// ignoreHeaderErr：内容的问题（越界之类）当作判断不了、不拒绝；读文件本身出错原样返回。
func ignoreHeaderErr(err error) error {
	if err == nil || isHeaderErr(err) {
		return nil
	}
	return err
}
