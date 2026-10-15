package file

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"doclite/server/internal/platform/httpx"
	"doclite/shared/imagehdr"
)

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withOrientation 在 SOI 之后插一个只有 Orientation 的 EXIF（APP1，大端）。
func withOrientation(data []byte, o byte) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 1, 0x12, 0, 3, 0, 0, 0, 1, 0, o, 0, 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	out := append([]byte{}, data[:2]...)
	out = append(append(out, seg...), payload...)
	return append(out, data[2:]...)
}

func TestValidateCover(t *testing.T) {
	portrait := imagehdr.Info{Format: imagehdr.JPEG, Width: 2480, Height: 3508}
	small := imagehdr.Info{Format: imagehdr.PNG, Width: 300, Height: 200}
	square := imagehdr.Info{Format: imagehdr.TIFF, Width: 1000, Height: 1000}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, image.NewGray(image.Rect(0, 0, 283, 400)))
	ok := jpegOf(t, 283, 400)

	cases := []struct {
		name  string
		cover []byte
		orig  imagehdr.Info
		valid bool
	}{
		{"合规", ok, portrait, true},
		{"短边差 1 像素", jpegOf(t, 284, 400), portrait, true},
		{"原图比 400 小、不放大", jpegOf(t, 300, 200), small, true},
		{"正方形不查横竖", jpegOf(t, 400, 399), square, true},
		{"方向 1 的 EXIF 可以", withOrientation(ok, 1), portrait, true},
		{"不是 JPEG", pngBuf.Bytes(), portrait, false},
		{"不以 FF D9 结尾", ok[:len(ok)-1], portrait, false},
		{"带方向 6", withOrientation(ok, 6), portrait, false},
		{"长边超过 400", jpegOf(t, 354, 500), portrait, false},
		{"比原图大", jpegOf(t, 360, 240), small, false},
		{"横竖不一致", jpegOf(t, 400, 283), portrait, false},
		{"长宽比不对", jpegOf(t, 281, 400), portrait, false},
		{"超过 256 KiB", append(append([]byte{}, ok...), make([]byte, MaxCoverBytes)...), portrait, false},
	}
	for _, c := range cases {
		err := ValidateCover(c.cover, c.orig)
		if c.valid != (err == nil) {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		if err != nil {
			if e, isErr := httpx.AsError(err); !isErr || e.Code != CodeInvalidCover || e.Status != 400 {
				t.Errorf("%s：错误应是 400 invalid_cover，实际 %v", c.name, err)
			}
		}
	}
}
