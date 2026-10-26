package imagehdr

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 样本目录（client/testdata/samples，server/testdata/samples 的副本），清单和期望值在它的 README.md 里。
const samplesDir = "../../testdata/samples"

type sampleRow struct {
	name               string
	format             Format
	width, height, dpi int
	color              ColorMode
	orientation        int
	multiPage          bool
}

// readSampleTable 读 README.md 里的清单表格（以 "| `" 开头的行）。
func readSampleTable(t *testing.T) []sampleRow {
	t.Helper()
	f, err := os.Open(filepath.Join(samplesDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []sampleRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) < 7 {
			t.Fatalf("README 的表格行列数不对：%q", line)
		}
		w, h, ok := strings.Cut(cells[2], "×")
		r := sampleRow{
			name:        strings.Trim(cells[0], "`"),
			format:      Format(strings.ToLower(cells[1])),
			width:       atoi(t, w),
			height:      atoi(t, h),
			dpi:         atoi(t, cells[3]),
			color:       ColorMode(cells[4]),
			orientation: atoi(t, cells[5]),
			multiPage:   cells[6] == "多页",
		}
		if !ok || (cells[6] != "通过" && cells[6] != "多页") {
			t.Fatalf("README 的表格行写法不对：%q", line)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("README 里的 %q 不是整数", s)
	}
	return n
}

// countingReader 记下读了多少字节。
type countingReader struct {
	r     *os.File
	bytes int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.bytes += int64(len(p))
	return c.r.ReadAt(p, off)
}

// 样本文件的解析结果和 README 的表格一致；目录里的每个文件都在表里；只读了开头一段（不读整个文件）。
func TestSamplesMatchReadme(t *testing.T) {
	rows := readSampleTable(t)
	listed := map[string]bool{}
	for _, r := range rows {
		listed[r.name] = true
		t.Run(r.name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(samplesDir, r.name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			cr := &countingReader{r: f}
			info, err := Parse(cr, st.Size(), DefaultMaxPixels)
			if cr.bytes > headBytes+4096 {
				t.Errorf("读了 %d 字节，应该只读开头一段（文件 %d 字节）", cr.bytes, st.Size())
			}
			if r.multiPage {
				if !errors.Is(err, ErrMultiPage) {
					t.Fatalf("应该是多页 TIFF，得到 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := Info{Format: r.format, Width: r.width, Height: r.height, DPI: r.dpi, ColorMode: r.color, Orientation: r.orientation}
			if info != want {
				t.Errorf("解析结果\n  得到 %+v\n  README %+v", info, want)
			}
		})
	}
	entries, err := os.ReadDir(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "README.md" && !listed[e.Name()] {
			t.Errorf("%s 不在 README 的表格里", e.Name())
		}
	}
	if len(rows) < 10 {
		t.Errorf("README 的表格只读到 %d 行", len(rows))
	}
}

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(samplesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseBytes(b []byte, maxPixels int64) (Info, error) {
	return Parse(bytes.NewReader(b), int64(len(b)), maxPixels)
}

// ---------------------------------------------------------------------------
// 拼字节的助手

type appendOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// tiffEntry：BYTE、SHORT、LONG 的值，或 RATIONAL 的分子分母（成对）。
type tiffEntry struct {
	tag, typ uint16
	vals     []uint32
}

func short(tag uint16, v ...uint32) tiffEntry { return tiffEntry{tag, typeShort, v} }
func long(tag uint16, v ...uint32) tiffEntry  { return tiffEntry{tag, typeLong, v} }
func rational(tag uint16, num, den uint32) tiffEntry {
	return tiffEntry{tag, typeRational, []uint32{num, den}}
}

// buildTIFF 拼一个经典 TIFF：文件头、依次的 IFD（next 指针串起来），放不进条目的值紧跟在各自的 IFD 后面。
// 没有像素数据（不需要）。
func buildTIFF(bo appendOrder, ifds ...[]tiffEntry) []byte {
	b := []byte("II")
	if bo == binary.BigEndian {
		b = []byte("MM")
	}
	b = bo.AppendUint16(b, 42)
	b = bo.AppendUint32(b, 8)
	prevNext := -1
	for _, entries := range ifds {
		start := len(b)
		if prevNext >= 0 {
			bo.PutUint32(b[prevNext:], uint32(start))
		}
		b = bo.AppendUint16(b, uint16(len(entries)))
		extAt := start + 2 + 12*len(entries) + 4
		var ext []byte
		for _, e := range entries {
			var data []byte
			count := len(e.vals)
			for _, v := range e.vals {
				switch e.typ {
				case typeByte:
					data = append(data, byte(v))
				case typeShort:
					data = bo.AppendUint16(data, uint16(v))
				default:
					data = bo.AppendUint32(data, v)
				}
			}
			if e.typ == typeRational {
				count /= 2
			}
			b = bo.AppendUint16(b, e.tag)
			b = bo.AppendUint16(b, e.typ)
			b = bo.AppendUint32(b, uint32(count))
			if len(data) <= 4 {
				b = append(b, append(data, make([]byte, 4-len(data))...)...)
			} else {
				b = bo.AppendUint32(b, uint32(extAt+len(ext)))
				ext = append(ext, data...)
			}
		}
		prevNext = len(b)
		b = bo.AppendUint32(b, 0)
		b = append(b, ext...)
	}
	return b
}

// 一页 200×100 的 8 位灰度 TIFF 的基本标签。
func grayPage(extra ...tiffEntry) []tiffEntry {
	return append([]tiffEntry{
		short(tagImageWidth, 200), short(tagImageLength, 100),
		short(tagBitsPerSample, 8), short(tagPhotometric, 1), short(tagSamplesPerPix, 1),
	}, extra...)
}

func segment(marker byte, payload []byte) []byte {
	b := []byte{0xFF, marker}
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)+2))
	return append(b, payload...)
}

func jfif(units byte, density uint16) []byte {
	p := []byte("JFIF\x00\x01\x02")
	p = append(p, units)
	p = binary.BigEndian.AppendUint16(p, density)
	p = binary.BigEndian.AppendUint16(p, density)
	return segment(0xE0, append(p, 0, 0))
}

func exif(tiff []byte) []byte { return segment(0xE1, append([]byte("Exif\x00\x00"), tiff...)) }

// sof 是 SOF0：8 位，高、宽，分量数。
func sof(w, h uint16, comps byte) []byte {
	p := []byte{8}
	p = binary.BigEndian.AppendUint16(p, h)
	p = binary.BigEndian.AppendUint16(p, w)
	p = append(p, comps)
	for i := range comps {
		p = append(p, i+1, 0x11, 0)
	}
	return segment(0xC0, p)
}

func buildJPEG(parts ...[]byte) []byte {
	b := []byte{0xFF, 0xD8}
	for _, p := range parts {
		b = append(b, p...)
	}
	return append(b, 0xFF, 0xD9)
}

func pngChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	return append(b, 0, 0, 0, 0) // CRC 不校验
}

func buildPNG(w, h uint32, depth, colorType byte, chunks ...[]byte) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, depth, colorType, 0, 0, 0)
	b := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	for _, c := range chunks {
		b = append(b, c...)
	}
	b = append(b, pngChunk("IDAT", nil)...)
	return append(b, pngChunk("IEND", nil)...)
}

