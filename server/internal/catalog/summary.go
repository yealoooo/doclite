package catalog

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 项目看板（requirements 5.10；2026-10-19 用户决定简化：去掉矩阵，只要简单统计，审核按页，归档、锁定、导出按件）：
// 一个文件夹整棵子树、直接放在里面的、每个子文件夹各自子树的计数。每次实时聚合，不建汇总表；只读，在一个只读快照里。
//   - 按页（文件）：审核状态 folder_files.review_status（unassigned、pending、approved、rejected）；归档状态：未归档
//     （没有挂接行）、已归档未锁定（条目 draft，含修改中）、已锁定（条目 locked），JSON 的键沿用 uncataloged、unlocked、
//     locked。只含未删除的文件（所在文件夹也未删除），一个文件算在它所在的文件夹里。
//   - 按件（items）：未删除的条目，算在它的来源文件夹（archive_items.source_folder_id，未删除的文件夹）里：子树的计数
//     算来源文件夹在子树里的件，direct_counts 算来源文件夹就是这个文件夹本身的件。"可导出"见 [summaryItemBuckets]。
//
// 每次实时聚合（整个项目时是全项目的文件和挂接），实时事件一来几个客户端会同时重新取：同一个（项目、文件夹、
// require_locked_item、项目的数据版本 events.Bus.Version）在 summaryTTL 内只算一次，同时来的合并成一次
// （internal/platform/memo）。发了事件就换键，所以看不到事件之前的旧结果；没发事件的变化（改显示名）最多晚 summaryTTL。
//
// 界面取数：已上传 total；审核通过 review.approved（小字待分配、待审、驳回）；已归档 items.total 件 · catalog.cataloged
// 页；已锁定 items.locked / items.total 件；可导出 items.exportable 件 · items.exportable_pages 页。

