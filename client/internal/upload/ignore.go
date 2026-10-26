package upload

import (
	"path/filepath"
	"strings"
)

// MaxDepth 是热文件夹（和上传按钮选的文件夹）最多往下看几层子文件夹（requirements 第 6 节：≤ 32 层）。
const MaxDepth = 32

// 只收这几种扩展名（和浏览器上传、server 收的格式一致；格式最终按文件头的魔数判断）。
var supportedExts = map[string]bool{".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true, ".png": true,
	".bmp": true, ".gif": true, ".webp": true}

// SupportedImage：扩展名是 jpg、jpeg、tif、tiff、png、bmp、gif、webp（不分大小写）。
func SupportedImage(name string) bool {
	return supportedExts[strings.ToLower(filepath.Ext(name))]
}

// IgnoredName 判断热文件夹里的一个文件或目录要不要忽略（requirements 第 6 节：忽略隐藏、系统、临时文件）：
// `.` 开头（含 .DS_Store）、`~$` 开头（Office 的锁文件）、`.tmp` 结尾、Thumbs.db、desktop.ini；目录另外忽略
// $RECYCLE.BIN、System Volume Information。Windows 上带隐藏或系统属性的另外由 [hiddenOrSystem] 判断。
func IgnoredName(name string, isDir bool) bool {
	lower := strings.ToLower(name)
	switch {
	case name == "", strings.HasPrefix(name, "."), strings.HasPrefix(name, "~$"), strings.HasSuffix(lower, ".tmp"):
		return true
	case lower == "thumbs.db", lower == "desktop.ini":
		return true
	case isDir && (lower == "$recycle.bin" || lower == "system volume information"):
		return true
	}
	return false
}
