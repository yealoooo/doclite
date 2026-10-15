package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
	"doclite/shared/names"
)

// 归类节点（requirements 5.9"节点"）：
//   - 类型 fonds、category、year、retention、org、custom（root 只在建项目时建）；同一链可以有多层同类型；
//   - 编码 1–32 字，规则见 names.ValidateCategoryCode（分隔符来自当前模板），只在新建和改编码时校验；同一层按编码
//     判重（区分大小写，409 `conflict`）；名称去首尾空白 1–100 字，不能有控制字符，同层可重名；
//   - 根节点不能改名、改编码、改类型、移动、删除（409 `invalid_state`），原样提交按没有变化处理；
//   - 移动、改编码、改类型：归类树排他锁；子树里有定稿的条目 409 `item_locked`；否则同一个事务里重算子树里未定稿
//     条目的档号（numbering.go 的 renumberSubtree），撞号的留空；移进自己的子树 409 `tree_cycle`；
//   - 删除：软删除整棵子树（排他锁），子树里还有未删除的条目 409 `category_not_empty`；
//   - 新建子节点的 path_code 由父节点 last_child_no 原子分配，不回收，每层 ≤ 99999（409 `tree_full`）；
//     sort_no 是父节点下的序号（新建、移入的排在最后）；
//   - 审计 archive_category：create、update、move、delete、renumber（按新模板重新生成），detail 的节点状态是
//     {category_name, category_code, category_type, path_code, parent_id, category_path}，只放变了的；
//   - 事件 catalog.changed：新建 [新节点, 父节点]；修改 [自己, 原父节点, 新父节点]、item_ids 是档号变了的条目；
//     删除 [父节点, 删掉的节点…]。

// CategoryList 是 `GET /api/projects/{pid}/archive-categories?parent_id=`：父节点本身和它的直接子节点。
type CategoryList struct {
	Parent Category   `json:"parent"`
	Items  []Category `json:"items"`
}

// UpdatedCategory 是修改节点的响应：节点加这次重算的结果。
type UpdatedCategory struct {
	Category
	// Renumbered 是这次档号变了的条目数。
	Renumbered int `json:"renumbered"`
	// ArchiveNoConflicts 是重算后因为撞号档号留空的条目数。
	ArchiveNoConflicts int `json:"archive_no_conflicts"`
}

// ListCategories 是 parentID（nil 是根节点）的直接子节点。父节点不在这个项目里或已删除 404。
func (s *Service) ListCategories(ctx context.Context, projectID uuid.UUID, parentID *uuid.UUID) (*CategoryList, error) {
	var out *CategoryList
	err := readSnapshot(ctx, s.pool, func(tx pgx.Tx) error {
		var parent *catRow
		var err error
		if parentID == nil {
			parent, err = rootCategory(ctx, tx, projectID)
		} else {
			parent, err = getCategory(ctx, tx, projectID, *parentID)
		}
		if err != nil {
			return err
		}
		if parent == nil {
			return errCategoryNotFound
		}
		pv, err := categoryView(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
		items, err := childCategories(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
		out = &CategoryList{Parent: pv, Items: items}
		return nil
	})
	return out, err
}

// CategoryInput 是新建节点的输入。ParentID 为 nil 时建在根节点下。
type CategoryInput struct {
	ParentID *uuid.UUID
	Type     string
	Code     string
	Name     string
}

func codeConflict(code string) error {
	return httpx.Conflict("", fmt.Sprintf("同一层已有编码为“%s”的节点（区分大小写），请换一个编码", code))
}

var errTreeFull = httpx.Conflict("tree_full", fmt.Sprintf("这一层的节点已经到上限（%d 个，编码不回收），请换一个上级节点", maxChildNo))

// categoryState 是审计里节点的状态。
func categoryState(c *catRow, path string) map[string]any {
	var parent any
	if c.ParentID != nil {
		parent = c.ParentID.String()
	}
	return map[string]any{"category_name": c.Name, "category_code": c.Code, "category_type": c.Type,
		"path_code": c.PathCode, "parent_id": parent, "category_path": path}
}

// CreateCategory 新建节点。
func (s *Service) CreateCategory(ctx context.Context, operatorID, projectID uuid.UUID, in CategoryInput) (*Category, error) {
	if err := checkCategoryType(in.Type); err != nil {
		return nil, err
	}
	name, err := normalizeCategoryName(in.Name)
	if err != nil {
		return nil, err
	}
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	if err := names.ValidateCategoryCode(in.Code, st.separators()); err != nil {
		return nil, httpx.Invalid("%s", err.Error())
	}
	var out Category
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		parentID := in.ParentID
		if parentID == nil {
			root, err := rootCategory(ctx, tx, projectID)
			if err != nil {
				return err
			}
			if root == nil {
				return errCategoryNotFound
			}
			parentID = &root.ID
		}
		locked, err := lockCategories(ctx, tx, projectID, *parentID)
		if err != nil {
			return err
		}
		parent := locked[*parentID]
		if parent == nil || parent.Deleted {
			return httpx.NotFound("上级归类节点")
		}
		if dup, err := siblingWithCode(ctx, tx, parent.ID, in.Code, uuid.Nil); err != nil {
			return err
		} else if dup != uuid.Nil {
			return codeConflict(in.Code)
		}
		path, childNo, ok, err := allocChildPath(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
		if !ok {
			return errTreeFull
		}
		c := &catRow{ID: db.NewID(), ProjectID: projectID, ParentID: &parent.ID, PathCode: path, Type: in.Type, Code: in.Code,
			Name: name, SortNo: childNo}
		if err := insertCategory(ctx, tx, c); err != nil {
			if db.UniqueViolation(err, "ux_archive_categories_code") {
				return codeConflict(in.Code)
			}
			return err
		}
		ch, err := categoryChain(ctx, tx, projectID, path)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveCategory, TargetID: c.ID, Action: "create",
			Detail: map[string]any{"after": categoryState(c, ch.path())}}); err != nil {
			return err
		}
		if out, err = categoryView(ctx, tx, c.ID); err != nil {
			return err
		}
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{c.ID, parent.ID}, nil))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CategoryPatch 是修改节点的输入；nil 表示不改，和现在一样的当作没给。
type CategoryPatch struct {
	Name     *string
	Code     *string
	Type     *string
	ParentID *uuid.UUID
}

