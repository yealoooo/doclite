//go:build windows

package upload

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile 以排他方式锁住 path（不等待）：同一个数据目录只有一个进程打开队列。进程退出时系统自动解锁。
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	var ol windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrQueueLocked
		}
		return nil, err
	}
	return f, nil
}
