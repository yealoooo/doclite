package catalog

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/xslices"
	"doclite/server/internal/project"
)

// 整个文件夹归档（requirements 5.9"归档"，用户 2026-10-18、19）：条目只能这样产生。
//
//   - **件文件夹**：不是根、没有未删除的子文件夹、有 1–2000 个未删除文件、还不是别的未删除条目的来源；
//   - folder_ids 1–500 个，每个是件文件夹（自己成一件）或上级文件夹（有未删除的子文件夹：它的直接子文件夹各成一件，
//     它自己直接放的文件不算；根文件夹总是当上级文件夹）；展开后去重 ≤ 500 个文件夹；
//   - 不合格的跳过并给原因：archived（已归档为某件，带那一件）、has_subfolders、empty、too_many_pages、
//     file_cataloged（里面有文件还在别的条目里——从修改中的文件夹移过来的，带那一件，要先重新归档那一件）；
//   - 选中的文件夹当上级展开、而它自己直接放了文件时，loose_files 列出来（这些文件不归档，只是提示）；
//   - 要建的件的页数合计 ≤ 50000（MaxArchivePages），超过 400（预览也是）；
//   - dry_run 只返回预览（将新建的件：来源文件夹、题名、页数、件号、按模板算的档号——预览不查撞号），不写库；
//     不带时一个事务建全部件，一件都建不出 409 `invalid_state`（details 即预览）；
//   - 题名默认 = 文件夹名，titles {folder_id: 题名} 可改（只查格式，必填在锁定时查）；没给的著录项用设置里的 default；
//   - 页 = 文件夹里未删除的文件按 sort_no、名称排，页号 1..n；新件 draft、sealed，件号按节点 max+1 依次领，档号照常
//     生成（撞号留空）。
//
// 加锁（requirements 3.6）：归类树锁（共享）→ 目标节点行（件号）→ 文件树锁（共享）→ 展开出来的文件夹行（id 升序，
// 和文件目录的写操作锁同一行：封存检查和读文件清单互斥，见 seal.go）→ 锁后读文件夹的状态和文件清单 → 新条目的档号
// 咨询锁（6005，一次按键排序全部拿到）→ 插入。新条目是新行，不等别人；拿着 6005 的事务（改件号、移动条目）不再等
// 文件树锁和文件夹行，所以这里在文件夹行之后拿 6005 不会成环。
//
// 审计 archive_item / archive（每件一条：节点、件号、档号、题名、著录、来源文件夹和路径、页数）；每页一条 file_logs
// link。事件（提交后）：files.changed（来源文件夹、挂上的文件）→ folders.changed（来源文件夹）→ catalog.changed。

// MaxArchiveFolders 是一次归档的 folder_ids 和展开后的文件夹数的上限；MaxArchivePages 是一次要建的件的页数合计的上限。
const (
	MaxArchiveFolders = 500
	MaxArchivePages   = 50000
)

// 跳过的原因。
const (
	skipArchived      = "archived"
	skipHasSubfolders = "has_subfolders"
	skipEmpty         = "empty"
	skipTooManyPages  = "too_many_pages"
	skipFileCataloged = httpx.CodeFileCataloged
)

// ArchiveInput 是 `POST /api/archive-categories/{id}/items:from-folders` 的输入。
type ArchiveInput struct {
	FolderIDs []uuid.UUID          `json:"folder_ids"`
	Titles    map[uuid.UUID]string `json:"titles"`
	DryRun    bool                 `json:"dry_run"`
}

// ArchivePlanItem 是将新建（预览）或已新建的一件。
type ArchivePlanItem struct {
	FolderID   uuid.UUID `json:"folder_id"`
	FolderName string    `json:"folder_name"`
	// FolderPath 从项目根文件夹起用 `/` 连接。
	FolderPath string `json:"folder_path"`
	Title      string `json:"title"`
	PageCount  int    `json:"page_count"`
	ItemNo     int    `json:"item_no"`
	// ArchiveNo：预览时是按模板算的（不查撞号，缺级为 null）；新建后是实际生成的（撞号留空为 null）。
	ArchiveNo *string `json:"archive_no"`
	// ItemID：预览时为 null。
	ItemID *uuid.UUID `json:"item_id"`
}

