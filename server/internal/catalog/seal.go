package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// 封存（requirements 5.5"封存"、5.9"封存""解除封存""重新归档"，用户 2026-10-18、19）：
//
//   - **封存**：文件夹是某个未删除条目的来源、且条目不在修改中（source_status = sealed）。文件目录对它的写操作
//     （往里上传、复制进去、移入移出文件、删除里面的文件、给里面的文件改名、在里面建子文件夹、这个文件夹本身改名、
//     移动、删除）一律 409 `folder_archived`（[SealedCheck]，文件目录、上传在锁住文件夹行之后调用）；重扫、审核、
//     改备注照常。不变量：sealed 的条目，页 = 来源文件夹里未删除的文件（按 sort_no、名称），由封存检查保证。
//   - **解除封存** `unseal`：只有 draft 且 sealed 的（locked 409 item_locked，已是 modifying 409 invalid_state）；
//     reason 选填 ≤ 200 字。→ modifying，记下谁、何时、原因；件号、档号、著录、页都不变，和文件夹的绑定不断。
//     修改中：文件夹和未归档的一样可以整理（删掉、移走的文件在重新归档前仍是这一件的页）；不能锁定、导出预检不就绪。
//   - **重新归档** `reseal`：只有 modifying 的（sealed 409 invalid_state，locked 409 item_locked）；来源文件夹要仍是
//     件文件夹（被删了、有了子文件夹、空了、> 2000 页 → 409 invalid_state 写明原因）；新增的文件已在别的条目里
//     409 file_cataloged。成功：页换成文件夹现在的清单（link / unlink 日志、留下的页相对顺序变了写 reorder 审计）、
//     → sealed、清掉解除封存的人和原因（历史在审计里）；没有变化也可以。`reseal-preview` 给同样的对比，不写库。
//
// 加锁（requirements 3.6"封存检查和归档读文件清单要互斥"）：文件目录的每个写操作在文件树共享锁之后都锁住它改动的
// 文件夹行（上传、复制锁目标，移动锁源和目标，删除、改名锁所在的文件夹，建子文件夹锁父文件夹，改名、移动文件夹锁
// 它自己），锁住之后再做封存检查。归档、重新归档在"归类树锁 → 节点行 / 条目行 → 文件树共享锁"之后锁住来源文件夹行
// （FOR NO KEY UPDATE，同一种锁），锁住之后才读文件清单、写条目和页。两边对同一个文件夹串行：先归档的提交后，写
// 操作锁到文件夹行时（READ COMMITTED，锁后的新语句看得到刚提交的条目）就看到封存、409；先写的提交后，归档读到的
// 清单里就有它。顺序上文件夹行在 files 行之后，所以归档、重新归档锁了文件夹之后不再锁 files 行（往这个文件夹里放、
// 拿文件都要先锁文件夹行，锁文件夹已经够了）。解除封存、锁定、解锁、撤销归档只改条目，不锁文件夹：写操作看到的是
// 提交了的状态，最多晚一步看到"已解除"。

// MaxUnsealReasonChars 是解除封存原因的最大字符数。
const MaxUnsealReasonChars = 200

// SealedDetails 是 409 `folder_archived` 的 details：第一个封存的文件夹和它对应的条目（界面据此给"解除封存去修改"
// 的引导），folder_ids 是这次碰到的全部封存的文件夹。
type SealedDetails struct {
	FolderID   uuid.UUID `json:"folder_id"`
	FolderName string    `json:"folder_name"`
	archivedAs
	Locked    bool        `json:"locked"`
	FolderIDs []uuid.UUID `json:"folder_ids"`
}

