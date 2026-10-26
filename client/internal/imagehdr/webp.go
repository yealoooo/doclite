package imagehdr

import "encoding/binary"

// maxWebPChunks：VP8X 之后最多看这么多个块（ICCP、EXIF、XMP、ALPH……）找图像。
const maxWebPChunks = 1024

// readWebP：RIFF 头（"RIFF"、长度、"WEBP"）之后的第一个块决定写法：
//   - "VP8 "：有损，帧头（关键帧、起始码 9D 01 2A）里 14 位的宽高；
//   - "VP8L"：无损，签名 0x2F 之后 14 位的宽减一、高减一，版本号要是 0；
//   - "VP8X"：扩展，10 字节：标记（透明 0x10、动画 0x02）、24 位的画布宽减一、高减一。动画是多帧（[ErrMultiPage]）；
//     否则往后找 ALPH（透明度）和 VP8 / VP8L，图像的宽高要和画布一致。
//
// 块长按 RIFF 的规则补成偶数；只读块头和几字节的帧头，不看压缩数据。上传时另外拒绝 golang.org/x/image/webp 解不了的
// 透明度写法（header.reject）：VP8X 标了透明但有损图像前没有 ALPH，没标透明却有 ALPH，ALPH 配无损图像，ALPH 的
// 压缩方式不是 0、1。颜色都算 color，没有 DPI。
func readWebP(s *source) (header, error) {
	le := binary.LittleEndian
	b, err := s.bytes(4, 4)
	if err != nil {
		return header{}, err
	}
	end := min(8+int64(le.Uint32(b)), s.size)
	pos := int64(12)
	// next 读下一个块头：类型、长度、内容的位置；读完 pos 指向下一个块。
	next := func() (id string, n int64, data int64, err error) {
		if pos+8 > end {
			return "", 0, 0, invalidf("没有图像数据")
		}
		c, err := s.bytes(pos, 8)
		if err != nil {
			return "", 0, 0, err
		}
		n, data = int64(le.Uint32(c[4:8])), pos+8
		pos = data + n + n&1
		return string(c[0:4]), n, data, nil
	}
	h := header{channels: 3}
	id, n, data, err := next()
	if err != nil {
		return header{}, err
	}
	switch id {
	case "VP8 ":
		h.width, h.height, err = vp8Size(s, data, n)
		return h, err
	case "VP8L":
		h.width, h.height, err = vp8lSize(s, data, n)
		return h, err
	case "VP8X":
	default:
		return header{}, invalidf("第一个块是 %q，不是 VP8、VP8L、VP8X", id)
	}

	if n != 10 {
		return header{}, invalidf("VP8X 块的长度是 %d，不是 10", n)
	}
	x, err := s.bytes(data, 10)
	if err != nil {
		return header{}, err
	}
	h.width = 1 + (uint32(x[4]) | uint32(x[5])<<8 | uint32(x[6])<<16)
	h.height = 1 + (uint32(x[7]) | uint32(x[8])<<8 | uint32(x[9])<<16)
	if x[0]&0x02 != 0 {
		h.multiPage = true
		return h, nil
	}
	alphaFlag, alph := x[0]&0x10 != 0, false
	for range maxWebPChunks {
		id, n, data, err := next()
		if err != nil {
			return header{}, err
		}
		var w, ht uint32
		switch id {
		case "ANIM", "ANMF":
			h.multiPage = true
			return h, nil
		case "ALPH":
			alph = true
			m, err := s.bytes(data, 1)
			if err != nil {
				return header{}, err
			}
			if m[0]&3 > 1 {
				h.reject = "透明度压缩方式不认识的 WebP"
			}
			continue
		case "VP8 ":
			if w, ht, err = vp8Size(s, data, n); err != nil {
				return header{}, err
			}
			if alphaFlag != alph {
				h.reject = "透明度标记和透明度数据对不上的 WebP"
			}
		case "VP8L":
			if w, ht, err = vp8lSize(s, data, n); err != nil {
				return header{}, err
			}
			if alph {
				h.reject = "透明度数据配无损图像的 WebP"
			}
		default:
			continue
		}
		if w != h.width || ht != h.height {
			return header{}, invalidf("图像 %d×%d 和画布 %d×%d 不一致", w, ht, h.width, h.height)
		}
		return h, nil
	}
	return header{}, invalidf("VP8X 之后的块超过 %d 个", maxWebPChunks)
}

// vp8Size 读有损图像（VP8 关键帧）的宽高。
func vp8Size(s *source, data, n int64) (w, h uint32, err error) {
	if n < 10 {
		return 0, 0, invalidf("VP8 块只有 %d 字节", n)
	}
	b, err := s.bytes(data, 10)
	if err != nil {
		return 0, 0, err
	}
	if b[0]&1 != 0 {
		return 0, 0, invalidf("VP8 的第一帧不是关键帧")
	}
	if b[3] != 0x9D || b[4] != 0x01 || b[5] != 0x2A {
		return 0, 0, invalidf("VP8 的起始码不对")
	}
	le := binary.LittleEndian
	return uint32(le.Uint16(b[6:8]) & 0x3FFF), uint32(le.Uint16(b[8:10]) & 0x3FFF), nil
}

// vp8lSize 读无损图像（VP8L）的宽高：签名 0x2F，然后低位在前的 14 位宽减一、14 位高减一、1 位透明提示、3 位版本。
func vp8lSize(s *source, data, n int64) (w, h uint32, err error) {
	if n < 5 {
		return 0, 0, invalidf("VP8L 块只有 %d 字节", n)
	}
	b, err := s.bytes(data, 5)
	if err != nil {
		return 0, 0, err
	}
	if b[0] != 0x2F {
		return 0, 0, invalidf("VP8L 的签名不对")
	}
	v := binary.LittleEndian.Uint32(b[1:5])
	if v>>29 != 0 {
		return 0, 0, invalidf("VP8L 的版本号是 %d，不是 0", v>>29)
	}
	return 1 + v&0x3FFF, 1 + (v>>14)&0x3FFF, nil
}
