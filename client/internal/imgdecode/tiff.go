package imgdecode

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"io"
	"os"

	"golang.org/x/image/tiff"
)

// TIFF：先读第一个 IFD 看压缩方式，JPEG 压缩（7、旧式 6）自己解，其余交给 x/image/tiff。
// 只读 JPEG 压缩要用的几个标签（internal/imagehdr 的 IFD 读取没有导出；server 的 export/bundle 有一份更全的，
// 客户端不能用 server 的 internal 包）。文件头里的个数、偏移都不可信：读之前检查范围，数组有上限。

const (
	tagImageWidth      = 256
	tagImageLength     = 257
	tagBitsPerSample   = 258
	tagCompression     = 259
	tagPhotometric     = 262
	tagStripOffsets    = 273
	tagSamplesPerPixel = 277
	tagRowsPerStrip    = 278
	tagStripByteCounts = 279
	tagPlanarConfig    = 284
	tagTileWidth       = 322
	tagTileLength      = 323
	tagTileOffsets     = 324
	tagTileByteCounts  = 325
	tagInkSet          = 332
	tagJPEGTables      = 347
	tagJPEGIF          = 513 // 旧式 JPEG：JPEGInterchangeFormat
	tagJPEGIFLength    = 514

	compOJPEG = 6
	compJPEG  = 7

	maxIFDEntries = 4096
	maxBlocks     = 1 << 18 // 条带、分块最多这么多个
	maxTables     = 1 << 20 // JPEGTables 最多 1 MiB
)

type ifdEntry struct {
	typ   uint16
	count uint32
	val   [4]byte
}

// ifd 是经典 TIFF 的第一个 IFD。
type ifd struct {
	ra      io.ReaderAt
	size    int64
	bo      binary.ByteOrder
	entries map[uint16]ifdEntry
}

func corruptf(format string, args ...any) *Error     { return errorf(KindCorrupt, format, args...) }
func unsupportedf(format string, args ...any) *Error { return errorf(KindUnsupported, format, args...) }

// read 读 [off, off+n)，不能超出文件。
func (d *ifd) read(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off > d.size || n > d.size-off {
		return nil, corruptf("TIFF 里偏移 %d 处的 %d 个字节超出了文件范围", off, n)
	}
	b := make([]byte, n)
	return b, readFull(d.ra, b, off)
}

// readFull 读满 b；文件比说的短（读到末尾）算文件坏了，其余的读错误原样返回。
func readFull(ra io.ReaderAt, b []byte, off int64) error {
	m, err := ra.ReadAt(b, off)
	if m == len(b) {
		return nil
	}
	if err == nil || err == io.EOF {
		return corruptf("TIFF 文件不完整")
	}
	return err
}

func readIFD(ra io.ReaderAt, size int64) (*ifd, error) {
	d := &ifd{ra: ra, size: size, entries: map[uint16]ifdEntry{}}
	h, err := d.read(0, 8)
	if err != nil {
		return nil, err
	}
	switch string(h[:4]) {
	case "II*\x00":
		d.bo = binary.LittleEndian
	case "MM\x00*":
		d.bo = binary.BigEndian
	default:
		return nil, corruptf("TIFF 文件头不对")
	}
	first := int64(d.bo.Uint32(h[4:]))
	b, err := d.read(first, 2)
	if err != nil {
		return nil, err
	}
	count := int64(d.bo.Uint16(b))
	if count > maxIFDEntries {
		return nil, corruptf("TIFF 的 IFD 有 %d 个条目，太多", count)
	}
	raw, err := d.read(first+2, count*12)
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < count; i++ {
		e := raw[i*12 : i*12+12]
		tag := d.bo.Uint16(e)
		if _, dup := d.entries[tag]; dup {
			continue
		}
		ent := ifdEntry{typ: d.bo.Uint16(e[2:]), count: d.bo.Uint32(e[4:])}
		copy(ent.val[:], e[8:12])
		d.entries[tag] = ent
	}
	return d, nil
}

// uints 读整数数组（BYTE、SHORT、LONG），最多 maxCount 个；没有这个标签时 ok 为 false。
func (d *ifd) uints(tag uint16, maxCount int) (v []uint32, ok bool, err error) {
	e, ok := d.entries[tag]
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
	b := e.val[:min(total, 4)]
	if total > 4 {
		if b, err = d.read(int64(d.bo.Uint32(e.val[:])), total); err != nil {
			return nil, true, err
		}
	}
	v = make([]uint32, e.count)
	for i := range v {
		switch size {
		case 1:
			v[i] = uint32(b[i])
		case 2:
			v[i] = uint32(d.bo.Uint16(b[2*i:]))
		default:
			v[i] = d.bo.Uint32(b[4*i:])
		}
	}
	return v, true, nil
}

// uint 读整数标签的第一个值；没有时返回 def。
func (d *ifd) uint(tag uint16, def uint32) (uint32, error) {
	v, ok, err := d.uints(tag, 1<<16)
	if err != nil || !ok {
		return def, err
	}
	return v[0], nil
}

// bytes 读字节数组（BYTE、UNDEFINED），最多 maxCount 个；没有时返回 nil。
func (d *ifd) bytes(tag uint16, maxCount int) ([]byte, error) {
	e, ok := d.entries[tag]
	if !ok {
		return nil, nil
	}
	if (e.typ != 1 && e.typ != 7) || e.count == 0 || int64(e.count) > int64(maxCount) {
		return nil, corruptf("TIFF 标签 %d 的类型或长度不对", tag)
	}
	if e.count <= 4 {
		return bytes.Clone(e.val[:e.count]), nil
	}
	return d.read(int64(d.bo.Uint32(e.val[:])), int64(e.count))
}

// TIFFSamples 读 TIFF 第一个 IFD 的每个样本的位数（BitsPerSample 的第一个值，没写按 1）和每像素的样本数
// （SamplesPerPixel，没写按 1），估算整张解码要多少内存用（internal/tiles）。读不出来时 ok 为 false。
func (s *Source) TIFFSamples() (bits, samples int, ok bool) {
	f, err := os.Open(s.Path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	d, err := readIFD(f, s.Size)
	if err != nil {
		return 0, 0, false
	}
	b, err1 := d.uint(tagBitsPerSample, 1)
	n, err2 := d.uint(tagSamplesPerPixel, 1)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return int(min(b, 64)), int(min(n, 64)), true
}

// decodeTIFF：JPEG 压缩的自己解，其余交给 x/image/tiff。第一个 IFD 读不出来时也交给它（由它报错）。
func decodeTIFF(ctx context.Context, r *ctxFile, size int64) (image.Image, error) {
	d, err := readIFD(r, size)
	if err == nil {
		var comp uint32
		comp, err = d.uint(tagCompression, 1)
		if err == nil && (comp == compJPEG || comp == compOJPEG) {
			l, err := d.jpegLayout(int(comp))
			if err != nil {
				return nil, err
			}
			return l.decode(ctx, r)
		}
	}
	return tiff.Decode(r)
}
