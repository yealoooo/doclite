package catalog

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

// TestPlanPages：整体替换页序时算出的挂上、移出、改页号和"挪动过的页"（最长递增子序列之外的）。
func TestPlanPages(t *testing.T) {
	ids := make([]uuid.UUID, 8)
	for i := range ids {
		ids[i] = uuid.New()
	}
	a, b, c, d, e := ids[0], ids[1], ids[2], ids[3], ids[4]
	cur := func(fs ...uuid.UUID) []curPage {
		out := make([]curPage, len(fs))
		for i, f := range fs {
			out[i] = curPage{FileID: f, PageNo: i + 1}
		}
		return out
	}
	moved := func(p pagePlan) []uuid.UUID { return fileIDsOf(p.Moved) }

	cases := []struct {
		name                     string
		current                  []curPage
		wanted                   []uuid.UUID
		linked, unlinked, repage int
		moved                    []uuid.UUID
		changed                  bool
	}{
		{"空到空", nil, nil, 0, 0, 0, nil, false},
		{"不变", cur(a, b, c), []uuid.UUID{a, b, c}, 0, 0, 0, nil, false},
		{"挂到空条目", nil, []uuid.UUID{a, b}, 2, 0, 0, nil, true},
		{"清空", cur(a, b), nil, 0, 2, 0, nil, true},
		// 插在前面：后面的顺延，不算挪动。
		{"插入", cur(a, b, c), []uuid.UUID{d, a, b, c}, 1, 0, 3, nil, true},
		// 移出中间一页：后面的顺延，不算挪动。
		{"移出", cur(a, b, c), []uuid.UUID{a, c}, 0, 1, 1, nil, true},
		// 把最后一页拖到最前：只有它挪动过。
		{"拖一页", cur(a, b, c, d), []uuid.UUID{d, a, b, c}, 0, 0, 4, []uuid.UUID{d}, true},
		// 互换两页：最长递增子序列长 1，挪动 1 页。
		{"互换", cur(a, b), []uuid.UUID{b, a}, 0, 0, 2, []uuid.UUID{a}, true},
		// 倒过来：留下 1 页，其余都挪动过。
		{"倒序", cur(a, b, c, d, e), []uuid.UUID{e, d, c, b, a}, 0, 0, 4, []uuid.UUID{a, b, c, d}, true},
		// 同时插入、移出、拖动：b 移出，e 挂上，d 拖到 a 前面。
		{"混合", cur(a, b, c, d), []uuid.UUID{d, a, e, c}, 1, 1, 3, []uuid.UUID{d}, true},
	}
	for _, tc := range cases {
		p := planPages(tc.current, tc.wanted)
		if len(p.Linked) != tc.linked || len(p.Unlinked) != tc.unlinked || len(p.Repaged) != tc.repage ||
			!slices.Equal(moved(p), tc.moved) || p.changed() != tc.changed {
			t.Errorf("%s：linked %d unlinked %d repaged %d moved %v changed %v", tc.name,
				len(p.Linked), len(p.Unlinked), len(p.Repaged), moved(p), p.changed())
		}
		// 新页号是 1..n，移出的带原页号。
		for _, m := range p.Linked {
			if tc.wanted[m.To-1] != m.FileID {
				t.Errorf("%s：新挂上的页号不对：%+v", tc.name, m)
			}
		}
		for _, m := range p.Unlinked {
			if m.From == 0 || m.To != 0 {
				t.Errorf("%s：移出的页：%+v", tc.name, m)
			}
		}
	}

	// 原来的页号不连续（旧数据）：重排成 1..n 也算变化。
	p := planPages([]curPage{{a, 2}, {b, 5}}, []uuid.UUID{a, b})
	if !p.changed() || len(p.Repaged) != 2 || len(p.Moved) != 0 {
		t.Errorf("页号不连续：%+v", p)
	}
}

func TestLongestIncreasing(t *testing.T) {
	for _, tc := range []struct {
		xs   []int
		want int
	}{
		{nil, 0}, {[]int{5}, 1}, {[]int{1, 2, 3}, 3}, {[]int{3, 2, 1}, 1},
		{[]int{2, 6, 3, 4, 1, 5}, 4}, {[]int{10, 1, 2, 11, 3, 12, 4}, 4},
	} {
		in := longestIncreasing(tc.xs)
		var seq []int
		for i, ok := range in {
			if ok {
				seq = append(seq, tc.xs[i])
			}
		}
		if len(seq) != tc.want || !slices.IsSorted(seq) {
			t.Errorf("%v：%v", tc.xs, seq)
		}
	}
}
