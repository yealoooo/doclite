package catalog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
)

// 条目的页的写（requirements 5.9；2026-10-19 起页只来自来源文件夹，归类这边不能单独挂页、移页、排页序）：
//   - 归档（archive.go）一次建很多件，批量写挂接；
//   - 重新归档（seal.go）把页换成来源文件夹现在的文件（planReseal 算好、writePages 写），撤销归档（DeleteItem）一并
//     移出全部页（removeAllPages）；
//   - 一个文件只在一个条目里：要新挂上的文件已经在别的条目里 409 `file_cataloged`，details {ids, items}（在哪：
//     条目、档号、件号、题名、节点名称链、第几页、状态）。
//
// 加锁（requirements 3.6）：调用方先拿归类树锁（共享）和条目行。撤销归档在归类的锁和行之后再拿文件树锁
// （共享）和移出的文件的 files 行（id 升序），锁后重读 folder_files。重新归档已经按"文件树锁 → 来源文件夹行"锁住了
// 来源文件夹（封存检查和读文件清单互斥，见 seal.go），不再锁 files 行：files 行在 folders 行之前，锁了文件夹再锁
// files 行会和"先锁 files 行再锁文件夹"的删除、移动成环；往这个文件夹里放、拿文件都要先锁文件夹行。
//
// 日志：新挂上的每页一条 link、移出的每页一条 unlink（先 unlink 再 link），detail {item_id, item_no, archive_no, title,
// page_no, file_name, folder_path, version_no, review_status}，撤销归档时另带 item_deleted: true；留下的页相对顺序变了
// 写一条 archive_item / reorder 审计 {page_count, linked, unlinked, moved: [{file_id, file_name, from_page, to_page}]}，
// 只记挪动过的页（最长递增子序列之外），插入、移出引起的顺延不算。

// pageChange 是 writePages 做了的事。
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

// removeAllPages 撤销归档时把条目 it 的页全部移出（日志带 item_deleted）。调用前要拿着归类树锁（共享）、锁住条目行；
// 有页时在这之后拿文件树锁（共享）和这些文件的 files 行（requirements 3.6）。
func removeAllPages(ctx context.Context, tx pgx.Tx, projectID, operatorID uuid.UUID, it *itemRow) (*pageChange, error) {
	current, err := itemPageList(ctx, tx, it.ID)
	if err != nil {
		return nil, err
	}
	p := planPages(current, nil)
	if !p.changed() {
		return &pageChange{Plan: p}, nil
	}
	if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
		return nil, err
	}
	unlinked := fileIDsOf(p.Unlinked)
	if _, err := file.LockProjectFiles(ctx, tx, projectID, unlinked); err != nil {
		return nil, err
	}
	files, err := pageFiles(ctx, tx, projectID, unlinked)
	if err != nil {
		return nil, err
	}
	return writePages(ctx, tx, projectID, operatorID, it, p, 0, files, true)
}

// writePages 在调用方的事务里按 p 写条目 it 的挂接（页数 pageCount），记日志和 reorder 审计。p、files（p 里新挂上、
// 移出、挪动的文件）由调用方在锁内算好、读好，新挂上的已经查过不在别的条目里。不改条目行本身（row_version 由调用方
// 处理）。itemDeleted：撤销归档时一并移出（日志带 item_deleted）。
func writePages(ctx context.Context, tx pgx.Tx, projectID, operatorID uuid.UUID, it *itemRow, p pagePlan, pageCount int,
	files map[uuid.UUID]*pageFile, itemDeleted bool) (*pageChange, error) {
	out := &pageChange{Plan: p}
	if !p.changed() {
		return out, nil
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
	logs := make([]file.Log, 0, len(p.Linked)+len(p.Unlinked))
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
			Detail: map[string]any{"page_count": pageCount, "linked": len(p.Linked), "unlinked": len(p.Unlinked),
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
	msg := fmt.Sprintf("文件%s还在%s里（一个文件只能在一个条目里）；它是从那一件的来源文件夹移过来的，先重新归档那一件", name, at)
	if len(ids) > 1 {
		msg = fmt.Sprintf("有 %d 个文件还在别的条目里（例如%s在%s，一个文件只能在一个条目里）；它们是从修改中的文件夹移过来的，"+
			"先重新归档那几件", len(ids), name, at)
	}
	return httpx.Conflict(httpx.CodeFileCataloged, msg).WithDetails(map[string]any{"ids": ids, "items": items})
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
	names, err := CategoryNames(ctx, q, categoryIDsOf(rows))
	if err != nil {
		return nil, err
	}
	out := make([]CatalogedElsewhere, len(rows))
	for i, r := range rows {
		out[i] = CatalogedElsewhere{FileID: r.FileID, placementBase: r.base(names[r.CategoryID])}
	}
	return out, nil
}
