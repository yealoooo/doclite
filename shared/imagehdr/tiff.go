package imagehdr

import "encoding/binary"

// TIFF 的 IFD：整个 TIFF 文件（readTIFF），以及 JPEG 里的 EXIF（readEXIF，EXIF 就是一段 TIFF 结构）。
//
// 经典 TIFF：8 字节文件头（II/MM、42、第一个 IFD 的偏移）；IFD 依次是条目数（2 字节）、每条 12 字节的条目、
// 下一个 IFD 的偏移（4 字节）。条目的值不超过 4 字节时直接放在条目里，否则条目里放的是偏移。偏移都相对于
// TIFF 结构的开头，读之前按结构的范围检查。

const (
	maxIFDEntries = 1024 // 一个 IFD 最多多少个条目（扫描件一般二三十个）
	maxIFDs       = 64   // IFD 链最多走多少个 IFD（含第一个）
)

// 标签。
const (
	tagNewSubfileType = 254
	tagSubfileType    = 255
	tagImageWidth     = 256
	tagImageLength    = 257
	tagBitsPerSample  = 258
	tagPhotometric    = 262
	tagOrientation    = 274
	tagSamplesPerPix  = 277
	tagXResolution    = 282
	tagResolutionUnit = 296
	tagExtraSamples   = 338
)

// 值的类型。
const (
	typeByte     = 1
	typeShort    = 3
	typeLong     = 4
	typeRational = 5
)

type ifdEntry struct {
	tag, typ uint16
	count    uint32
	value    []byte // 4 字节：值（不超过 4 字节时，左对齐）或值的偏移
}

type ifd struct {
	entries []ifdEntry
	next    uint32 // 下一个 IFD 的偏移，0 表示没有了
}

func (d ifd) get(tag uint16) (ifdEntry, bool) {
	for _, e := range d.entries {
		if e.tag == tag {
			return e, true
		}
	}
	return ifdEntry{}, false
}

// tiffBlock 是一段 TIFF 结构，范围是 [base, end)，偏移都相对于 base。
type tiffBlock struct {
	s         *source
	base, end int64
	bo        binary.ByteOrder
	first     uint32 // 第一个 IFD 的偏移
}

// openTIFF 读 8 字节的文件头。
func openTIFF(s *source, base, end int64) (*tiffBlock, error) {
	t := &tiffBlock{s: s, base: base, end: end, bo: binary.LittleEndian}
	h, err := t.bytes(0, 8)
	if err != nil {
		return nil, err
	}
	switch string(h[:2]) {
	case "II":
	case "MM":
		t.bo = binary.BigEndian
	default:
		return nil, invalidf("开头不是 II 或 MM")
	}
	switch v := t.bo.Uint16(h[2:4]); v {
	case 42:
	case 43:
		return nil, newErr(ErrUnsupported, "不支持 BigTIFF，只接受 JPEG、PNG 和经典 TIFF")
	default:
		return nil, invalidf("版本号是 %d，不是 42", v)
	}
	t.first = t.bo.Uint32(h[4:8])
	return t, nil
}

// bytes 读结构内偏移 off 处的 n 个字节，不能超出结构的范围。
func (t *tiffBlock) bytes(off int64, n int) ([]byte, error) {
	if off < 0 || int64(n) > t.end-t.base-off {
		return nil, invalidf("偏移 %d 处的 %d 个字节超出了 TIFF 结构的范围（共 %d 字节）", off, n, t.end-t.base)
	}
	return t.s.bytes(t.base+off, n)
}

func (t *tiffBlock) readIFD(off uint32) (ifd, error) {
	if off < 8 {
		return ifd{}, invalidf("IFD 的偏移 %d 不对", off)
	}
	b, err := t.bytes(int64(off), 2)
	if err != nil {
		return ifd{}, err
	}
	count := int(t.bo.Uint16(b))
	if count > maxIFDEntries {
		return ifd{}, invalidf("IFD 有 %d 个条目，超过上限 %d", count, maxIFDEntries)
	}
	raw, err := t.bytes(int64(off)+2, count*12+4)
	if err != nil {
		return ifd{}, err
	}
	d := ifd{entries: make([]ifdEntry, count), next: t.bo.Uint32(raw[count*12:])}
	for i := range d.entries {
		e := raw[i*12 : i*12+12]
		d.entries[i] = ifdEntry{
			tag:   t.bo.Uint16(e[0:2]),
			typ:   t.bo.Uint16(e[2:4]),
			count: t.bo.Uint32(e[4:8]),
			value: e[8:12],
		}
	}
	return d, nil
}

