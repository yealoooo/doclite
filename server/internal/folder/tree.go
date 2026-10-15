package folder

import (
	"fmt"
	"strings"

	"doclite/server/internal/platform/httpx"
)

// 路径编码（硬性规则 8，requirements 3.3）：根是 `00001.`，每层 5 位数字加一个点；子编码 = 父编码 + 父节点
// last_child_no 原子 +1 的 5 位数 + 点，不回收；每层最多 99999 个（超出 409 tree_full）。列是 COLLATE "C"，
// 按字节序比较：子树是区间 [前缀, 前缀末尾的点换成 /)，按 path_code 排序就是先序。

// MaxChildNo 是每层最多的子节点数。
const MaxChildNo = 99999

const segmentLen = 6 // 5 位数字 + 点

// childPath 拼子编码；n 超过 99999 时 409 tree_full。
func childPath(parent string, n int) (string, error) {
	if n < 1 || n > MaxChildNo {
		return "", httpx.Conflict("tree_full", fmt.Sprintf("这一层的子文件夹编号已用完（每个文件夹下最多 %d 个，删除的也占编号）", MaxChildNo))
	}
	return fmt.Sprintf("%s%05d.", parent, n), nil
}

// subtreeRange 是 prefix 这棵子树（含自己）的编码区间 [lo, hi)：hi 是把末尾的点换成 /（'/' 紧跟在 '.' 后面）。
func subtreeRange(prefix string) (lo, hi string) {
	return prefix, prefix[:len(prefix)-1] + "/"
}

// ancestorPaths 是 path 的各级祖先的编码（从根起，不含自己）。
func ancestorPaths(path string) []string {
	var out []string
	for n := segmentLen; n < len(path); n += segmentLen {
		out = append(out, path[:n])
	}
	return out
}

// inSubtree：path 在 root 这棵子树里（含 root 自己）。
func inSubtree(path, root string) bool { return strings.HasPrefix(path, root) }
