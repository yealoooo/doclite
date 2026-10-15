package bundle

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/image/tiff"
)

// 用 macOS 自带的 sips（Quartz）把 PDF 渲染成 PNG（72 DPI，一磅一像素）：能打开、尺寸对；方向 6 的 JPEG
// 转正后左上角是样本里的深色"方向块"；JPEG 压缩的 TIFF 颜色对（YCbCr、ColorTransform 写错时整体偏绿或偏紫）、
// 条带和分块的位置对（和同一页内容的 LZW 样本按 4×4 的格子比较平均颜色）。只在 macOS 上编译（文件名带 _darwin）。
func TestSips(t *testing.T) {
	sips, err := exec.LookPath("sips")
	if err != nil {
		t.Skip("没有 sips")
	}
	const w, h = 170, 110
	fax := synthTIFF{w: w, h: h, spp: 1, photometric: 1, compression: 1, planar: 1, rps: 16,
		xres: 204, yres: 98, pix: bytes.Repeat([]byte{200}, w*h)}.encode()
	// 颜色的参照：同一页内容的 LZW 样本（tiff-jpeg-*.tif 是从它左上角裁下 400×421 生成的）。
	ref, err := tiff.Decode(bytes.NewReader(sample(t, "tiff-lzw-rgb.tif")))
	if err != nil {
		t.Fatal(err)
	}
	crop := ref.(*image.RGBA).SubImage(image.Rect(0, 0, 400, 421))
	cases := []struct {
		name         string
		file         []byte
		mime         string
		wantW, wantH float64 // 磅
		checkOrient  bool
		ref          image.Image // 不为 nil 时比较颜色
	}{
		{"jpeg-exif6.jpg", sample(t, "jpeg-exif6.jpg"), "image/jpeg", 1240 * 72.0 / 150, 1754 * 72.0 / 150, true, nil},
		{"tiff-g4-1bit.tif", sample(t, "tiff-g4-1bit.tif"), "image/tiff", 2480 * 72.0 / 300, 3508 * 72.0 / 300, true, nil},
		{"png-alpha.png", sample(t, "png-alpha.png"), "image/png", 413 * 72.0 / 300, 585 * 72.0 / 300, false, nil},
		{"传真 204×98", fax, "image/tiff", w * 72.0 / 204, h * 72.0 / 98, false, nil},
		{"tiff-jpeg-ycbcr.tif", sample(t, "tiff-jpeg-ycbcr.tif"), "image/tiff", 595 * 72.0 / 300, 842 * 72.0 / 300, false, ref},
		{"JPEG 压缩，条带 + JPEGTables", fixture(t, "tiff-jpeg-ycbcr-tables.tif"), "image/tiff", 288, 303.12, false, crop},
		{"JPEG 压缩，RGB", fixture(t, "tiff-jpeg-rgb.tif"), "image/tiff", 288, 303.12, false, crop},
		{"JPEG 压缩，分块", fixture(t, "tiff-jpeg-tiles.tif"), "image/tiff", 288, 303.12, false, crop},
	}
	dir := t.TempDir()
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newMemSource()
			job := &Job{Format: FormatPDF, Items: []Item{testItem("A-1", src.add(1, c.file, nil, c.mime))}}
			z, _, err := generate(t, job, src, Options{})
			if err != nil {
				t.Fatal(err)
			}
			pdfPath := filepath.Join(dir, "p.pdf")
			pngPath := filepath.Join(dir, "p.png")
			if err := os.WriteFile(pdfPath, zipFile(t, z, "A-1.pdf"), 0o644); err != nil {
				t.Fatal(err)
			}
			os.Remove(pngPath)
			if out, err := exec.Command(sips, "-s", "format", "png", pdfPath, "--out", pngPath).CombinedOutput(); err != nil {
				t.Fatalf("sips 打不开第 %d 个 PDF：%v\n%s", i+1, err, out)
			}
			f, err := os.Open(pngPath)
			if err != nil {
				t.Fatalf("sips 没有渲染出来：%v", err)
			}
			defer f.Close()
			img, err := png.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			b := img.Bounds()
			if math.Abs(float64(b.Dx())-c.wantW) > 1 || math.Abs(float64(b.Dy())-c.wantH) > 1 {
				t.Fatalf("渲染出来 %d×%d，应该约 %.1f×%.1f", b.Dx(), b.Dy(), c.wantW, c.wantH)
			}
			if c.checkOrient {
				// 方向块在左上角（宽 8%–16%、高 5%–7%）；转错了的话那里是浅色，块到了别的角上。
				tl := meanGray(img, 0.08, 0.05, 0.16, 0.07)
				br := meanGray(img, 0.84, 0.93, 0.92, 0.95)
				if tl > 80 || br < 180 {
					t.Fatalf("方向不对：左上角平均灰度 %.0f、右下角 %.0f", tl, br)
				}
			}
			if c.ref != nil {
				compareColors(t, img, c.ref)
			}
		})
	}
}

// compareColors：整页的平均颜色每个通道相差不超过 6；4×4 的每格不超过 24（格子按比例取，两边分辨率不同）。
func compareColors(t *testing.T, got, ref image.Image) {
	t.Helper()
	g, r := meanRGB(got, 0, 0, 1, 1), meanRGB(ref, 0, 0, 1, 1)
	for c := range 3 {
		if math.Abs(g[c]-r[c]) > 6 {
			t.Fatalf("整页的平均颜色是 %.0f，参照是 %.0f（偏色）", g, r)
		}
	}
	const n = 4
	for gy := range n {
		for gx := range n {
			x0, y0, x1, y1 := float64(gx)/n, float64(gy)/n, float64(gx+1)/n, float64(gy+1)/n
			g, r := meanRGB(got, x0, y0, x1, y1), meanRGB(ref, x0, y0, x1, y1)
			for c := range 3 {
				if math.Abs(g[c]-r[c]) > 24 {
					t.Fatalf("第 %d 行第 %d 格的平均颜色是 %.0f，参照是 %.0f（位置或颜色不对）", gy+1, gx+1, g, r)
				}
			}
		}
	}
}

// meanRGB 是图像里 [x0, x1) × [y0, y1)（按宽高的比例）的平均颜色（0–255）。
func meanRGB(img image.Image, x0, y0, x1, y1 float64) [3]float64 {
	b := img.Bounds()
	var sum [3]float64
	var n float64
	for y := int(y0 * float64(b.Dy())); y < int(y1*float64(b.Dy())); y++ {
		for x := int(x0 * float64(b.Dx())); x < int(x1*float64(b.Dx())); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			sum[0] += float64(r) / 257
			sum[1] += float64(g) / 257
			sum[2] += float64(bl) / 257
			n++
		}
	}
	return [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}
}

// meanGray 是图像里 [x0, x1) × [y0, y1)（按宽高的比例）的平均亮度。
func meanGray(img image.Image, x0, y0, x1, y1 float64) float64 {
	b := img.Bounds()
	var sum, n float64
	for y := int(y0 * float64(b.Dy())); y < int(y1*float64(b.Dy())); y++ {
		for x := int(x0 * float64(b.Dx())); x < int(x1*float64(b.Dx())); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			sum += float64(r+g+bl) / 3 / 257
			n++
		}
	}
	return sum / n
}