// uintValue 是条目的第一个值（BYTE、SHORT、LONG）。类型不对、没有值、偏移越界返回 ErrInvalid。
func (t *tiffBlock) uintValue(e ifdEntry) (uint32, error) {
	var size int
	switch e.typ {
	case typeByte:
		size = 1
	case typeShort:
		size = 2
	case typeLong:
		size = 4
	default:
		return 0, invalidf("标签 %d 的类型是 %d，不是整数", e.tag, e.typ)
	}
	if e.count == 0 {
		return 0, invalidf("标签 %d 没有值", e.tag)
	}
	b := e.value
	// 全部值放得进 4 字节时就在条目里，否则条目里是偏移。
	if uint64(e.count)*uint64(size) > 4 {
		var err error
		if b, err = t.bytes(int64(t.bo.Uint32(e.value)), size); err != nil {
			return 0, err
		}
	}
	switch size {
	case 1:
		return uint32(b[0]), nil
	case 2:
		return uint32(t.bo.Uint16(b)), nil
	default:
		return t.bo.Uint32(b), nil
	}
}

// reqUint：必需（或有默认值）的整数标签。没有这个标签时返回 def、present 为 false；有但读不出来是 ErrInvalid。
func (t *tiffBlock) reqUint(d ifd, tag uint16, def uint32) (v uint32, present bool, err error) {
	e, ok := d.get(tag)
	if !ok {
		return def, false, nil
	}
	v, err = t.uintValue(e)
	return v, true, err
}