func phys(ppu uint32, unit byte) []byte {
	p := binary.BigEndian.AppendUint32(nil, ppu)
	p = binary.BigEndian.AppendUint32(p, ppu)
	return pngChunk("pHYs", append(p, unit))
}

// ---------------------------------------------------------------------------

// 用代码拼的文件：样本里没有的方向、分辨率写法、颜色模式、多页和缩略图。
func TestParseSynthetic(t *testing.T) {
	le, be := binary.LittleEndian, binary.BigEndian
	exifIFD := func(entries ...tiffEntry) []byte { return buildTIFF(le, entries) }
	cases := []struct {
		name string
		data []byte
		want Info
	}{
		// TIFF：方向 5–8 交换宽高；大端；分辨率的几种写法。
		{"TIFF 方向 6", buildTIFF(le, grayPage(short(tagOrientation, 6), rational(tagXResolution, 300, 1))),
			Info{TIFF, 100, 200, 300, ColorGray, 6}},
		{"TIFF 大端 方向 8", buildTIFF(be, grayPage(short(tagOrientation, 8))),
			Info{TIFF, 100, 200, 0, ColorGray, 8}},
		{"TIFF 方向 3 不交换", buildTIFF(le, grayPage(short(tagOrientation, 3))),
			Info{TIFF, 200, 100, 0, ColorGray, 3}},
		{"TIFF 方向不认识按 1", buildTIFF(le, grayPage(short(tagOrientation, 9))),
			Info{TIFF, 200, 100, 0, ColorGray, 1}},
		{"TIFF 分辨率单位厘米", buildTIFF(le, grayPage(rational(tagXResolution, 118, 1), short(tagResolutionUnit, 3))),
			Info{TIFF, 200, 100, 300, ColorGray, 1}},
		{"TIFF 分辨率单位无", buildTIFF(le, grayPage(rational(tagXResolution, 300, 1), short(tagResolutionUnit, 1))),
			Info{TIFF, 200, 100, 0, ColorGray, 1}},
		{"TIFF 分辨率四舍五入", buildTIFF(le, grayPage(rational(tagXResolution, 2005, 10))),
			Info{TIFF, 200, 100, 201, ColorGray, 1}},
		{"TIFF 分辨率写成 SHORT", buildTIFF(le, grayPage(short(tagXResolution, 200))),
			Info{TIFF, 200, 100, 200, ColorGray, 1}},
		{"TIFF 分辨率分母为 0", buildTIFF(le, grayPage(rational(tagXResolution, 300, 0))),
			Info{TIFF, 200, 100, 0, ColorGray, 1}},
		{"TIFF 宽高写成 LONG", buildTIFF(le, []tiffEntry{long(tagImageWidth, 70000), long(tagImageLength, 3), short(tagBitsPerSample, 8, 8, 8), short(tagPhotometric, 2), short(tagSamplesPerPix, 3)}),
			Info{TIFF, 70000, 3, 0, ColorColor, 1}},
		// 颜色模式：调色板一律 color；带 alpha 的灰度是 gray；RGBA 是 color；没写 BitsPerSample 按 1 位。
		{"TIFF 调色板", buildTIFF(le, []tiffEntry{short(tagImageWidth, 10), short(tagImageLength, 10), short(tagBitsPerSample, 8), short(tagPhotometric, 3)}),
			Info{TIFF, 10, 10, 0, ColorColor, 1}},
		{"TIFF 灰度 + alpha", buildTIFF(le, []tiffEntry{short(tagImageWidth, 10), short(tagImageLength, 10), short(tagBitsPerSample, 8, 8), short(tagPhotometric, 1), short(tagSamplesPerPix, 2), short(tagExtraSamples, 2)}),
			Info{TIFF, 10, 10, 0, ColorGray, 1}},
		{"TIFF RGBA", buildTIFF(le, []tiffEntry{short(tagImageWidth, 10), short(tagImageLength, 10), short(tagBitsPerSample, 8, 8, 8, 8), short(tagPhotometric, 2), short(tagSamplesPerPix, 4), short(tagExtraSamples, 2)}),
			Info{TIFF, 10, 10, 0, ColorColor, 1}},
		{"TIFF 没写位数", buildTIFF(le, []tiffEntry{short(tagImageWidth, 10), short(tagImageLength, 10)}),
			Info{TIFF, 10, 10, 0, ColorBW, 1}},
		// 多页：后面的 IFD 是缩略图的不算页。
		{"TIFF 第二个 IFD 是缩略图", buildTIFF(le, grayPage(), []tiffEntry{long(tagNewSubfileType, 1), short(tagImageWidth, 20), short(tagImageLength, 10)}),
			Info{TIFF, 200, 100, 0, ColorGray, 1}},
		// JPEG：没有 JFIF 时看 EXIF 的分辨率；JFIF 单位是 0 时也看 EXIF；方向 8 交换宽高。
		{"JPEG EXIF 分辨率和方向", buildJPEG(exif(exifIFD(short(tagOrientation, 8), rational(tagXResolution, 300, 1), short(tagResolutionUnit, 2))), sof(400, 300, 3)),
			Info{JPEG, 300, 400, 300, ColorColor, 8}},
		{"JPEG JFIF 单位 0 看 EXIF", buildJPEG(jfif(0, 1), exif(exifIFD(rational(tagXResolution, 240, 1))), sof(40, 30, 1)),
			Info{JPEG, 40, 30, 240, ColorGray, 1}},
		{"JPEG JFIF 优先、单位厘米", buildJPEG(jfif(2, 118), exif(exifIFD(rational(tagXResolution, 72, 1))), sof(40, 30, 3)),
			Info{JPEG, 40, 30, 300, ColorColor, 1}},
		{"JPEG 坏的 EXIF 跳过、用下一个", buildJPEG(exif([]byte("garbage!")), exif(exifIFD(short(tagOrientation, 6))), sof(40, 30, 3)),
			Info{JPEG, 30, 40, 0, ColorColor, 6}},
		{"JPEG SOF 之后的 EXIF 不看", buildJPEG(sof(40, 30, 3), exif(exifIFD(short(tagOrientation, 6)))),
			Info{JPEG, 40, 30, 0, ColorColor, 1}},
		{"JPEG 标记前的 FF 填充", buildJPEG(jfif(1, 300), []byte{0xFF, 0xFF, 0xFF}, sof(40, 30, 4)),
			Info{JPEG, 40, 30, 300, ColorColor, 1}},
		{"JPEG 少见的分量数", buildJPEG(sof(40, 30, 2)),
			Info{JPEG, 40, 30, 0, "", 1}},
		// PNG：调色板、16 位灰度、灰度 + alpha；pHYs 单位未知时 DPI 未知。
		{"PNG 调色板", buildPNG(10, 20, 8, 3, phys(11811, 1)), Info{PNG, 10, 20, 300, ColorColor, 1}},
		{"PNG 16 位灰度", buildPNG(10, 20, 16, 0), Info{PNG, 10, 20, 0, ColorGray, 1}},
		{"PNG 灰度 + alpha", buildPNG(10, 20, 8, 4, phys(7874, 1)), Info{PNG, 10, 20, 200, ColorGray, 1}},
		{"PNG pHYs 单位未知", buildPNG(10, 20, 8, 2, phys(11811, 0)), Info{PNG, 10, 20, 0, ColorColor, 1}},
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

// 各类错误分得开：不支持的格式、读不出来、多页、超过像素上限。
func TestParseErrors(t *testing.T) {
	le := binary.LittleEndian
	jpegRGB := readSample(t, "jpeg-rgb.jpg")
	tiffNone := readSample(t, "tiff-none-gray8.tif") // IFD 在文件末尾
	pngAlpha := readSample(t, "png-alpha.png")

	selfLoop := buildTIFF(le, grayPage())
	le.PutUint32(selfLoop[8+2+12*5:], 8) // 第一个 IFD 的 next 指回自己

	manySegments := []byte{0xFF, 0xD8}
	for range 2000 {
		manySegments = append(manySegments, segment(0xFE, nil)...) // 空的 COM 段
	}

	cases := []struct {
		name string
		data []byte
		kind error
	}{
		// 不支持的格式（415）。
		{"PDF", []byte("%PDF-1.7\n"), ErrUnsupported},
		{"文本", []byte("hello"), ErrUnsupported},
		{"BigTIFF 小端", []byte("II+\x00\x08\x00\x00\x00\x10\x00\x00\x00"), ErrUnsupported},
		{"BigTIFF 大端", []byte("MM\x00+\x00\x08\x00\x00"), ErrUnsupported},
		{"FF D8 后面不是 FF", []byte{0xFF, 0xD8, 0x00, 0x01}, ErrUnsupported},
		// 读不出来（400）：空、比魔数短、截断、结构坏了。
		{"空文件", nil, ErrInvalid},
		{"只有 FF D8", []byte{0xFF, 0xD8}, ErrInvalid},
		{"PNG 签名不全", []byte("\x89PNG\r"), ErrInvalid},
		{"TIFF 只有 II", []byte("II"), ErrInvalid},
		{"JPEG 截在 SOF 之前", jpegRGB[:20], ErrInvalid},
		{"PNG 截在 IHDR 里", pngAlpha[:20], ErrInvalid},
		{"TIFF 截掉末尾的 IFD", tiffNone[:len(tiffNone)/2], ErrInvalid},
		{"TIFF 没有宽", buildTIFF(le, []tiffEntry{short(tagImageLength, 10)}), ErrInvalid},
		{"TIFF 宽为 0", buildTIFF(le, []tiffEntry{short(tagImageWidth, 0), short(tagImageLength, 10)}), ErrInvalid},
		{"TIFF IFD 偏移越界", []byte("II*\x00\xff\xff\x00\x00"), ErrInvalid},
		{"TIFF IFD 链成环", selfLoop, ErrInvalid},
		{"JPEG SOS 在 SOF 之前", buildJPEG(segment(0xDA, []byte{1, 1, 0, 0, 0x3F, 0}), sof(40, 30, 3)), ErrInvalid},
		{"JPEG 段之间有多余字节", buildJPEG(jfif(1, 300), []byte{0x00}, sof(40, 30, 3)), ErrInvalid},
		{"JPEG 段长度小于 2", buildJPEG([]byte{0xFF, 0xE0, 0x00, 0x01}), ErrInvalid},
		{"JPEG 标记段太多", manySegments, ErrInvalid},
		{"PNG 颜色类型和位深不配", buildPNG(10, 10, 4, 2), ErrInvalid},
		{"PNG 第一个块不是 IHDR", append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IDAT", nil)...), ErrInvalid},
		// 多页（400）。
		{"TIFF 两页", buildTIFF(le, grayPage(), grayPage()), ErrMultiPage},
		{"TIFF 缩略图之后还有一页", buildTIFF(le, grayPage(), []tiffEntry{long(tagNewSubfileType, 1), short(tagImageWidth, 2), short(tagImageLength, 2)}, grayPage()), ErrMultiPage},
		// 超出范围（400）：不设上限也不行。
		{"TIFF 宽超出 int32", buildTIFF(le, []tiffEntry{long(tagImageWidth, 1<<31), long(tagImageLength, 1)}), ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseBytes(c.data, 0)
			if !errors.Is(err, c.kind) {
				t.Fatalf("应为 %v，得到 %v", c.kind, err)
			}
			var e *Error
			if !errors.As(err, &e) || e.Msg == "" {
				t.Fatalf("应该是带说明的 *Error：%#v", err)
			}
		})
	}
}

func TestMaxPixels(t *testing.T) {
	b := readSample(t, "jpeg-rgb.jpg") // 2480×3508
	const pixels = 2480 * 3508
	if _, err := parseBytes(b, pixels); err != nil {
		t.Fatalf("正好等于上限应该通过：%v", err)
	}
	_, err := parseBytes(b, pixels-1)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超过上限应为 ErrTooLarge：%v", err)
	}
	if !strings.Contains(err.Error(), "2480×3508") {
		t.Errorf("说明里应有尺寸：%s", err)
	}
	if _, err := parseBytes(b, 0); err != nil {
		t.Fatalf("上限 <= 0 表示不限：%v", err)
	}
}

type failingReader struct{ err error }

func (f failingReader) ReadAt([]byte, int64) (int, error) { return 0, f.err }

// 读文件本身出错时原样带出（server 按 500），不归到任何一类；文件比给的 size 短时算截断。
func TestReadErrors(t *testing.T) {
	boom := errors.New("磁盘坏了")
	_, err := Parse(failingReader{boom}, 100, 0)
	if !errors.Is(err, boom) {
		t.Fatalf("应带出 ReadAt 的错误：%v", err)
	}
	for _, kind := range []error{ErrUnsupported, ErrInvalid, ErrMultiPage, ErrTooLarge} {
		if errors.Is(err, kind) {
			t.Fatalf("读文件出错不应归为 %v", kind)
		}
	}
	b := readSample(t, "tiff-g4-1bit.tif")
	if _, err := Parse(bytes.NewReader(b), int64(len(b))+10, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("文件比 size 短应为 ErrInvalid：%v", err)
	}
}

func TestFormatAndStoredSize(t *testing.T) {
	for f, want := range map[Format][2]string{JPEG: {"image/jpeg", "jpg"}, PNG: {"image/png", "png"}, TIFF: {"image/tiff", "tif"},
		BMP: {"image/bmp", "bmp"}, GIF: {"image/gif", "gif"}, WebP: {"image/webp", "webp"}} {
		if f.MIME() != want[0] || f.Ext() != want[1] {
			t.Errorf("%s：%s %s", f, f.MIME(), f.Ext())
		}
	}
	info := Info{Width: 1240, Height: 1754, Orientation: 6}
	if w, h := info.StoredSize(); w != 1754 || h != 1240 {
		t.Errorf("方向 6 存的是 1754×1240，得到 %d×%d", w, h)
	}
	info.Orientation = 1
	if w, h := info.StoredSize(); w != 1240 || h != 1754 {
		t.Errorf("方向 1 不交换，得到 %d×%d", w, h)
	}
}