// categoryDiff 是修改后的值和变了哪些。
type categoryDiff struct {
	name, code, typ                    string
	parentID                           uuid.UUID
	nameChanged, codeChanged, typeChgd bool
	move                               bool
}

func (d categoryDiff) any() bool       { return d.nameChanged || d.codeChanged || d.typeChgd || d.move }
func (d categoryDiff) exclusive() bool { return d.codeChanged || d.typeChgd || d.move }

// diffCategory 校验修改（400），算出变了哪些；根节点有变化 409 `invalid_state`。
func diffCategory(cur *catRow, in CategoryPatch, st *settings) (categoryDiff, error) {
	d := categoryDiff{name: cur.Name, code: cur.Code, typ: cur.Type}
	if in.Name != nil && strings.TrimSpace(*in.Name) != cur.Name {
		n, err := normalizeCategoryName(*in.Name)
		if err != nil {
			return d, err
		}
		d.name, d.nameChanged = n, true
	}
	if in.Code != nil && *in.Code != cur.Code {
		if err := names.ValidateCategoryCode(*in.Code, st.separators()); err != nil {
			return d, httpx.Invalid("%s", err.Error())
		}
		d.code, d.codeChanged = *in.Code, true
	}
	if in.Type != nil && *in.Type != cur.Type {
		if err := checkCategoryType(*in.Type); err != nil {
			return d, err
		}
		d.typ, d.typeChgd = *in.Type, true
	}
	if in.ParentID != nil && (cur.ParentID == nil || *in.ParentID != *cur.ParentID) {
		d.parentID, d.move = *in.ParentID, true
	}
	if cur.ParentID == nil && d.any() {
		return d, httpx.Conflict(httpx.CodeInvalidState, "根节点不能改名、改编码、改类型或移动（它的名称和编码跟着项目）")
	}
	return d, nil
}

