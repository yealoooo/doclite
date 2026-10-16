package imgdecode_test

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"doclite/client/internal/imgdecode"
	"doclite/client/internal/tiles"
	"doclite/client/internal/upload"
)

// 封面（upload）和切块（tiles）经这个包解 JPEG 压缩的 TIFF：做出来的封面、最高一级的块，和同一画面的 LZW 样本
// 做出来的比较平均颜色（RGB 当成 YCbCr 再转一次会整体偏紫，这里会发现）。
func TestCoverAndTilesJPEGTIFF(t *testing.T) {
	lzw := imgdecode.DecodeRef(t, "../../testdata/samples/tiff-lzw-rgb.tif").(*image.RGBA)
	crop := func(w, h int) image.Image { return lzw.SubImage(image.Rect(0, 0, w, h)) }
	ycbcr := "../../testdata/samples/tiff-jpeg-ycbcr.tif"
	rgb := "testdata/tiff-jpeg-rgb.tif"
	rgbIDs := "testdata/tiff-jpeg-rgb-ids.tif"

	decode := func(t *testing.T, data []byte) image.Image {
		t.Helper()
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	for _, c := range []struct {
		path string
		ref  image.Image
	}{{ycbcr, lzw}, {rgbIDs, crop(160, 136)}} { // 封面是整页缩小的，和 LZW 样本整页比
		t.Run("封面 "+filepath.Base(c.path), func(t *testing.T) {
			cv := upload.MakeCover(c.path)
			if cv.Data == nil {
				t.Fatalf("没有生成封面：%s", cv.Reason)
			}
			want, err := upload.EncodeCover(c.ref, 1)
			if err != nil {
				t.Fatal(err)
			}
			got, ref := decode(t, cv.Data), decode(t, want)
			if got.Bounds() != ref.Bounds() {
				t.Fatalf("封面 %v，应为 %v", got.Bounds(), ref.Bounds())
			}
			imgdecode.CompareColors(t, got, ref)
		})
	}

	// 400×421 最高一级（9）只有一块，和 LZW 样本左上角比；595×842 的样本只有版式和 LZW 样本相同（文字行不同），
	// 看第 9 级（缩小一半，一块 298×421）的整页。
	for _, c := range []struct {
		path        string
		level, w, h int
		ref         image.Image
	}{{rgb, 9, 400, 421, crop(400, 421)}, {ycbcr, 9, 298, 421, lzw}} {
		t.Run("切块 "+filepath.Base(c.path), func(t *testing.T) {
			plan, err := tiles.NewPlan(c.path)
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			if _, err := plan.Generate(context.Background(), out, tiles.Options{}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(out, tiles.TilePath(c.level, 0, 0, "jpg")))
			if err != nil {
				t.Fatal(err)
			}
			got := decode(t, data)
			if b := got.Bounds(); b.Dx() != c.w || b.Dy() != c.h {
				t.Fatalf("块是 %v，应为 %d×%d", b, c.w, c.h)
			}
			imgdecode.CompareColors(t, got, c.ref)
		})
	}
}
