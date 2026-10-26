package imagehdr

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// BMP、GIF、WebP（M11-7）：拼几种文件头核对宽高、DPI、颜色、多帧和上传时拒绝的写法。真实文件在样本目录里。

// bmpSpec 是 buildBMP 的参数：DIB 头长度（0 当 40；12 是 BITMAPCOREHEADER）、宽高（高为负表示从上往下存）、
// 每像素位数、压缩方式、像素/米、调色板（R、G、B）、V4 起的颜色掩码。
type bmpSpec struct {
	infoLen uint32
	w, h    int32
	bpp     uint16
	comp    uint32
	ppm     int32
	palette [][3]byte
	masks   [4]uint32
}

// buildBMP 拼一个像素全是 0 的 BMP（数据完整，golang.org/x/image/bmp 能解的写法就能解）。
func buildBMP(sp bmpSpec) []byte {
	le := binary.LittleEndian
	if sp.infoLen == 0 {
		sp.infoLen = 40
	}
	var pal []byte
	for _, c := range sp.palette {
		pal = append(pal, c[2], c[1], c[0], 0)
	}
	rows := sp.h
	if rows < 0 {
		rows = -rows
	}
	pix := make([]byte, (int(sp.w)*int(sp.bpp)+31)/32*4*int(rows))
	offset := 14 + sp.infoLen + uint32(len(pal))
	b := le.AppendUint32([]byte("BM"), offset+uint32(len(pix)))
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, offset)
	dib := make([]byte, sp.infoLen)
	le.PutUint32(dib, sp.infoLen)
	if sp.infoLen == 12 {
		le.PutUint16(dib[4:], uint16(sp.w))
		le.PutUint16(dib[6:], uint16(sp.h))
		le.PutUint16(dib[8:], 1)
		le.PutUint16(dib[10:], sp.bpp)
	} else {
		le.PutUint32(dib[4:], uint32(sp.w))
		le.PutUint32(dib[8:], uint32(sp.h))
		le.PutUint16(dib[12:], 1)
		le.PutUint16(dib[14:], sp.bpp)
		le.PutUint32(dib[16:], sp.comp)
		le.PutUint32(dib[24:], uint32(sp.ppm))
		le.PutUint32(dib[28:], uint32(sp.ppm))
		le.PutUint32(dib[32:], uint32(len(sp.palette)))
		if sp.infoLen >= 56 {
			for i, m := range sp.masks {
				le.PutUint32(dib[40+4*i:], m)
			}
		}
	}
	return append(append(append(b, dib...), pal...), pix...)
}

var (
	grayPal2  = [][3]byte{{0, 0, 0}, {255, 255, 255}}
	grayPal4  = [][3]byte{{0, 0, 0}, {85, 85, 85}, {170, 170, 170}, {255, 255, 255}}
	colorPal4 = [][3]byte{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 255}}
)

// gifLZW 是 n 个 0 号像素的 LZW 数据（码长 2，按 GIF 的子块切好、带结束的 0）：每个清除码之后只写 2 个像素，码宽一直是
// 3 位，不用真的压缩（字节固定，不随 Go 的压缩器变）。
func gifLZW(n int) []byte {
	var codes []uint32
	for i := 0; i < n; i += 2 {
		codes = append(codes, 4) // 清除
		for range min(2, n-i) {
			codes = append(codes, 0)
		}
	}
	codes = append(codes, 5) // 结束
	var data []byte
	var acc, bits uint32
	for _, c := range codes {
		acc |= c << bits
		for bits += 3; bits >= 8; bits -= 8 {
			data = append(data, byte(acc))
			acc >>= 8
		}
	}
	if bits > 0 {
		data = append(data, byte(acc))
	}
	out := []byte{2}
	for len(data) > 0 {
		k := min(255, len(data))
		out = append(append(out, byte(k)), data[:k]...)
		data = data[k:]
	}
	return append(out, 0)
}

// buildGIF 拼一个 w×h 画布、4 色全局调色板的 GIF89a；每帧是 {左, 上, 宽, 高}，像素都是 0 号色，帧前有图形控制扩展。
func buildGIF(w, h uint16, pal [][3]byte, frames ...[4]uint16) []byte {
	le := binary.LittleEndian
	b := le.AppendUint16([]byte("GIF89a"), w)
	b = le.AppendUint16(b, h)
	b = append(b, 0x81, 0, 0) // 全局调色板，4 项
	for _, c := range pal {
		b = append(b, c[0], c[1], c[2])
	}
	b = append(b, 0x21, 0xFE, 3, 'h', 'i', '!', 0) // 注释
	for _, f := range frames {
		b = append(b, 0x21, 0xF9, 4, 0, 10, 0, 0, 0)
		b = append(b, 0x2C)
		for _, v := range f {
			b = le.AppendUint16(b, v)
		}
		b = append(append(b, 0), gifLZW(int(f[2])*int(f[3]))...)
	}
	return append(b, 0x3B)
}

