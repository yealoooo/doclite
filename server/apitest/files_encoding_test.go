package apitest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUploadRareEncodings：导出 PDF 做不了的罕见编码（internal/platform/imagehdr/testdata/encoding 的 reject-*：算术编码、无损、
// 12 位的 JPEG，JPEG 2000、JBIG 压缩的 TIFF，RLE 压缩的 BMP……）上传和重扫时 400 invalid_image，提示换个保存格式重扫，
// 什么都没写；常见样本（server/testdata/samples，含 BMP、GIF、WebP）和少见但导出做得了的写法（accept-*）照常入库，
// 多页 TIFF、多帧 GIF、动画 WebP 按多页拒绝。
func TestUploadRareEncodings(t *testing.T) {
	t.Parallel()
	env, tmp := newUploadEnv(t)
	admin := env.Admin()
	p := env.CreateProject("ENC")
	f := mkdir(admin, p.RootFolderID, "盒")

	dir := filepath.Join("..", "internal", "platform", "imagehdr", "testdata", "encoding")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rejected := 0
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		res := admin.Upload(f.ID, e.Name(), data, nil)
		if strings.HasPrefix(e.Name(), "accept-") {
			res.Expect(http.StatusCreated)
			continue
		}
		rejected++
		res.ExpectError(http.StatusBadRequest, "invalid_image")
		if msg := Decode[struct{ Message string }](res).Message; !strings.HasPrefix(msg, "不支持的图片格式（") ||
			!strings.HasSuffix(msg, "请换个保存格式重扫") {
			t.Errorf("%s 的提示：%s", e.Name(), msg)
		}
	}
	if rejected < 10 {
		t.Fatalf("只试了 %d 个罕见编码", rejected)
	}

	// 重扫件也查（同一个接收流程）。
	up := uploadOK(t, admin, f.ID, "IMG_0001.jpg", "enc-1")
	env.Exec(`UPDATE folder_files SET review_status = 'rejected' WHERE file_id = $1`, up.FileID)
	arith, err := os.ReadFile(filepath.Join(dir, "reject-jpeg-arith.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	body, ct := multipartBody(t, textPart("file_name", "IMG_0001.jpg"), filePart("file", arith))
	admin.PostRaw("/api/files/"+up.FileID.String()+"/rescan", ct, body).ExpectError(http.StatusBadRequest, "invalid_image")

	// 常见样本照常入库（多页 TIFF、多帧 GIF、动画 WebP 按多页拒绝，提示保存成单张）；BMP、GIF、WebP 的宽高、DPI、颜色。
	samples, err := os.ReadDir(filepath.Join("..", "testdata", "samples"))
	if err != nil {
		t.Fatal(err)
	}
	type spec struct {
		w, h, dpi int
		color     string
	}
	newFormats := map[string]spec{
		"bmp-rgb24.bmp": {40, 56, 200, "color"}, "bmp-1bit.bmp": {40, 56, 300, "bw"}, "bmp-gray4.bmp": {40, 56, 150, "gray"},
		"gif-single.gif": {40, 56, 0, "color"}, "webp-lossy.webp": {40, 56, 0, "color"}, "webp-lossless-alpha.webp": {40, 56, 0, "color"},
	}
	prefix := QueryValue[string](env, `SELECT path_prefix FROM storage_profiles WHERE is_default`)
	want := 1 // 上面的重扫用的那个
	for _, e := range samples {
		name := e.Name()
		if filepath.Ext(name) == ".md" {
			continue
		}
		res := admin.Upload(f.ID, name, unique(sampleBytes(t, name), "enc"), nil)
		if strings.Contains(name, "multipage") {
			res.ExpectError(http.StatusBadRequest, "invalid_image")
			msg := Decode[struct{ Message string }](res).Message
			if ext := filepath.Ext(name); ext != ".tif" && !strings.Contains(msg, "请保存成单张图片再上传") {
				t.Errorf("%s 的提示：%s", name, msg)
			}
			continue
		}
		want++
		got := Decode[ingestResponse](res.Expect(http.StatusCreated))
		if s, ok := newFormats[name]; ok {
			dpi := 0
			if got.DPI != nil {
				dpi = *got.DPI
			}
			if *got.WidthPx != s.w || *got.HeightPx != s.h || dpi != s.dpi || got.ColorMode == nil || *got.ColorMode != s.color {
				t.Errorf("%s：%d×%d %d DPI %v", name, *got.WidthPx, *got.HeightPx, dpi, got.ColorMode)
			}
			ext := strings.TrimPrefix(filepath.Ext(name), ".")
			if v := QueryValue[string](env, `SELECT mime_type || ' ' || storage_key FROM file_snapshots WHERE id = $1`, got.SnapshotID); v !=
				"image/"+ext+" "+prefix+"ENC/"+got.FileID.String()+"/v1."+ext {
				t.Errorf("%s 的 mime_type 和 key：%s", name, v)
			}
		}
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "accept-") {
			want++
		}
	}
	if n := QueryValue[int](env, `SELECT count(*) FROM folder_files WHERE project_id = $1`, p.ID); n != want {
		t.Fatalf("入库 %d 个，应有 %d 个", n, want)
	}
	if n := QueryValue[int](env, `SELECT count(*) FROM file_snapshots s JOIN folder_files ff ON ff.file_id = s.file_id
WHERE ff.project_id = $1 AND s.version_no > 1`, p.ID); n != 0 {
		t.Fatalf("被拒绝的重扫件不应写快照：%d", n)
	}
	if left := uploadTmpFiles(t, tmp); len(left) != 0 {
		t.Fatalf("临时文件没删：%v", left)
	}
}
