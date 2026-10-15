package upload

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"testing"

	"doclite/shared/imagehdr"
)

// 样本目录（server/testdata/samples，清单见它的 README.md）。
const samplesDir = "../../../server/testdata/samples"

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

// 封面尺寸：长边 400、不放大、短边按比例；方向 6 的 JPEG 转正后是竖的；JPEG 压缩的 TIFF 也有封面（颜色的比较见
// imgdecode 的 consumers_test.go）；多页 TIFF 不带封面。
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

// 封面方向：8 种 EXIF 方向转正后，标记点都回到左上角；透明部分铺白；小图不放大。
func TestEncodeCoverOrientation(t *testing.T) {
	// 转正后的图：宽 60、高 90，左上角 10×10 是黑块，其余白色。按每种方向反推出文件里存的样子。
	const ow, oh = 60, 90
	upright := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x < 10 && y < 10 {
				c = color.RGBA{0, 0, 0, 255}
			}
			upright.SetRGBA(x, y, c)
		}
	}
	for o := 1; o <= 8; o++ {
		stored := unorient(upright, o)
		data, err := EncodeCover(stored, o)
		if err != nil {
			t.Fatalf("方向 %d：%v", o, err)
		}
		img := checkCover(t, data, ow, oh) // 小图不放大：封面就是 60×90
		if l := luma(img, 3, 3); l > 60 {
			t.Errorf("方向 %d：左上角应该是黑的，亮度 %d", o, l)
		}
		if l := luma(img, ow-3, oh-3); l < 200 {
			t.Errorf("方向 %d：右下角应该是白的，亮度 %d", o, l)
		}
	}
	// 透明铺白。
	clear := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	data, err := EncodeCover(clear, 1)
	if err != nil {
		t.Fatal(err)
	}
	if l := luma(checkCover(t, data, 20, 20), 10, 10); l < 245 {
		t.Errorf("透明部分应该铺白，亮度 %d", l)
	}
}

// unorient 按 EXIF 方向的定义（第 0 行、第 0 列在转正后的哪一边）从转正后的图 up 造出文件里存的图，
// 不借助 orient，两边各写各的，方向公式写错了测试能发现。
func unorient(up *image.RGBA, o int) *image.RGBA {
	w, h := up.Bounds().Dx(), up.Bounds().Dy()
	sw, sh := w, h
	if o >= 5 {
		sw, sh = h, w
	}
	stored := image.NewRGBA(image.Rect(0, 0, sw, sh))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			var ux, uy int
			switch o {
			case 1: // 第 0 行在上、第 0 列在左
				ux, uy = x, y
			case 2: // 上、右
				ux, uy = w-1-x, y
			case 3: // 下、右
				ux, uy = w-1-x, h-1-y
			case 4: // 下、左
				ux, uy = x, h-1-y
			case 5: // 第 0 行在左、第 0 列在上
				ux, uy = y, x
			case 6: // 右、上
				ux, uy = w-1-y, x
			case 7: // 右、下
				ux, uy = w-1-y, h-1-x
			case 8: // 左、下
				ux, uy = y, h-1-x
			}
			stored.SetRGBA(x, y, up.RGBAAt(ux, uy))
		}
	}
	return stored
}
