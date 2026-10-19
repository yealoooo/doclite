package catalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// 档案条目（requirements 5.9"条目"，只做"件"）：
//   - 条目只能由整个文件夹归档产生（archive.go）；不能新建空条目、不能单独挂页（2026-10-19）；
//   - PATCH：改题名、著录（按 key 合并）、移到别的节点（件号取目标的 max+1，同时带 item_no 就用它）、改件号
//     （1–999999999，节点里已占，含已删除的，409 `conflict`）；移动、改件号时用当前模板重算档号，只有显式改件号时
//     撞号 409；没有实际变化不写库；封存、修改中都可以改，著录只查格式（必填在锁定时查）；
//   - 删除 = 撤销归档：软删除，件号不回收；页一并移出（每页一条 unlink，带 item_deleted，见 pages.go），来源文件夹
//     回到未归档；
//   - 已锁定的条目改著录、移动、改件号、删除 409 `item_locked`；每个成功的动作 row_version + 1 并审计；
//     PATCH、状态动作带 row_version，不一致 409 `conflict`；删除可以在查询参数里带。
//   - 审计 archive_item：update、move、delete，detail {before, after} 只放变了的（title、extra_fields 里变了
//     的 key、category_id、category_path、item_no、archive_no），撞号时 after 带 archive_no_conflict。

const (
	// MaxItemNo 是件号的上限。
	MaxItemNo = 999_999_999
	// DefaultItemLimit、MaxItemLimit 是条目列表每页的个数。
	DefaultItemLimit = 500
	MaxItemLimit     = 2000
)

// Item 是条目详情（新建、修改、状态动作也返回它）。
type Item struct {
	ItemSummary
	ProjectID uuid.UUID `json:"project_id"`
	// CategoryPath 是所在节点的名称路径（从根节点起用 `/` 连接）。
	CategoryPath    string           `json:"category_path"`
	ArchiveNoReason *ArchiveNoReason `json:"archive_no_reason"`
	// ExtraFields 是库里存的全部著录项（含不在选项里的旧值、设置里已经没有的）。
	ExtraFields map[string]any `json:"extra_fields"`
	// FieldIssues 是存着的值里和现在的设置对不上的。
	FieldIssues []FieldIssue `json:"field_issues"`
	CreatedBy   uuid.UUID    `json:"created_by"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedBy   *uuid.UUID   `json:"updated_by"`
	Pages       []Page       `json:"pages"`
}

// readItem 读未删除条目的详情；没有 404。
func readItem(ctx context.Context, q db.Querier, st *settings, id uuid.UUID) (*Item, error) {
	r, err := getItem(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errItemNotFound
	}
	sum, err := itemSummary(ctx, q, id)
	if err != nil {
		return nil, err
	}
	ch, err := categoryChain(ctx, q, r.ProjectID, r.CategoryPath)
	if err != nil {
		return nil, err
	}
	out := &Item{ItemSummary: sum, ProjectID: r.ProjectID, CategoryPath: ch.path(), ExtraFields: r.ExtraFields,
		FieldIssues: st.fieldIssues(r.ExtraFields), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedBy: r.UpdatedBy}
	if r.ArchiveNo == nil {
		if out.ArchiveNoReason, err = emptyReason(ctx, q, st, r.ProjectID, r.ID, ch, r.ItemNo); err != nil {
			return nil, err
		}
	}
	if out.Pages, err = itemPages(ctx, q, id, sum.LockedAt); err != nil {
		return nil, err
	}
	return out, nil
}

// GetItem 是条目详情（在一个只读快照里读）。
func (s *Service) GetItem(ctx context.Context, projectID, id uuid.UUID) (*Item, error) {
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *Item
	err = db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = readItem(ctx, tx, st, id)
		return err
	})
	return out, err
}

// ItemList 是 `GET /api/archive-categories/{id}/items`（keyset 分页，total、counts 不受分页影响）。
type ItemList struct {
	CategoryID uuid.UUID     `json:"category_id"`
	Total      int           `json:"total"`
	Counts     StatusCounts  `json:"counts"`
	Items      []ItemSummary `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// parseCursor 解析 `<item_no>_<id>`；空串是从头开始。
func parseCursor(v string) (*int, uuid.UUID, error) {
	if v == "" {
		return nil, uuid.Nil, nil
	}
	bad := httpx.Invalid("cursor 不合法，请用上一页响应里的 next_cursor")
	noText, idText, ok := strings.Cut(v, "_")
	if !ok {
		return nil, uuid.Nil, bad
	}
	no, err := strconv.Atoi(noText)
	if err != nil {
		return nil, uuid.Nil, bad
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		return nil, uuid.Nil, bad
	}
	return &no, id, nil
}

// ListItems 是直接放在节点里的未删除条目（不含下级节点），按件号、id 排。
func (s *Service) ListItems(ctx context.Context, projectID, categoryID uuid.UUID, limit int, cursor string) (*ItemList, error) {
	afterNo, afterID, err := parseCursor(cursor)
	if err != nil {
		return nil, err
	}
	var out *ItemList
	err = db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		cat, err := getCategory(ctx, tx, projectID, categoryID)
		if err != nil {
			return err
		}
		if cat == nil {
			return errCategoryNotFound
		}
		total, counts, err := countItems(ctx, tx, categoryID)
		if err != nil {
			return err
		}
		items, err := listItemSummaries(ctx, tx, categoryID, afterNo, afterID, limit+1)
		if err != nil {
			return err
		}
		out = &ItemList{CategoryID: categoryID, Total: total, Counts: counts, Items: items}
		if len(items) > limit {
			out.Items = items[:limit]
			last := out.Items[limit-1]
			next := fmt.Sprintf("%d_%s", last.ItemNo, last.ID)
			out.NextCursor = &next
		}
		return nil
	})
	return out, err
}

