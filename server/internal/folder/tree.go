package folder

import (
	"fmt"

	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/pathcode"
)

// 路径编码见 internal/platform/pathcode（硬性规则 8，requirements 3.3）。

// childPath 拼子编码；n 超过 99999 时 409 tree_full。
func childPath(parent string, n int) (string, error) {
	if n < 1 || n > pathcode.MaxChildNo {
		return "", httpx.Conflict(httpx.CodeTreeFull, fmt.Sprintf("这一层的子文件夹编号已用完（每个文件夹下最多 %d 个，删除的也占编号）", pathcode.MaxChildNo))
	}
	return pathcode.Child(parent, n), nil
}
