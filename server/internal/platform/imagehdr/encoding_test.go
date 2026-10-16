package imagehdr

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"hash/adler32"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 上传时拒绝的罕见编码（encoding.go）。
//
// 夹具在 testdata/encoding/：reject-* 上传应被拒绝，accept-* 是少见但导出做得了的写法（不能误伤）。都是 8×8 的最小
// 文件，由下面的 encodingFixtures 用代码拼出来（只有文件头和几十字节的假像素数据，不是能看的图），改了生成代码后
// 重新生成：
//
//	cd server && go test ./internal/platform/imagehdr/ -run TestEncodingFixtures -update
//
// 客户端那份（client/internal/imagehdr/testdata/encoding）是同样的文件，重新生成后复制过去。server 的导出
// （internal/export/bundle 的 TestUploadCheckMatchesExport）和上传的 API 测试也读这个目录：导出判为"不支持"的正好是
// 这里拒绝的。
var update = flag.Bool("update", false, "重新生成 testdata/encoding 里的夹具")

const encodingDir = "testdata/encoding"

// tiffWith 拼一个小端的经典 TIFF：8 字节文件头，data 紧跟其后（从第 8 字节开始，条带偏移按 8 + 位置写），然后是
// IFD，放不进条目的值接在 IFD 后面。
func tiffWith(data []byte, entries ...tiffEntry) []byte {
	le := binary.LittleEndian
	b := append([]byte("II*\x00"), 0, 0, 0, 0)
	b = append(b, data...)
	if len(b)%2 == 1 {
		b = append(b, 0)
	}
	le.PutUint32(b[4:], uint32(len(b)))
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b tiffEntry) int { return int(a.tag) - int(b.tag) })
	extAt := len(b) + 2 + 12*len(entries) + 4
	var ext []byte
	b = le.AppendUint16(b, uint16(len(entries)))
	for _, e := range entries {
		var v []byte
		for _, x := range e.vals {
			switch e.typ {
			case typeByte:
				v = append(v, byte(x))
			case typeShort:
				v = le.AppendUint16(v, uint16(x))
			default:
				v = le.AppendUint32(v, x)
			}
		}
		count := len(e.vals)
		if e.typ == typeRational {
			count /= 2
		}
		b = le.AppendUint16(b, e.tag)
		b = le.AppendUint16(b, e.typ)
		b = le.AppendUint32(b, uint32(count))
		if len(v) <= 4 {
			b = append(b, append(v, make([]byte, 4-len(v))...)...)
		} else {
			b = le.AppendUint32(b, uint32(extAt+len(ext)))
			ext = append(ext, v...)
		}
	}
	b = le.AppendUint32(b, 0)
	return append(b, ext...)
}

// page8 是 8×8、一个条带（数据在第 8 字节）的基本标签：每个样本 bits 位、spp 个样本、压缩方式、颜色。
func page8(data []byte, bits, spp, comp, photo uint32, extra ...tiffEntry) []byte {
	bs := make([]uint32, spp)
	for i := range bs {
		bs[i] = bits
	}
	return tiffWith(data, append([]tiffEntry{
		short(tagImageWidth, 8), short(tagImageLength, 8), short(tagBitsPerSample, bs...),
		short(tagCompression, comp), short(tagPhotometric, photo), short(tagSamplesPerPix, spp),
		short(tagRowsPerStrip, 8), long(tagStripOffsets, 8), long(tagStripByteCounts, uint32(len(data))),
	}, extra...)...)
}

// jpegWith 是只有文件头的 8×8 JPEG：SOI、SOF（标记、精度、分量数）、SOS 和两字节假数据、EOI。
func jpegWith(marker, precision, comps byte) []byte {
	p := []byte{precision, 0, 8, 0, 8, comps}
	sos := []byte{comps}
	for i := range comps {
		p = append(p, i+1, 0x11, 0)
		sos = append(sos, i+1, 0)
	}
	sos = append(sos, 0, 63, 0)
	b := []byte{0xFF, 0xD8}
	b = append(b, segment(marker, p)...)
	b = append(b, segment(0xDA, sos)...)
	return append(b, 0x12, 0x34, 0xFF, 0xD9)
}

// deflate 是 raw 的 zlib 流，用不压缩的存储块（每次生成的字节都一样，不随 Go 版本的压缩器变）。
func deflate(raw []byte) []byte {
	b := []byte{0x78, 0x01, 0x01} // zlib 头；最后一块、不压缩
	b = binary.LittleEndian.AppendUint16(b, uint16(len(raw)))
	b = binary.LittleEndian.AppendUint16(b, ^uint16(len(raw)))
	b = append(b, raw...)
	return binary.BigEndian.AppendUint32(b, adler32.Checksum(raw))
}

