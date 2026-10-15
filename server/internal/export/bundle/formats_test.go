package bundle

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"golang.org/x/image/tiff"
)

// 常见扫描格式：原样嵌入的核对嵌入的字节等于原图里的压缩数据，转换的解出 PDF 里的图像和原图逐像素比较。

func onlyImage(t *testing.T, pg pdfPage) pdfPlaced {
	t.Helper()
	if len(pg.images) != 1 {
		t.Fatalf("页上有 %d 个图像，应该是 1 个", len(pg.images))
	}
	return pg.images[0]
}

func onePage(t *testing.T, d *pdfDoc) pdfPage {
	t.Helper()
	pages := d.pages()
	if len(pages) != 1 {
		t.Fatalf("PDF 有 %d 页", len(pages))
	}
	return pages[0]
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.0002 }

func assertBox(t *testing.T, pg pdfPage, w, h float64) {
	t.Helper()
	b := pg.mediaBox
	if b[0] != 0 || b[1] != 0 || !near(b[2], w) || !near(b[3], h) {
		t.Fatalf("MediaBox 是 %v，应该是 [0 0 %.4f %.4f]", b, w, h)
	}
}

// assertFills：图像（单个）铺满页面、不转（方向 1）。
func assertFills(t *testing.T, img pdfPlaced, pg pdfPage) {
	t.Helper()
	want := [6]float64{pg.mediaBox[2], 0, 0, pg.mediaBox[3], 0, 0}
	for i := range want {
		if !near(img.matrix[i], want[i]) {
			t.Fatalf("图像的矩阵是 %v，应该是 %v", img.matrix, want)
		}
	}
}

func assertColorTransform(t *testing.T, d *pdfDoc, img pdfPlaced, want float64) {
	t.Helper()
	if got := d.dict(img.dict["DecodeParms"])["ColorTransform"]; got != want {
		t.Fatalf("ColorTransform 是 %v，应该是 %v", got, want)
	}
}

func assertEmbedded(t *testing.T, img pdfPlaced, filter, cs string, want []byte) {
	t.Helper()
	if filter == "" {
		if _, ok := img.dict["Filter"]; ok {
			t.Fatalf("不该有 Filter，却是 %v", img.dict["Filter"])
		}
	} else if img.dict["Filter"] != pdfName(filter) {
		t.Fatalf("Filter 是 %v，应该是 %s", img.dict["Filter"], filter)
	}
	if cs != "" && img.dict["ColorSpace"] != pdfName(cs) {
		t.Fatalf("ColorSpace 是 %v，应该是 %s", img.dict["ColorSpace"], cs)
	}
	if !bytes.Equal(img.data, want) {
		t.Fatalf("嵌入的 %d 字节和原图里的压缩数据（%d 字节）不一致", len(img.data), len(want))
	}
}

func TestJPEG(t *testing.T) {
	cases := []struct {
		file, cs string
		w, h     float64 // 页面尺寸（磅）
		decode   bool
	}{
		{"jpeg-rgb.jpg", "DeviceRGB", 2480 * 72.0 / 300, 3508 * 72.0 / 300, false},
		{"jpeg-gray.jpg", "DeviceGray", 1654 * 72.0 / 200, 2339 * 72.0 / 200, false},
		{"jpeg-progressive.jpg", "DeviceRGB", 595 * 72.0 / 300, 842 * 72.0 / 300, false}, // 72 DPI 不合理，按 300
		{"jpeg-cmyk.jpg", "DeviceCMYK", 595 * 72.0 / 300, 842 * 72.0 / 300, true},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			src := newMemSource()
			d := pdfOf(t, Options{}, src, src.addSample(t, 1, c.file))
			pg := onePage(t, d)
			assertBox(t, pg, c.w, c.h)
			img := onlyImage(t, pg)
			assertFills(t, img, pg)
			assertEmbedded(t, img, "DCTDecode", c.cs, sample(t, c.file))
			_, hasDecode := img.dict["Decode"]
			if hasDecode != c.decode {
				t.Fatalf("Decode 数组：有 %v，应该 %v", hasDecode, c.decode)
			}
		})
	}

	t.Run("EXIF 方向 6", func(t *testing.T) {
		src := newMemSource()
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "jpeg-exif6.jpg"))
		pg := onePage(t, d)
		// 文件里存的是横的 1754×1240，转正后是竖的 A4（150 DPI）。
		pw, ph := 1240*72.0/150, 1754*72.0/150
		assertBox(t, pg, pw, ph)
		img := onlyImage(t, pg)
		assertEmbedded(t, img, "DCTDecode", "DeviceRGB", sample(t, "jpeg-exif6.jpg"))
		want := [6]float64{0, -ph, pw, 0, 0, ph} // 顺时针转 90°：原图的第一行在页面右边
		for i := range want {
			if !near(img.matrix[i], want[i]) {
				t.Fatalf("矩阵是 %v，应该是 %v", img.matrix, want)
			}
		}
	})
}

