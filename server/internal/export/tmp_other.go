//go:build !unix

package export

// freeSpace 在非 unix 系统上不查（server 部署在 Linux 容器里）。
func freeSpace(string) (int64, bool) { return 0, false }
