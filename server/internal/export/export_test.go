package export

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLongestIncreasing(t *testing.T) {
	for _, c := range []struct {
		in    []int
		moved int
	}{
		{nil, 0}, {[]int{1, 2, 3}, 0}, {[]int{2, 1, 3}, 1}, {[]int{3, 1, 2}, 1}, {[]int{3, 2, 1}, 2},
		{[]int{1, 5, 2, 3, 4}, 1}, // 插入、移出引起的顺延不算：只有 5 挪过
		{[]int{2, 3, 4, 5}, 0},
	} {
		in := longestIncreasing(c.in)
		moved, last := 0, 0
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
			t.Errorf("%v：挪过 %d 页，期望 %d", c.in, moved, c.moved)
		}
	}
}

func TestDownloadFileName(t *testing.T) {
	day := time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC) // UTC+8 已经是 16 号
	cat := func(names ...[]string) *taskRow {
		r := &taskRow{ScopeType: scopeTypeCategory}
		for _, n := range names {
			r.Header.ScopeCategories = append(r.Header.ScopeCategories, ScopeCategory{ID: uuid.New(), CategoryNames: n})
		}
		return r
	}
	items := &taskRow{ScopeType: scopeTypeItem}
	items.Header.ItemCount = 12
	resub := &taskRow{ScopeType: scopeTypeItem}
	resub.Header.ItemCount, resub.Header.ResubmitOf = 3, &uuid.UUID{}
	for _, c := range []struct {
		t     *taskRow
		first string
		want  string
	}{
		{cat([]string{"全宗", "文书"}), "", "WS_文书_20261016.zip"},
		{cat([]string{}), "", "WS_全部_20261016.zip"},
		{cat([]string{"文书"}, []string{"会计"}), "", "WS_文书等2个分类_20261016.zip"},
		{cat([]string{"a/b: c..."}), "", "WS_a_b_ c_20261016.zip"},
		{cat([]string{strings.Repeat("很", 50)}), "", "WS_" + strings.Repeat("很", 40) + "_20261016.zip"},
		{items, "Z1-WS-0001", "WS_Z1-WS-0001等12件_20261016.zip"},
		{resub, "", "WS_补交3件_20261016.zip"},
	} {
		if got := DownloadFileName("WS", scopeLabel(c.t, c.first), day); got != c.want {
			t.Errorf("文件名 %q，期望 %q", got, c.want)
		}
	}
	cd := ContentDisposition(asciiFileName("WS-2025", day), "WS_文书 1_20261016.zip")
	if cd != `attachment; filename="WS-2025_export_20261016.zip"; filename*=UTF-8''WS_%E6%96%87%E4%B9%A6%201_20261016.zip` {
		t.Errorf("Content-Disposition：%s", cd)
	}
}
