//go:build !windows

package upload

import "io/fs"

// hiddenOrSystem：macOS、Linux 上隐藏文件就是 `.` 开头的（[IgnoredName] 已经管了）。
func hiddenOrSystem(fs.FileInfo) bool { return false }

// canOpenExclusive：macOS 上没有"独占打开"（文件锁是建议性的），这一条跳过（requirements 第 6 节）。
func canOpenExclusive(string) bool { return true }
