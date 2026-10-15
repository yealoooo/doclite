package imgdecode

import (
	"context"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/tiff"
)

// 样本和夹具：
//   - server/testdata/samples：tiff-jpeg-ycbcr.tif 和 tiff-lzw-rgb.tif 版式、颜色相同（595×842，文字行不同，只比整页）；
//   - server/internal/export/bundle/testdata：tiff-jpeg-*.tif 是 LZW 样本左上角 400×421 用 tiffcp 生成的
//     （RGB 的分量 ID 是 'R','G','B'），分平面的是左上角 64×48；
//   - testdata（这个包的）：LZW 样本、无压缩灰度样本的左上角 160×136，用 libtiff 4.7 的 tiffcp 生成：
//     tiff-jpeg-gray.tif（tiffcp -c jpeg:90 -r 48，3 个条带，最后一个 40 行）；tiff-jpeg-cmyk.tif（C、M、Y = 255 − R、G、B，
//     K = 0，同样的参数，没有 Adobe 标记）；tiff-jpeg-rgb-ids.tif（tiffcp -c jpeg:90:r -r 48，再把各条带 SOF、SOS 里的
//     分量 ID 改成 0、1、2——旧版 libjpeg 写的 RGB 就是这样，image/jpeg 会当成 YCbCr）；tiff-ojpeg.tif（旧式 JPEG：
//     手拼的 TIFF，JPEGInterchangeFormat 指向一张完整的基线 JPEG，YCbCr 4:2:0，带 JFIF）。
const (
	samplesDir = "../../../server/testdata/samples"
	bundleDir  = "../../../server/internal/export/bundle/testdata"
)

// DecodeRef 用 x/image/tiff 解参照用的 TIFF（无压缩、LZW）。导出给 consumers_test.go 用。
func DecodeRef(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := tiff.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

type subImager interface {
	SubImage(image.Rectangle) image.Image
}

// JPEG 压缩的 TIFF：解出的像素和同一画面的 LZW、无压缩样本比较平均颜色（JPEG 有损，留余量）。
// RGB 当成 YCbCr 再转一次、CMYK 反相、条带和分块贴错位置、灰度反相，都会远远超出容差。
func TestJPEGTIFF(t *testing.T) {
	lzw := DecodeRef(t, filepath.Join(samplesDir, "tiff-lzw-rgb.tif")).(subImager)
	gray := DecodeRef(t, filepath.Join(samplesDir, "tiff-none-gray8.tif")).(subImager)
	crop := func(s subImager, w, h int) image.Image { return s.SubImage(image.Rect(0, 0, w, h)) }
	cases := []struct {
		name, path string
		ref        image.Image
	}{
		{"YCbCr，一个条带，JFIF", filepath.Join(samplesDir, "tiff-jpeg-ycbcr.tif"), lzw.(image.Image)},
		{"YCbCr，7 个条带 + JPEGTables", filepath.Join(bundleDir, "tiff-jpeg-ycbcr-tables.tif"), crop(lzw, 400, 421)},
		{"YCbCr，128×128 分块", filepath.Join(bundleDir, "tiff-jpeg-tiles.tif"), crop(lzw, 400, 421)},
		{"RGB，分量 ID 是 R G B", filepath.Join(bundleDir, "tiff-jpeg-rgb.tif"), crop(lzw, 400, 421)},
		{"RGB，分量 ID 是 0 1 2", "testdata/tiff-jpeg-rgb-ids.tif", crop(lzw, 160, 136)},
		{"RGB，分平面", filepath.Join(bundleDir, "tiff-jpeg-planar.tif"), crop(lzw, 64, 48)},
		{"灰度", "testdata/tiff-jpeg-gray.tif", crop(gray, 160, 136)},
		{"CMYK", "testdata/tiff-jpeg-cmyk.tif", crop(lzw, 160, 136)},
		{"旧式 JPEG", "testdata/tiff-ojpeg.tif", crop(lzw, 160, 136)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, err := Open(c.path, 0)
			if err != nil {
				t.Fatal(err)
			}
			img, err := src.Decode(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds() != c.ref.Bounds() {
				t.Fatalf("解出来 %v，应为 %v", img.Bounds(), c.ref.Bounds())
			}
			CompareColors(t, img, c.ref)
		})
	}

	// 解到一半取消：canceled。多页 TIFF：unsupported。
	src, err := Open(filepath.Join(bundleDir, "tiff-jpeg-ycbcr-tables.tif"), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var de *Error
	if _, err := src.Decode(ctx); !errors.As(err, &de) || de.Kind != KindCanceled {
		t.Errorf("取消后应为 canceled，实际 %v", err)
	}
	if _, err := Open(filepath.Join(samplesDir, "tiff-multipage-2p.tif"), 0); !errors.As(err, &de) || de.Kind != KindUnsupported {
		t.Errorf("多页 TIFF 应为 unsupported，实际 %v", err)
	}
}

// CompareColors：整张的平均颜色每个通道相差不超过 6；4×4 的每格不超过 24（格子按比例取，两边尺寸可以不同）。
// 和 server 导出的 sips 测试同一个标准。导出给 consumers_test.go 用。
func CompareColors(t *testing.T, got, ref image.Image) {
	t.Helper()
	g, r := meanRGB(got, 0, 0, 1, 1), meanRGB(ref, 0, 0, 1, 1)
	for c := range 3 {
		if math.Abs(g[c]-r[c]) > 6 {
			t.Fatalf("整张的平均颜色是 %.0f，参照是 %.0f（偏色）", g, r)
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

// meanRGB 是 [x0, x1) × [y0, y1)（按宽高的比例）的平均颜色（0–255）。
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
