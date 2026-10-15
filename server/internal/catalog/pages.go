package catalog

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// 挂接、页序和删除条目时一并移出（requirements 5.9"挂接"，用户决定 Q3、Q8）：
//   - PUT 整体替换条目的页，页号重排成 1..n；≤ 2000 页；同一个文件出现两次 400；和现在完全一样时不写库、不写日志、
//     不发事件，row_version 不变；有变化时 row_version + 1（一个请求只加 1）；
//   - 一个文件只在一个条目里：要新挂上的文件已经在别的条目里 409 `file_cataloged`，details {ids, items}（在哪：条目、
//     档号、件号、题名、节点名称链、第几页、状态）；不存在、已删除（含所在文件夹）、不在这个项目里 404，details.ids
//     是 file_id（按请求里的顺序）。任何一个错误都整体不做；
//   - 新建条目带 file_ids：插入条目之后同样挂上（row_version 仍是 1），失败整体回滚，条目也不建；
//   - 删除条目：有页时一并移出（unlink 带 item_deleted: true）。
//
// 加锁（requirements 3.6，硬性要求）：归类树锁（共享）→ 条目行（新建时是节点行，再插入条目）→ 有新挂上或移出的页时
// 才拿文件树锁（共享）→ 这些文件的 files 行（只锁这个项目里的，id 升序）→ 用新语句重读 folder_files。只换页号时
// 不拿文件树锁、不锁 files 行（这些页在这个条目里，条目行已经锁住；已归类的文件也删不掉）。拿着文件树锁的事务（审核、
// 文件目录、上传）从不等归类的锁和行，所以等条目行的挂接不会把审核拖住。
//
// 日志：新挂上的每页一条 link、移出的每页一条 unlink（先 unlink 再 link），detail {item_id, item_no, archive_no, title,
// page_no, file_name, folder_path, version_no, review_status}，删除条目时另带 item_deleted: true；留下的页相对顺序变了
// 写一条 archive_item / reorder 审计 {page_count, linked, unlinked, moved: [{file_id, file_name, from_page, to_page}]}，
// 只记挪动过的页（最长递增子序列之外），插入、移出引起的顺延不算。
//
// 事件（提交后）：有挂上、移出的页时先发 files.changed（这些文件直接所在的文件夹 + file_ids），再发 catalog.changed
// {category_ids: [所在节点], item_ids: [条目]}；只换页号时只发 catalog.changed。

// pageChange 是 applyPages 做了的事。
type pageChange struct {
	Plan pagePlan
	// FolderIDs、FileIDs 是新挂上和移出的文件、它们所在的文件夹（files.changed 用）。
	FolderIDs []uuid.UUID
	FileIDs   []uuid.UUID
}

func (c *pageChange) filesEvent(projectID uuid.UUID) []events.Event {
	if len(c.FileIDs) == 0 {
		return nil
	}
	return []events.Event{events.FilesChanged(projectID, c.FolderIDs, c.FileIDs)}
}