// webpChunk 是一个 RIFF 块（长度补成偶数）。
func webpChunk(id string, data []byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte(id), uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func buildWebP(chunks ...[]byte) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c...)
	}
	b := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(4+len(body)))
	return append(append(b, "WEBP"...), body...)
}

// vp8Frame 是只有帧头的有损图像（关键帧）。
func vp8Frame(w, h uint16) []byte {
	b := []byte{0x10, 0x02, 0x00, 0x9D, 0x01, 0x2A}
	b = binary.LittleEndian.AppendUint16(b, w)
	b = binary.LittleEndian.AppendUint16(b, h)
	return webpChunk("VP8 ", append(b, 0, 0, 0, 0))
}

// vp8lFrame 是只有头的无损图像。
func vp8lFrame(w, h uint32) []byte {
	v := (w - 1) | (h-1)<<14
	return webpChunk("VP8L", binary.LittleEndian.AppendUint32([]byte{0x2F}, v))
}

// vp8x 是扩展头：标记（透明 0x10、动画 0x02）、画布宽高。
func vp8x(flags byte, w, h uint32) []byte {
	b := []byte{flags, 0, 0, 0, byte(w - 1), byte((w - 1) >> 8), byte((w - 1) >> 16), byte(h - 1), byte((h - 1) >> 8), byte((h - 1) >> 16)}
	return webpChunk("VP8X", b)
}

func TestParseNewFormats(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Info
	}{
		// BMP：像素/米换算成 DPI；从上往下存的高是负数；调色板全是灰色按灰度（1 位黑白），否则彩色；24、32 位彩色。
		{"BMP 24 位 300 DPI", buildBMP(bmpSpec{w: 30, h: 20, bpp: 24, ppm: 11811}), Info{BMP, 30, 20, 300, ColorColor, 1}},
		{"BMP 从上往下存", buildBMP(bmpSpec{w: 30, h: -20, bpp: 24, ppm: 7874}), Info{BMP, 30, 20, 200, ColorColor, 1}},
		{"BMP 1 位黑白", buildBMP(bmpSpec{w: 30, h: 20, bpp: 1, palette: grayPal2}), Info{BMP, 30, 20, 0, ColorBW, 1}},
		{"BMP 8 位灰度调色板", buildBMP(bmpSpec{w: 30, h: 20, bpp: 8, palette: grayPal4}), Info{BMP, 30, 20, 0, ColorGray, 1}},
		{"BMP 4 位彩色调色板", buildBMP(bmpSpec{w: 30, h: 20, bpp: 4, palette: colorPal4}), Info{BMP, 30, 20, 0, ColorColor, 1}},
		{"BMP OS/2 文件头", buildBMP(bmpSpec{infoLen: 12, w: 30, h: 20, bpp: 24}), Info{BMP, 30, 20, 0, ColorColor, 1}},
		// GIF：画布的宽高；调色板全是灰色按灰度。
		{"GIF 彩色", buildGIF(16, 9, colorPal4, [4]uint16{0, 0, 16, 9}), Info{GIF, 16, 9, 0, ColorColor, 1}},
		{"GIF 灰度", buildGIF(16, 9, grayPal4, [4]uint16{0, 0, 16, 9}), Info{GIF, 16, 9, 0, ColorGray, 1}},
		// WebP：有损、无损、扩展头（带 ICCP 和透明度）。
		{"WebP 有损", buildWebP(vp8Frame(300, 200)), Info{WebP, 300, 200, 0, ColorColor, 1}},
		{"WebP 无损", buildWebP(vp8lFrame(300, 200)), Info{WebP, 300, 200, 0, ColorColor, 1}},
		{"WebP 扩展、有损带透明", buildWebP(vp8x(0x10, 300, 200), webpChunk("ICCP", []byte{1, 2, 3}), webpChunk("ALPH", []byte{0, 0}), vp8Frame(300, 200)),
			Info{WebP, 300, 200, 0, ColorColor, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := parseBytes(c.data, DefaultMaxPixels)
			if err != nil {
				t.Fatal(err)
			}
			if info != c.want {
				t.Errorf("\n  得到 %+v\n  应为 %+v", info, c.want)
			}
		})
	}
}

