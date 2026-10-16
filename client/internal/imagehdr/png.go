package imagehdr

import (
	"encoding/binary"
	"slices"
)

const (
	maxPNGChunks   = 1024       // IDAT 之前最多读多少个块
	maxPNGChunkLen = 0x7FFFFFFF // 块长度的上限（PNG 规范）
)

// readPNG：签名后面第一个块必须是 IHDR（宽高、位深、颜色类型）；再逐块找 pHYs（按规范在 IDAT 之前），
// 读到 IDAT 或 IEND 就停。块的内容只读 IHDR 和 pHYs，其余按块长跳过，不校验 CRC。PNG 不看方向。
func readPNG(s *source) (header, error) {
	be := binary.BigEndian
	// 签名 8 字节之后：IHDR 的长度（13）、类型、13 字节内容。
	b, err := s.bytes(8, 8+13)
	if err != nil {
		return header{}, err
	}
	if be.Uint32(b[0:4]) != 13 || string(b[4:8]) != "IHDR" {
		return header{}, invalidf("第一个块不是 13 字节的 IHDR")
	}
	h := header{width: be.Uint32(b[8:12]), height: be.Uint32(b[12:16]), bits: int(b[16])}
	colorType := b[17]
	// 颜色类型：0 灰度、2 RGB、3 调色板、4 灰度 + alpha、6 RGBA；各自允许的位深。
	var depths []int
	switch colorType {
	case 0:
		h.channels, depths = 1, []int{1, 2, 4, 8, 16}
	case 2:
		h.channels, depths = 3, []int{8, 16}
	case 3:
		h.channels, h.palette, depths = 1, true, []int{1, 2, 4, 8}
	case 4:
		h.channels, depths = 1, []int{8, 16}
	case 6:
		h.channels, depths = 3, []int{8, 16}
	default:
		return header{}, invalidf("IHDR 的颜色类型 %d 不对", colorType)
	}
	if !slices.Contains(depths, h.bits) {
		return header{}, invalidf("颜色类型 %d 不能用 %d 位", colorType, h.bits)
	}

	physSeen := false
	pos := int64(8 + 8 + 13 + 4) // 签名、IHDR（长度、类型、内容、CRC）
	for range maxPNGChunks {
		c, err := s.bytes(pos, 8)
		if err != nil {
			return header{}, err
		}
		n := be.Uint32(c[0:4])
		if n > maxPNGChunkLen {
			return header{}, invalidf("块的长度 %d 超出范围", n)
		}
		switch string(c[4:8]) {
		case "IDAT", "IEND":
			return h, nil
		case "pHYs":
			if !physSeen && n >= 9 {
				physSeen = true
				p, err := s.bytes(pos+8, 9)
				if err != nil {
					return header{}, err
				}
				// X 方向每单位的像素数；单位 1 是米，0 是未知（只表示长宽比）。
				if p[8] == 1 {
					h.dpi = roundDPI(float64(be.Uint32(p[0:4])) * cmPerInch / 100)
				}
			}
		}
		pos += 12 + int64(n) // 长度、类型、内容、CRC
	}
	return header{}, invalidf("IDAT 之前的块超过 %d 个", maxPNGChunks)
}
