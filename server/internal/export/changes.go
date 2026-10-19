package export

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/xslices"
)

// 交付后变更（requirements 5.11，用户决定 Q6）：只对 done 的任务。在一个只读快照里拿冻结的明细和 payload（条目当时的
// 状态）和现在的库比较，按件列出原因：
//   - new_version：还在这个条目里的导出页出了新版本；
//   - pages_removed：导出的页现在不在任何条目里；pages_moved：移到了别的条目；
//   - pages_reordered：留下的页相对顺序变了（按导出的页号排，取现在页号的最长递增子序列，其余是挪过的；插入、移出
//     引起的顺延不算）；
//   - pages_added：条目里现在有、导出时不在这个条目里的页；
//   - not_approved：任务勾了"只导出审核通过的"，还在这个条目里的导出页现在不是 approved；
//   - item_changed：题名、档号、著录和导出时不一样（著录按 key 比，空串、null、没有这个 key 算一样）；
//   - item_deleted：条目已删除（只报这一条）。
//
// 只换节点而档号不变、节点改名不算。和页有关的原因带页数和前 5 页；最多列 500 件。resubmit_item_ids 是有变更、
// 没删除的全部条目（按导出顺序），"只补交有变更的 n 件"就是它们。

// 原因码（顺序就是输出的顺序）。
var changeCodes = []string{"new_version", "pages_removed", "pages_moved", "pages_reordered", "pages_added", "not_approved",
	"item_changed", "item_deleted"}

// ChangePage 是交付后变更原因里举例的一页（字段按原因取，用不到的不输出）。
type ChangePage struct {
	FileID            uuid.UUID  `json:"file_id"`
	FileName          *string    `json:"file_name"`
	ExportedPageNo    *int       `json:"exported_page_no,omitempty"`
	PageNo            *int       `json:"page_no,omitempty"`
	ExportedVersionNo *int       `json:"exported_version_no,omitempty"`
	VersionNo         *int       `json:"version_no,omitempty"`
	ReviewStatus      *string    `json:"review_status,omitempty"`
	ToItemID          *uuid.UUID `json:"to_item_id,omitempty"`
	ToItemNo          *int       `json:"to_item_no,omitempty"`
	ToArchiveNo       *string    `json:"to_archive_no,omitempty"`
}

// FieldChange 是著录的变化（item_changed）：label 是导出时的著录项名称（题名、档号另有名称）。
type FieldChange struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// ChangeReason 是一种原因。
type ChangeReason struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Count   *int          `json:"count,omitempty"`
	Pages   []ChangePage  `json:"pages,omitempty"`
	Fields  []FieldChange `json:"fields,omitempty"`
}