// encodingFixtures 是 testdata/encoding 里的文件：名字、内容、拒绝时提示里应有的文字（accept-* 为空）。
func encodingFixtures() []struct {
	name, want string
	data       []byte
} {
	zero := func(n int) []byte { return make([]byte, n) }
	return []struct {
		name, want string
		data       []byte
	}{
		{"reject-jpeg-lossless.jpg", "无损压缩的 JPEG", jpegWith(0xC3, 8, 1)},
		{"reject-jpeg-arith.jpg", "算术编码的 JPEG", jpegWith(0xC9, 8, 3)},
		{"reject-jpeg-12bit.jpg", "12 位精度的 JPEG", jpegWith(0xC1, 12, 1)},
		{"reject-tiff-jpeg2000.tif", "JPEG 2000 压缩的 TIFF", page8(zero(16), 8, 1, 34712, 1)},
		{"reject-tiff-jbig.tif", "JBIG 压缩的 TIFF", page8(zero(8), 1, 1, 34661, 0)},
		{"reject-tiff-ojpeg-noif.tif", "旧式 JPEG 压缩、找不到完整 JPEG", page8(zero(16), 8, 1, 6, 1)},
		{"reject-tiff-jpeg-arith.tif", "里面是算术编码的 JPEG", page8(jpegWith(0xC9, 8, 1), 8, 1, 7, 1)},
		{"reject-tiff-float32.tif", "每个样本 32 位", page8(zero(256), 32, 1, 1, 1, short(tagSampleFormat, 3))},
		{"reject-tiff-cielab.tif", "CIELab 颜色的 TIFF", page8(zero(192), 8, 3, 1, 8)},
		{"reject-tiff-ycbcr-lzw.tif", "YCbCr 颜色、不是 JPEG 压缩", page8([]byte{0x80, 0, 0, 0}, 8, 3, 5, 6)},
		{"reject-tiff-lzw-old.tif", "旧式 LZW", page8([]byte{0, 1, 0, 0, 0, 0, 0, 0}, 8, 1, 5, 1)},
		{"reject-tiff-predictor3.tif", "预测器 3", page8(deflate(zero(64)), 8, 1, 8, 1, short(tagPredictor, 3))},
		{"reject-tiff-4bit-predictor.tif", "每个样本 4 位又用了差分预测器", page8(deflate(zero(32)), 4, 1, 8, 1, short(tagPredictor, 2))},
		{"reject-tiff-g4-tiled.tif", "分块存的 CCITT", tiffWith(zero(8),
			short(tagImageWidth, 8), short(tagImageLength, 8), short(tagBitsPerSample, 1), short(tagCompression, 4),
			short(tagPhotometric, 0), short(tagSamplesPerPix, 1), short(tagTileWidth, 16), short(tagTileLength, 16),
			long(tagTileOffsets, 8), long(tagTileByteCounts, 8))},
		{"accept-jpeg-sof1.jpg", "", jpegWith(0xC1, 8, 3)},
		{"accept-tiff-cmyk.tif", "", page8(zero(256), 8, 4, 1, 5)},
		{"accept-tiff-gray4.tif", "", page8(zero(32), 4, 1, 1, 1)},
		{"accept-tiff-16bit-predictor.tif", "", page8(deflate(zero(128)), 16, 1, 8, 1, short(tagPredictor, 2))},
		{"accept-tiff-planar-rgb.tif", "", tiffWith(zero(192),
			short(tagImageWidth, 8), short(tagImageLength, 8), short(tagBitsPerSample, 8, 8, 8), short(tagCompression, 1),
			short(tagPhotometric, 2), short(tagSamplesPerPix, 3), short(tagRowsPerStrip, 8), short(tagPlanarConfig, 2),
			long(tagStripOffsets, 8, 72, 136), long(tagStripByteCounts, 64, 64, 64))},
	}
}

func parseUploadBytes(b []byte) (Info, error) {
	return ParseUpload(bytes.NewReader(b), int64(len(b)), DefaultMaxPixels)
}

