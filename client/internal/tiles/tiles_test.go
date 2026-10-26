package tiles

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"doclite/client/internal/imgdecode"
)

func TestDZIMath(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{{1, 1, 0}, {2, 1, 1}, {512, 512, 9}, {513, 10, 10}, {2480, 3508, 12}} {
		if got := MaxLevel(c.w, c.h); got != c.want {
			t.Errorf("MaxLevel(%d, %d) = %d，应为 %d", c.w, c.h, got, c.want)
		}
	}
	for _, c := range []struct{ w, h, max, l, ww, wh int }{
		{2480, 3508, 12, 12, 2480, 3508},
		{2480, 3508, 12, 11, 1240, 1754},
		{2480, 3508, 12, 10, 620, 877},
		{2480, 3508, 12, 0, 1, 1},
		{5, 3, 3, 1, 2, 1},
	} {
		if w, h := LevelSize(c.w, c.h, c.max, c.l); w != c.ww || h != c.wh {
			t.Errorf("LevelSize(%d, %d, %d, %d) = %d×%d，应为 %d×%d", c.w, c.h, c.max, c.l, w, h, c.ww, c.wh)
		}
	}
	for _, c := range []struct{ i, n, lo, hi int }{{0, 1000, 0, 513}, {1, 1000, 511, 1000}, {0, 300, 0, 300}, {2, 1025, 1023, 1025}} {
		if lo, hi := TileSpan(c.i, c.n); lo != c.lo || hi != c.hi {
			t.Errorf("TileSpan(%d, %d) = [%d, %d)，应为 [%d, %d)", c.i, c.n, lo, hi, c.lo, c.hi)
		}
	}
	if cols, rows := Grid(1024, 513); cols != 2 || rows != 2 {
		t.Errorf("Grid(1024, 513) = %d×%d", cols, rows)
	}
}

// 存的 3×2 灰度图（a b c / d e f），各方向转正后的样子。
func TestOrientation(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 3, 2))
	copy(src.Pix, []byte{'a', 'b', 'c', 'd', 'e', 'f'})
	want := map[int]struct {
		w, h int
		pix  string
	}{
		1: {3, 2, "abcdef"},
		2: {3, 2, "cbafed"},
		3: {3, 2, "fedcba"},
		4: {3, 2, "defabc"},
		5: {2, 3, "adbecf"},
		6: {2, 3, "daebfc"},
		7: {2, 3, "fcebda"},
		8: {2, 3, "cfbead"},
	}
	for o, wt := range want {
		lv, err := toLevel(context.Background(), src, 1, o)
		if err != nil {
			t.Fatal(err)
		}
		if lv.w != wt.w || lv.h != wt.h || string(lv.pix) != wt.pix {
			t.Errorf("方向 %d：%d×%d %q，应为 %d×%d %q", o, lv.w, lv.h, lv.pix, wt.w, wt.h, wt.pix)
		}
		if w, h := imgdecode.OrientedSize(o, 3, 2); w != wt.w || h != wt.h {
			t.Errorf("OrientedSize(%d) = %d×%d", o, w, h)
		}
	}
	// 彩色走同一套映射，透明合成到白底。
	rgba := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	rgba.Set(0, 0, color.NRGBA{R: 255, A: 255})
	rgba.Set(1, 0, color.NRGBA{A: 0})
	lv, err := toLevel(context.Background(), rgba, 3, 6)
	if err != nil {
		t.Fatal(err)
	}
	if lv.w != 1 || lv.h != 2 || !bytes.Equal(lv.pix, []byte{255, 0, 0, 255, 255, 255}) {
		t.Errorf("彩色方向 6：%d×%d %v", lv.w, lv.h, lv.pix)
	}
}

// 逐级减半；封面的 Shrink 按 2ⁿ 的方块取平均后再转正。
func TestHalveAndShrink(t *testing.T) {
	src := &level{w: 3, h: 3, ch: 1, pix: []byte{0, 10, 20, 30, 40, 50, 60, 70, 80}}
	got := halve(src)
	if got.w != 2 || got.h != 2 || !bytes.Equal(got.pix, []byte{20, 35, 65, 80}) {
		t.Errorf("halve = %d×%d %v", got.w, got.h, got.pix)
	}
	// 存的 4×2，方向 6 转正后 2×4；缩到不小于 1×2：2×2 的方块取平均（25、45），再转正成 1×2。
	gray := image.NewGray(image.Rect(0, 0, 4, 2))
	copy(gray.Pix, []byte{0, 10, 20, 30, 40, 50, 60, 70})
	img, err := Shrink(context.Background(), gray, 6, 1, 2)
	if g, ok := img.(*image.Gray); err != nil || !ok || g.Rect.Dx() != 1 || g.Rect.Dy() != 2 || !bytes.Equal(g.Pix, []byte{25, 45}) {
		t.Errorf("Shrink = %v %v", img, err)
	}
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeFile(t *testing.T, path string) (image.Image, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s：%v", path, err)
	}
	return img, data
}

