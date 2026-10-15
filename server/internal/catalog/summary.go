package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 项目看板（requirements 5.10，用户决定 Q6）：一个文件夹整棵子树、直接放在里面的、每个子文件夹各自子树的
// "审核状态 × 归类状态"矩阵，加上可导出数。每次实时聚合，不建汇总表；只读，在一个只读快照里。
//   - 审核状态：folder_files.review_status（unassigned、pending、approved、rejected）；
//   - 归类状态：未归类（没有挂接行）、已归类未定稿（条目 draft、done）、已定稿（条目 locked）；
//   - 可导出：export.require_locked_item 为 true 时是已定稿的，false 时是全部已归类的，按审核状态分开（"只导出审核
//     通过的"是导出时勾选的，看板的主 KPI 取 exportable.approved）；
//   - 只含未删除的文件（所在文件夹也未删除）。
//
// KPI 由界面从 counts 取：已上传 total、通过 review.approved、待重扫 review.rejected、已归类 catalog.cataloged、
// 已定稿 catalog.locked、可导出 exportable.approved（主）/ exportable.total（次）。

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
	}
	c.Total += n
}

// RowCounts 是矩阵的一行（一种审核状态）按归类状态的文件数。
type RowCounts struct {
	Uncataloged int `json:"uncataloged"`
	Unlocked    int `json:"unlocked"`
	Locked      int `json:"locked"`
	Total       int `json:"total"`
}

// Matrix 是 4 种审核状态 × 3 种归类状态。
type Matrix struct {
	Unassigned RowCounts `json:"unassigned"`
	Pending    RowCounts `json:"pending"`
	Approved   RowCounts `json:"approved"`
	Rejected   RowCounts `json:"rejected"`
}

func (m *Matrix) row(reviewStatus string) *RowCounts {
	switch reviewStatus {
	case "unassigned":
		return &m.Unassigned
	case "pending":
		return &m.Pending
	case "approved":
		return &m.Approved
	case "rejected":
		return &m.Rejected
	}
	return nil
}

// ColumnCounts 是每列（归类状态）的合计；cataloged = unlocked + locked。
type ColumnCounts struct {
	Uncataloged int `json:"uncataloged"`
	Unlocked    int `json:"unlocked"`
	Locked      int `json:"locked"`
	Cataloged   int `json:"cataloged"`
	Total       int `json:"total"`
}

// SummaryCounts 是一组计数（一个文件夹整棵子树、直接放的文件或一个子文件夹的子树）。
type SummaryCounts struct {
	Total   int          `json:"total"`
	Review  ReviewCounts `json:"review"`
	Catalog ColumnCounts `json:"catalog"`
	Matrix  Matrix       `json:"matrix"`
	// Exportable 是可导出的文件数（按 export.require_locked_item），按审核状态分开。
	Exportable ReviewCounts `json:"exportable"`
}

// add 记 n 个文件：审核状态 reviewStatus，所在条目的状态 itemStatus（未归类为 nil）。
func (c *SummaryCounts) add(reviewStatus string, itemStatus *string, n int, requireLocked bool) {
	row := c.Matrix.row(reviewStatus)
	if row == nil {
		return // 不认识的审核状态（库里有 CHECK，走不到）
	}
	c.Total += n
	c.Review.add(reviewStatus, n)
	row.Total += n
	c.Catalog.Total += n
	switch {
	case itemStatus == nil:
		row.Uncataloged += n
		c.Catalog.Uncataloged += n
		return
	case *itemStatus == statusLocked:
		row.Locked += n
		c.Catalog.Locked += n
	default:
		row.Unlocked += n
		c.Catalog.Unlocked += n
	}
	c.Catalog.Cataloged += n
	if !requireLocked || *itemStatus == statusLocked {
		c.Exportable.add(reviewStatus, n)
	}
}

// SummaryChild 是一个子文件夹（整棵子树的计数）。
type SummaryChild struct {
	FolderID   uuid.UUID     `json:"folder_id"`
	FolderName string        `json:"folder_name"`
	Counts     SummaryCounts `json:"counts"`
}

// Summary 是 `GET /api/projects/{pid}/catalog-summary?folder_id=`。
type Summary struct {
	FolderID          uuid.UUID  `json:"folder_id"`
	FolderName        string     `json:"folder_name"`
	ParentID          *uuid.UUID `json:"parent_id"`
	RequireLockedItem bool       `json:"require_locked_item"`
	// Counts 是这个文件夹整棵子树。
	Counts SummaryCounts `json:"counts"`
	// DirectCounts 是直接放在里面的文件。
	DirectCounts SummaryCounts `json:"direct_counts"`
	// Children 是未删除的子文件夹（按 sort_no、名称排），各自整棵子树。
	Children []SummaryChild `json:"children"`
}

var errFolderNotFound = httpx.NotFound("文件夹")

// Summary 是看板：folderID 为 nil 时是根文件夹（整个项目）；文件夹不在这个项目里或已删除 404。
func (s *Service) Summary(ctx context.Context, projectID uuid.UUID, folderID *uuid.UUID) (*Summary, error) {
	st, err := loadSettings(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	var out *Summary
	err = readSnapshot(ctx, s.pool, func(tx pgx.Tx) error {
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
		out = &Summary{FolderID: f.ID, FolderName: f.Name, ParentID: f.ParentID, RequireLockedItem: st.requireLockedItem,
			Children: make([]SummaryChild, len(children))}
		byPath := make(map[string]*SummaryCounts, len(children))
		for i, c := range children {
			out.Children[i] = SummaryChild{FolderID: c.ID, FolderName: c.Name}
			byPath[c.PathCode] = &out.Children[i].Counts
		}
		buckets, err := summaryBuckets(ctx, tx, projectID, f.PathCode)
		if err != nil {
			return err
		}
		for _, b := range buckets {
			out.Counts.add(b.ReviewStatus, b.ItemStatus, b.N, st.requireLockedItem)
			if b.Bucket == f.PathCode {
				out.DirectCounts.add(b.ReviewStatus, b.ItemStatus, b.N, st.requireLockedItem)
			} else if c := byPath[b.Bucket]; c != nil {
				c.add(b.ReviewStatus, b.ItemStatus, b.N, st.requireLockedItem)
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

// summaryChildren 是未删除的直接子文件夹，按 sort_no、名称排（和 children 接口一样）。跨模块的读（folders）。
func summaryChildren(ctx context.Context, q db.Querier, parentID uuid.UUID) ([]summaryFolderRow, error) {
	rows, err := q.Query(ctx, `SELECT id, parent_id, folder_name, path_code FROM folders
WHERE parent_id = $1 AND deleted_at IS NULL ORDER BY sort_no, folder_name, id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []summaryFolderRow
	for rows.Next() {
		var f summaryFolderRow
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.PathCode); err != nil {
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

// summaryBuckets 按（分组、审核状态、条目状态）数 path 子树里未删除的文件。跨模块的读（folders、folder_files）。
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