// 夹具：和生成代码一致；reject-* 被 ParseUpload 拒绝（提示里写明是什么写法），accept-* 通过；[Parse] 都照常通过
// （已经入库的旧文件不受影响）。
func TestEncodingFixtures(t *testing.T) {
	if *update {
		if err := os.MkdirAll(encodingDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]bool{}
	for _, f := range encodingFixtures() {
		names[f.name] = true
		t.Run(f.name, func(t *testing.T) {
			path := filepath.Join(encodingDir, f.name)
			if *update {
				if err := os.WriteFile(path, f.data, 0o644); err != nil {
					t.Fatal(err)
				}
			} else if disk, err := os.ReadFile(path); err != nil || !bytes.Equal(disk, f.data) {
				t.Fatalf("%s 和生成代码不一致（%v），用 -update 重新生成", path, err)
			}
			if _, err := parseBytes(f.data, DefaultMaxPixels); err != nil {
				t.Fatalf("Parse 应该通过：%v", err)
			}
			_, err := parseUploadBytes(f.data)
			if f.want == "" {
				if err != nil {
					t.Fatalf("应该通过：%v", err)
				}
				return
			}
			if !errors.Is(err, ErrUnsupportedEncoding) || !strings.Contains(err.Error(), f.want) ||
				!strings.HasPrefix(err.Error(), "不支持的图片格式（") || !strings.HasSuffix(err.Error(), "），请换个保存格式重扫") {
				t.Fatalf("应该拒绝（%s），得到 %v", f.want, err)
			}
		})
	}
	entries, err := os.ReadDir(encodingDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !names[e.Name()] {
			t.Errorf("%s 不是生成代码里的夹具", e.Name())
		}
	}
}

// 常见样本（server/testdata/samples）上传照常通过；多页 TIFF 仍是 ErrMultiPage。
func TestParseUploadSamples(t *testing.T) {
	for _, r := range readSampleTable(t) {
		info, err := parseUploadBytes(readSample(t, r.name))
		if r.multiPage {
			if !errors.Is(err, ErrMultiPage) {
				t.Errorf("%s：应该是多页 TIFF，得到 %v", r.name, err)
			}
			continue
		}
		if err != nil || info.Width != r.width {
			t.Errorf("%s：%+v %v", r.name, info, err)
		}
	}
}

// 夹具之外的几种写法（只拼文件头，不写文件）。
func TestParseUploadSynthetic(t *testing.T) {
	zero := func(n int) []byte { return make([]byte, n) }
	// 旧式 JPEG：JPEGInterchangeFormat 指向一段算术编码的 JPEG（在第 8 字节）。
	arith := jpegWith(0xC9, 8, 1)
	ojpeg := page8(arith, 8, 1, 6, 1, long(tagJPEGIF, 8), long(tagJPEGIFLength, uint32(len(arith))))
	okOJPEG := page8(jpegWith(0xC0, 8, 1), 8, 1, 6, 1, long(tagJPEGIF, 8), long(tagJPEGIFLength, uint32(len(arith))))
	cases := []struct {
		name, want string
		data       []byte
	}{
		{"分层 JPEG", "分层编码的 JPEG", jpegWith(0xC5, 8, 1)},
		{"2 个分量的 JPEG", "2 个颜色分量的 JPEG", jpegWith(0xC0, 8, 2)},
		{"渐进 JPEG", "", jpegWith(0xC2, 8, 3)},
		{"各通道位数不同", "各通道位数不同", tiffWith(zero(8), short(tagImageWidth, 8), short(tagImageLength, 8),
			short(tagBitsPerSample, 8, 8, 16), short(tagPhotometric, 2), short(tagSamplesPerPix, 3))},
		{"CMYK 以外的分色", "CMYK 以外的分色", page8(zero(256), 8, 4, 1, 5, short(tagInkSet, 2))},
		{"16 位调色板", "16 位调色板", page8(zero(128), 16, 1, 1, 3)},
		{"G3 非压缩模式", "CCITT 非压缩模式", page8(zero(8), 1, 1, 3, 0, long(tagT4Options, 2))},
		{"G4 二维编码（正常）", "", page8(zero(8), 1, 1, 4, 0)},
		{"条带太多", "条带或分块太多", tiffWith(zero(8), short(tagImageWidth, 8), long(tagImageLength, 300000),
			short(tagBitsPerSample, 8), short(tagPhotometric, 1), short(tagRowsPerStrip, 1))},
		{"4 位分平面", "4 位、分平面存或带额外通道", page8(zero(96), 4, 3, 8, 2, short(tagPlanarConfig, 2))},
		{"8 位带透明（要转换，能做）", "", page8(zero(256), 8, 4, 8, 2, short(tagExtraSamples, 2))},
		{"旧式 JPEG 里是算术编码", "里面是算术编码的 JPEG", ojpeg},
		{"旧式 JPEG 正常", "", okOJPEG},
		{"JPEG 压缩又分平面", "JPEG 压缩、分平面存", page8(jpegWith(0xC0, 8, 1), 8, 3, 7, 2, short(tagPlanarConfig, 2))},
		{"JPEG 压缩 16 位", "JPEG 压缩、每个样本 16 位", page8(jpegWith(0xC1, 12, 1), 16, 1, 7, 1)},
		{"16 位 RGBA 的 PNG（能转换）", "", buildPNG(8, 8, 16, 6)},
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