// ArchiveSkip 是跳过的一个文件夹。
type ArchiveSkip struct {
	FolderID   uuid.UUID `json:"folder_id"`
	FolderName string    `json:"folder_name"`
	FolderPath string    `json:"folder_path"`
	// Reason 是 archived、has_subfolders、empty、too_many_pages。
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// PageCount、SubfolderCount 是文件夹里未删除的文件数、子文件夹数。
	PageCount      int `json:"page_count"`
	SubfolderCount int `json:"subfolder_count"`
	// Item：reason 为 archived 时是已经归档成的那一件，为 file_cataloged 时是文件还在的那一件，否则为 null。
	Item *archivedAs `json:"item"`
}

// LooseFiles 是当上级展开的选中文件夹里直接放的文件（不归档，只提示）。
type LooseFiles struct {
	FolderID   uuid.UUID `json:"folder_id"`
	FolderName string    `json:"folder_name"`
	FolderPath string    `json:"folder_path"`
	FileCount  int       `json:"file_count"`
}

// ArchiveResult 是归档的响应（预览 200，新建 201；一件都建不出时 409 的 details 也是它）。
type ArchiveResult struct {
	CategoryID uuid.UUID         `json:"category_id"`
	DryRun     bool              `json:"dry_run"`
	Items      []ArchivePlanItem `json:"items"`
	Skipped    []ArchiveSkip     `json:"skipped"`
	LooseFiles []LooseFiles      `json:"loose_files"`
}