func TestPNG(t *testing.T) {
	t.Run("1 位灰度：原样嵌入", func(t *testing.T) {
		src := newMemSource()
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "png-1bit.png"))
		pg := onePage(t, d)
		dpi := 11811 * 0.0254 // pHYs 是每米 11811 像素，不取整
		assertBox(t, pg, 2480*72/dpi, 3508*72/dpi)
		img := onlyImage(t, pg)
		assertEmbedded(t, img, "FlateDecode", "DeviceGray", pngIDAT(sample(t, "png-1bit.png")))
		parms := d.dict(img.dict["DecodeParms"])
		for k, v := range map[string]float64{"Predictor": 15, "Colors": 1, "BitsPerComponent": 1, "Columns": 2480} {
			if parms[k] != v {
				t.Fatalf("DecodeParms %s 是 %v，应该是 %v", k, parms[k], v)
			}
		}
	})

	t.Run("调色板：Indexed，同一个调色板只写一份", func(t *testing.T) {
		pal := color.Palette{color.RGBA{255, 255, 255, 255}, color.RGBA{200, 30, 30, 255}, color.RGBA{0, 0, 0, 255}}
		img := image.NewPaletted(image.Rect(0, 0, 40, 30), pal)
		for i := range img.Pix {
			img.Pix[i] = uint8(i % 3)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		file := buf.Bytes()
		src := newMemSource()
		d := pdfOf(t, Options{}, src, src.add(1, file, nil, "image/png"), src.add(2, file, nil, "image/png"))
		pages := d.pages()
		a, b := onlyImage(t, pages[0]), onlyImage(t, pages[1])
		ref, ok := a.dict["ColorSpace"].(pdfRef)
		if !ok || b.dict["ColorSpace"] != ref {
			t.Fatalf("两页的调色板应该引用同一个间接对象：%v、%v", a.dict["ColorSpace"], b.dict["ColorSpace"])
		}
		cs := d.obj(int(ref)).([]any)
		if cs[0] != pdfName("Indexed") || cs[1] != pdfName("DeviceRGB") || cs[2] != 2.0 || cs[3] != "hex:ffffffc81e1e000000" {
			t.Fatalf("调色板是 %v", cs)
		}
		assertEmbedded(t, a, "FlateDecode", "", pngIDAT(file))
	})

	t.Run("带透明：转换", func(t *testing.T) {
		src := newMemSource()
		file := sample(t, "png-alpha.png")
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "png-alpha.png"))
		pg := onePage(t, d)
		// pHYs 写的是 50 DPI，不合理，按 300。
		assertBox(t, pg, 413*72.0/300, 585*72.0/300)
		img := onlyImage(t, pg)
		if img.dict["ColorSpace"] != pdfName("DeviceRGB") || img.dict["BitsPerComponent"] != 8.0 {
			t.Fatalf("颜色空间或位深不对：%v", img.dict)
		}
		ref, err := png.Decode(bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}
		nrgba := ref.(*image.NRGBA)
		smask := d.resolve(img.dict["SMask"]).(*pdfStream)
		colors := decodeImage(t, d, img.dict, img.data)
		alphas := decodeImage(t, d, smask.dict, smask.data)
		translucent := 0
		for y := range 585 {
			for x := range 413 {
				px := nrgba.Pix[y*nrgba.Stride+4*x:]
				got := colors[y][3*x : 3*x+3]
				if got[0] != uint32(px[0]) || got[1] != uint32(px[1]) || got[2] != uint32(px[2]) || alphas[y][x] != uint32(px[3]) {
					t.Fatalf("(%d,%d) 的像素是 %v / %d，原图是 %v", x, y, got, alphas[y][x], px[:4])
				}
				if px[3] != 255 {
					translucent++
				}
			}
		}
		if translucent == 0 {
			t.Fatal("样本里没有透明的像素，测不出 SMask")
		}
	})
}