// SealedCheck：folderIDs 里有封存的文件夹时返回 409 `folder_archived`（what 是被挡住的操作，如"上传文件"），否则 nil。
// 在调用方的事务里、锁住这些文件夹行之后调用（文件目录、上传用）。
func SealedCheck(ctx context.Context, q db.Querier, what string, folderIDs ...uuid.UUID) error {
	folderIDs = xslices.Unique(folderIDs)
	if len(folderIDs) == 0 {
		return nil
	}
	sealed, err := sealedAmong(ctx, q, folderIDs)
	if err != nil || len(sealed) == 0 {
		return err
	}
	first := sealed[0]
	names, err := CategoryNames(ctx, q, []uuid.UUID{first.CategoryID})
	if err != nil {
		return err
	}
	first.CategoryNames = names[first.CategoryID]
	d := SealedDetails{FolderID: first.FolderID, FolderName: first.FolderName, archivedAs: first.archivedAs,
		Locked: first.Status == statusLocked, FolderIDs: make([]uuid.UUID, len(sealed))}
	for i, s := range sealed {
		d.FolderIDs[i] = s.FolderID
	}
	where := strings.Join(append(first.CategoryNames[:len(first.CategoryNames):len(first.CategoryNames)],
		strings.TrimSpace(itemLabel(first.ItemNo, first.ArchiveNo))), " / ")
	msg := fmt.Sprintf("文件夹「%s」已归档为 %s（%s）并封存，不能%s；要修改请到归类页解除封存", first.FolderName, where,
		titleOrPlaceholder(first.Title), what)
	if d.Locked {
		msg = fmt.Sprintf("文件夹「%s」已归档为 %s（%s）并锁定，不能%s；要修改先解锁这一件、再解除封存", first.FolderName, where,
			titleOrPlaceholder(first.Title), what)
	}
	return httpx.Conflict(httpx.CodeFolderArchived, msg).WithDetails(d)
}

// normalizeReason：去掉首尾空白，空串为 nil；≤ 200 字，不能有控制字符（换行、制表符可以）。
func normalizeReason(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	r := strings.TrimSpace(*v)
	if r == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(r) > MaxUnsealReasonChars {
		return nil, httpx.Invalid("解除封存的原因最长 %d 个字", MaxUnsealReasonChars)
	}
	if strings.IndexFunc(r, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' }) >= 0 {
		return nil, httpx.Invalid("解除封存的原因不能有控制字符")
	}
	return &r, nil
}

// lockedLiveItem 在事务里拿归类树共享锁、锁住条目行；不存在、已删除 404。
func lockedLiveItem(ctx context.Context, tx pgx.Tx, projectID, id uuid.UUID) (*itemRow, error) {
	if err := project.LockCatalogTree(ctx, tx, projectID, false); err != nil {
		return nil, err
	}
	r, err := lockItem(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.Deleted || r.SourceFolderID == nil {
		return nil, errItemNotFound
	}
	return r, nil
}

// Unseal 解除封存。projectID 是条目所在的项目（中间件已经查过）。
func (s *Service) Unseal(ctx context.Context, operatorID, projectID, id uuid.UUID, rowVersion int, reason *string) (*Item, error) {
	if err := checkRowVersion(rowVersion); err != nil {
		return nil, err
	}
	why, err := normalizeReason(reason)
	if err != nil {
		return nil, err
	}
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *Item
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		r, err := lockedLiveItem(ctx, tx, projectID, id)
		if err != nil {
			return err
		}
		switch {
		case r.Status == statusLocked:
			return errLockedEdit("解除封存")
		case r.SourceStatus == sourceModifying:
			return httpx.InvalidState("条目已经在修改中了（来源文件夹已解除封存）")
		}
		if r.RowVersion != rowVersion {
			return staleVersion(r.RowVersion)
		}
		if err := setSourceStatus(ctx, tx, id, sourceModifying, operatorID, why); err != nil {
			return err
		}
		paths, err := file.FolderPaths(ctx, tx, []uuid.UUID{*r.SourceFolderID})
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveItem, TargetID: id, Action: "unseal",
			Detail: map[string]any{"before": map[string]any{"source_status": sourceSealed},
				"after": map[string]any{"source_status": sourceModifying}, "reason": why,
				"source_folder_id": *r.SourceFolderID, "source_folder_path": paths[*r.SourceFolderID]}}); err != nil {
			return err
		}
		if out, err = readItem(ctx, tx, st, id); err != nil {
			return err
		}
		ev.Add(events.FoldersChanged(projectID, *r.SourceFolderID))
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{r.CategoryID}, []uuid.UUID{id}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ResealPage 是重新归档对比里的一页。
type ResealPage struct {
	FileID   uuid.UUID `json:"file_id"`
	FileName string    `json:"file_name"`
	// PageNo：新增的是在新的第几页，去掉的是原来的第几页。
	PageNo int `json:"page_no"`
	// FolderPath 是文件现在所在的文件夹（去掉的页可能已经移到别处）；Deleted：文件已删除。
	FolderPath   string `json:"folder_path"`
	Deleted      bool   `json:"deleted"`
	ReviewStatus string `json:"review_status"`
	VersionNo    int    `json:"version_no"`
}

