package bundle

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"doclite/server/internal/platform/imagehdr"
)

// 上传时的检查（platform/imagehdr 的 ParseUpload）和导出对同一批文件的结论一致：导出 PDF 判为"不支持"的，上传时就被
// 拒绝；上传拒绝的，导出也确实做不了。两边各写了一份判断（imagehdr 只用标准库，不依赖 server 的其他包），这个测试防止
// 它们走偏。客户端的 imagehdr 是 server 这份的副本（改动两边同步），上传是否拒收以 server 为准。
//
// 文件：常见样本、这个包的夹具（tiff-jpeg-cmyk、-gray、-rgb-ids、ojpeg 四个和客户端 internal/imgdecode/testdata 里的
// 相同）、imagehdr 的罕见编码夹具（reject-*、accept-*，生成方法见 platform/imagehdr/encoding_test.go）。
func TestUploadCheckMatchesExport(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "..", "..", "testdata", "samples"),
		"testdata",
		filepath.Join("..", "..", "platform", "imagehdr", "testdata", "encoding"),
	}
	opts := Options{}.withDefaults()
	n := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			ext := filepath.Ext(e.Name())
			if ext != ".jpg" && ext != ".png" && ext != ".tif" {
				continue
			}
			n++
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			_, upErr := imagehdr.ParseUpload(bytes.NewReader(data), int64(len(data)), 0)
			rejected := errors.Is(upErr, imagehdr.ErrUnsupportedEncoding) || errors.Is(upErr, imagehdr.ErrMultiPage)
			if upErr != nil && !rejected {
				t.Errorf("%s：上传时应该通过或按编码拒绝，得到 %v", e.Name(), upErr)
				continue
			}
			page := Page{Size: int64(len(data)), MIME: mimeOf(e.Name())}
			_, exErr := analyze(bytes.NewReader(data), &page, opts, false)
			var p *problem
			unsupported := errors.As(exErr, &p) && p.kind == KindUnsupported ||
				errors.Is(exErr, imagehdr.ErrUnsupported) || errors.Is(exErr, imagehdr.ErrMultiPage)
			if exErr != nil && !unsupported {
				t.Errorf("%s：导出报的不是\"不支持\"：%v", e.Name(), exErr)
				continue
			}
			if rejected != unsupported {
				t.Errorf("%s：上传 %v，导出 %v", e.Name(), upErr, exErr)
			}
		}
	}
	if n < 30 {
		t.Fatalf("只检查了 %d 个文件", n)
	}
}