// UpdateCategory 改名、改编码、改类型、移动。
func (s *Service) UpdateCategory(ctx context.Context, operatorID, projectID, id uuid.UUID, in CategoryPatch) (*UpdatedCategory, error) {
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	cur, err := getCategory(ctx, s.pool, projectID, id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errCategoryNotFound
	}
	d, err := diffCategory(cur, in, st)
	if err != nil {
		return nil, err
	}
	if !d.any() {
		v, err := categoryView(ctx, s.pool, id)
		if err != nil {
			return nil, err
		}
		return &UpdatedCategory{Category: v}, nil
	}
	exclusive := d.exclusive()

	var out *UpdatedCategory
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, exclusive); err != nil {
			return err
		}
		ids := []uuid.UUID{id}
		if cur.ParentID != nil {
			ids = append(ids, *cur.ParentID)
		}
		if d.move {
			ids = append(ids, d.parentID)
		}
		locked, err := lockCategories(ctx, tx, projectID, ids...)
		if err != nil {
			return err
		}
		node := locked[id]
		if node == nil || node.Deleted {
			return errCategoryNotFound
		}
		// 锁住之后按最新的值重新算一遍（拿锁之前别人可能改过）。
		d, err := diffCategory(node, in, st)
		if err != nil {
			return err
		}
		if !d.any() {
			v, err := categoryView(ctx, tx, id)
			out = &UpdatedCategory{Category: v}
			return err
		}
		if d.exclusive() && !exclusive {
			return httpx.Conflict("", "节点刚被别人修改过，请刷新后再试")
		}
		var newParent *catRow
		if d.move {
			newParent = locked[d.parentID]
			if newParent == nil || newParent.Deleted {
				return httpx.NotFound("目标归类节点")
			}
			if strings.HasPrefix(newParent.PathCode, node.PathCode) {
				return httpx.Conflict("tree_cycle", "不能把节点移到它自己或它下面的节点里")
			}
		}
		if exclusive {
			n, ex, err := subtreeItems(ctx, tx, projectID, node.PathCode, "locked")
			if err != nil {
				return err
			}
			if n > 0 {
				return httpx.Conflict("item_locked", fmt.Sprintf(
					"「%s」下面（含下级节点）有 %d 个已定稿的条目（例如「%s」第 %d 件），不能移动、改编码或改类型；先解锁这些条目",
					node.Name, n, ex.CategoryName, ex.ItemNo))
			}
		}
		parentID := *node.ParentID
		if d.move {
			parentID = newParent.ID
		}
		if d.codeChanged || d.move {
			if dup, err := siblingWithCode(ctx, tx, parentID, d.code, id); err != nil {
				return err
			} else if dup != uuid.Nil {
				return codeConflict(d.code)
			}
		}
		beforeChain, err := categoryChain(ctx, tx, projectID, node.PathCode)
		if err != nil {
			return err
		}
		after := *node
		after.Name, after.Code, after.Type = d.name, d.code, d.typ
		if d.move {
			path, childNo, ok, err := allocChildPath(ctx, tx, newParent.ID)
			if err != nil {
				return err
			}
			if !ok {
				return errTreeFull
			}
			if err := moveSubtreePaths(ctx, tx, projectID, node.PathCode, path); err != nil {
				return err
			}
			after.PathCode, after.ParentID, after.SortNo = path, &newParent.ID, childNo
		}
		if err := updateCategoryRow(ctx, tx, &after); err != nil {
			if db.UniqueViolation(err, "ux_archive_categories_code") {
				return codeConflict(d.code)
			}
			return err
		}
		var rr renumberResult
		if exclusive {
			var changes []string
			if d.move {
				changes = append(changes, "move")
			}
			if d.codeChanged {
				changes = append(changes, "category_code")
			}
			if d.typeChgd {
				changes = append(changes, "category_type")
			}
			rr, err = renumberSubtree(ctx, tx, projectID, operatorID, after.PathCode, st,
				map[string]any{"category_id": id, "changes": changes}, "修改节点的编码、类型或位置")
			if err != nil {
				return err
			}
		}
		afterChain, err := categoryChain(ctx, tx, projectID, after.PathCode)
		if err != nil {
			return err
		}
		bs, as := categoryState(node, beforeChain.path()), categoryState(&after, afterChain.path())
		for k, v := range bs {
			if v == as[k] {
				delete(bs, k)
				delete(as, k)
			}
		}
		as["renumbered"] = len(rr.Changed)
		if rr.Conflicts > 0 {
			as["archive_no_conflicts"] = rr.Conflicts
		}
		action := "update"
		if d.move {
			action = "move"
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveCategory, TargetID: id, Action: action,
			Detail: map[string]any{"before": bs, "after": as}}); err != nil {
			return err
		}
		v, err := categoryView(ctx, tx, id)
		if err != nil {
			return err
		}
		out = &UpdatedCategory{Category: v, Renumbered: len(rr.Changed), ArchiveNoConflicts: rr.Conflicts}
		catIDs := []uuid.UUID{id, *node.ParentID}
		if d.move {
			catIDs = append(catIDs, newParent.ID)
		}
		ev.Add(events.CatalogChanged(projectID, catIDs, rr.Changed))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteCategory 软删除节点的整棵子树。
func (s *Service) DeleteCategory(ctx context.Context, operatorID, projectID, id uuid.UUID) error {
	cur, err := getCategory(ctx, s.pool, projectID, id)
	if err != nil {
		return err
	}
	if cur == nil {
		return errCategoryNotFound
	}
	if cur.ParentID == nil {
		return httpx.Conflict(httpx.CodeInvalidState, "根节点不能删除")
	}
	return s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, true); err != nil {
			return err
		}
		locked, err := lockCategories(ctx, tx, projectID, id)
		if err != nil {
			return err
		}
		node := locked[id]
		if node == nil || node.Deleted {
			return errCategoryNotFound
		}
		n, ex, err := subtreeItems(ctx, tx, projectID, node.PathCode, "")
		if err != nil {
			return err
		}
		if n > 0 {
			title := ex.Title
			if title == "" {
				title = "未填题名"
			}
			return httpx.Conflict("category_not_empty", fmt.Sprintf(
				"「%s」下面（含下级节点）还有 %d 个条目（例如「%s」第 %d 件「%s」），不能删除；先把这些条目删除或移走",
				node.Name, n, ex.CategoryName, ex.ItemNo, title))
		}
		ch, err := categoryChain(ctx, tx, projectID, node.PathCode)
		if err != nil {
			return err
		}
		deleted, err := softDeleteSubtree(ctx, tx, projectID, node.PathCode)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveCategory, TargetID: id, Action: "delete",
			Detail: map[string]any{"before": categoryState(node, ch.path()),
				"after": map[string]any{"deleted_categories": len(deleted)}}}); err != nil {
			return err
		}
		ev.Add(events.CatalogChanged(projectID, append([]uuid.UUID{*node.ParentID}, deleted...), nil))
		return nil
	})
}