// ItemNow 是条目现在的件号、档号、题名、状态。
type ItemNow struct {
	ItemNo    int     `json:"item_no"`
	ArchiveNo *string `json:"archive_no"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
}

// ChangedItem 是有变更的一件（件号、档号、题名、节点是导出时的，now 是现在的）。
type ChangedItem struct {
	Seq           int            `json:"seq"`
	ItemID        uuid.UUID      `json:"item_id"`
	CategoryID    uuid.UUID      `json:"category_id"`
	CategoryNames []string       `json:"category_names"`
	ItemNo        int            `json:"item_no"`
	ArchiveNo     *string        `json:"archive_no"`
	Title         string         `json:"title"`
	Deleted       bool           `json:"deleted"`
	Now           *ItemNow       `json:"now"`
	Reasons       []ChangeReason `json:"reasons"`
}

// Changes 是 `GET /api/export-tasks/{id}/changes` 的响应。
type Changes struct {
	ExportTaskID    uuid.UUID      `json:"export_task_id"`
	OnlyApproved    bool           `json:"only_approved"`
	ItemCount       int            `json:"item_count"`
	ChangedCount    int            `json:"changed_count"`
	ReasonCounts    map[string]int `json:"reason_counts"`
	ResubmitItemIDs []uuid.UUID    `json:"resubmit_item_ids"`
	Items           []ChangedItem  `json:"items"`
	Truncated       bool           `json:"truncated"`
}

// Changes 是任务的交付后变更（只对 done 的任务，其他 409）。
func (s *Service) Changes(ctx context.Context, id uuid.UUID) (*Changes, error) {
	var out *Changes
	err := db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if t == nil {
			return errTaskNotFound
		}
		if t.Status != StatusDone {
			return httpx.InvalidState(fmt.Sprintf("交付后变更只对已完成的导出有意义，这个任务现在是“%s”", statusLabel(t.Status)))
		}
		out, err = s.changesIn(ctx, tx, t)
		return err
	})
	return out, err
}

// changesIn 在快照 q 里算一个 done 任务的交付后变更。
func (s *Service) changesIn(ctx context.Context, q db.Querier, t *taskRow) (*Changes, error) {
	if t.JobID == nil {
		return nil, errInternal("导出任务 %s 没有后台任务", t.ID)
	}
	pl, err := jobPayload(ctx, q, *t.JobID)
	if err != nil {
		return nil, err
	}
	pages, err := exportedPages(ctx, q, t.ID)
	if err != nil {
		return nil, err
	}
	fileIDs := make([]uuid.UUID, len(pages))
	for i, p := range pages {
		fileIDs[i] = p.FileID
	}
	snaps, err := file.CurrentOf(ctx, q, fileIDs)
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		if sn := snaps[p.FileID]; sn != nil {
			p.VersionNow = sn.VersionNo
		}
	}
	itemIDs := make([]uuid.UUID, len(pl.Items))
	for i, it := range pl.Items {
		itemIDs[i] = it.ID
	}
	added, err := addedPages(ctx, q, t.ID, itemIDs)
	if err != nil {
		return nil, err
	}
	// 现在的条目：导出的条目，和页被移到的条目。
	want := slices.Clone(itemIDs)
	for _, p := range pages {
		if p.ItemNow != nil {
			want = append(want, *p.ItemNow)
		}
	}
	now, err := itemsNow(ctx, q, want)
	if err != nil {
		return nil, err
	}
	return computeChanges(t.ID, t.OnlyApproved, pl, pages, added, now), nil
}

// computeChanges 按冻结的条目、页和现在的样子算交付后变更。纯函数。
func computeChanges(taskID uuid.UUID, onlyApproved bool, pl *payload, pages []*exportedPage, added []addedPage,
	now map[uuid.UUID]*itemNow) *Changes {
	out := &Changes{ExportTaskID: taskID, OnlyApproved: onlyApproved, ItemCount: len(pl.Items), ReasonCounts: map[string]int{},
		ResubmitItemIDs: []uuid.UUID{}, Items: []ChangedItem{}}
	for _, c := range changeCodes {
		out.ReasonCounts[c] = 0
	}
	pagesOf := map[uuid.UUID][]*exportedPage{}
	for _, p := range pages {
		pagesOf[p.ItemID] = append(pagesOf[p.ItemID], p)
	}
	addedOf := map[uuid.UUID][]addedPage{}
	for _, a := range added {
		addedOf[a.ItemID] = append(addedOf[a.ItemID], a)
	}
	labels := map[string]string{}
	order := map[string]int{}
	for i, f := range pl.ItemFields {
		labels[f.Key] = f.Label
		order[f.Key] = i
	}
	for i := range pl.Items {
		item := &pl.Items[i]
		cur := now[item.ID]
		var reasons []ChangeReason
		if cur == nil || cur.Deleted {
			reasons = append(reasons, ChangeReason{Code: "item_deleted", Message: "条目已删除"})
		} else {
			ps := pagesOf[item.ID]
			sort.Slice(ps, func(a, b int) bool { return ps[a].PageNo < ps[b].PageNo })
			reasons = pageReasons(item.ID, ps, addedOf[item.ID], onlyApproved, now)
			if fields := fieldChanges(item, cur, labels, order); len(fields) > 0 {
				names := make([]string, len(fields))
				for j, f := range fields {
					names[j] = f.Label
				}
				reasons = append(reasons, ChangeReason{Code: "item_changed", Fields: fields,
					Message: strings.Join(names, "、") + "和导出时不一样"})
			}
		}
		if len(reasons) == 0 {
			continue
		}
		out.ChangedCount++
		for _, r := range reasons {
			out.ReasonCounts[r.Code]++
		}
		deleted := cur == nil || cur.Deleted
		if !deleted {
			out.ResubmitItemIDs = append(out.ResubmitItemIDs, item.ID)
		}
		if len(out.Items) >= MaxListed {
			out.Truncated = true
			continue
		}
		ci := ChangedItem{Seq: i + 1, ItemID: item.ID, CategoryID: item.CategoryID, CategoryNames: item.CategoryNames,
			ItemNo: item.ItemNo, ArchiveNo: item.ArchiveNo, Title: item.Title, Deleted: deleted, Reasons: reasons}
		if ci.CategoryNames == nil {
			ci.CategoryNames = []string{}
		}
		if !deleted {
			ci.Now = &ItemNow{ItemNo: cur.ItemNo, ArchiveNo: cur.ArchiveNo, Title: cur.Title, Status: cur.Status}
		}
		out.Items = append(out.Items, ci)
	}
	return out
}

// pageReasons 是一个没删除的条目和页有关的原因（ps 按导出的页号排好）。
func pageReasons(itemID uuid.UUID, ps []*exportedPage, added []addedPage, onlyApproved bool, now map[uuid.UUID]*itemNow) []ChangeReason {
	note := func(p *exportedPage) ChangePage {
		return ChangePage{FileID: p.FileID, FileName: p.FileName, ExportedPageNo: intp(p.PageNo), PageNo: p.PageNow}
	}
	var kept []*exportedPage
	var newVersion, removed, moved, notApproved []ChangePage
	for _, p := range ps {
		switch {
		case p.ItemNow == nil:
			n := note(p)
			n.PageNo = nil
			removed = append(removed, n)
		case *p.ItemNow != itemID:
			n := note(p)
			n.ToItemID = p.ItemNow
			if o := now[*p.ItemNow]; o != nil {
				n.ToItemNo, n.ToArchiveNo = intp(o.ItemNo), o.ArchiveNo
			}
			moved = append(moved, n)
		default:
			kept = append(kept, p)
		}
	}
	for _, p := range kept {
		if p.VersionNow > p.ExportedVersion {
			n := note(p)
			n.ExportedVersionNo, n.VersionNo = intp(p.ExportedVersion), intp(p.VersionNow)
			newVersion = append(newVersion, n)
		}
		if onlyApproved && (p.ReviewStatus == nil || *p.ReviewStatus != "approved") {
			n := note(p)
			n.ReviewStatus = p.ReviewStatus
			notApproved = append(notApproved, n)
		}
	}
	// 页序：留下的页按导出时的顺序，现在的页号要递增；不在最长递增子序列里的就是挪过的。
	ranks := make([]int, len(kept))
	for i, p := range kept {
		if p.PageNow != nil {
			ranks[i] = *p.PageNow
		}
	}
	inOrder := xslices.LongestIncreasing(ranks)
	var reordered []ChangePage
	for i, p := range kept {
		if !inOrder[i] {
			reordered = append(reordered, note(p))
		}
	}
	sort.Slice(added, func(a, b int) bool { return added[a].PageNo < added[b].PageNo })
	var add []ChangePage
	for _, a := range added {
		add = append(add, ChangePage{FileID: a.FileID, FileName: a.FileName, PageNo: intp(a.PageNo), ReviewStatus: a.ReviewStatus})
	}
	var out []ChangeReason
	for _, x := range []struct {
		code  string
		pages []ChangePage
	}{{"new_version", newVersion}, {"pages_removed", removed}, {"pages_moved", moved}, {"pages_reordered", reordered},
		{"pages_added", add}, {"not_approved", notApproved}} {
		if len(x.pages) == 0 {
			continue
		}
		out = append(out, pageReason(x.code, x.pages))
	}
	return out
}

// pageReason 组装一种和页有关的原因：页数、前 5 页、说明（写前 3 页）。
func pageReason(code string, pages []ChangePage) ChangeReason {
	n := len(pages)
	ex := pages
	if len(ex) > MaxExamplePages {
		ex = ex[:MaxExamplePages]
	}
	num := func(v *int) string {
		if v == nil {
			return "?"
		}
		return fmt.Sprint(*v)
	}
	var shown []string
	for _, p := range ex {
		if len(shown) == messageExamples {
			break
		}
		var s string
		switch code {
		case "new_version":
			s = fmt.Sprintf("第 %s 页：导出的是第 %s 版，现在是第 %s 版", num(p.ExportedPageNo), num(p.ExportedVersionNo), num(p.VersionNo))
		case "pages_removed":
			s = fmt.Sprintf("第 %s 页", num(p.ExportedPageNo))
		case "pages_moved":
			itemNo := 0
			if p.ToItemNo != nil {
				itemNo = *p.ToItemNo
			}
			s = fmt.Sprintf("导出时的第 %s 页现在在 %s 第 %s 页", num(p.ExportedPageNo), itemName(p.ToArchiveNo, itemNo), num(p.PageNo))
		case "pages_reordered":
			s = fmt.Sprintf("导出时的第 %s 页现在是第 %s 页", num(p.ExportedPageNo), num(p.PageNo))
		case "pages_added":
			s = fmt.Sprintf("第 %s 页", num(p.PageNo))
		case "not_approved":
			st := "已删除"
			if p.ReviewStatus != nil {
				st = reviewLabel(*p.ReviewStatus)
			}
			s = fmt.Sprintf("第 %s 页：%s", num(p.PageNo), st)
		}
		shown = append(shown, s)
	}
	list := strings.Join(shown, "；")
	if n > len(shown) {
		list += "……"
	}
	var msg string
	switch code {
	case "new_version":
		msg = fmt.Sprintf("有 %d 页出了新版本（%s）", n, list)
	case "pages_removed":
		msg = fmt.Sprintf("有 %d 页被移出了条目（导出时的%s）", n, list)
	case "pages_moved":
		msg = fmt.Sprintf("有 %d 页移到了别的条目（%s）", n, list)
	case "pages_reordered":
		msg = fmt.Sprintf("页序变了，%d 页挪了位置（%s）", n, list)
	case "pages_added":
		msg = fmt.Sprintf("条目新加了 %d 页（现在的%s）", n, list)
	case "not_approved":
		msg = fmt.Sprintf("有 %d 页现在不是审核通过（%s）", n, list)
	}
	return ChangeReason{Code: code, Message: msg, Count: &n, Pages: ex}
}

// fieldChanges 比较题名、档号、著录（按当时设置里的顺序，设置里没有的排在后面、按 key）。
func fieldChanges(item *frozenItem, cur *itemNow, labels map[string]string, order map[string]int) []FieldChange {
	var out []FieldChange
	if item.Title != cur.Title {
		out = append(out, FieldChange{Key: "title", Label: "题名", Before: item.Title, After: cur.Title})
	}
	if str(item.ArchiveNo) != str(cur.ArchiveNo) {
		out = append(out, FieldChange{Key: "archive_no", Label: "档号", Before: item.ArchiveNo, After: cur.ArchiveNo})
	}
	keys := map[string]bool{}
	for k := range item.ExtraFields {
		keys[k] = true
	}
	for k := range cur.ExtraFields {
		keys[k] = true
	}
	list := make([]string, 0, len(keys))
	for k := range keys {
		list = append(list, k)
	}
	pos := func(k string) int {
		if i, ok := order[k]; ok {
			return i
		}
		return len(order)
	}
	sort.Slice(list, func(a, b int) bool {
		pa, pb := pos(list[a]), pos(list[b])
		if pa != pb {
			return pa < pb
		}
		return list[a] < list[b]
	})
	for _, k := range list {
		a, b := norm(item.ExtraFields[k]), norm(cur.ExtraFields[k])
		if reflect.DeepEqual(a, b) {
			continue
		}
		label := labels[k]
		if label == "" {
			label = k
		}
		out = append(out, FieldChange{Key: k, Label: label, Before: a, After: b})
	}
	return out
}

// norm：空串、null、没有这个 key 都当作没有值（nil）。
func norm(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}

func str(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func intp(v int) *int { return &v }