// 小图实际切一次：块数、块的尺寸（含重叠）、格式（彩色 JPEG；1 位的最高一级是 1 位 PNG、其余 8 位灰度 PNG）。
func TestGenerateSmall(t *testing.T) {
	dir := t.TempDir()

	// 彩色 1100×600。
	src := image.NewRGBA(image.Rect(0, 0, 1100, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 1100; x++ {
			src.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 128, A: 255})
		}
	}
	srcPath := filepath.Join(dir, "rgb.png")
	writePNG(t, srcPath, src)
	plan, err := NewPlan(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeRGB || plan.Format != "jpg" || plan.Width != 1100 || plan.Height != 600 || plan.Estimate <= 0 {
		t.Fatalf("计划不对：%+v", plan)
	}
	out := filepath.Join(dir, "rgb.tiles")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := plan.Generate(context.Background(), out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxLevel != 11 || res.TileCount != 6+2+10 || res.Bytes <= 0 {
		t.Fatalf("结果不对：%+v", res)
	}
	for _, c := range []struct{ l, x, y, w, h int }{{11, 0, 0, 513, 513}, {11, 2, 1, 77, 89}, {10, 1, 0, 39, 300}, {0, 0, 0, 1, 1}} {
		img, data := decodeFile(t, filepath.Join(out, TilePath(c.l, c.x, c.y, "jpg")))
		if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
			t.Errorf("块 %d/%d_%d 不是 JPEG", c.l, c.x, c.y)
		}
		if b := img.Bounds(); b.Dx() != c.w || b.Dy() != c.h {
			t.Errorf("块 %d/%d_%d 是 %d×%d，应为 %d×%d", c.l, c.x, c.y, b.Dx(), b.Dy(), c.w, c.h)
		}
	}
	// 最高一级第一块的左上角颜色对得上（JPEG 有损，留余量）。
	img, _ := decodeFile(t, filepath.Join(out, TilePath(11, 1, 0, "jpg")))
	r, g, b, _ := img.At(200, 100).RGBA()
	if d := func(a, b int) int { return max(a-b, b-a) }; d(int(r>>8), (511+200)%256) > 12 || d(int(g>>8), 100) > 12 || d(int(b>>8), 128) > 12 {
		t.Errorf("块 11/1_0 (200,100) 的颜色 %d,%d,%d 不对", r>>8, g>>8, b>>8)
	}

	// 1 位黑白 700×300（PNG 1 位灰度）。
	bw := image.NewGray(image.Rect(0, 0, 700, 300))
	for i := range bw.Pix {
		if (i/7)%2 == 0 {
			bw.Pix[i] = 255
		}
	}
	bwPath := filepath.Join(dir, "bw.png")
	var buf bytes.Buffer
	pal := image.NewPaletted(bw.Rect, color.Palette{color.Gray{Y: 0}, color.Gray{Y: 255}})
	for i, v := range bw.Pix {
		if v == 255 {
			pal.Pix[i] = 1
		}
	}
	// 调色板图在 imagehdr 里算彩色；1 位黑白要写成颜色类型 0、位深 1 的灰度 PNG：把调色板图的 IHDR 改成灰度、去掉 PLTE。
	if err := png.Encode(&buf, pal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bwPath, grayscale1Bit(t, buf.Bytes()), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err = NewPlan(bwPath)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeBilevel || plan.Format != "png" {
		t.Fatalf("1 位图的计划不对：%+v", plan)
	}
	out = filepath.Join(dir, "bw.tiles")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Generate(context.Background(), out, Options{}); err != nil {
		t.Fatal(err)
	}
	_, top := decodeFile(t, filepath.Join(out, TilePath(10, 1, 0, "png")))
	if top[24] != 1 {
		t.Errorf("最高一级的块位深 %d，应为 1", top[24])
	}
	_, lower := decodeFile(t, filepath.Join(out, TilePath(9, 0, 0, "png")))
	if lower[24] != 8 || lower[25] != 0 {
		t.Errorf("缩小的一级应为 8 位灰度，实际位深 %d、颜色类型 %d", lower[24], lower[25])
	}

	// 输出超出上限：too_large。
	out = filepath.Join(dir, "small-limit.tiles")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, _ = NewPlan(srcPath)
	_, err = plan.Generate(context.Background(), out, Options{MaxOutputBytes: 1000})
	var te *Error
	if !errors.As(err, &te) || te.Code != imgdecode.KindTooLarge {
		t.Errorf("输出超出上限应为 too_large，实际 %v", err)
	}

	// 认不出的文件：unsupported_format。
	junk := filepath.Join(dir, "junk.bin")
	if err := os.WriteFile(junk, []byte("%PDF-1.7……"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(junk); !errors.As(err, &te) || te.Code != imgdecode.KindUnsupportedFormat {
		t.Errorf("PDF 应为 unsupported_format，实际 %v", err)
	}
}

// grayscale1Bit 把两色调色板 PNG（1 位）改写成 1 位灰度 PNG（颜色类型 0，去掉 PLTE）。像素数据不变：
// 调色板第 0 项是黑、第 1 项是白，和灰度 1 位的 0、1 一致。
func grayscale1Bit(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	out.Write(data[:8])
	for i := 8; i < len(data); {
		n := int(data[i])<<24 | int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
		typ := string(data[i+4 : i+8])
		chunk := append([]byte(nil), data[i:i+12+n]...)
		i += 12 + n
		switch typ {
		case "PLTE", "tRNS":
			continue
		case "IHDR":
			if chunk[8+8] != 1 {
				t.Fatalf("调色板 PNG 的位深是 %d", chunk[8+8])
			}
			chunk[8+9] = 0
			fixCRC(chunk)
		}
		out.Write(chunk)
	}
	return out.Bytes()
}

// fixCRC 重算一个 PNG 块的 CRC（长度 4 字节 + 类型 + 数据 + CRC）。
func fixCRC(chunk []byte) {
	n := len(chunk)
	binary.BigEndian.PutUint32(chunk[n-4:], crc32.ChecksumIEEE(chunk[4:n-4]))
}

// writeTIFF 写一张 w×h、一个条带、不压缩的 RGB TIFF（每个样本 bits 位，像素全 0）。
func writeTIFF(t *testing.T, path string, w, h, bits int) {
	t.Helper()
	le := binary.LittleEndian
	const entries = 9
	bitsAt := 8 + 2 + entries*12 + 4 // BitsPerSample 的 3 个值放在 IFD 后面
	dataAt := bitsAt + 6
	size := w * h * 3 * bits / 8
	b := le.AppendUint32([]byte("II*\x00"), 8)
	b = le.AppendUint16(b, entries)
	short := func(tag uint16, v int) { // SHORT，一个值
		b = le.AppendUint16(le.AppendUint16(b, tag), 3)
		b = le.AppendUint16(le.AppendUint32(b, 1), uint16(v))
		b = le.AppendUint16(b, 0)
	}
	long := func(tag uint16, v int) { // LONG，一个值
		b = le.AppendUint16(le.AppendUint16(b, tag), 4)
		b = le.AppendUint32(le.AppendUint32(b, 1), uint32(v))
	}
	short(256, w)
	short(257, h)
	b = le.AppendUint16(le.AppendUint16(b, 258), 3)
	b = le.AppendUint32(le.AppendUint32(b, 3), uint32(bitsAt))
	short(259, 1) // 不压缩
	short(262, 2) // RGB
	long(273, dataAt)
	short(277, 3)
	short(278, h)
	long(279, size)
	b = le.AppendUint32(b, 0)
	for range 3 {
		b = le.AppendUint16(b, uint16(bits))
	}
	b = append(b, make([]byte, size)...)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// 内存估算按 TIFF 的位深：16 位 RGB 解码出来是 RGBA64（每像素 8 字节）、条带缓冲每像素 6 字节，是 8 位的两倍。
func TestDecodeEstimateBitDepth(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ bits, perPixel int }{{8, 4 + 3}, {16, 8 + 6}} {
		path := filepath.Join(dir, "rgb.tif")
		writeTIFF(t, path, 100, 50, c.bits)
		src, err := imgdecode.Open(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := src.Decode(context.Background()); err != nil {
			t.Fatalf("%d 位的样本解不了：%v", c.bits, err)
		}
		if decoded, raw := decodeBytes(src); decoded+raw != int64(100*50*c.perPixel) {
			t.Errorf("%d 位 RGB 估算 %d + %d 字节，应为每像素 %d 字节", c.bits, decoded, raw, c.perPixel)
		}
	}
}
