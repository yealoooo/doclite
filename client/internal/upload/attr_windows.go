//go:build windows

package upload

import (
	"errors"
	"io/fs"
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
	p, err := windows.UTF16PtrFromString(path)
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
