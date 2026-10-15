//go:build !windows

package upload

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile 以排他方式锁住 path（不等待）：同一个数据目录只有一个进程打开队列。进程退出时系统自动解锁。
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, ErrQueueLocked
		}
		return nil, err
	}
	return f, nil
}