func TestTIFF(t *testing.T) {
	t.Run("无压缩 8 位灰度：原样嵌入", func(t *testing.T) {
		src := newMemSource()
		file := sample(t, "tiff-none-gray8.tif")
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "tiff-none-gray8.tif"))
		pg := onePage(t, d)
		assertBox(t, pg, 595*72.0/300, 842*72.0/300)
		img := onlyImage(t, pg)
		assertFills(t, img, pg)
		off, _ := tiffStrips(t, file)
		if len(off) != 1 {
			t.Fatalf("样本应该只有一个条带")
		}
		assertEmbedded(t, img, "", "DeviceGray", file[off[0]:off[0]+595*842])
	})

	t.Run("LZW RGB：每个条带一个图像对象", func(t *testing.T) {
		src := newMemSource()
		file := sample(t, "tiff-lzw-rgb.tif")
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "tiff-lzw-rgb.tif"))
		pg := onePage(t, d)
		pw, ph := 595*72.0/300, 842*72.0/300
		assertBox(t, pg, pw, ph)
		off, cnt := tiffStrips(t, file)
		if len(pg.images) != len(off) || len(off) != 2 {
			t.Fatalf("有 %d 个图像对象、%d 个条带", len(pg.images), len(off))
		}
		const rps = 561
		top := ph
		for i, img := range pg.images {
			assertEmbedded(t, img, "LZWDecode", "DeviceRGB", file[off[i]:off[i]+cnt[i]])
			rows := min(rps, 842-i*rps)
			if img.dict["Height"] != float64(rows) {
				t.Fatalf("第 %d 个条带的高是 %v，应该是 %d", i+1, img.dict["Height"], rows)
			}
			// 条带从上往下首尾相接，铺满整页的宽。
			h := float64(rows) / 842 * ph
			want := [6]float64{pw, 0, 0, h, 0, top - h}
			for k := range want {
				if !near(img.matrix[k], want[k]) {
					t.Fatalf("第 %d 个条带的矩阵是 %v，应该是 %v", i+1, img.matrix, want)
				}
			}
			top -= h
		}
	})

	t.Run("G4：原样嵌入", func(t *testing.T) {
		src := newMemSource()
		file := sample(t, "tiff-g4-1bit.tif")
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "tiff-g4-1bit.tif"))
		pg := onePage(t, d)
		assertBox(t, pg, 2480*72.0/300, 3508*72.0/300)
		img := onlyImage(t, pg)
		off, cnt := tiffStrips(t, file)
		assertEmbedded(t, img, "CCITTFaxDecode", "DeviceGray", file[off[0]:off[0]+cnt[0]])
		parms := d.dict(img.dict["DecodeParms"])
		for k, v := range map[string]any{"K": -1.0, "Columns": 2480.0, "Rows": 3508.0} {
			if parms[k] != v {
				t.Fatalf("DecodeParms %s 是 %v，应该是 %v", k, parms[k], v)
			}
		}
		if _, ok := parms["BlackIs1"]; ok {
			t.Fatal("WhiteIsZero 不该写 BlackIs1")
		}
	})

	t.Run("JPEG 压缩，单条带、没有 JPEGTables：条带原样嵌入", func(t *testing.T) {
		src := newMemSource()
		file := sample(t, "tiff-jpeg-ycbcr.tif")
		d := pdfOf(t, Options{}, src, src.addSample(t, 1, "tiff-jpeg-ycbcr.tif"))
		pg := onePage(t, d)
		assertBox(t, pg, 595*72.0/300, 842*72.0/300)
		img := onlyImage(t, pg)
		assertFills(t, img, pg)
		off, cnt := tiffStrips(t, file)
		assertEmbedded(t, img, "DCTDecode", "DeviceRGB", file[off[0]:off[0]+cnt[0]])
		assertColorTransform(t, d, img, 1)
	})

	jpegCases := []struct {
		name, file string
		transform  float64
		tiled      bool
	}{
		{"JPEG 压缩，条带 + JPEGTables（YCbCr）", "tiff-jpeg-ycbcr-tables.tif", 1, false},
		{"JPEG 压缩，RGB", "tiff-jpeg-rgb.tif", 0, false},
		{"JPEG 压缩，分块", "tiff-jpeg-tiles.tif", 1, true},
	}
	for _, c := range jpegCases {
		t.Run(c.name, func(t *testing.T) {
			file := fixture(t, c.file)
			src := newMemSource()
			d := pdfOf(t, Options{}, src, src.add(1, file, nil, "image/tiff"))
			pg := onePage(t, d)
			const W, H = 400, 421
			pw, ph := W*72.0/100, H*72.0/100
			assertBox(t, pg, pw, ph)
			// 每个条带（分块）：SOI + 表（去掉表里的 SOI、EOI）+ 条带（去掉条带自己的 SOI）。
			tables, _, _ := tiffTag(file, 347)
			if !bytes.HasPrefix(tables, []byte{0xFF, 0xD8}) || !bytes.HasSuffix(tables, []byte{0xFF, 0xD9}) {
				t.Fatal("样本的 JPEGTables 应该以 SOI 开头、EOI 结尾")
			}
			body := tables[2 : len(tables)-2]
			off, cnt := tiffStrips(t, file)
			if len(pg.images) != len(off) || len(off) < 4 {
				t.Fatalf("有 %d 个图像对象、%d 个条带", len(pg.images), len(off))
			}
			bw, bh := 128, 128
			if !c.tiled {
				bw, bh = W, int(tiffUints(file, 278)[0])
			}
			across := (W + bw - 1) / bw
			for i, img := range pg.images {
				strip := file[off[i] : off[i]+cnt[i]]
				if !bytes.HasPrefix(strip, []byte{0xFF, 0xD8}) {
					t.Fatalf("第 %d 个条带开头不是 SOI", i+1)
				}
				want := append(append([]byte{0xFF, 0xD8}, body...), strip[2:]...)
				assertEmbedded(t, img, "DCTDecode", "DeviceRGB", want)
				assertColorTransform(t, d, img, c.transform)
				// 位置：第 i 块在原图 (x0, y0)，w × h；分块是整块，超出原图的部分落在页面外。
				x0, y0 := (i%across)*bw, (i/across)*bh
				w, h := bw, min(bh, H-y0)
				if c.tiled {
					h = bh
				}
				if img.dict["Width"] != float64(w) || img.dict["Height"] != float64(h) {
					t.Fatalf("第 %d 块是 %v×%v，应该是 %d×%d", i+1, img.dict["Width"], img.dict["Height"], w, h)
				}
				m := [6]float64{float64(w) / W * pw, 0, 0, float64(h) / H * ph, float64(x0) / W * pw, ph - float64(y0+h)/H*ph}
				for k := range m {
					if !near(img.matrix[k], m[k]) {
						t.Fatalf("第 %d 块的矩阵是 %v，应该是 %v", i+1, img.matrix, m)
					}
				}
			}
		})
	}

	t.Run("旧式 JPEG：嵌入 JPEGInterchangeFormat 指向的 JPEG", func(t *testing.T) {
		jpg := sample(t, "jpeg-gray.jpg")
		n := uint32(len(jpg))
		file := buildTIFF(append(make([]byte, 8), jpg...), []tiffEntryT{
			{256, 4, []uint32{1654}}, {257, 4, []uint32{2339}}, {258, 3, []uint32{8}}, {259, 3, []uint32{6}},
			{262, 3, []uint32{1}}, {273, 4, []uint32{8}}, {277, 3, []uint32{1}}, {278, 4, []uint32{2339}},
			{279, 4, []uint32{n}}, {513, 4, []uint32{8}}, {514, 4, []uint32{n}},
		})
		src := newMemSource()
		d := pdfOf(t, Options{}, src, src.add(1, file, nil, "image/tiff"))
		pg := onePage(t, d)
		assertBox(t, pg, 1654*72.0/300, 2339*72.0/300) // TIFF 没写分辨率，按 300
		img := onlyImage(t, pg)
		assertEmbedded(t, img, "DCTDecode", "DeviceGray", jpg)
		if _, ok := img.dict["DecodeParms"]; ok {
			t.Fatal("灰度不该写 ColorTransform")
		}
	})

	t.Run("LZW RGB 强制转换：像素不变", func(t *testing.T) {
		src := newMemSource()
		file := sample(t, "tiff-lzw-rgb.tif")
		d := pdfOf(t, Options{forceConvert: true}, src, src.addSample(t, 1, "tiff-lzw-rgb.tif"))
		img := onlyImage(t, onePage(t, d))
		ref, err := tiff.Decode(bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}
		rgba := ref.(*image.RGBA)
		rows := decodeImage(t, d, img.dict, img.data)
		for y := range 842 {
			for x := range 595 {
				px := rgba.Pix[y*rgba.Stride+4*x:]
				got := rows[y][3*x : 3*x+3]
				if got[0] != uint32(px[0]) || got[1] != uint32(px[1]) || got[2] != uint32(px[2]) {
					t.Fatalf("(%d,%d) 的像素是 %v，原图是 %v", x, y, got, px[:3])
				}
			}
		}
	})

	// 现场拼的图：带一点规律和"噪点"，尺寸取奇数。
	const w, h = 67, 45
	pixels := func(spp int) []byte {
		pix := make([]byte, w*h*spp)
		for i := range pix {
			pix[i] = byte(i*7 + i/spp*13 + (i*i)%251)
		}
		return pix
	}

	t.Run("分平面、Deflate、预测器：转换", func(t *testing.T) {
		pix := pixels(3)
		file := synthTIFF{w: w, h: h, spp: 3, photometric: 2, compression: 8, planar: 2, predictor: 2,
			rps: 8, xres: 300, yres: 300, pix: pix}.encode()
		src := newMemSource()
		d := pdfOf(t, Options{}, src, src.add(1, file, nil, "image/tiff"))
		img := onlyImage(t, onePage(t, d))
		if img.dict["ColorSpace"] != pdfName("DeviceRGB") {
			t.Fatalf("ColorSpace 是 %v", img.dict["ColorSpace"])
		}
		rows := decodeImage(t, d, img.dict, img.data)
		for y := range h {
			for i := range 3 * w {
				if rows[y][i] != uint32(pix[y*3*w+i]) {
					t.Fatalf("第 %d 行第 %d 个样本是 %d，原图是 %d", y, i, rows[y][i], pix[y*3*w+i])
				}
			}
		}
	})

	t.Run("RGBA 无压缩：转换，透明度做成 SMask", func(t *testing.T) {
		pix := pixels(4)
		file := synthTIFF{w: w, h: h, spp: 4, photometric: 2, compression: 1, planar: 1,
			rps: h, xres: 300, yres: 300, extra: []uint16{2}, pix: pix}.encode()
		src := newMemSource()
		d := pdfOf(t, Options{}, src, src.add(1, file, nil, "image/tiff"))
		img := onlyImage(t, onePage(t, d))
		smask := d.resolve(img.dict["SMask"]).(*pdfStream)
		if _, ok := smask.dict["Matte"]; ok {
			t.Fatal("非预乘的 alpha 不该写 Matte")
		}
		rows := decodeImage(t, d, img.dict, img.data)
		alphas := decodeImage(t, d, smask.dict, smask.data)
		for y := range h {
			for x := range w {
				px := pix[(y*w+x)*4:]
				got := rows[y][3*x : 3*x+3]
				if got[0] != uint32(px[0]) || got[1] != uint32(px[1]) || got[2] != uint32(px[2]) || alphas[y][x] != uint32(px[3]) {
					t.Fatalf("(%d,%d) 是 %v / %d，原图是 %v", x, y, got, alphas[y][x], px[:4])
				}
			}
		}
	})
}