// ReviewCounts 是按审核状态的计数（和审核的按文件夹计数相同）。
type ReviewCounts struct {
	Unassigned int `json:"unassigned"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	Total      int `json:"total"`
}

func (c *ReviewCounts) add(reviewStatus string, n int) {
	switch reviewStatus {
	case "unassigned":
		c.Unassigned += n
	case "pending":
		c.Pending += n
	case "approved":
		c.Approved += n
	case "rejected":
		c.Rejected += n
	default:
		return // 不认识的审核状态（库里有 CHECK，走不到）
	}
	c.Total += n
}

// ColumnCounts 是按归档状态的文件数；cataloged = unlocked + locked。
type ColumnCounts struct {
	Uncataloged int `json:"uncataloged"`
	Unlocked    int `json:"unlocked"`
	Locked      int `json:"locked"`
	Cataloged   int `json:"cataloged"`
	Total       int `json:"total"`
}

// ItemCounts 是按件的计数（来源文件夹在这一组里的未删除条目）。
type ItemCounts struct {
	// Total 是已归档的件数；Locked 是其中已锁定的，Modifying 是修改中的（来源文件夹解除了封存）。
	Total     int `json:"total"`
	Locked    int `json:"locked"`
	Modifying int `json:"modifying"`
	// Exportable 是可导出的件数：和导出预检勾"只导出审核通过的"时的"就绪"相同；ExportablePages 是这些件的页数。
	Exportable      int `json:"exportable"`
	ExportablePages int `json:"exportable_pages"`
}

func (c *ItemCounts) addBucket(b *summaryItemBucket) {
	c.Total += b.Total
	c.Locked += b.Locked
	c.Modifying += b.Modifying
	c.Exportable += b.Exportable
	c.ExportablePages += b.ExportablePages
}

// SummaryCounts 是一组计数（一个文件夹整棵子树、直接放的文件或一个子文件夹的子树）。
type SummaryCounts struct {
	// Total 是文件数（页数）。
	Total   int          `json:"total"`
	Review  ReviewCounts `json:"review"`
	Catalog ColumnCounts `json:"catalog"`
	Items   ItemCounts   `json:"items"`
}

// add 记 n 个文件：审核状态 reviewStatus，所在条目的状态 itemStatus（未归档为 nil）。
func (c *SummaryCounts) add(reviewStatus string, itemStatus *string, n int) {
	c.Total += n
	c.Review.add(reviewStatus, n)
	c.Catalog.Total += n
	switch {
	case itemStatus == nil:
		c.Catalog.Uncataloged += n
		return
	case *itemStatus == statusLocked:
		c.Catalog.Locked += n
	default:
		c.Catalog.Unlocked += n
	}
	c.Catalog.Cataloged += n
}

// SummaryChild 是一个子文件夹（整棵子树的计数）。
type SummaryChild struct {
	FolderID   uuid.UUID `json:"folder_id"`
	FolderName string    `json:"folder_name"`
	// FolderCount 是它未删除的直接子文件夹数（和文件目录 children 的 folder_count 相同）。
	FolderCount int `json:"folder_count"`
	// Archive 是它自己的归档信息（是某个未删除条目的来源时，和文件目录 children 的 archive 相同；否则为 null）。
	Archive *FolderArchive `json:"archive"`
	Counts  SummaryCounts  `json:"counts"`
}

// Summary 是 `GET /api/projects/{pid}/catalog-summary?folder_id=`。
type Summary struct {
	FolderID          uuid.UUID  `json:"folder_id"`
	FolderName        string     `json:"folder_name"`
	ParentID          *uuid.UUID `json:"parent_id"`
	RequireLockedItem bool       `json:"require_locked_item"`
	// Counts 是这个文件夹整棵子树。
	Counts SummaryCounts `json:"counts"`
	// DirectCounts 是直接放在里面的文件（件：来源文件夹就是这个文件夹的）。
	DirectCounts SummaryCounts `json:"direct_counts"`
	// Children 是未删除的子文件夹（按 sort_no、名称排），各自整棵子树。
	Children []SummaryChild `json:"children"`
}

var errFolderNotFound = httpx.NotFound("文件夹")

// summaryTTL 是看板结果的缓存时间。
const summaryTTL = 5 * time.Second

// summaryKey 是看板缓存的键：根文件夹的 folder 是 uuid.Nil。
type summaryKey struct {
	project, folder uuid.UUID
	requireLocked   bool
	version         uint64
}

// Summary 是看板：folderID 为 nil 时是根文件夹（整个项目）；文件夹不在这个项目里或已删除 404。返回的结果是共用的，不能改。
func (s *Service) Summary(ctx context.Context, projectID uuid.UUID, folderID *uuid.UUID) (*Summary, error) {
	version := s.events.Version(projectID) // 先取版本再读库
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	key := summaryKey{project: projectID, requireLocked: st.requireLockedItem, version: version}
	if folderID != nil {
		key.folder = *folderID
	}
	return s.summaries.Get(ctx, key, func() (*Summary, error) { return s.summary(ctx, projectID, folderID, st) })
}

// summary 是不经缓存的看板。
func (s *Service) summary(ctx context.Context, projectID uuid.UUID, folderID *uuid.UUID, st *settings) (*Summary, error) {
	var out *Summary
	err := readSnapshot(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := summaryFolder(ctx, tx, projectID, folderID)
		if err != nil {
			return err
		}
		if f == nil {
			return errFolderNotFound
		}
		children, err := summaryChildren(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(children))
		for i, c := range children {
			ids[i] = c.ID
		}
		archives, err := FolderArchives(ctx, tx, ids)
		if err != nil {
			return err
		}
		out = &Summary{FolderID: f.ID, FolderName: f.Name, ParentID: f.ParentID, RequireLockedItem: st.requireLockedItem,
			Children: make([]SummaryChild, len(children))}
		byPath := make(map[string]*SummaryCounts, len(children))
		for i, c := range children {
			out.Children[i] = SummaryChild{FolderID: c.ID, FolderName: c.Name, FolderCount: c.FolderCount, Archive: archives[c.ID]}
			byPath[c.PathCode] = &out.Children[i].Counts
		}
		buckets, err := summaryBuckets(ctx, tx, projectID, f.PathCode)
		if err != nil {
			return err
		}
		for _, b := range buckets {
			out.Counts.add(b.ReviewStatus, b.ItemStatus, b.N)
			if b.Bucket == f.PathCode {
				out.DirectCounts.add(b.ReviewStatus, b.ItemStatus, b.N)
			} else if c := byPath[b.Bucket]; c != nil {
				c.add(b.ReviewStatus, b.ItemStatus, b.N)
			}
		}
		items, err := summaryItemBuckets(ctx, tx, projectID, f.PathCode, st.requireLockedItem)
		if err != nil {
			return err
		}
		for i := range items {
			b := &items[i]
			out.Counts.Items.addBucket(b)
			if b.Bucket == f.PathCode {
				out.DirectCounts.Items.addBucket(b)
			} else if c := byPath[b.Bucket]; c != nil {
				c.Items.addBucket(b)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// summaryFolderRow 是看板的文件夹。
type summaryFolderRow struct {
	ID       uuid.UUID
	ParentID *uuid.UUID
	Name     string
	PathCode string
	// FolderCount 是未删除的直接子文件夹数（只有 summaryChildren 填）。
	FolderCount int
}

// summaryFolder 读项目里未删除的文件夹（id 为 nil 时是根文件夹）；没有为 nil。跨模块的读（folders）。
func summaryFolder(ctx context.Context, q db.Querier, projectID uuid.UUID, id *uuid.UUID) (*summaryFolderRow, error) {
	var f summaryFolderRow
	var err error
	if id == nil {
		err = q.QueryRow(ctx, `SELECT id, parent_id, folder_name, path_code FROM folders
WHERE project_id = $1 AND parent_id IS NULL AND deleted_at IS NULL`, projectID).Scan(&f.ID, &f.ParentID, &f.Name, &f.PathCode)
	} else {
		err = q.QueryRow(ctx, `SELECT id, parent_id, folder_name, path_code FROM folders
WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`, *id, projectID).Scan(&f.ID, &f.ParentID, &f.Name, &f.PathCode)
	}
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// summaryChildren 是未删除的直接子文件夹和它们各自未删除的直接子文件夹数，按 sort_no、名称排（和 children 接口
// 一样）。跨模块的读（folders）。
func summaryChildren(ctx context.Context, q db.Querier, parentID uuid.UUID) ([]summaryFolderRow, error) {
	rows, err := q.Query(ctx, `SELECT f.id, f.parent_id, f.folder_name, f.path_code,
  (SELECT count(*) FROM folders k WHERE k.parent_id = f.id AND k.deleted_at IS NULL)
FROM folders f
WHERE f.parent_id = $1 AND f.deleted_at IS NULL ORDER BY f.sort_no, f.folder_name, f.id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []summaryFolderRow
	for rows.Next() {
		var f summaryFolderRow
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.PathCode, &f.FolderCount); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// summaryBucket 是一个分组的计数：Bucket 是文件所在文件夹编码的前 len(path)+6 个字符（等于 path 的是直接放在这个
// 文件夹里的，否则是某个直接子文件夹的编码）。
type summaryBucket struct {
	Bucket       string
	ReviewStatus string
	ItemStatus   *string
	N            int
}

// summaryBuckets 按（分组、审核状态、条目状态：draft、locked，未归档为 NULL）数 path 子树里未删除的文件。跨模块的读（folders、folder_files）。
// ff.project_id 让 PG 能用 ix_folder_files_review 只读这个项目的文件；af.project_id 在哈希连接时只装这个项目的挂接。
func summaryBuckets(ctx context.Context, q db.Querier, projectID uuid.UUID, path string) ([]summaryBucket, error) {
	rows, err := q.Query(ctx, `SELECT left(f.path_code, length($2) + 6), ff.review_status, a.status, count(*)
FROM folders f
JOIN folder_files ff ON ff.folder_id = f.id AND ff.project_id = $1 AND ff.deleted_at IS NULL
LEFT JOIN archive_item_files af ON af.file_id = ff.file_id AND af.project_id = $1
LEFT JOIN archive_items a ON a.id = af.archive_item_id
WHERE f.project_id = $1 AND f.path_code >= $2 AND f.path_code < $3 AND f.deleted_at IS NULL
GROUP BY 1, 2, 3`, projectID, path, subtreeUpper(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []summaryBucket
	for rows.Next() {
		var b summaryBucket
		if err := rows.Scan(&b.Bucket, &b.ReviewStatus, &b.ItemStatus, &b.N); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// summaryItemBucket 是一个分组（同 [summaryBucket]，按来源文件夹的编码）的件数。
type summaryItemBucket struct {
	Bucket          string
	Total           int
	Locked          int
	Modifying       int
	Exportable      int
	ExportablePages int
}

// summaryItemBuckets 按分组（来源文件夹编码的前 len(path)+6 个字符）数 path 子树里的未删除条目：来源文件夹在子树里、
// 未删除。跨模块的读（folders、folder_files）。
//
// "可导出"（ready）和导出预检勾"只导出审核通过的"时的"就绪"必须完全一致（export/precheck.go 的 notReadyReasons，
// requirements 5.11"预检"）：不在修改中（source_status <> modifying）、require_locked_item 时已锁定、档号不空
// （archive_no 不为 NULL 也不是空串）、≥ 1 页、全部页 approved。页和导出的一样（export/store.go 的 scopeItems）：
// 挂接行里文件（folder_files 未删除）和所在文件夹都未删除的。两边任何一边改了，另一边要跟着改
// （apitest 的 TestCatalogSummary 用预检的就绪件数、页数核对）。
func summaryItemBuckets(ctx context.Context, q db.Querier, projectID uuid.UUID, path string, requireLocked bool) ([]summaryItemBucket, error) {
	rows, err := q.Query(ctx, `WITH it AS (
  SELECT a.id, left(f.path_code, length($2) + 6) AS bucket, a.status, a.source_status, a.archive_no
  FROM archive_items a
  JOIN folders f ON f.id = a.source_folder_id AND f.deleted_at IS NULL
  WHERE a.project_id = $1 AND a.deleted_at IS NULL AND f.project_id = $1 AND f.path_code >= $2 AND f.path_code < $3
), pg AS (
  SELECT af.archive_item_id AS item_id, count(*) AS pages,
    count(*) FILTER (WHERE ff.review_status <> 'approved') AS unapproved
  FROM it
  JOIN archive_item_files af ON af.archive_item_id = it.id
  JOIN folder_files ff ON ff.file_id = af.file_id AND ff.deleted_at IS NULL
  JOIN folders pf ON pf.id = ff.folder_id AND pf.deleted_at IS NULL
  GROUP BY af.archive_item_id
), r AS (
  SELECT it.bucket, it.status, it.source_status, COALESCE(pg.pages, 0) AS pages,
    it.source_status <> 'modifying' AND (NOT $4 OR it.status = 'locked') AND it.archive_no IS NOT NULL
      AND it.archive_no <> '' AND COALESCE(pg.pages, 0) >= 1 AND COALESCE(pg.unapproved, 0) = 0 AS ready
  FROM it LEFT JOIN pg ON pg.item_id = it.id
)
SELECT bucket, count(*), count(*) FILTER (WHERE status = 'locked'), count(*) FILTER (WHERE source_status = 'modifying'),
  count(*) FILTER (WHERE ready), COALESCE(sum(pages) FILTER (WHERE ready), 0)
FROM r GROUP BY bucket`, projectID, path, subtreeUpper(path), requireLocked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []summaryItemBucket
	for rows.Next() {
		var b summaryItemBucket
		if err := rows.Scan(&b.Bucket, &b.Total, &b.Locked, &b.Modifying, &b.Exportable, &b.ExportablePages); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
