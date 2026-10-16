package imagehdr

import "encoding/binary"

// maxJPEGMarkers：SOF 之前最多读多少个标记（FF 填充也算）。正常的 JPEG 在 SOF 之前只有十几个段。
const maxJPEGMarkers = 1024

// isSOF：C0–CF 里除去 C4（DHT）、C8（JPG）、CC（DAC）。
func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}

// readJPEG 从 SOI 起逐个读标记段的段头，读到第一个 SOF 就停（JFIF、EXIF 按规范在 SOF 之前）。
// 段的内容只读用得到的那几个字节，其余按段长跳过。
func readJPEG(s *source) (header, error) {
	be := binary.BigEndian
	pos := int64(2) // SOI 之后
	jfifSeen := false
	jfifDPI := 0
	var exif *exifInfo
	for range maxJPEGMarkers {
		if pos+2 > s.size {
			return header{}, invalidf("没读到宽高（SOF）就到了文件末尾")
		}
		b, err := s.bytes(pos, 2)
		if err != nil {
			return header{}, err
		}
		if b[0] != 0xFF {
			return header{}, invalidf("第 %d 字节处应该是标记（FF），文件可能已损坏", pos)
		}
		marker := b[1]
		switch {
		case marker == 0xFF: // 标记前的 FF 填充
			pos++
			continue
		case marker == 0x00:
			return header{}, invalidf("第 %d 字节处应该是标记，却是 FF 00", pos)
		case marker == 0xD8:
			return header{}, invalidf("SOI 出现了两次")
		case marker == 0xD9:
			return header{}, invalidf("没读到 SOF 就到了 EOI")
		case marker == 0xDA:
			return header{}, invalidf("没读到 SOF 就开始了图像数据（SOS）")
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // 没有长度的标记：TEM、RST0–RST7
			pos += 2
			continue
		}
		lb, err := s.bytes(pos+2, 2)
		if err != nil {
			return header{}, err
		}
		length := int64(be.Uint16(lb))
		if length < 2 {
			return header{}, invalidf("标记段 FF %02X 的长度是 %d，不对", marker, length)
		}
		payload, plen := pos+4, length-2
		switch {
		case marker == 0xE0 && !jfifSeen && plen >= 12:
			// "JFIF\0"、版本（2 字节）、单位、X 密度、Y 密度……
			p, err := s.bytes(payload, 12)
			if err != nil {
				return header{}, err
			}
			if string(p[:5]) == "JFIF\x00" {
				jfifSeen = true
				jfifDPI = jfifDensity(p[7], be.Uint16(p[8:10]))
			}
		case marker == 0xE1 && exif == nil && plen > 6:
			p, err := s.bytes(payload, 6)
			if err != nil {
				return header{}, err
			}
			if string(p) == "Exif\x00\x00" {
				// 段声明的长度超出文件末尾时只在文件范围内读。
				end := min(payload+plen, s.size)
				info, ok, err := readEXIF(s, payload+6, end)
				if err != nil {
					return header{}, err
				}
				if ok {
					exif = &info
				}
			}
		case isSOF(marker):
			if plen < 6 {
				return header{}, invalidf("SOF 段只有 %d 字节，读不出宽高", plen)
			}
			p, err := s.bytes(payload, 6)
			if err != nil {
				return header{}, err
			}
			h := header{
				bits:   int(p[0]),
				height: uint32(be.Uint16(p[1:3])),
				width:  uint32(be.Uint16(p[3:5])),
				dpi:    jfifDPI,
				sof:    marker,
				comps:  int(p[5]),
			}
			// 分量数：1 灰度，3 YCbCr / RGB，4 CMYK / YCCK；其他少见的值颜色模式留空。
			if c := int(p[5]); c == 1 || c == 3 || c == 4 {
				h.channels = c
			}
			if exif != nil {
				h.orientation = exif.orientation
				if h.dpi == 0 {
					h.dpi = exif.dpi
				}
			}
			return h, nil
		}
		pos = payload + plen
	}
	return header{}, invalidf("SOF 之前的标记超过 %d 个", maxJPEGMarkers)
}

// jfifDensity：单位是英寸（1）或厘米（2）时换算成 DPI；单位是 0（只表示长宽比）或密度为 0 时未知。
func jfifDensity(units byte, density uint16) int {
	switch units {
	case 1:
		return roundDPI(float64(density))
	case 2:
		return roundDPI(float64(density) * cmPerInch)
	}
	return 0
}
