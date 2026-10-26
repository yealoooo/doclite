package imagehdr

import (
	"encoding/binary"
	"fmt"
	"math"
)

// readBMP：14 字节的文件头（"BM"、文件大小、保留、像素数据的偏移）+ DIB 头。DIB 头认 BITMAPCOREHEADER（12 字节，
// OS/2 和 Windows 2.x：宽高是 16 位）和 16 字节及更长的那些（OS/2 2.x、BITMAPINFOHEADER 40 字节、V2–V5：宽高是
// 有符号 32 位，高为负表示从上往下存）。只读头里的字段和调色板，不看像素数据（数据不完整导出时按损坏报）。
//
// 上传时拒绝 golang.org/x/image/bmp 解不了的写法（header.reject，和它的 decodeConfig 一一对应）：DIB 头不是 40、
// 108、124 字节；平面数不是 1；有压缩（RLE、JPEG、PNG、位域；位域只认 V4、V5 头里写的掩码正好是默认 BGRA 的）；
// 每像素不是 1、2、4、8、24、32 位；调色板项数超过位深；像素数据不是紧跟在头（和调色板）后面。
func readBMP(s *source) (header, error) {
	le := binary.LittleEndian
	b, err := s.bytes(0, 18)
	if err != nil {
		return header{}, err
	}
	offset, infoLen := le.Uint32(b[10:14]), le.Uint32(b[14:18])
	var h header
	var planes, compression uint32
	var clrUsed uint32
	var masks [4]uint32
	paletteEntry := 4 // 调色板每项的字节数（BITMAPCOREHEADER 是 3）
	switch {
	case infoLen == 12:
		d, err := s.bytes(18, 8)
		if err != nil {
			return header{}, err
		}
		h.width, h.height = uint32(le.Uint16(d[0:2])), uint32(le.Uint16(d[2:4]))
		planes, h.bits = uint32(le.Uint16(d[4:6])), int(le.Uint16(d[6:8]))
		paletteEntry = 3
	case infoLen >= 16 && infoLen <= 1024:
		d, err := s.bytes(18, int(min(infoLen, 56))-4)
		if err != nil {
			return header{}, err
		}
		field := func(off int) uint32 { // DIB 头里第 off 字节起的 4 字节（d 从 DIB 头的第 4 字节开始）；头比它短时是 0
			if off > len(d) {
				return 0
			}
			return le.Uint32(d[off-4 : off])
		}
		w, ht := int32(field(4)), int64(int32(field(8)))
		if w < 0 {
			return header{}, invalidf("宽度是负数（%d）", w)
		}
		if ht < 0 {
			ht = -ht // 从上往下存
		}
		if ht > math.MaxUint32 {
			return header{}, invalidf("高度超出范围")
		}
		h.width, h.height = uint32(w), uint32(ht)
		pb := field(12)
		planes, h.bits = pb&0xFFFF, int(pb>>16)
		compression = field(16)
		h.dpi = roundDPI(float64(int32(field(24))) * cmPerInch / 100)
		clrUsed = field(32)
		for i := range masks {
			masks[i] = field(40 + 4*i)
		}
	default:
		return header{}, invalidf("DIB 头的长度 %d 不认识", infoLen)
	}

	// 颜色：不超过 8 位的是调色板图，调色板全是灰色时按单通道算。
	h.channels = 3
	if h.bits >= 1 && h.bits <= 8 {
		h.palette = true
		n := uint32(1) << h.bits
		if clrUsed > 0 && clrUsed < n {
			n = clrUsed
		}
		if p, err := s.bytes(14+int64(infoLen), int(n)*paletteEntry); err == nil && grayPalette(p, paletteEntry) {
			h.palette, h.channels = false, 1
		}
	}

	h.reject = bmpReject(infoLen, offset, planes, compression, uint32(h.bits), clrUsed, masks)
	return h, nil
}

// grayPalette：调色板每项（B、G、R[、保留]，或 GIF 的 R、G、B）三个分量都相等。
func grayPalette(p []byte, entry int) bool {
	for i := 0; i+3 <= len(p); i += entry {
		if p[i] != p[i+1] || p[i] != p[i+2] {
			return false
		}
	}
	return true
}

// bmpReject：golang.org/x/image/bmp 解不了的写法，返回说明；能解时返回空串。
func bmpReject(infoLen, offset, planes, compression, bpp, clrUsed uint32, masks [4]uint32) string {
	switch {
	case infoLen != 40 && infoLen != 108 && infoLen != 124:
		return fmt.Sprintf("文件头是 %d 字节的旧式或少见版本 BMP", infoLen)
	case planes != 1:
		return fmt.Sprintf("%d 个平面的 BMP", planes)
	}
	// 位域（3）写的掩码正好是默认的 BGRA 时和不压缩一样。
	if compression == 3 && infoLen > 40 && masks == [4]uint32{0xFF0000, 0xFF00, 0xFF, 0xFF000000} {
		compression = 0
	}
	switch compression {
	case 0:
	case 1, 2:
		return "RLE 压缩的 BMP"
	case 3, 6:
		return "位域（BITFIELDS）格式的 BMP"
	case 4:
		return "内嵌 JPEG 的 BMP"
	case 5:
		return "内嵌 PNG 的 BMP"
	default:
		return fmt.Sprintf("压缩方式 %d 的 BMP", compression)
	}
	switch bpp {
	case 1, 2, 4, 8:
		used := clrUsed
		if used == 0 {
			used = 1 << bpp
		} else if used > 1<<bpp {
			return "调色板项数超过位深的 BMP"
		}
		if uint64(offset) != 14+uint64(infoLen)+4*uint64(used) {
			return "像素数据没有紧跟在调色板后面的 BMP"
		}
	case 24, 32:
		if uint64(offset) != 14+uint64(infoLen) {
			return "像素数据没有紧跟在文件头后面的 BMP"
		}
	default:
		return fmt.Sprintf("每像素 %d 位的 BMP", bpp)
	}
	return ""
}