// ResealMove 是留下的页里相对顺序变了的一页（和 reorder 审计同一个算法：最长递增子序列之外的）。
type ResealMove struct {
	FileID   uuid.UUID `json:"file_id"`
	FileName string    `json:"file_name"`
	FromPage int       `json:"from_page"`
	ToPage   int       `json:"to_page"`
}

// ResealProblem 是现在不能重新归档的原因（重新归档会 409）。
type ResealProblem struct {
	// Code：folder_deleted、has_subfolders、empty、too_many_pages（409 invalid_state）、file_cataloged（新增的文件还在
	// 别的条目里，409 file_cataloged）。
	Code    string               `json:"code"`
	Message string               `json:"message"`
	Items   []CatalogedElsewhere `json:"items,omitempty"`
}

// ResealPreview 是 `GET /api/archive-items/{id}/reseal-preview`。
type ResealPreview struct {
	ItemID       uuid.UUID `json:"item_id"`
	RowVersion   int       `json:"row_version"`
	Status       string    `json:"status"`
	SourceStatus string    `json:"source_status"`
	FolderID     uuid.UUID `json:"folder_id"`
	FolderName   string    `json:"folder_name"`
	FolderPath   string    `json:"folder_path"`
	OldPageCount int       `json:"old_page_count"`
	NewPageCount int       `json:"new_page_count"`
	// Added 按新页号，Removed 按原页号，Moved 按原页号。
	Added   []ResealPage `json:"added"`
	Removed []ResealPage `json:"removed"`
	Moved   []ResealMove `json:"moved"`
	// Changed：页有变化（新增、去掉、页号变了任一）。
	Changed bool           `json:"changed"`
	Problem *ResealProblem `json:"problem"`
}

// resealPlan 是重新归档要做的事（预览和执行共用）。
type resealPlan struct {
	preview *ResealPreview
	wanted  []uuid.UUID
	plan    pagePlan
	// files 是 plan 里新挂上、移出、挪动的文件。
	files map[uuid.UUID]*pageFile
	// stateErr 是 409 invalid_state（来源文件夹不是件文件夹）；elsewhere 是 409 file_cataloged。
	stateErr  error
	elsewhere error
}

