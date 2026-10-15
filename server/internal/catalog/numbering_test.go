package catalog

import (
	"testing"

	"github.com/google/uuid"
)

// TestAllocate：批量重算的分档先到先得——范围外的先占；① 有号不换 ② 有号要换 ③ 原来没号，档内按 id；结果确定。
func TestAllocate(t *testing.T) {
	id := func(n byte) uuid.UUID { return uuid.UUID{15: n} }
	p := func(s string) *string { return &s }
	outsider := id(99)
	cands := []candidate{
		{ID: id(1), Current: nil, Generated: "K"},     // ③：K 被 4 号（①）占着 → 空
		{ID: id(2), Current: p("A"), Generated: "B"},  // ②：2 号、3 号都要 B，同一档按 id，2 号先拿到
		{ID: id(3), Current: p("C"), Generated: "B"},  // ②：B 已被 2 号拿走 → 空（撞号）
		{ID: id(4), Current: p("K"), Generated: "K"},  // ①：不换
		{ID: id(5), Current: p("X"), Generated: "O"},  // ②：O 被范围外的占着 → 空
		{ID: id(6), Current: nil, Generated: ""},      // 缺级 → 空，不算撞号
		{ID: id(7), Current: p("Y"), Generated: "A"},  // ②：A 是 2 号原来的号，2 号换走了 → 拿到
		{ID: id(8), Current: nil, Generated: "C"},     // ③：C 是 3 号原来的号，3 号留空了 → 拿到
		{ID: id(0), Current: p("M"), Generated: "M2"}, // ②：id 最小，先拿
	}
	got := allocate(cands, map[string]uuid.UUID{"O": outsider})
	want := map[byte]struct {
		no     string
		holder uuid.UUID
	}{
		0: {"M2", uuid.Nil}, 1: {"", id(4)}, 2: {"B", uuid.Nil}, 3: {"", id(2)}, 4: {"K", uuid.Nil},
		5: {"", outsider}, 6: {"", uuid.Nil}, 7: {"A", uuid.Nil}, 8: {"C", uuid.Nil},
	}
	if len(got) != len(cands) {
		t.Fatalf("个数：%d", len(got))
	}
	conflicts := 0
	for i, a := range got {
		if i > 0 && a.ID[15] <= got[i-1].ID[15] {
			t.Fatalf("结果应按 id 升序：%v", got)
		}
		w := want[a.ID[15]]
		no := ""
		if a.New != nil {
			no = *a.New
		}
		if no != w.no || a.Holder != w.holder {
			t.Errorf("%d 号：%q（占用者 %s），期望 %q（%s）", a.ID[15], no, a.Holder, w.no, w.holder)
		}
		if a.conflicted() {
			conflicts++
		}
	}
	if conflicts != 3 {
		t.Errorf("撞号留空的个数：%d", conflicts)
	}
	// 再按同样的输入算一次，结果相同（确定）；输入顺序不影响。
	rev := make([]candidate, len(cands))
	for i := range cands {
		rev[i] = cands[len(cands)-1-i]
	}
	again := allocate(rev, map[string]uuid.UUID{"O": outsider})
	for i := range got {
		if !equalStr(got[i].New, again[i].New) {
			t.Fatalf("结果不确定：%d", got[i].ID[15])
		}
	}
}
