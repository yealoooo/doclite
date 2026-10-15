//go:build unix

package export

import "syscall"

// freeSpace 是 dir 所在磁盘给普通用户的剩余空间；查不了时 ok 为 false。
func freeSpace(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true //nolint:unconvert
}