// planReseal 读来源文件夹现在的文件，和条目现在的页比较。执行时调用前要锁住条目行和来源文件夹行。
func planReseal(ctx context.Context, q db.Querier, r *itemRow) (*resealPlan, error) {
	folderID := *r.SourceFolderID
	nodes, err := folderNodes(ctx, q, []uuid.UUID{folderID})
	if err != nil {
		return nil, err
	}
	states, err := folderStates(ctx, q, []uuid.UUID{folderID})
	if err != nil {
		return nil, err
	}
	paths, err := file.FolderPaths(ctx, q, []uuid.UUID{folderID})
	if err != nil {
		return nil, err
	}
	node, fs := nodes[folderID], states[folderID]
	pv := &ResealPreview{ItemID: r.ID, RowVersion: r.RowVersion, Status: r.Status, SourceStatus: r.SourceStatus,
		FolderID: folderID, FolderName: node.Name, FolderPath: paths[folderID],
		Added: []ResealPage{}, Removed: []ResealPage{}, Moved: []ResealMove{}}
	out := &resealPlan{preview: pv}
	switch {
	case node.Deleted:
		pv.Problem = &ResealProblem{Code: "folder_deleted", Message: "来源文件夹「" + node.Name + "」已经删除了，不能重新归档；不要这一件就撤销归档"}
	case fs.Subfolders > 0:
		pv.Problem = &ResealProblem{Code: skipHasSubfolders, Message: fmt.Sprintf(
			"来源文件夹「%s」里有了 %d 个子文件夹，只能归档没有子文件夹的文件夹；先把子文件夹移走", node.Name, fs.Subfolders)}
	case fs.Files == 0:
		pv.Problem = &ResealProblem{Code: skipEmpty, Message: "来源文件夹「" + node.Name + "」里没有文件了，不能重新归档；不要这一件就撤销归档"}
	case fs.Files > MaxItemPages:
		pv.Problem = &ResealProblem{Code: skipTooManyPages, Message: fmt.Sprintf(
			"来源文件夹「%s」里有 %d 个文件，一件最多 %d 页", node.Name, fs.Files, MaxItemPages)}
	}
	if pv.Problem != nil {
		out.stateErr = httpx.InvalidState(pv.Problem.Message)
	}

	var files []folderFile
	if !node.Deleted {
		byFolder, err := folderFiles(ctx, q, []uuid.UUID{folderID})
		if err != nil {
			return nil, err
		}
		files = byFolder[folderID]
	}
	current, err := itemPageList(ctx, q, r.ID)
	if err != nil {
		return nil, err
	}
	out.wanted = make([]uuid.UUID, len(files))
	for i, f := range files {
		out.wanted[i] = f.FileID
	}
	out.plan = planPages(current, out.wanted)
	p := &out.plan
	pv.OldPageCount, pv.NewPageCount, pv.Changed = len(current), len(files), p.changed()

	read := append(append(fileIDsOf(p.Linked), fileIDsOf(p.Unlinked)...), fileIDsOf(p.Moved)...)
	info, err := pageFiles(ctx, q, r.ProjectID, read)
	if err != nil {
		return nil, err
	}
	out.files = info
	page := func(id uuid.UUID, no int) ResealPage {
		rp := ResealPage{FileID: id, PageNo: no}
		if f := info[id]; f != nil {
			rp.FileName, rp.FolderPath, rp.Deleted, rp.ReviewStatus, rp.VersionNo = f.FileName, f.FolderPath, !f.Live, f.ReviewStatus, f.VersionNo
		}
		return rp
	}
	for _, m := range p.Linked {
		pv.Added = append(pv.Added, page(m.FileID, m.To))
	}
	for _, m := range p.Unlinked {
		pv.Removed = append(pv.Removed, page(m.FileID, m.From))
	}
	for _, m := range p.Moved {
		pv.Moved = append(pv.Moved, ResealMove{FileID: m.FileID, FileName: page(m.FileID, 0).FileName, FromPage: m.From, ToPage: m.To})
	}
	if pv.Problem == nil && len(p.Linked) > 0 {
		if err := checkNotElsewhere(ctx, q, r.ID, fileIDsOf(p.Linked), info); err != nil {
			e, ok := httpx.AsError(err)
			if !ok || e.Code != httpx.CodeFileCataloged {
				return nil, err
			}
			out.elsewhere = err
			d, _ := e.Details.(map[string]any)
			items, _ := d["items"].([]CatalogedElsewhere)
			pv.Problem = &ResealProblem{Code: httpx.CodeFileCataloged, Message: e.Message, Items: items}
		}
	}
	return out, nil
}

// ResealPreview 是重新归档的对比（不写库，任何状态都能看；只有修改中的能真的重新归档）。
func (s *Service) ResealPreview(ctx context.Context, projectID, id uuid.UUID) (*ResealPreview, error) {
	var out *ResealPreview
	err := db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := getItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if r == nil || r.SourceFolderID == nil {
			return errItemNotFound
		}
		p, err := planReseal(ctx, tx, r)
		if err != nil {
			return err
		}
		out = p.preview
		return nil
	})
	return out, err
}

