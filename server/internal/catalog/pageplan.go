package catalog

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// 页序的纯函数（requirements 5.9）：重新归档把条目的页换成来源文件夹现在的文件，页号重排成 1..n；
// 调序（reorder 审计、reseal-preview 的 moved）只算留下的页里相对顺序变了的。

// MaxItemPages 是一个条目最多的页数（件文件夹最多 2000 个文件）。
const MaxItemPages = 2000

// curPage 是条目现在的一页。
type curPage struct {
	FileID uuid.UUID
	PageNo int
}

// pageMove 是一页的变化：From 是原页号（新挂上的为 0），To 是新页号（移出的为 0）。
type pageMove struct {
	FileID uuid.UUID
	From   int
	To     int
}

// pagePlan 是把条目的页换成 wanted 要做的事。
type pagePlan struct {
	// Linked 是新挂上的（按新页号）。
	Linked []pageMove
	// Unlinked 是移出的（按原页号）。
	Unlinked []pageMove
	// Repaged 是留下、页号变了的（按原页号）。
	Repaged []pageMove
	// Moved 是留下的页里"挪动过的"（按原页号）：留下的页按原来的顺序取最长递增子序列，其余的就是。只是插入、
	// 移出引起的页号顺延不算。
	Moved []pageMove
	// Kept 是留下的页数。
	Kept int
}

// changed：有要写库的变化（页号不连续的旧数据也算，重排成 1..n）。
func (p *pagePlan) changed() bool {
	return len(p.Linked) > 0 || len(p.Unlinked) > 0 || len(p.Repaged) > 0
}

// touched 是新挂上的和移出的文件（要锁 files 行的那些），按新挂上、移出的顺序。
func (p *pagePlan) touched() []uuid.UUID {
	out := make([]uuid.UUID, 0, len(p.Linked)+len(p.Unlinked))
	for _, m := range p.Linked {
		out = append(out, m.FileID)
	}
	for _, m := range p.Unlinked {
		out = append(out, m.FileID)
	}
	return out
}

// planPages 比较现在的页（current，按页号排）和要的页（wanted，按页序，不重复）。
func planPages(current []curPage, wanted []uuid.UUID) pagePlan {
	cur := slices.Clone(current)
	slices.SortStableFunc(cur, func(a, b curPage) int { return cmp.Compare(a.PageNo, b.PageNo) })
	newPage := make(map[uuid.UUID]int, len(wanted))
	for i, id := range wanted {
		newPage[id] = i + 1
	}
	oldPage := make(map[uuid.UUID]int, len(cur))
	var p pagePlan
	var keptTo []int // 留下的页按原来的顺序，各自的新页号
	var kept []curPage
	for _, c := range cur {
		oldPage[c.FileID] = c.PageNo
		to, ok := newPage[c.FileID]
		if !ok {
			p.Unlinked = append(p.Unlinked, pageMove{FileID: c.FileID, From: c.PageNo})
			continue
		}
		kept = append(kept, c)
		keptTo = append(keptTo, to)
		if to != c.PageNo {
			p.Repaged = append(p.Repaged, pageMove{FileID: c.FileID, From: c.PageNo, To: to})
		}
	}
	for i, id := range wanted {
		if _, ok := oldPage[id]; !ok {
			p.Linked = append(p.Linked, pageMove{FileID: id, To: i + 1})
		}
	}
	p.Kept = len(kept)
	inLIS := longestIncreasing(keptTo)
	for i, c := range kept {
		if !inLIS[i] {
			p.Moved = append(p.Moved, pageMove{FileID: c.FileID, From: c.PageNo, To: keptTo[i]})
		}
	}
	return p
}

// longestIncreasing 标出 xs 的一个最长严格递增子序列（O(n log n)）。xs 互不相同。
func longestIncreasing(xs []int) []bool {
	in := make([]bool, len(xs))
	if len(xs) == 0 {
		return in
	}
	tails := make([]int, 0, len(xs)) // tails[k] 是长度 k+1 的递增子序列里最小结尾的下标
	prev := make([]int, len(xs))
	for i, x := range xs {
		k, _ := slices.BinarySearchFunc(tails, x, func(t, v int) int { return cmp.Compare(xs[t], v) })
		if k > 0 {
			prev[i] = tails[k-1]
		} else {
			prev[i] = -1
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
