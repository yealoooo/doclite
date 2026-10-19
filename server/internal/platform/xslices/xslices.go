// Package xslices 是几个各模块都要用的切片小工具（标准库 slices 没有的）。
package xslices

import (
	"bytes"
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Unique 去重，保持第一次出现的顺序；返回值不为 nil。
func Unique[T comparable](s []T) []T {
	out := make([]T, 0, len(s))
	seen := make(map[T]bool, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// SortedIDs 去重并按字节序（和 PostgreSQL 里 uuid 的顺序一致）排好；返回值不为 nil（空集合在 SQL 里是 '{}'，不是 NULL）。
func SortedIDs(ids []uuid.UUID) []uuid.UUID {
	out := append([]uuid.UUID{}, ids...)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(out)
}

// LongestIncreasing 标出 xs 的一个最长严格递增子序列（true 是在子序列里的），O(n log n)。页序变化里"挪动过的页"
// 就是不在子序列里的（插入、移出引起的顺延不算）。
func LongestIncreasing(xs []int) []bool {
	in := make([]bool, len(xs))
	if len(xs) == 0 {
		return in
	}
	tails := make([]int, 0, len(xs)) // tails[k] 是长度 k+1 的递增子序列里最小结尾的下标
	prev := make([]int, len(xs))
	for i, x := range xs {
		k, _ := slices.BinarySearchFunc(tails, x, func(t, v int) int { return cmp.Compare(xs[t], v) })
		prev[i] = -1
		if k > 0 {
			prev[i] = tails[k-1]
		}
		if k == len(tails) {
			tails = append(tails, i)
		} else {
			tails[k] = i
		}
	}
	for i := tails[len(tails)-1]; i >= 0; i = prev[i] {
		in[i] = true
	}
	return in
}