// Reseal 重新归档。检查顺序：404 → 状态 409（item_locked / invalid_state）→ row_version 409 conflict →
// 来源文件夹不是件文件夹 409 invalid_state → 新增的文件在别的条目里 409 file_cataloged。projectID 是条目所在的项目
// （中间件已经查过）。
func (s *Service) Reseal(ctx context.Context, operatorID, projectID, id uuid.UUID, rowVersion int) (*Item, error) {
	if err := checkRowVersion(rowVersion); err != nil {
		return nil, err
	}
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *Item
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		r, err := lockedLiveItem(ctx, tx, projectID, id)
		if err != nil {
			return err
		}
		switch {
		case r.Status == statusLocked:
			return errLockedEdit("重新归档")
		case r.SourceStatus != sourceModifying:
			return httpx.InvalidState("条目没有解除封存，不用重新归档")
		}
		if r.RowVersion != rowVersion {
			return staleVersion(r.RowVersion)
		}
		// 文件树锁（共享）→ 来源文件夹行，锁住之后读文件清单（和往里放、拿文件互斥）。
		if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
			return err
		}
		if err := lockFolderRows(ctx, tx, []uuid.UUID{*r.SourceFolderID}); err != nil {
			return err
		}
		p, err := planReseal(ctx, tx, r)
		if err != nil {
			return err
		}
		if p.stateErr != nil {
			return p.stateErr
		}
		if p.elsewhere != nil {
			return p.elsewhere
		}
		ch, err := writePages(ctx, tx, projectID, operatorID, r, p.plan, len(p.wanted), p.files, false)
		if err != nil {
			return err
		}
		if err := setSourceStatus(ctx, tx, id, sourceSealed, operatorID, nil); err != nil {
			return err
		}
		pv := p.preview
		brief := func(ps []ResealPage) []map[string]any {
			out := make([]map[string]any, len(ps))
			for i, x := range ps {
				out[i] = map[string]any{"file_id": x.FileID, "file_name": x.FileName, "page_no": x.PageNo}
			}
			return out
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(operatorID),
			TargetType: audit.TargetArchiveItem, TargetID: id, Action: "reseal",
			Detail: map[string]any{"before": map[string]any{"source_status": sourceModifying, "page_count": pv.OldPageCount},
				"after":            map[string]any{"source_status": sourceSealed, "page_count": pv.NewPageCount},
				"source_folder_id": pv.FolderID, "source_folder_path": pv.FolderPath,
				"added": brief(pv.Added), "removed": brief(pv.Removed), "moved": pv.Moved}}); err != nil {
			return err
		}
		if out, err = readItem(ctx, tx, st, id); err != nil {
			return err
		}
		ev.Add(ch.filesEvent(projectID)...)
		ev.Add(events.FoldersChanged(projectID, pv.FolderID))
		ev.Add(events.CatalogChanged(projectID, []uuid.UUID{r.CategoryID}, []uuid.UUID{id}))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MaxModifyingItems 是"修改中的件"一次最多返回多少件。
const MaxModifyingItems = 500

// ModifyingItem 是项目里一件修改中的条目（归类页顶部"修改中 n 件"的清单，M9-2）。
type ModifyingItem struct {
	ItemID     uuid.UUID `json:"item_id"`
	ItemNo     int       `json:"item_no"`
	ArchiveNo  *string   `json:"archive_no"`
	Title      string    `json:"title"`
	CategoryID uuid.UUID `json:"category_id"`
	// CategoryNames 是所在节点的名称链（不含根）。
	CategoryNames    []string  `json:"category_names"`
	SourceFolderID   uuid.UUID `json:"source_folder_id"`
	SourceFolderName string    `json:"source_folder_name"`
	// SourceFolderPath 从项目根文件夹起用 / 连接。
	SourceFolderPath string     `json:"source_folder_path"`
	UnsealedBy       *uuid.UUID `json:"unsealed_by"`
	UnsealedByName   *string    `json:"unsealed_by_name"`
	UnsealedAt       *time.Time `json:"unsealed_at"`
	UnsealReason     *string    `json:"unseal_reason"`
	RowVersion       int        `json:"row_version"`
}

// ModifyingList 是 `GET /api/projects/{pid}/archive-items:modifying` 的响应：最多 500 件（按解除封存的时间倒序），
// total 是真实总数。
type ModifyingList struct {
	Items []ModifyingItem `json:"items"`
	Total int             `json:"total"`
}

// Modifying 列出项目里修改中的条目。
func (s *Service) Modifying(ctx context.Context, projectID uuid.UUID) (*ModifyingList, error) {
	var out *ModifyingList
	err := db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		items, total, err := listModifying(ctx, tx, projectID, MaxModifyingItems)
		if err != nil {
			return err
		}
		cats := make([]uuid.UUID, len(items))
		folders := make([]uuid.UUID, len(items))
		for i, m := range items {
			cats[i], folders[i] = m.CategoryID, m.SourceFolderID
		}
		names, err := CategoryNames(ctx, tx, cats)
		if err != nil {
			return err
		}
		paths, err := file.FolderPaths(ctx, tx, folders)
		if err != nil {
			return err
		}
		for i := range items {
			items[i].CategoryNames = names[items[i].CategoryID]
			items[i].SourceFolderPath = paths[items[i].SourceFolderID]
		}
		out = &ModifyingList{Items: items, Total: total}
		return nil
	})
	return out, err
}
