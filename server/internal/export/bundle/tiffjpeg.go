package bundle

import (
	"bytes"
	"io"
)

// TIFF 的 JPEG 压缩：原样嵌入，不解码（只重新组织字节）。
//
//   - 新式（Compression=7）：每个条带、分块一个 DCTDecode 图像对象，拼在同一页上。有 JPEGTables 时把表拼进
//     每个条带：SOI + 表（去掉表里的 SOI、EOI）+ 条带（去掉条带自己的 SOI）；没有时条带本身就是完整的 JPEG。
//   - 旧式（Compression=6）：只认 JPEGInterchangeFormat 指向一个完整 JPEG 的写法，嵌入那一段；否则不支持。
//   - ColorTransform：Photometric 是 YCbCr 时 1（阅读器把 YCbCr 转成 RGB），RGB、CMYK 时 0；灰度不写。
//   - 分平面、带透明度或额外通道、不是 8 位的不支持（DCTDecode 只能给出一组颜色）。
//
// 只检查第一个条带的 JPEG 文件头（SOF 的编码方式、尺寸、分量数），其余条带只查开头是不是 SOI（Inspect 时不查）。

var (
	jpegSOI = []byte{0xFF, 0xD8}
	jpegEOI = []byte{0xFF, 0xD9}
)

func jpegTIFFCheck(l *tiffLayout) error {
	switch {
	case l.planes() > 1 || l.spp > l.colors:
		return unsupportedf("不支持分平面存或带透明度的 JPEG 压缩 TIFF")
	case l.bits != 8:
		return unsupportedf("不支持 %d 位的 JPEG 压缩 TIFF", l.bits)
	case l.photometric == 3:
		return unsupportedf("不支持 JPEG 压缩的调色板 TIFF")
	}
	return nil
}

// jpegTIFFColor 是 JPEG 压缩的 TIFF 的颜色空间、Decode 数组、DecodeParms。
func jpegTIFFColor(l *tiffLayout) (colorSpace, string, string) {
	switch l.photometric {
	case 6:
		return csRGB, "", "/ColorTransform 1"
	case 2:
		return csRGB, "", "/ColorTransform 0"
	case 5:
		return csCMYK, "", "/ColorTransform 0"
	case 0:
		return csGray, "1 0", ""
	}
	return csGray, "", ""
}

func jpegTIFFImages(ra io.ReaderAt, l *tiffLayout, inspect bool) ([]imageObj, error) {
	if err := jpegTIFFCheck(l); err != nil {
		return nil, err
	}
	var prefix []byte
	if len(l.jpegTables) > 0 {
		body, ok := bytes.CutPrefix(l.jpegTables, jpegSOI)
		if !ok {
			return nil, corruptf("TIFF 的 JPEGTables 开头不是 SOI")
		}
		body = bytes.TrimSuffix(body, jpegEOI)
		prefix = append(bytes.Clone(jpegSOI), body...)
	}
	cs, decode, parms := jpegTIFFColor(l)
	var out []imageObj
	head := make([]byte, 2)
	for by := range l.down {
		for bx := range l.across {
			idx := by*l.across + bx
			off, n := int64(l.offsets[idx]), int64(l.counts[idx])
			if n < 4 {
				return nil, corruptf("TIFF 第 %d 个条带的 JPEG 数据不完整", idx+1)
			}
			if !inspect || idx == 0 {
				if err := readFull(ra, head, off); err != nil {
					return nil, err
				}
				if !bytes.Equal(head, jpegSOI) {
					return nil, corruptf("TIFF 第 %d 个条带不是 JPEG（开头不是 SOI）", idx+1)
				}
			}
			img := imageObj{
				width:  l.blockW,
				height: l.blockRows(by),
				cs:     cs,
				bpc:    8,
				filter: "DCTDecode",
				parms:  parms,
				decode: decode,
				col0:   bx * l.blockW,
				row0:   by * l.blockH,
				ranges: []byteRange{{off: off, n: n}},
			}
			if prefix != nil {
				img.prefix = prefix
				img.ranges = []byteRange{{off: off + 2, n: n - 2}}
			}
			out = append(out, img)
		}
	}
	// 第一个条带：拼好之后是不是 PDF 能解的 JPEG，尺寸、分量数和 TIFF 对不对得上。
	first := &out[0]
	view := &prefixedReaderAt{prefix: first.prefix, ra: ra, off: first.ranges[0].off, n: first.ranges[0].n}
	if err := checkEmbeddedJPEG(view, view.size(), first.width, first.height, l.colors, "TIFF 第 1 个条带里的 JPEG"); err != nil {
		return nil, err
	}
	return out, nil
}

func oldJPEGImages(ra io.ReaderAt, l *tiffLayout) ([]imageObj, error) {
	if err := jpegTIFFCheck(l); err != nil {
		return nil, err
	}
	head := make([]byte, 2)
	if err := readFull(ra, head, l.ojpegOff); err != nil {
		return nil, err
	}
	if !bytes.Equal(head, jpegSOI) {
		return nil, unsupportedf("不支持这种旧式 JPEG 压缩的 TIFF（JPEGInterchangeFormat 指向的不是完整的 JPEG）")
	}
	view := io.NewSectionReader(ra, l.ojpegOff, l.ojpegLen)
	if err := checkEmbeddedJPEG(view, l.ojpegLen, l.width, l.height, l.colors, "旧式 JPEG 压缩的 TIFF 里的 JPEG"); err != nil {
		return nil, err
	}
	cs, decode, parms := jpegTIFFColor(l)
	return []imageObj{{
		width:  l.width,
		height: l.height,
		cs:     cs,
		bpc:    8,
		filter: "DCTDecode",
		parms:  parms,
		decode: decode,
		ranges: []byteRange{{off: l.ojpegOff, n: l.ojpegLen}},
	}}, nil
}

// checkEmbeddedJPEG 读一段 JPEG 的文件头（和单独的 JPEG 文件同样的规则），核对尺寸和分量数。
func checkEmbeddedJPEG(ra io.ReaderAt, size int64, width, height, comps int, what string) error {
	jp, err := analyzeJPEG(ra, size)
	if err != nil {
		return err
	}
	if jp.width != width || jp.height != height || jp.images[0].cs.comps() != comps {
		return corruptf("%s是 %d×%d、%d 个分量，和 TIFF 写的 %d×%d、%d 个分量不一致",
			what, jp.width, jp.height, jp.images[0].cs.comps(), width, height, comps)
	}
	return nil
}

// prefixedReaderAt 是"前面补几个字节 + 原图里的一段"拼起来的 io.ReaderAt。
type prefixedReaderAt struct {
	prefix []byte
	ra     io.ReaderAt
	off, n int64
}

func (v *prefixedReaderAt) size() int64 { return int64(len(v.prefix)) + v.n }

func (v *prefixedReaderAt) ReadAt(b []byte, off int64) (int, error) {
	n := 0
	if off < int64(len(v.prefix)) {
		n = copy(b, v.prefix[off:])
	}
	if n == len(b) {
		return n, nil
	}
	rest := off + int64(n) - int64(len(v.prefix))
	m, err := io.NewSectionReader(v.ra, v.off, v.n).ReadAt(b[n:], rest)
	return n + m, err
}