// optUint：可选的整数标签。没有、读不出来都返回 ok = false；只有读文件本身出错才返回 err。
func (t *tiffBlock) optUint(d ifd, tag uint16) (v uint32, ok bool, err error) {
	e, found := d.get(tag)
	if !found {
		return 0, false, nil
	}
	v, err = t.uintValue(e)
	if err != nil {
		if isHeaderErr(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return v, true, nil
}

// orientation：Orientation 标签的值；没写、读不出来为 0。
func (t *tiffBlock) orientation(d ifd) (int, error) {
	v, ok, err := t.optUint(d, tagOrientation)
	if err != nil || !ok || v > 8 {
		return 0, err
	}
	return int(v), nil
}

// dpi：XResolution 和 ResolutionUnit（没写单位按英寸；单位是"无"或不认识时未知）。XResolution 一般是 RATIONAL，
// 有的软件写成 SHORT、LONG，按"值 / 1"。读不出来当作没写。
func (t *tiffBlock) dpi(d ifd) (int, error) {
	e, ok := d.get(tagXResolution)
	if !ok || e.count == 0 {
		return 0, nil
	}
	var x float64
	if e.typ == typeRational {
		b, err := t.bytes(int64(t.bo.Uint32(e.value)), 8)
		if err != nil {
			if isHeaderErr(err) {
				return 0, nil
			}
			return 0, err
		}
		num, den := t.bo.Uint32(b[0:4]), t.bo.Uint32(b[4:8])
		if den == 0 {
			return 0, nil
		}
		x = float64(num) / float64(den)
	} else {
		v, ok, err := t.optUint(d, tagXResolution)
		if err != nil || !ok {
			return 0, err
		}
		x = float64(v)
	}
	unit, ok, err := t.optUint(d, tagResolutionUnit)
	if err != nil {
		return 0, err
	}
	if !ok {
		unit = 2
	}
	switch unit {
	case 2:
		return roundDPI(x), nil
	case 3:
		return roundDPI(x * cmPerInch), nil
	default:
		return 0, nil
	}
}

// isThumbnail：NewSubfileType 的第 0 位（reduced-resolution）为 1，或旧的 SubfileType 为 2。
func (t *tiffBlock) isThumbnail(d ifd) (bool, error) {
	n, okN, err := t.optUint(d, tagNewSubfileType)
	if err != nil {
		return false, err
	}
	o, okO, err := t.optUint(d, tagSubfileType)
	if err != nil {
		return false, err
	}
	return (okN && n&1 == 1) || (okO && o == 2), nil
}

// hasMorePages 沿第一个 IFD 的 next 指针往后走，遇到第一个不是缩略图的 IFD 就返回 true。
// 成环、超过 maxIFDs 个、偏移越界是 ErrInvalid。
func (t *tiffBlock) hasMorePages(first ifd) (bool, error) {
	seen := map[uint32]bool{t.first: true}
	for next := first.next; next != 0; {
		if seen[next] {
			return false, invalidf("IFD 链成环（偏移 %d 处的 IFD 又出现了一次）", next)
		}
		if len(seen) >= maxIFDs {
			return false, invalidf("IFD 超过 %d 个", maxIFDs)
		}
		seen[next] = true
		d, err := t.readIFD(next)
		if err != nil {
			return false, err
		}
		thumb, err := t.isThumbnail(d)
		if err != nil {
			return false, err
		}
		if !thumb {
			return true, nil
		}
		next = d.next
	}
	return false, nil
}

// readTIFF 读整个 TIFF 文件的文件头。
func readTIFF(s *source) (header, error) {
	t, err := openTIFF(s, 0, s.size)
	if err != nil {
		return header{}, err
	}
	if t.first == 0 {
		return header{}, invalidf("没有 IFD")
	}
	d, err := t.readIFD(t.first)
	if err != nil {
		return header{}, err
	}
	var h header
	w, ok, err := t.reqUint(d, tagImageWidth, 0)
	if err == nil && !ok {
		err = invalidf("第一个 IFD 里没有宽（ImageWidth）")
	}
	if err != nil {
		return header{}, err
	}
	ht, ok, err := t.reqUint(d, tagImageLength, 0)
	if err == nil && !ok {
		err = invalidf("第一个 IFD 里没有高（ImageLength）")
	}
	if err != nil {
		return header{}, err
	}
	bits, _, err := t.reqUint(d, tagBitsPerSample, 1)
	if err != nil {
		return header{}, err
	}
	samples, _, err := t.reqUint(d, tagSamplesPerPix, 1)
	if err != nil {
		return header{}, err
	}
	photometric, hasPhotometric, err := t.reqUint(d, tagPhotometric, 0)
	if err != nil {
		return header{}, err
	}
	h.width, h.height, h.bits = w, ht, int(min(bits, 64))
	h.palette = hasPhotometric && photometric == 3
	if hasPhotometric && (photometric == 0 || photometric == 1) {
		// WhiteIsZero、BlackIsZero：灰度，额外的通道是 alpha。
		h.channels = 1
	} else {
		// 其余按 SamplesPerPixel 减去 ExtraSamples（alpha 之类）的个数，至少 1。
		var extra uint32
		if e, ok := d.get(tagExtraSamples); ok {
			extra = e.count
		}
		h.channels = 1
		if samples > extra {
			h.channels = int(min(samples-extra, 64))
		}
	}
	if h.orientation, err = t.orientation(d); err != nil {
		return header{}, err
	}
	if h.dpi, err = t.dpi(d); err != nil {
		return header{}, err
	}
	if h.multiPage, err = t.hasMorePages(d); err != nil {
		return header{}, err
	}
	h.tiff, h.ifd0 = t, d
	return h, nil
}

// exifInfo 是 EXIF（IFD0）里读出的东西。
type exifInfo struct {
	orientation int // 0 表示没写
	dpi         int // 0 表示未知
}

// readEXIF 读 JPEG 里的 EXIF：[base, end) 是 APP1 段里 `Exif\0\0` 之后的 TIFF 结构。结构读不下去时 ok 为 false
// （整个 EXIF 当作没有，不拒绝图片）；只有读文件本身出错才返回 err。
func readEXIF(s *source, base, end int64) (info exifInfo, ok bool, err error) {
	t, err := openTIFF(s, base, end)
	if err == nil && t.first == 0 {
		return exifInfo{}, false, nil
	}
	var d ifd
	if err == nil {
		d, err = t.readIFD(t.first)
	}
	if err != nil {
		if isHeaderErr(err) {
			return exifInfo{}, false, nil
		}
		return exifInfo{}, false, err
	}
	if info.orientation, err = t.orientation(d); err != nil {
		return exifInfo{}, false, err
	}
	if info.dpi, err = t.dpi(d); err != nil {
		return exifInfo{}, false, err
	}
	return info, true, nil
}