// 传真 204×98 DPI：横纵分别算，不压扁。
func TestDPIPerAxis(t *testing.T) {
	const w, h = 170, 110
	file := synthTIFF{w: w, h: h, spp: 1, photometric: 1, compression: 1, planar: 1, rps: 16,
		xres: 204, yres: 98, pix: make([]byte, w*h)}.encode()
	src := newMemSource()
	d := pdfOf(t, Options{}, src, src.add(1, file, nil, "image/tiff"))
	pg := onePage(t, d)
	assertBox(t, pg, w*72.0/204, h*72.0/98)
	img := onlyImage(t, pg) // 无压缩的条带拼成一个图像对象
	if img.dict["Height"] != float64(h) {
		t.Fatalf("Height 是 %v", img.dict["Height"])
	}

	cases := []struct{ x, y, wantX, wantY float64 }{
		{300, 300, 300, 300},
		{204, 98, 204, 98},    // 一个合理、另一个 ≥ 50：各用各的
		{98, 204, 98, 204},    // 反过来也一样
		{300, 30, 300, 300},   // 另一个太低：都用合理的那个
		{600, 5000, 600, 600}, // 另一个超过 2400：同样
		{72, 72, 300, 300},    // 都不合理
		{0, 0, 300, 300},      // 没写
		{2400, 100, 2400, 100},
	}
	for _, c := range cases {
		if x, y := chooseDPI(c.x, c.y); x != c.wantX || y != c.wantY {
			t.Errorf("chooseDPI(%v, %v) = %v, %v，应该是 %v, %v", c.x, c.y, x, y, c.wantX, c.wantY)
		}
	}
}
