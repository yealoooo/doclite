package upload

import (
	"bytes"
	"image"
	"image/jpeg"
	"path/filepath"
	"testing"

	"doclite/client/internal/imagehdr"
)

// 样本目录（client/testdata/samples，清单见它的 README.md）。
const samplesDir = "../../testdata/samples"

// checkCover 按 server 的校验规则核对封面（server/internal/file/cover.go 的 ValidateCover）：JPEG、以 FF D9 结尾、
// 不带方向、≤ 256 KiB、尺寸等于 CoverSize（长边 400 不放大，短边按比例四舍五入）。返回解码后的封面。
func checkCover(t *testing.T, data []byte, w, h int) image.Image {
	t.Helper()
	if len(data) > MaxCoverBytes || len(data) < 2 || data[len(data)-2] != 0xFF || data[len(data)-1] != 0xD9 {
		t.Fatalf("封面不是完整的 JPEG 或太大（%d 字节）", len(data))
	}
	info, err := imagehdr.Parse(bytes.NewReader(data), int64(len(data)), 0)
	if err != nil || info.Format != imagehdr.JPEG || info.Orientation != 1 {
		t.Fatalf("封面的文件头不对：%+v %v", info, err)
	}
	cw, ch := CoverSize(w, h)
	if info.Width != cw || info.Height != ch {
		t.Fatalf("封面 %d×%d，应为 %d×%d（原图转正后 %d×%d）", info.Width, info.Height, cw, ch, w, h)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func luma(img image.Image, x, y int) int {
	r, g, b, _ := img.At(x, y).RGBA()
	return int((299*r + 587*g + 114*b) / 1000 >> 8)
}

// boxLuma 是 (fx, fy)（相对位置）附近 5×5 像素的平均亮度。
func boxLuma(img image.Image, b image.Rectangle, fx, fy float64) int {
	cx, cy := int(fx*float64(b.Dx())), int(fy*float64(b.Dy()))
	sum, n := 0, 0
	for y := cy - 2; y <= cy+2; y++ {
		for x := cx - 2; x <= cx+2; x++ {
			sum += luma(img, x, y)
			n++
		}
	}
	return sum / n
}

// 封面尺寸：长边 400、不放大、短边按比例；方向 6 的 JPEG 转正后是竖的；透明铺白；JPEG 压缩的 TIFF 也有封面（颜色的
// 比较见 imgdecode 的 consumers_test.go）；多页 TIFF 不带封面。转正、铺白和切块是同一套代码（tiles 的 TestOrientation）。
func TestCoverSamples(t *testing.T) {
	cases := []struct {
		file string
		w, h int // 转正后的原图尺寸
		ok   bool
	}{
		{"jpeg-rgb.jpg", 2480, 3508, true},
		{"jpeg-gray.jpg", 1654, 2339, true},
		{"jpeg-exif6.jpg", 1240, 1754, true},
		{"jpeg-cmyk.jpg", 595, 842, true},
		{"png-alpha.png", 413, 585, true},
		{"png-1bit.png", 2480, 3508, true},
		{"tiff-g4-1bit.tif", 2480, 3508, true},
		{"tiff-lzw-rgb.tif", 595, 842, true},
		{"tiff-none-gray8.tif", 595, 842, true},
		{"tiff-jpeg-ycbcr.tif", 595, 842, true},
		{"tiff-multipage-2p.tif", 827, 1169, false},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			cv := MakeCover(filepath.Join(samplesDir, c.file))
			if !c.ok {
				if cv.Data != nil || cv.Reason == "" {
					t.Fatalf("应该生成不了并给出原因：%d 字节，%q", len(cv.Data), cv.Reason)
				}
				return
			}
			if cv.Data == nil {
				t.Fatalf("没有生成封面：%s", cv.Reason)
			}
			img := checkCover(t, cv.Data, c.w, c.h)
			// 样本左上角有一块深色的"方向块"（约在宽 7%–18%、高 4%–8% 处），右下角是空白：转正后的封面左上角那块
			// 应该比右下角暗得多。
			b := img.Bounds()
			if tl, br := boxLuma(img, b, 0.10, 0.05), boxLuma(img, b, 0.90, 0.95); tl > br-100 {
				t.Errorf("方向不对：左上角亮度 %d，右下角 %d", tl, br)
			}
		})
	}
}