// applyPages 在调用方的事务里把条目 it 的页换成 wanted（已经 checkPageList 过）。调用前要拿着归类树锁（共享）、锁住
// 条目行（新建时是节点行，条目已插入）。不改条目行本身（row_version 由调用方处理）。itemDeleted：删除条目时一并移出。
func applyPages(ctx context.Context, tx pgx.Tx, projectID, operatorID uuid.UUID, it *itemRow, wanted []uuid.UUID, itemDeleted bool) (*pageChange, error) {
	current, err := itemPageList(ctx, tx, it.ID)
	if err != nil {
		return nil, err
	}
	out := &pageChange{Plan: planPages(current, wanted)}
	p := &out.Plan
	if !p.changed() {
		return out, nil
	}
	touched := p.touched()
	locked := map[uuid.UUID]bool{}
	if len(touched) > 0 {
		// 文件树锁在归类的锁和行之后（requirements 3.6）。
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return nil, err
		}
		ids, err := projectFileIDs(ctx, tx, projectID, touched)
		if err != nil {
			return nil, err
		}
		if err := file.LockFiles(ctx, tx, ids); err != nil {
			return nil, err
		}
		for _, id := range ids {
			locked[id] = true
		}
	}
	read := append(touched[:len(touched):len(touched)], fileIDsOf(p.Moved)...)
	files, err := pageFiles(ctx, tx, projectID, read)
	if err != nil {
		return nil, err
	}

	// 要新挂上的：不存在、已删除、不在这个项目里 404；已经在别的条目里 409 file_cataloged。筛选和加锁之后才在这个项目
	// 里出现的文件（不在已锁住的集合里）也按 404，只挂锁住了 files 行的文件。
	var missing []uuid.UUID
	linkedIDs := fileIDsOf(p.Linked)
	for _, id := range linkedIDs {
		if f := files[id]; f == nil || !f.Live || !locked[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, httpx.NewError(http.StatusNotFound, httpx.CodeNotFound,
			fmt.Sprintf("有 %d 个文件不存在、已删除或不在这个项目里，没有挂接", len(missing))).
			WithDetails(map[string]any{"ids": missing})
	}
	if err := checkNotElsewhere(ctx, tx, it.ID, linkedIDs, files); err != nil {
		return nil, err
	}

	// 写挂接（只写 archive_item_files）。
	if err := deletePages(ctx, tx, it.ID, fileIDsOf(p.Unlinked)); err != nil {
		return nil, err
	}
	if err := repage(ctx, tx, it.ID, p.Repaged); err != nil {
		return nil, err
	}
	if err := insertPages(ctx, tx, projectID, it.ID, operatorID, p.Linked); err != nil {
		if db.UniqueViolation(err, "archive_item_files_file_id_key") {
			// 兜底：一个文件只能在一个条目里（正常走不到，挂接前都锁了 files 行）。
			return nil, httpx.Conflict("", "有文件刚被挂到了别的条目里，请刷新后再试")
		}
		return nil, err
	}

	// 日志：先 unlink（按原页号）再 link（按新页号）。
	logs := make([]file.Log, 0, len(touched))
	folderSet := map[uuid.UUID]bool{}
	logOf := func(action string, m pageMove, page int) file.Log {
		d := map[string]any{"item_id": it.ID, "item_no": it.ItemNo, "archive_no": it.ArchiveNo, "title": it.Title,
			"page_no": page}
		if f := files[m.FileID]; f != nil {
			d["file_name"], d["folder_path"], d["version_no"], d["review_status"] = f.FileName, f.FolderPath, f.VersionNo, f.ReviewStatus
			if !folderSet[f.FolderID] {
				folderSet[f.FolderID] = true
				out.FolderIDs = append(out.FolderIDs, f.FolderID)
			}
		}
		if itemDeleted {
			d["item_deleted"] = true
		}
		out.FileIDs = append(out.FileIDs, m.FileID)
		return file.Log{ProjectID: projectID, FileID: m.FileID, Action: action, OperatorID: operatorID, Detail: d}
	}
	for _, m := range p.Unlinked {
		logs = append(logs, logOf(file.ActionUnlink, m, m.From))
	}
	for _, m := range p.Linked {
		logs = append(logs, logOf(file.ActionLink, m, m.To))
	}
	if err := file.WriteLogs(ctx, tx, logs...); err != nil {
		return nil, err
	}

	// 留下的页相对顺序变了：一条 reorder 审计，只记挪动过的页。
	if len(p.Moved) > 0 {
		moved := make([]map[string]any, len(p.Moved))
		for i, m := range p.Moved {
			name := ""
			if f := files[m.FileID]; f != nil {
				name = f.FileName
			}
			moved[i] = map[string]any{"file_id": m.FileID, "file_name": name, "from_page": m.From, "to_page": m.To}
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveItem, TargetID: it.ID, Action: "reorder",
			Detail: map[string]any{"page_count": len(wanted), "linked": len(p.Linked), "unlinked": len(p.Unlinked),
				"moved": moved}}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func fileIDsOf(ms []pageMove) []uuid.UUID {
	out := make([]uuid.UUID, len(ms))
	for i, m := range ms {
		out[i] = m.FileID
	}
	return out
}

// checkNotElsewhere：要新挂上的文件（files 行已锁住）已经在别的条目里时 409 `file_cataloged`，details {ids, items}。
func checkNotElsewhere(ctx context.Context, q db.Querier, itemID uuid.UUID, linked []uuid.UUID, files map[uuid.UUID]*pageFile) error {
	where, err := placementsOf(ctx, q, linked, itemID)
	if err != nil || len(where) == 0 {
		return err
	}
	var ids []uuid.UUID
	var rows []*placementRow
	for _, id := range linked {
		if p := where[id]; p != nil {
			ids = append(ids, id)
			rows = append(rows, p)
		}
	}
	items, err := elsewhereDetails(ctx, q, rows)
	if err != nil {
		return err
	}
	first := rows[0]
	name := ""
	if f := files[first.FileID]; f != nil {
		name = "「" + f.FileName + "」"
	}
	at := fmt.Sprintf("%s第 %d 页", itemLabel(first.ItemNo, first.ArchiveNo), first.PageNo)
	msg := fmt.Sprintf("文件%s已经挂在%s，一个文件只能在一个条目里；要挂到这里先从原条目移出", name, at)
	if len(ids) > 1 {
		msg = fmt.Sprintf("有 %d 个文件已经挂在别的条目里（例如%s在%s），一个文件只能在一个条目里；要挂到这里先从原条目移出",
			len(ids), name, at)
	}
	return httpx.Conflict("file_cataloged", msg).WithDetails(map[string]any{"ids": ids, "items": items})
}

// itemLabel 是条目的称呼：有档号用档号，没有时"第 n 件"。
func itemLabel(itemNo int, archiveNo *string) string {
	if archiveNo != nil && *archiveNo != "" {
		return "档号 " + *archiveNo + " "
	}
	return fmt.Sprintf("第 %d 件", itemNo)
}

// CatalogedElsewhere 是 409 `file_cataloged` 的 details.items 的一项：字段同 placements，没有 category_path、多了 file_id。
type CatalogedElsewhere struct {
	FileID uuid.UUID `json:"file_id"`
	placementBase
}

func elsewhereDetails(ctx context.Context, q db.Querier, rows []*placementRow) ([]CatalogedElsewhere, error) {
	names, err := categoryNames(ctx, q, categoryIDsOf(rows))
	if err != nil {
		return nil, err
	}
	out := make([]CatalogedElsewhere, len(rows))
	for i, r := range rows {
		out[i] = CatalogedElsewhere{FileID: r.FileID, placementBase: r.base(names[r.CategoryID])}
	}
	return out, nil
}

// PageInput 是 `PUT /api/archive-items/{id}/files` 的输入。
type PageInput struct {
	RowVersion int
	FileIDs    []uuid.UUID
}

// ReplacePages 整体替换条目的页。检查顺序：参数 400 → 条目 404 → 定稿 409 item_locked → row_version 409 conflict →
// 要挂的文件 404（details.ids）→ 409 file_cataloged。
func (s *Service) ReplacePages(ctx context.Context, operatorID, id uuid.UUID, in PageInput) (*Item, error) {
	if err := checkRowVersion(in.RowVersion); err != nil {
		return nil, err
	}
	if err := checkPageList(in.FileIDs); err != nil {
		return nil, err
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
	var out *Item
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
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
			return errLockedEdit("改页序、挂接或移出")
		}
		if r.RowVersion != in.RowVersion {
			return staleVersion(r.RowVersion)
		}
		ch, err := applyPages(ctx, tx, projectID, operatorID, r, in.FileIDs, false)
		if err != nil {
			return err
		}
		if ch.Plan.changed() {
			if err := touchItem(ctx, tx, id, operatorID); err != nil {
				return err
			}
			ev.Add(ch.filesEvent(projectID)...)
			ev.Add(events.CatalogChanged(projectID, []uuid.UUID{r.CategoryID}, []uuid.UUID{id}))
		}
		out, err = readItem(ctx, tx, st, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
