package xslices

import "testing"

func TestLongestIncreasing(t *testing.T) {
	for _, c := range []struct {
		in    []int
		moved int
	}{
		{nil, 0}, {[]int{5}, 0}, {[]int{1, 2, 3}, 0}, {[]int{3, 2, 1}, 2}, {[]int{2, 1, 3}, 1},
		{[]int{1, 5, 2, 3, 4}, 1}, // 插入、移出引起的顺延不算：只有 5 挪过
		{[]int{10, 1, 2, 11, 3, 12, 4}, 3},
	} {
		in := LongestIncreasing(c.in)
		moved, last := 0, -1
		for i, ok := range in {
			if !ok {
				moved++
				continue
			}
			if c.in[i] <= last {
				t.Errorf("%v：标出的子序列不递增 %v", c.in, in)
			}
			last = c.in[i]
		}
		if moved != c.moved {
			t.Errorf("%v：挪过 %d 个，期望 %d", c.in, moved, c.moved)
		}
	}
}
