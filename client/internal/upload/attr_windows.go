//go:build windows

package upload

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// hiddenOrSystem：Windows 上带隐藏或系统属性的文件、目录（热文件夹忽略）。
func hiddenOrSystem(info fs.FileInfo) bool {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&(windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM) != 0
	}
	return false
}

// canOpenExclusive：能不能以独占方式打开（不和任何人共享读写删）。扫描软件还在写的文件打不开（共享冲突）。
// 只有共享冲突、锁冲突算"不能"；别的错误（文件不见了、没权限）交给后面的步骤处理，这里算能。
func canOpenExclusive(path string) bool {
	p, err := windows.UTF16PtrFromString(longPath(path))
	if err != nil {
		return true
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION)
	}
	windows.CloseHandle(h)
	return true
}

// longPath：长路径（和 os 包一样从 248 个字符起）加上扩展长度前缀 \\?\（网络路径 \\server\share 换成
// \\?\UNC\server\share），直接调 CreateFile 才打得开（os 包自己会加，这里直接调系统接口要自己加）。
// 短路径、相对路径、已经带前缀的原样返回。
func longPath(path string) string {
	if len(path) < 248 || !filepath.IsAbs(path) || strings.HasPrefix(path, `\\?\`) {
		return path
	}
	p := filepath.Clean(path)
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}