// assignArchiveNo 在共享归类树锁下给条目生成档号：拿这个档号的咨询锁后查占用，被别的条目（except 以外）占着就
// 留空，返回撞了谁（审计用）。缺级时为空、conflict 也为空。st.tmpl 不能为 nil。
func assignArchiveNo(ctx context.Context, tx pgx.Tx, st *settings, projectID, except uuid.UUID, ch chain, itemNo int) (*string, map[string]any, error) {
	no, _ := render(st.tmpl, ch, itemNo)
	if no == "" {
		return nil, nil, nil
	}
	if err := lockArchiveNo(ctx, tx, projectID, no); err != nil {
		return nil, nil, err
	}
	h, err := archiveNoHolder(ctx, tx, projectID, no, except)
	if err != nil {
		return nil, nil, err
	}
	if h != nil {
		return nil, map[string]any{"archive_no": no, "item_id": h.ID}, nil
	}
	return &no, nil, nil
}

// ItemPatch 是修改条目的输入；nil 表示不改。
type ItemPatch struct {
	RowVersion int
	Title      *string
	// Fields 按 key 合并（nil 或空串清空）；nil map 表示不改著录项。
	Fields     map[string]any
	CategoryID *uuid.UUID
	ItemNo     *int
}

func checkRowVersion(v int) error {
	if v <= 0 {
		return httpx.Invalid("row_version 要是正整数（用读到的条目里的 row_version）")
	}
	return nil
}

func staleVersion(current int) error {
	return httpx.Conflict("", fmt.Sprintf("条目刚被别人修改过（现在是第 %d 版），请重新载入后再操作", current))
}

func titleOrPlaceholder(t string) string {
	if t == "" {
		return "未填题名"
	}
	return t
}

