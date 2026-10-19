package catalog

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/xslices"
)

// 归类信息（只读，requirements 5.9"归类信息"，用户决定 Q10）：一个文件挂在哪个条目。能进项目就能看。
//   - "已归类" = archive_item_files 里有这个文件的行（和文件目录的 409 file_cataloged 同一个定义，不看条目的
//     deleted_at；不变量：已删除的条目没有挂接行，删除条目时一并移出）；
//   - 只含未删除的文件（所在文件夹也未删除）；不加锁，一个接口的查询在同一个只读快照里。

// placementBase 是"挂在哪"的公共字段（placements、409 file_cataloged 的 details.items）。
type placementBase struct {
	ItemID uuid.UUID `json:"item_id"`
	ItemNo int       `json:"item_no"`
	// ArchiveNo 为空（缺级、撞号）时为 null，界面显示"第 n 件"。
	ArchiveNo  *string   `json:"archive_no"`
	Title      string    `json:"title"`
	PageNo     int       `json:"page_no"`
	ItemStatus string    `json:"item_status"`
	CategoryID uuid.UUID `json:"category_id"`
	// CategoryNames 是条目所在节点的名称链，从根节点的下一级起；条目直接在根节点下时为空数组。
	CategoryNames []string `json:"category_names"`
}

func (r *placementRow) base(names []string) placementBase {
	if names == nil {
		names = []string{}
	}
	return placementBase{ItemID: r.ItemID, ItemNo: r.ItemNo, ArchiveNo: r.ArchiveNo, Title: r.Title, PageNo: r.PageNo,
		ItemStatus: r.ItemStatus, CategoryID: r.CategoryID, CategoryNames: names}
}

func categoryIDsOf(rows []*placementRow) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, r := range rows {
		if r != nil && !seen[r.CategoryID] {
			seen[r.CategoryID] = true
			out = append(out, r.CategoryID)
		}
	}
	return out
}

// Placement 是一个文件挂在哪个条目。
type Placement struct {
	placementBase
	// CategoryPath 是 category_names 用 `/` 连接，只作显示用（名称里可能本身有 `/`，按层级处理要用 category_names）。
	CategoryPath string `json:"category_path"`
}

// FilePlacement 是 placements 的一项；没挂接时 placement 为 null。
type FilePlacement struct {
	FileID    uuid.UUID  `json:"file_id"`
	Placement *Placement `json:"placement"`
}

// Placements 是 `POST /api/projects/{pid}/catalog-placements` 的响应。
type Placements struct {
	// Items 按请求里的顺序（去重），只有未删除、在这个项目里的文件。
	Items []FilePlacement `json:"items"`
	// Missing 是不存在、已删除（含所在文件夹）、不在这个项目里的，按请求里的顺序，不报错。
	Missing []uuid.UUID `json:"missing"`
}

// placements 在 q 上读这些文件（已去重）挂在哪。
func placements(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) (*Placements, error) {
	out := &Placements{Items: []FilePlacement{}, Missing: []uuid.UUID{}}
	rows, err := liveProjectPlacements(ctx, q, projectID, ids)
	if err != nil {
		return nil, err
	}
	list := make([]*placementRow, 0, len(rows))
	for _, r := range rows {
		list = append(list, r)
	}
	names, err := CategoryNames(ctx, q, categoryIDsOf(list))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		r, ok := rows[id]
		if !ok {
			out.Missing = append(out.Missing, id)
			continue
		}
		fp := FilePlacement{FileID: id}
		if r != nil {
			n := names[r.CategoryID]
			fp.Placement = &Placement{placementBase: r.base(n), CategoryPath: strings.Join(n, "/")}
		}
		out.Items = append(out.Items, fp)
	}
	return out, nil
}

// Placements 是一批文件（0–500 个）各挂在哪个条目。
func (s *Service) Placements(ctx context.Context, projectID uuid.UUID, fileIDs []uuid.UUID) (*Placements, error) {
	if len(fileIDs) > httpx.MaxBatchIDs {
		return nil, httpx.Invalid("一次最多查 %d 个文件，这次有 %d 个", httpx.MaxBatchIDs, len(fileIDs))
	}
	ids := xslices.Unique(fileIDs)
	var out *Placements
	err := db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = placements(ctx, tx, projectID, ids)
		return err
	})
	return out, err
}

// FileCatalog 是 `GET /api/files/{fid}/catalog` 的响应。
type FileCatalog struct {
	FileID    uuid.UUID  `json:"file_id"`
	ProjectID uuid.UUID  `json:"project_id"`
	Placement *Placement `json:"placement"`
}

var errFileNotFound = httpx.NotFound("文件")

// ProjectOfFile 是未删除文件（所在文件夹也未删除）所在的项目；没有 404（项目已删除由 auth.InProject 按 404 处理）。
func (s *Service) ProjectOfFile(ctx context.Context, fileID uuid.UUID) (uuid.UUID, error) {
	pid, err := liveFileProject(ctx, s.pool, fileID)
	if err != nil {
		return uuid.Nil, err
	}
	if pid == uuid.Nil {
		return uuid.Nil, errFileNotFound
	}
	return pid, nil
}

// FileCatalog 是一个文件挂在哪个条目（没挂接时 placement 为 null）；文件已删除 404。
func (s *Service) FileCatalog(ctx context.Context, projectID, fileID uuid.UUID) (*FileCatalog, error) {
	var out *FileCatalog
	err := db.ReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := placements(ctx, tx, projectID, []uuid.UUID{fileID})
		if err != nil {
			return err
		}
		if len(p.Items) == 0 {
			return errFileNotFound
		}
		out = &FileCatalog{FileID: fileID, ProjectID: projectID, Placement: p.Items[0].Placement}
		return nil
	})
	return out, err
}
