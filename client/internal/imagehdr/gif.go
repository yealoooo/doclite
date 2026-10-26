package imagehdr

import "encoding/binary"

// maxGIFBlocks：第一帧之前、之后各最多读这么多个扩展块（一共最多两倍）。
const maxGIFBlocks = 4096

// readGIF：13 字节的文件头和逻辑画布描述（宽高、全局调色板），然后逐块往后读：扩展块（0x21）跳过，图像（0x2C）读
// 9 字节的描述、局部调色板、LZW 码长，再跳过数据子块。第一帧之后再找到一个图像就是多帧（动画），找到结尾（0x3B）、
// 读到文件末尾或者认不出的字节都算单帧（和 image/gif 的 Decode 一样只看第一帧）。宽高是逻辑画布的。
//
// 第一帧之前的结构问题按 image/gif 的规则算读不出来（[ErrInvalid]）：认不出的扩展块、图形控制扩展的长度不对、
// 没有调色板、LZW 码长不在 2–8、帧超出画布、没有图像。上传时另外拒绝第一帧没有占满画布的（header.reject：Go 解出来的
// 只是那一块，看图、导出都按整张画布，不另外合成）。
func readGIF(s *source) (header, error) {
	le := binary.LittleEndian
	b, err := s.bytes(0, 13)
	if err != nil {
		return header{}, err
	}
	h := header{width: uint32(le.Uint16(b[6:8])), height: uint32(le.Uint16(b[8:10]))}
	pos := int64(13)
	var global []byte
	globalBits := 0
	if b[10]&0x80 != 0 {
		globalBits = int(b[10]&7) + 1
		if global, err = s.bytes(pos, 3<<globalBits); err != nil {
			return header{}, err
		}
		pos += int64(len(global))
	}

	// 第一帧之后的问题都不管（image/gif 只解第一帧）：当单帧。
	frames := 0
	stop := func(err error) (header, error) {
		if frames > 0 {
			return h, nil
		}
		return header{}, err
	}
	for range 2 * maxGIFBlocks {
		c, err := s.bytes(pos, 1)
		if err != nil {
			return stop(err)
		}
		pos++
		switch c[0] {
		case 0x21: // 扩展块：标签、（固定长度的头、）数据子块
			l, err := s.bytes(pos, 1)
			if err != nil {
				return stop(err)
			}
			pos++
			switch l[0] {
			case 0xF9: // 图形控制：长度 4、4 字节、结束 0（没有子块）
				g, err := s.bytes(pos, 6)
				if err != nil {
					return stop(err)
				}
				if g[0] != 4 || g[5] != 0 {
					return stop(invalidf("图形控制扩展的长度不对"))
				}
				pos += 6
				continue
			case 0x01: // 文字：12 字节的头（连长度共 13 字节）
				pos += 13
			case 0xFF: // 应用：长度、标识
				n, err := s.bytes(pos, 1)
				if err != nil {
					return stop(err)
				}
				pos += 1 + int64(n[0])
			case 0xFE: // 注释
			default:
				return stop(invalidf("认不出的扩展块 0x%02X", l[0]))
			}
			if pos, err = skipSubBlocks(s, pos); err != nil {
				return stop(err)
			}
		case 0x2C: // 图像
			if frames > 0 {
				h.multiPage = true
				return h, nil
			}
			d, err := s.bytes(pos, 9)
			if err != nil {
				return header{}, err
			}
			pos += 9
			left, top := uint32(le.Uint16(d[0:2])), uint32(le.Uint16(d[2:4]))
			w, ht := uint32(le.Uint16(d[4:6])), uint32(le.Uint16(d[6:8]))
			if left+w > h.width || top+ht > h.height {
				return header{}, invalidf("第一帧（%d×%d，在 %d,%d）超出了画布 %d×%d", w, ht, left, top, h.width, h.height)
			}
			if left != 0 || top != 0 || w != h.width || ht != h.height {
				h.reject = "第一帧没有占满画布的 GIF"
			}
			table, tableBits := global, globalBits
			if d[8]&0x80 != 0 {
				tableBits = int(d[8]&7) + 1
				if table, err = s.bytes(pos, 3<<tableBits); err != nil {
					return header{}, err
				}
				pos += int64(len(table))
			}
			if table == nil {
				return header{}, invalidf("第一帧没有调色板")
			}
			lit, err := s.bytes(pos, 1)
			if err != nil {
				return header{}, err
			}
			if lit[0] < 2 || lit[0] > 8 {
				return header{}, invalidf("LZW 码长 %d 不对", lit[0])
			}
			// 颜色：只有调色板，全是灰色时按单通道算（2 色的是 1 位）。
			h.bits, h.channels, h.palette = tableBits, 3, true
			if grayPalette(table, 3) {
				h.channels, h.palette = 1, false
			}
			frames++
			if pos, err = skipSubBlocks(s, pos+1); err != nil {
				return h, nil // 第一帧的数据不完整：解码时按损坏报
			}
		case 0x3B: // 结尾
			return stop(invalidf("没有图像"))
		default:
			return stop(invalidf("认不出的块 0x%02X", c[0]))
		}
	}
	return stop(invalidf("扩展块超过 %d 个", 2*maxGIFBlocks))
}

// skipSubBlocks 跳过从 pos 起的数据子块（每块一个长度字节加内容，长度 0 结束），返回结束之后的位置。按 64 KiB
// 一段读，不逐字节读文件。
func skipSubBlocks(s *source, pos int64) (int64, error) {
	var win []byte
	var winOff int64
	for {
		if pos < winOff || pos >= winOff+int64(len(win)) {
			n := min(int64(headBytes), s.size-pos)
			if n <= 0 {
				return 0, invalidf("数据子块没有结束，文件不完整")
			}
			b, err := s.bytes(pos, int(n))
			if err != nil {
				return 0, err
			}
			win, winOff = b, pos
		}
		n := win[pos-winOff]
		pos++
		if n == 0 {
			return pos, nil
		}
		pos += int64(n)
	}
}