// ArchiveFolders 把文件夹整个归档到节点 categoryID（dry_run 时只预览）。
func (s *Service) ArchiveFolders(ctx context.Context, operatorID, projectID, categoryID uuid.UUID, in ArchiveInput) (*ArchiveResult, error) {
	if len(in.FolderIDs) == 0 {
		return nil, httpx.Invalid("至少要选一个文件夹")
	}
	if len(in.FolderIDs) > MaxArchiveFolders {
		return nil, httpx.Invalid("一次最多选 %d 个文件夹，这次有 %d 个", MaxArchiveFolders, len(in.FolderIDs))
	}
	selected := xslices.Unique(in.FolderIDs)
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *ArchiveResult
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		var cat *catRow
		if in.DryRun {
			if cat, err = getCategory(ctx, tx, projectID, categoryID); err != nil {
				return err
			}
		} else {
			locked, err := lockCategories(ctx, tx, projectID, categoryID)
			if err != nil {
				return err
			}
			cat = locked[categoryID]
		}
		if cat == nil || cat.Deleted {
			return errCategoryNotFound
		}
		if !in.DryRun {
			if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
				return err
			}
		}
		list, parents, err := expandFolders(ctx, tx, projectID, selected)
		if err != nil {
			return err
		}
		if cat.ParentID == nil {
			return httpx.InvalidState("不能归档到根节点下，请先选一个归类节点")
		}
		if st.tmpl == nil {
			return st.templateInvalid("归档")
		}
		ids := make([]uuid.UUID, len(list))
		for i, f := range list {
			ids[i] = f.ID
		}
		// 锁住文件夹行之后再读状态和文件清单（和往里放文件、拿文件互斥）；预览不锁。
		if !in.DryRun {
			if err := lockFolderRows(ctx, tx, ids); err != nil {
				return err
			}
		}
		states, err := folderStates(ctx, tx, ids)
		if err != nil {
			return err
		}
		paths, err := file.FolderPaths(ctx, tx, ids)
		if err != nil {
			return err
		}
		out = &ArchiveResult{CategoryID: cat.ID, DryRun: in.DryRun, Items: []ArchivePlanItem{}, Skipped: []ArchiveSkip{},
			LooseFiles: []LooseFiles{}}
		if out.LooseFiles, err = looseFiles(ctx, tx, parents); err != nil {
			return err
		}
		// 合格的文件夹里的文件（就是页）；有文件还在别的条目里的跳过 file_cataloged。
		var eligible []uuid.UUID
		for _, f := range list {
			if skipReason(states[f.ID]) == "" {
				eligible = append(eligible, f.ID)
			}
		}
		pageFiles, err := folderFiles(ctx, tx, eligible)
		if err != nil {
			return err
		}
		elsewhere, err := filesElsewhere(ctx, tx, pageFiles)
		if err != nil {
			return err
		}
		var cands []*folderNode
		pages := 0
		for _, f := range list {
			fs := states[f.ID]
			skip := skipReason(fs)
			msg := skipMessage(skip, fs)
			item := fs.SourceOf
			if e := elsewhere[f.ID]; skip == "" && e != nil {
				skip, item = skipFileCataloged, &e.archivedAs
				msg = fmt.Sprintf("里面的「%s」还在%s里（从修改中的文件夹移过来的），先重新归档那一件", e.FileName,
					strings.TrimSpace(itemLabel(e.ItemNo, e.ArchiveNo)))
			}
			if skip != "" {
				out.Skipped = append(out.Skipped, ArchiveSkip{FolderID: f.ID, FolderName: f.Name,
					FolderPath: paths[f.ID], Reason: skip, Message: msg, PageCount: fs.Files,
					SubfolderCount: fs.Subfolders, Item: item})
				continue
			}
			cands = append(cands, f)
			pages += fs.Files
			out.Items = append(out.Items, ArchivePlanItem{FolderID: f.ID, FolderName: f.Name,
				FolderPath: paths[f.ID], Title: f.Name, PageCount: fs.Files})
		}
		if pages > MaxArchivePages {
			return httpx.Invalid("这次要归档 %d 件、合计 %d 页，一次最多 %d 页，请分几次归档", len(cands), pages, MaxArchivePages)
		}

		// 题名、著录（没给的用 default）。
		defaults := map[string]any{}
		for _, d := range st.ItemFields {
			if d.Default != nil {
				defaults[d.Key] = *d.Default
			}
		}
		extras := make([]map[string]any, len(cands))
		var issues []FieldIssue
		for i := range out.Items {
			title := out.Items[i].Title
			if t, ok := in.Titles[out.Items[i].FolderID]; ok {
				title = t
			}
			t, extra, is := st.applyEdits("", map[string]any{}, edits{Title: &title, Fields: defaults})
			for _, x := range is {
				x.Message = fmt.Sprintf("「%s」：%s", out.Items[i].FolderName, x.Message)
				issues = append(issues, x)
			}
			out.Items[i].Title, extras[i] = t, extra
		}
		if len(issues) > 0 {
			return fieldsError(issues)
		}

		// 件号、档号（预览按模板算，不查撞号）。
		if len(cands) > 0 {
			first, err := nextItemNo(ctx, tx, cat.ID)
			if err != nil {
				return err
			}
			if last := first + len(cands) - 1; last > MaxItemNo {
				return httpx.InvalidState(fmt.Sprintf("这个节点里的件号已经用到 %d，不能再归档", MaxItemNo))
			}
			ch, err := categoryChain(ctx, tx, projectID, cat.PathCode)
			if err != nil {
				return err
			}
			for i := range out.Items {
				out.Items[i].ItemNo = first + i
				if no, _ := render(st.tmpl, ch, first+i); no != "" {
					out.Items[i].ArchiveNo = &no
				}
			}
			if !in.DryRun {
				return s.createArchived(ctx, tx, ev, operatorID, projectID, cat, ch, out, cands, extras, pageFiles)
			}
		}
		if !in.DryRun {
			return httpx.InvalidState(fmt.Sprintf("选的文件夹里没有能归档成一件的（跳过了 %d 个）",
				len(out.Skipped))).WithDetails(out)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// expandFolders 读选中的文件夹并展开：不存在、已删除、不在这个项目里的整体 404（details.ids）；有未删除子文件夹的
// （和根文件夹）换成它的直接子文件夹。结果按选中的顺序、子文件夹按 sort_no、名称排，去重，≤ 500 个；parents 是当
// 上级展开了的选中文件夹。
func expandFolders(ctx context.Context, q db.Querier, projectID uuid.UUID, selected []uuid.UUID) (list, parents []*folderNode, err error) {
	nodes, err := folderNodes(ctx, q, selected)
	if err != nil {
		return nil, nil, err
	}
	var missing []uuid.UUID
	for _, id := range selected {
		if n := nodes[id]; n == nil || n.Deleted || n.ProjectID != projectID {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, nil, httpx.NewError(http.StatusNotFound, httpx.CodeNotFound,
			fmt.Sprintf("有 %d 个文件夹不存在、已删除或不在这个项目里", len(missing))).WithDetails(map[string]any{"ids": missing})
	}
	children, err := liveChildren(ctx, q, selected)
	if err != nil {
		return nil, nil, err
	}
	var out []*folderNode
	seen := map[uuid.UUID]bool{}
	add := func(n *folderNode) {
		if !seen[n.ID] {
			seen[n.ID] = true
			out = append(out, n)
		}
	}
	for _, id := range selected {
		n := nodes[id]
		if kids := children[id]; len(kids) > 0 || n.ParentID == nil {
			parents = append(parents, n)
			for _, k := range kids {
				add(k)
			}
			continue
		}
		add(n)
	}
	if len(out) > MaxArchiveFolders {
		return nil, nil, httpx.Invalid("一次最多归档 %d 个文件夹，展开子文件夹后有 %d 个，请分几次归档", MaxArchiveFolders, len(out))
	}
	return out, parents, nil
}

// looseFiles 是这些（当上级展开的）文件夹里直接放的未删除文件数，没有的不列。
func looseFiles(ctx context.Context, q db.Querier, parents []*folderNode) ([]LooseFiles, error) {
	out := []LooseFiles{}
	if len(parents) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(parents))
	for i, p := range parents {
		ids[i] = p.ID
	}
	counts, err := liveFileCounts(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	paths, err := file.FolderPaths(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for _, p := range parents {
		if n := counts[p.ID]; n > 0 {
			out = append(out, LooseFiles{FolderID: p.ID, FolderName: p.Name, FolderPath: paths[p.ID], FileCount: n})
		}
	}
	return out, nil
}

// skipReason 是文件夹不能成为一件的原因（空串表示可以）。
func skipReason(s *folderState) string {
	switch {
	case s.SourceOf != nil:
		return skipArchived
	case s.Subfolders > 0:
		return skipHasSubfolders
	case s.Files == 0:
		return skipEmpty
	case s.Files > MaxItemPages:
		return skipTooManyPages
	}
	return ""
}

func skipMessage(reason string, s *folderState) string {
	switch reason {
	case skipArchived:
		return "已经归档为" + itemLabel(s.SourceOf.ItemNo, s.SourceOf.ArchiveNo)
	case skipHasSubfolders:
		return fmt.Sprintf("里面还有 %d 个子文件夹，只能归档没有子文件夹的文件夹（或者选它，让子文件夹各成一件）", s.Subfolders)
	case skipEmpty:
		return "文件夹里没有文件"
	case skipTooManyPages:
		return fmt.Sprintf("文件夹里有 %d 个文件，一件最多 %d 页", s.Files, MaxItemPages)
	}
	return ""
}

// fileElsewhere 是文件夹里第一个还在别的条目里的文件和那一件。
type fileElsewhere struct {
	FileName string
	archivedAs
}

// filesElsewhere 是这些文件夹里有文件还在别的条目里的（从修改中的文件夹移过来的）：文件夹 → 页序最前的那个文件。
func filesElsewhere(ctx context.Context, q db.Querier, files map[uuid.UUID][]folderFile) (map[uuid.UUID]*fileElsewhere, error) {
	out := map[uuid.UUID]*fileElsewhere{}
	var ids []uuid.UUID
	for _, fs := range files {
		for _, f := range fs {
			ids = append(ids, f.FileID)
		}
	}
	where, err := placementsOf(ctx, q, ids, uuid.Nil)
	if err != nil || len(where) == 0 {
		return out, err
	}
	var cats []uuid.UUID
	for folderID, fs := range files {
		for _, f := range fs {
			if p := where[f.FileID]; p != nil {
				out[folderID] = &fileElsewhere{FileName: f.FileName, archivedAs: archivedAs{ItemID: p.ItemID, ItemNo: p.ItemNo,
					ArchiveNo: p.ArchiveNo, Title: p.Title, CategoryID: p.CategoryID}}
				cats = append(cats, p.CategoryID)
				break
			}
		}
	}
	names, err := CategoryNames(ctx, q, cats)
	if err != nil {
		return nil, err
	}
	for _, e := range out {
		e.CategoryNames = names[e.CategoryID]
	}
	return out, nil
}

// createArchived 写归档：条目、页、日志、审计、事件（锁和检查都已经做了）。
func (s *Service) createArchived(ctx context.Context, tx pgx.Tx, ev *events.Pending, operatorID, projectID uuid.UUID,
	cat *catRow, ch chain, out *ArchiveResult, cands []*folderNode, extras []map[string]any,
	pageFiles map[uuid.UUID][]folderFile) error {
	// 档号：一次按键排序拿全部咨询锁，再查占用；被别的条目占着的留空。
	var nos []string
	for _, it := range out.Items {
		if it.ArchiveNo != nil {
			nos = append(nos, *it.ArchiveNo)
		}
	}
	if err := lockArchiveNos(ctx, tx, projectID, nos); err != nil {
		return err
	}
	holders, err := archiveNoHolders(ctx, tx, projectID, nos)
	if err != nil {
		return err
	}
	items := make([]newItem, len(cands))
	conflicts := make([]map[string]any, len(cands))
	for i := range out.Items {
		it := &out.Items[i]
		if it.ArchiveNo != nil {
			if h, taken := holders[*it.ArchiveNo]; taken {
				conflicts[i] = map[string]any{"archive_no": *it.ArchiveNo, "item_id": h}
				it.ArchiveNo = nil
			}
		}
		id := db.NewID()
		it.ItemID = &id
		items[i] = newItem{ID: id, ItemNo: it.ItemNo, ArchiveNo: it.ArchiveNo, Title: it.Title, ExtraFields: extras[i],
			FolderID: it.FolderID}
	}
	if err := insertItems(ctx, tx, projectID, cat.ID, operatorID, items); err != nil {
		if db.UniqueViolation(err, "ux_archive_items_source") {
			return httpx.Conflict("", "有文件夹刚被别人归档了，请刷新后再试")
		}
		return err
	}

	var pItems, pFiles, folderIDs, fileIDs []uuid.UUID
	var pNos []int32
	var logs []file.Log
	audits := make([]audit.Entry, len(items))
	for i, it := range items {
		folderIDs = append(folderIDs, it.FolderID)
		plan := out.Items[i]
		for p, f := range pageFiles[it.FolderID] {
			pItems, pFiles, pNos = append(pItems, it.ID), append(pFiles, f.FileID), append(pNos, int32(p+1))
			fileIDs = append(fileIDs, f.FileID)
			logs = append(logs, file.Log{ProjectID: projectID, FileID: f.FileID, Action: file.ActionLink, OperatorID: operatorID,
				Detail: map[string]any{"item_id": it.ID, "item_no": it.ItemNo, "archive_no": it.ArchiveNo, "title": it.Title,
					"page_no": p + 1, "file_name": f.FileName, "folder_path": plan.FolderPath, "version_no": f.VersionNo,
					"review_status": f.ReviewStatus}})
		}
		after := map[string]any{"category_id": cat.ID, "category_path": ch.path(), "item_no": it.ItemNo,
			"archive_no": it.ArchiveNo, "title": it.Title, "extra_fields": it.ExtraFields, "status": statusDraft,
			"source_status": sourceSealed, "source_folder_id": it.FolderID, "source_folder_path": plan.FolderPath,
			"page_count": len(pageFiles[it.FolderID])}
		if conflicts[i] != nil {
			after["archive_no_conflict"] = conflicts[i]
		}
		audits[i] = audit.Entry{ProjectID: &projectID, OperatorID: &operatorID, TargetType: audit.TargetArchiveItem, TargetID: it.ID, Action: "archive", Detail: map[string]any{"after": after}}
	}
	if err := insertItemPages(ctx, tx, projectID, operatorID, pItems, pFiles, pNos); err != nil {
		if db.UniqueViolation(err, "archive_item_files_file_id_key") {
			return httpx.Conflict("", "有文件刚被挂到了别的条目里，请刷新后再试")
		}
		return err
	}
	if err := file.WriteLogs(ctx, tx, logs...); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audits...); err != nil {
		return err
	}
	itemIDs := make([]uuid.UUID, len(items))
	for i, it := range items {
		itemIDs[i] = it.ID
	}
	ev.Add(events.FilesChanged(projectID, folderIDs, fileIDs))
	ev.Add(events.FoldersChanged(projectID, folderIDs...))
	ev.Add(events.CatalogChanged(projectID, []uuid.UUID{cat.ID}, itemIDs))
	return nil
}

// lockArchiveNos 按键排序拿这些档号的咨询锁（一次拿多个时顺序一致，不会和别的归档互相等成环；一条语句拿完）。
func lockArchiveNos(ctx context.Context, tx db.Querier, projectID uuid.UUID, nos []string) error {
	keys := make([]int32, 0, len(nos))
	for _, no := range nos {
		keys = append(keys, archiveNoKey(projectID, no))
	}
	return db.AdvisoryXactLocks(ctx, tx, lockClassArchiveNo, keys)
}
