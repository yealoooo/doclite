package upload

import (
	"errors"
	"os"

	"doclite/client/internal/imagehdr"
)

// 上传前按文件头拒绝导出不了的罕见编码（算术编码、无损、12 位的 JPEG，JPEG 2000、JBIG 压缩的 TIFF……）：和 server
// 上传时同样的判断（internal/imagehdr 的 ParseUpload，和 server 的那份同步，拒不拒以 server 为准），被拒的不发请求，
// 队列里记成 server 会给的同一个错误（400 invalid_image、同样的提示），扫描员当场知道要换个保存格式重扫。
//
// 只管编码方式：文件头的其他问题（读不出、多页、太大）照旧由 server 判断，这里放过；读本机文件出错也放过（后面
// 算 sha 时会按读不了处理）。

// ErrCodeInvalidImage 是 server 的错误码（文件头不合规）。
const ErrCodeInvalidImage = "invalid_image"

// UnsupportedEncoding 返回 path 的编码方式导出不了时给人看的原因（和 server 的提示一样）；能上传、判断不了时为空串。
func UnsupportedEncoding(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	_, err = imagehdr.ParseUpload(f, size, 0)
	if errors.Is(err, imagehdr.ErrUnsupportedEncoding) {
		return err.Error()
	}
	return ""
}

// checkEncoding 是队列里每一项上传前的检查：被拒时永久失败（不重试）。
func checkEncoding(it *Item) *result {
	if msg := UnsupportedEncoding(it.LocalPath, it.Size); msg != "" {
		return failed(ErrCodeInvalidImage, msg)
	}
	return nil
}