func TestParseNewFormatErrors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		kind error
	}{
		{"GIF 两帧", buildGIF(8, 8, colorPal4, [4]uint16{0, 0, 8, 8}, [4]uint16{0, 0, 4, 4}), ErrMultiPage},
		{"WebP 动画标记", buildWebP(vp8x(0x02, 8, 8), webpChunk("ANIM", make([]byte, 6))), ErrMultiPage},
		{"WebP 没标动画但有 ANMF", buildWebP(vp8x(0, 8, 8), webpChunk("ANMF", make([]byte, 16))), ErrMultiPage},
		{"RIFF 但不是 WebP", append([]byte("RIFF\x04\x00\x00\x00WAVE"), make([]byte, 8)...), ErrUnsupported},
		{"HEIC", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), ErrUnsupported},
		{"BMP 截断", []byte("BM\x00\x00\x00\x00\x00\x00"), ErrInvalid},
		{"GIF 截在画布描述里", []byte("GIF89a\x01\x00\x01\x00"), ErrInvalid},
		{"GIF 没有图像", buildGIF(8, 8, colorPal4), ErrInvalid},
		{"GIF 帧超出画布", buildGIF(8, 8, colorPal4, [4]uint16{4, 4, 8, 8}), ErrInvalid},
		{"RIFF 只有 8 字节", []byte("RIFF\x00\x00\x00\x00"), ErrInvalid},
		{"WebP 图像和画布不一致", buildWebP(vp8x(0, 8, 8), vp8Frame(9, 8)), ErrInvalid},
		{"WebP 没有图像", buildWebP(vp8x(0, 8, 8)), ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseBytes(c.data, 0)
			if !errors.Is(err, c.kind) {
				t.Fatalf("应为 %v，得到 %v", c.kind, err)
			}
		})
	}
	// 多帧的提示说清楚是哪种。
	_, err := parseBytes(buildGIF(8, 8, colorPal4, [4]uint16{0, 0, 8, 8}, [4]uint16{0, 0, 8, 8}), 0)
	if err == nil || !strings.Contains(err.Error(), "多帧（动画）GIF") || !strings.Contains(err.Error(), "保存成单张") {
		t.Errorf("多帧 GIF 的提示：%v", err)
	}
}

// 上传时拒绝 Go 解码不了的写法（夹具之外的几种）；能解的照常通过。
func TestParseUploadNewFormats(t *testing.T) {
	v4 := func(bpp uint16, comp uint32, masks [4]uint32) []byte {
		return buildBMP(bmpSpec{infoLen: 108, w: 8, h: 8, bpp: bpp, comp: comp, masks: masks})
	}
	cases := []struct {
		name, want string
		data       []byte
	}{
		{"BMP 32 位 V4 位域默认掩码", "", v4(32, 3, [4]uint32{0xFF0000, 0xFF00, 0xFF, 0xFF000000})},
		{"BMP 32 位 V4 位域其他掩码", "位域", v4(32, 3, [4]uint32{0xFF, 0xFF00, 0xFF0000, 0xFF000000})},
		{"BMP 16 位", "每像素 16 位的 BMP", buildBMP(bmpSpec{w: 8, h: 8, bpp: 16})},
		{"BMP RLE4", "RLE 压缩的 BMP", buildBMP(bmpSpec{w: 8, h: 8, bpp: 4, comp: 2, palette: colorPal4})},
		{"BMP 内嵌 PNG", "内嵌 PNG 的 BMP", buildBMP(bmpSpec{w: 8, h: 8, bpp: 0, comp: 5})},
		{"BMP 8 位", "", buildBMP(bmpSpec{w: 8, h: 8, bpp: 8, palette: colorPal4})},
		{"GIF 单帧", "", buildGIF(8, 8, colorPal4, [4]uint16{0, 0, 8, 8})},
		{"WebP 无损", "", buildWebP(vp8lFrame(8, 8))},
		{"WebP 扩展、无损带透明", "", buildWebP(vp8x(0x10, 8, 8), vp8lFrame(8, 8))},
		{"WebP 有透明度但没标", "透明度标记和透明度数据对不上", buildWebP(vp8x(0, 8, 8), webpChunk("ALPH", []byte{0, 0}), vp8Frame(8, 8))},
		{"WebP 透明度配无损", "透明度数据配无损图像", buildWebP(vp8x(0x10, 8, 8), webpChunk("ALPH", []byte{0, 0}), vp8lFrame(8, 8))},
		{"WebP 透明度压缩方式不认识", "透明度压缩方式不认识", buildWebP(vp8x(0x10, 8, 8), webpChunk("ALPH", []byte{2, 0}), vp8Frame(8, 8))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseBytes(c.data, 0); err != nil {
				t.Fatalf("Parse 应该通过：%v", err)
			}
			_, err := parseUploadBytes(c.data)
			if c.want == "" {
				if err != nil {
					t.Fatalf("应该通过：%v", err)
				}
				return
			}
			if !errors.Is(err, ErrUnsupportedEncoding) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("应该拒绝（%s），得到 %v", c.want, err)
			}
		})
	}
}