// UpdateItem 改题名、著录、移到别的节点、改件号。
func (s *Service) UpdateItem(ctx context.Context, operatorID, id uuid.UUID, in ItemPatch) (*Item, error) {
	if err := checkRowVersion(in.RowVersion); err != nil {
		return nil, err
	}
	if in.ItemNo != nil && (*in.ItemNo < 1 || *in.ItemNo > MaxItemNo) {
		return nil, httpx.Invalid("件号要是 1–%d 的整数", MaxItemNo)
	}
	cur, err := getItem(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errItemNotFound
	}
	projectID := cur.ProjectID
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var target *catRow
	if in.CategoryID != nil && *in.CategoryID != cur.CategoryID {
		if target, err = getCategory(ctx, s.pool, projectID, *in.CategoryID); err != nil {
			return nil, err
		}
		if target == nil {
			return nil, httpx.NotFound("目标归类节点")
		}
	}

	var out *Item
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		// 节点行：移动锁目标节点，改件号锁所在节点（同一个节点里分配和检查件号的事务串行）。
		var cat *catRow
		lockID := uuid.Nil
		switch {
		case target != nil:
			lockID = target.ID
		case in.ItemNo != nil:
			lockID = cur.CategoryID
		}
		if lockID != uuid.Nil {
			locked, err := lockCategories(ctx, tx, projectID, lockID)
			if err != nil {
				return err
			}
			if cat = locked[lockID]; cat == nil || cat.Deleted {
				return httpx.NotFound("目标归类节点")
			}
		}
		r, err := lockItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if r == nil || r.Deleted {
			return errItemNotFound
		}
		if r.Status == statusLocked {
			return errLockedEdit("修改")
		}
		if r.RowVersion != in.RowVersion || r.CategoryID != cur.CategoryID {
			return staleVersion(r.RowVersion)
		}

		// 著录。
		after := *r
		fieldsChanged := false
		if in.Title != nil || in.Fields != nil {
			t, f, issues := st.applyEdits(r.Title, r.ExtraFields, edits{Title: in.Title, Fields: in.Fields})
			if len(issues) > 0 {
				return fieldsError(issues)
			}
			fieldsChanged = t != r.Title || !sameFields(f, r.ExtraFields)
			after.Title, after.ExtraFields = t, f
		}

		// 件号和档号。
		moving := target != nil
		explicitNo := in.ItemNo != nil && (moving || *in.ItemNo != r.ItemNo)
		var conflict map[string]any
		var afterChain chain
		if moving || explicitNo {
			if moving {
				after.CategoryID, after.CategoryPath = cat.ID, cat.PathCode
			}
			if explicitNo {
				after.ItemNo = *in.ItemNo
				u, err := itemNoTaken(ctx, tx, after.CategoryID, after.ItemNo, r.ID)
				if err != nil {
					return err
				}
				if u != nil && u.Deleted {
					return httpx.Conflict("", fmt.Sprintf("这个归类节点里的第 %d 件以前用过（条目已删除，件号不回收），请换一个件号", after.ItemNo))
				}
				if u != nil {
					return httpx.Conflict("", fmt.Sprintf("这个归类节点里已有第 %d 件（%s），请换一个件号", after.ItemNo, titleOrPlaceholder(u.Title)))
				}
			} else {
				if after.ItemNo, err = nextItemNo(ctx, tx, after.CategoryID); err != nil {
					return err
				}
				if after.ItemNo > MaxItemNo {
					return httpx.InvalidState(fmt.Sprintf("目标节点里的件号已经用到 %d，不能再移进条目", MaxItemNo))
				}
			}
			if st.tmpl == nil {
				what := "改件号"
				if moving {
					what = "移动条目"
				}
				return st.templateInvalid(what)
			}
			if afterChain, err = categoryChain(ctx, tx, projectID, after.CategoryPath); err != nil {
				return err
			}
			no, _ := render(st.tmpl, afterChain, after.ItemNo)
			if explicitNo && no != "" {
				if err := lockArchiveNo(ctx, tx, projectID, no); err != nil {
					return err
				}
				h, err := archiveNoHolder(ctx, tx, projectID, no, r.ID)
				if err != nil {
					return err
				}
				if h != nil {
					return httpx.Conflict("", fmt.Sprintf("改了件号之后档号“%s”和已有的条目（第 %d 件，%s）重复，请换一个件号",
						no, h.ItemNo, titleOrPlaceholder(h.Title)))
				}
				after.ArchiveNo = &no
			} else if after.ArchiveNo, conflict, err = assignArchiveNo(ctx, tx, st, projectID, r.ID, afterChain, after.ItemNo); err != nil {
				return err
			}
		}

		if !fieldsChanged && after.CategoryID == r.CategoryID && after.ItemNo == r.ItemNo && equalStr(after.ArchiveNo, r.ArchiveNo) {
			out, err = readItem(ctx, tx, st, id) // 没有变化
			return err
		}
		if err := updateItemRow(ctx, tx, &after, operatorID); err != nil {
			return err
		}

		b, a := map[string]any{}, map[string]any{}
		if after.Title != r.Title {
			b["title"], a["title"] = r.Title, after.Title
		}
		if fb, fa := fieldDiff(r.ExtraFields, after.ExtraFields); len(fa) > 0 {
			b["extra_fields"], a["extra_fields"] = fb, fa
		}
		action := "update"
		if after.CategoryID != r.CategoryID {
			action = "move"
			beforeChain, err := categoryChain(ctx, tx, projectID, r.CategoryPath)
			if err != nil {
				return err
			}
			b["category_id"], a["category_id"] = r.CategoryID, after.CategoryID
			b["category_path"], a["category_path"] = beforeChain.path(), afterChain.path()
		}
		if after.ItemNo != r.ItemNo {
			b["item_no"], a["item_no"] = r.ItemNo, after.ItemNo
		}
		if !equalStr(after.ArchiveNo, r.ArchiveNo) {
			b["archive_no"], a["archive_no"] = r.ArchiveNo, after.ArchiveNo
		}
		if conflict != nil {
			a["archive_no_conflict"] = conflict
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveItem, TargetID: id, Action: action,
			Detail: map[string]any{"before": b, "after": a}}); err != nil {
			return err
		}
		if out, err = readItem(ctx, tx, st, id); err != nil {
			return err
		}
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{after.CategoryID, r.CategoryID}, []uuid.UUID{id}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteItem 撤销归档：软删除条目，页一并移出（每页一条 unlink，带 item_deleted），来源文件夹回到未归档。
// 只有未锁定的可以（封存、修改中都行）。rowVersion 为 nil 时不核对版本。
func (s *Service) DeleteItem(ctx context.Context, operatorID, id uuid.UUID, rowVersion *int) error {
	if rowVersion != nil {
		if err := checkRowVersion(*rowVersion); err != nil {
			return err
		}
	}
	cur, err := getItem(ctx, s.pool, id)
	if err != nil {
		return err
	}
	if cur == nil {
		return errItemNotFound
	}
	projectID := cur.ProjectID
	return s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		r, err := lockItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if r == nil || r.Deleted {
			return errItemNotFound
		}
		if r.Status == statusLocked {
			return errLockedEdit("撤销归档")
		}
		if rowVersion != nil && r.RowVersion != *rowVersion {
			return staleVersion(r.RowVersion)
		}
		// 一并移出（不变量：已删除的条目没有挂接行）；没有页时不拿文件树锁。
		pages, err := applyPages(ctx, tx, projectID, operatorID, r, nil, pageOpts{itemDeleted: true, lockFiles: true})
		if err != nil {
			return err
		}
		ch, err := categoryChain(ctx, tx, projectID, r.CategoryPath)
		if err != nil {
			return err
		}
		if err := softDeleteItem(ctx, tx, id, operatorID); err != nil {
			return err
		}
		before := map[string]any{"category_id": r.CategoryID, "category_path": ch.path(),
			"item_no": r.ItemNo, "archive_no": r.ArchiveNo, "title": r.Title, "status": r.Status,
			"source_status": r.SourceStatus, "page_count": len(pages.Plan.Unlinked)}
		if r.SourceFolderID != nil {
			paths, err := file.FolderPaths(ctx, tx, []uuid.UUID{*r.SourceFolderID})
			if err != nil {
				return err
			}
			before["source_folder_id"], before["source_folder_path"] = *r.SourceFolderID, paths[*r.SourceFolderID]
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveItem, TargetID: id, Action: "delete",
			Detail: map[string]any{"before": before}}); err != nil {
			return err
		}
		ev.Add(pages.filesEvent(projectID)...)
		ev.Add(events.FoldersChanged(projectID, sourceIDs(r)...))
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{r.CategoryID}, []uuid.UUID{id}))
		return nil
	})
}
