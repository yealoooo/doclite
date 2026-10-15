package bundle

import (
	"io"
)

// JPEG 原样嵌入：整个文件作为 DCTDecode 的流（PDF 阅读器自己解）。只支持基线、扩展（8 位）、渐进的哈夫曼编码，
// 1、3、4 个分量；无损、分层、算术编码、12 位是 KindUnsupported（PDF 的 DCTDecode 不支持，Go 也解不了）。
//
// 读到第一个 SOS 为止：SOF 的宽高、精度、分量数；APP0 JFIF 的横纵密度；APP1 EXIF 的 XResolution、YResolution；
// APP14 Adobe（CMYK 带它时 PDF 要反相）。方向由 imagehdr 读（和上传时一致）。

const maxJPEGMarkers = 4096

func analyzeJPEG(ra io.ReaderAt, size int64) (*pagePlan, error) {
	plan := &pagePlan{}
	var (
		sofSeen    bool
		comps      int
		adobe      bool
		jfifOK     bool
		jfifX      float64
		jfifY      float64
		exifX      float64
		exifY      float64
		exifSeen   bool
		pos        = int64(2)
		markerHead [4]byte
	)
	for range maxJPEGMarkers {
		if err := readFull(ra, markerHead[:2], pos); err != nil {
			return nil, corruptf("JPEG 没读到图像数据（SOS）就到了文件末尾")
		}
		if markerHead[0] != 0xFF {
			return nil, corruptf("JPEG 第 %d 字节处应该是标记", pos)
		}
		m := markerHead[1]
		switch {
		case m == 0xFF:
			pos++
			continue
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			pos += 2
			continue
		case m == 0xD8 || m == 0xD9 || m == 0x00:
			return nil, corruptf("JPEG 在图像数据之前出现了标记 FF %02X", m)
		}
		if err := readFull(ra, markerHead[2:4], pos+2); err != nil {
			return nil, err
		}
		length := int64(be16(markerHead[2:4]))
		if length < 2 {
			return nil, corruptf("JPEG 标记段 FF %02X 的长度不对", m)
		}
		payload, plen := pos+4, length-2
		switch {
		case m == 0xDA: // SOS
			if !sofSeen {
				return nil, corruptf("JPEG 没有 SOF 就开始了图像数据")
			}
			return finishJPEG(plan, size, comps, adobe, jfifOK, jfifX, jfifY, exifX, exifY)
		case m == 0xE0 && plen >= 12 && !jfifOK:
			b := make([]byte, 12)
			if err := readFull(ra, b, payload); err != nil {
				return nil, err
			}
			if string(b[:5]) == "JFIF\x00" {
				jfifX, jfifY, jfifOK = jfifDensity(b[7], be16(b[8:10]), be16(b[10:12]))
			}
		case m == 0xE1 && plen > 14 && !exifSeen:
			b := make([]byte, 6)
			if err := readFull(ra, b, payload); err != nil {
				return nil, err
			}
			if string(b) == "Exif\x00\x00" {
				exifSeen = true
				exifX, exifY = exifDPI(ra, payload+6, min(payload+plen, size))
			}
		case m == 0xEE && plen >= 12:
			b := make([]byte, 5)
			if err := readFull(ra, b, payload); err != nil {
				return nil, err
			}
			if string(b) == "Adobe" {
				adobe = true
			}
		case isSOF(m):
			if sofSeen {
				return nil, corruptf("JPEG 有两个 SOF")
			}
			if m != 0xC0 && m != 0xC1 && m != 0xC2 {
				return nil, unsupportedf("不支持这种 JPEG 编码（SOF%d：无损、分层或算术编码）", m-0xC0)
			}
			b := make([]byte, 6)
			if plen < 6 {
				return nil, corruptf("JPEG 的 SOF 段太短")
			}
			if err := readFull(ra, b, payload); err != nil {
				return nil, err
			}
			if b[0] != 8 {
				return nil, unsupportedf("不支持 %d 位的 JPEG", b[0])
			}
			plan.height, plan.width, comps = int(be16(b[1:3])), int(be16(b[3:5])), int(b[5])
			if comps != 1 && comps != 3 && comps != 4 {
				return nil, unsupportedf("不支持 %d 个分量的 JPEG", comps)
			}
			sofSeen = true
		}
		pos = payload + plen
	}
	return nil, corruptf("JPEG 图像数据之前的标记超过 %d 个", maxJPEGMarkers)
}

func isSOF(m byte) bool { return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC }

func finishJPEG(plan *pagePlan, size int64, comps int, adobe, jfifOK bool, jfifX, jfifY, exifX, exifY float64) (*pagePlan, error) {
	// DPI：JFIF 写了（单位是英寸或厘米、密度不为 0）就用 JFIF，否则用 EXIF（和 imagehdr 一样的先后）。
	if jfifOK {
		plan.dpiX, plan.dpiY = jfifX, jfifY
	} else {
		plan.dpiX, plan.dpiY = exifX, exifY
	}
	img := imageObj{
		width:  plan.width,
		height: plan.height,
		bpc:    8,
		filter: "DCTDecode",
		ranges: []byteRange{{off: 0, n: size}},
	}
	switch comps {
	case 1:
		img.cs = csGray
	case 3:
		img.cs = csRGB
	case 4:
		img.cs = csCMYK
		if adobe {
			// Photoshop 写的 CMYK JPEG（带 Adobe APP14）存的是反相的值。
			img.decode = "1 0 1 0 1 0 1 0"
		}
	}
	plan.images = []imageObj{img}
	return plan, nil
}

// jfifDensity：单位是英寸（1）或厘米（2）、X 密度不为 0 时有效。
func jfifDensity(units byte, x, y uint16) (dx, dy float64, ok bool) {
	var f float64
	switch units {
	case 1:
		f = 1
	case 2:
		f = 2.54
	default:
		return 0, 0, false
	}
	if x == 0 {
		return 0, 0, false
	}
	return float64(x) * f, float64(y) * f, true
}

// exifDPI 读 EXIF（[base, end) 是一段 TIFF 结构）IFD0 的 XResolution、YResolution。读不出来当作没写。
func exifDPI(ra io.ReaderAt, base, end int64) (x, y float64) {
	t, err := openTIFF(ra, base, end)
	if err != nil {
		return 0, 0
	}
	d, err := t.readIFD()
	if err != nil {
		return 0, 0
	}
	return t.resolution(d)
}
