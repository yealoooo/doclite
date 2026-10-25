// Package pathcode 是树的路径编码（硬性规则 8，requirements 3.3），文件夹（folders）和归类节点（archive_categories）
// 共用：根是 `00001.`，每层 5 位数字加一个点；子编码 = 父编码 + 父节点 last_child_no 原子 +1 的 5 位数 + 点，不回收；
// 每层最多 99999 个。列是 COLLATE "C"，按字节序比较：子树是区间 [前缀, 前缀末尾的点换成 /)，按 path_code 排序就是先序。
package pathcode

import (
	"fmt"
	"strings"
)

// MaxChildNo 是每层最多的子节点数。
const MaxChildNo = 99999

// segmentLen 是一层的长度：5 位数字 + 点。
const segmentLen = 6

// Child 拼第 n 个子编码（n 在 1–MaxChildNo，由调用方检查）。
func Child(parent string, n int) string {
	return fmt.Sprintf("%s%05d.", parent, n)
}

// Upper 是 prefix 这棵子树（含自己）的编码区间 [prefix, Upper) 的上界：末尾的点换成 /（'/' 紧跟在 '.' 后面）。
func Upper(prefix string) string {
	return prefix[:len(prefix)-1] + "/"
}

// Ancestors 是 path 的各级祖先的编码（从根起，不含自己）。
func Ancestors(path string) []string {
	var out []string
	for n := segmentLen; n < len(path); n += segmentLen {
		out = append(out, path[:n])
	}
	return out
}

// Prefixes 是 path 的各级前缀（从根到自己）。
func Prefixes(path string) []string {
	return append(Ancestors(path), path)
}

// InSubtree：path 在 root 这棵子树里（含 root 自己）。
func InSubtree(path, root string) bool { return strings.HasPrefix(path, root) }
