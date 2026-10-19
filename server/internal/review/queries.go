package review

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/httpx"
)

// 读接口（requirements 5.8"读接口"）：只含未删除的文件和文件夹；folder_id 是子树（不传是整个项目，不在这个项目里或
// 已删除 404）；列表按所在文件夹 path_code、sort_no、file_id 排，keyset 分页（limit 默认 500、1–2000，cursor 原样传回
// 上一页的 next_cursor）；计数不受分页影响，和列表在同一个只读快照里。列表不返回图片地址。

// 分页的上限。
const (
	DefaultLimit = 500
	MaxLimit     = 2000
)

// ListParams 是列表接口共同的参数（handler 解析好）。
type ListParams struct {
	FolderID *uuid.UUID
	Limit    int
	After    *keyset
}

var cursorPattern = regexp.MustCompile(`^((?:[0-9]{5}\.)+)_(-?[0-9]{1,10})_([0-9a-f-]{36})$`)

// ParseCursor 解析 cursor（`<path_code>_<sort_no>_<file_id>`）；空串是第一页。不对 400。
func ParseCursor(s string) (*keyset, error) {
	if s == "" {
		return nil, nil
	}
	m := cursorPattern.FindStringSubmatch(s)
	if m == nil {
		return nil, httpx.Invalid("cursor 不对：要原样传回上一页的 next_cursor")
	}
	n, err1 := strconv.Atoi(m[2])
	id, err2 := uuid.Parse(m[3])
	if err1 != nil || err2 != nil {
		return nil, httpx.Invalid("cursor 不对：要原样传回上一页的 next_cursor")
	}
	return &keyset{PathCode: m[1], SortNo: n, FileID: id}, nil
}

func (k keyset) String() string {
	return k.PathCode + "_" + strconv.Itoa(k.SortNo) + "_" + k.FileID.String()
}

// ParseLimit 解析 limit：空串是默认的 500，1–2000，不对 400。
func ParseLimit(s string) (int, error) {
	if s == "" {
		return DefaultLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxLimit {
		return 0, httpx.Invalid("limit 要是 1 到 %d 的整数", MaxLimit)
	}
	return n, nil
}

// ParseOptionalUUID 解析可选的 id 参数：空串当作没传；不是 UUID 400。
func ParseOptionalUUID(name, s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, httpx.Invalid("参数 %s 不是合法的 UUID", name)
	}
	return &id, nil
}

// scope 在只读事务里读范围文件夹（nil 是根文件夹）：不在这个项目里或已删除 404。
func scope(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, folderID *uuid.UUID) (*scopeFolder, error) {
	f, err := getScopeFolder(ctx, tx, projectID, folderID)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errFolderNotFound
	}
	return f, nil
}

// nextCursor：多取了一行时去掉它，返回这一页最后一行的 cursor；没有下一页为 nil。
func nextCursor[T any](items []T, limit int, key func(T) keyset) ([]T, *string) {
	if len(items) <= limit {
		return items, nil
	}
	items = items[:limit]
	c := key(items[limit-1]).String()
	return items, &c
}

// ---------- 我的审核 ----------

// MyReviewsQuery 是 `GET /api/me/reviews` 的参数。
type MyReviewsQuery struct {
	ProjectID uuid.UUID
	ListParams
	// State：all、open、approved、rejected。
	State  string
	LowDPI bool
}

// MyReviewCounts 是我的审核的计数（每个文件算一次；不受 state、low_dpi 影响，受 folder_id 影响）。
type MyReviewCounts struct {
	All      int `json:"all"`
	Open     int `json:"open"`
	Approved int `json:"approved"`
	Rejected int `json:"rejected"`
	// LowDPI：当前版本的 dpi 低于 200（读不出的不算），和其他几种交叉。
	LowDPI int `json:"low_dpi"`
}

func (c *MyReviewCounts) add(o MyReviewCounts) {
	c.All += o.All
	c.Open += o.Open
	c.Approved += o.Approved
	c.Rejected += o.Rejected
	c.LowDPI += o.LowDPI
}

// MyReviewFolder 是有我的审核的文件夹（文件直接所在的），按文件夹先序排。
type MyReviewFolder struct {
	FolderID   uuid.UUID      `json:"folder_id"`
	FolderName string         `json:"folder_name"`
	FolderPath string         `json:"folder_path"`
	Counts     MyReviewCounts `json:"counts"`
}

// OtherProjectOpen 是我能进的其他项目里的待审个数。
type OtherProjectOpen struct {
	ProjectID uuid.UUID `json:"project_id"`
	Status    string    `json:"status"`
	Open      int       `json:"open"`
}

// MyReviewItem 是我的审核的一行：文件和最近一次分给我、没有取消的那一行分配。
type MyReviewItem struct {
	AssignmentID uuid.UUID `json:"assignment_id"`
	FileID       uuid.UUID `json:"file_id"`
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FolderID     uuid.UUID `json:"folder_id"`
	FolderPath   string    `json:"folder_path"`
	FileName     string    `json:"file_name"`
	SortNo       int       `json:"sort_no"`
	ReviewRound  int       `json:"review_round"`
	// ReviewStatus 是文件现在的审核状态。
	ReviewStatus string    `json:"review_status"`
	VersionNo    int       `json:"version_no"`
	DPI          *int      `json:"dpi"`
	AssignedAt   time.Time `json:"assigned_at"`
	// Status 是那一行：open（待审）或 done（已审）；Result 是那一行的结果，open 时为 null。
	Status     string     `json:"status"`
	Result     *string    `json:"result"`
	ReviewedAt *time.Time `json:"reviewed_at"`
	// IsCurrent：这一行是这个文件上 id 最大的分配（不论是谁的）；false 时只是历史。
	IsCurrent bool `json:"is_current"`

	pathCode string
}

// MyReviews 是 `GET /api/me/reviews` 的响应。
type MyReviews struct {
	ProjectID         uuid.UUID          `json:"project_id"`
	LowDPIThreshold   int                `json:"low_dpi_threshold"`
	Counts            MyReviewCounts     `json:"counts"`
	Folders           []MyReviewFolder   `json:"folders"`
	OtherProjectsOpen int                `json:"other_projects_open"`
	OtherProjects     []OtherProjectOpen `json:"other_projects"`
	Items             []MyReviewItem     `json:"items"`
	NextCursor        *string            `json:"next_cursor"`
}

// MyReviewStates 是 state 的取值。
var MyReviewStates = []string{"all", statusOpen, resultApproved, resultRejected}

// MyReviews 是审核员的待办（项目授权已经查过）。
func (s *Service) MyReviews(ctx context.Context, userID uuid.UUID, q MyReviewsQuery) (*MyReviews, error) {
	out := &MyReviews{ProjectID: q.ProjectID, LowDPIThreshold: LowDPI, Folders: []MyReviewFolder{}, Items: []MyReviewItem{}}
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		sc, err := scope(ctx, tx, q.ProjectID, q.FolderID)
		if err != nil {
			return err
		}
		lo, hi := sc.subtreeRange()
		rows, err := myReviews(ctx, tx, userID, q.ProjectID, lo, hi, q.State, q.LowDPI, q.After, q.Limit+1)
		if err != nil {
			return err
		}
		var folderIDs []uuid.UUID
		for _, r := range rows {
			if r.Kind == 0 {
				folderIDs = append(folderIDs, r.FolderID)
				out.Counts.add(r.Counts)
				out.Folders = append(out.Folders, MyReviewFolder{FolderID: r.FolderID, Counts: r.Counts})
			} else {
				out.Items = append(out.Items, r.Item)
			}
		}
		paths, err := s.folders.Paths(ctx, tx, folderIDs)
		if err != nil {
			return err
		}
		names, err := folderNames(ctx, tx, folderIDs)
		if err != nil {
			return err
		}
		for i := range out.Folders {
			f := &out.Folders[i]
			f.FolderName, f.FolderPath = names[f.FolderID], paths[f.FolderID]
		}
		out.Items, out.NextCursor = nextCursor(out.Items, q.Limit, func(it MyReviewItem) keyset {
			return keyset{PathCode: it.pathCode, SortNo: it.SortNo, FileID: it.FileID}
		})
		for i := range out.Items {
			out.Items[i].FolderPath = paths[out.Items[i].FolderID]
		}
		if out.OtherProjects, err = otherProjectsOpen(ctx, tx, userID, q.ProjectID); err != nil {
			return err
		}
		for _, o := range out.OtherProjects {
			out.OtherProjectsOpen += o.Open
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------- 可以分配的人 ----------

// ProjectReviewer 是能分给的人（启用、有 review 菜单、能进这个项目）。
type ProjectReviewer struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	// Open 是他在这个项目里未删除文件上的 open 分配个数（待办）。
	Open int `json:"open"`
}

// ProjectReviewers 是 `GET /api/projects/{pid}/reviewers` 的响应。
type ProjectReviewers struct {
	ProjectID uuid.UUID         `json:"project_id"`
	Reviewers []ProjectReviewer `json:"reviewers"`
}

// Reviewers 是能分给的人和各自的待办数，按显示名、登录名排，不分页。
func (s *Service) Reviewers(ctx context.Context, projectID uuid.UUID) (*ProjectReviewers, error) {
	list, err := projectReviewers(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	return &ProjectReviewers{ProjectID: projectID, Reviewers: list}, nil
}

// ---------- 按文件夹的状态计数 ----------

// StatusCounts 是按审核状态的计数。
type StatusCounts struct {
	Unassigned int `json:"unassigned"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	Total      int `json:"total"`
}

func (c *StatusCounts) add(status string, n int) {
	switch status {
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

func (c *StatusCounts) merge(o *StatusCounts) {
	if o == nil {
		return
	}
	c.Unassigned += o.Unassigned
	c.Pending += o.Pending
	c.Approved += o.Approved
	c.Rejected += o.Rejected
	c.Total += o.Total
}

// SummaryChild 是一个子文件夹整棵子树的计数。
type SummaryChild struct {
	FolderID   uuid.UUID    `json:"folder_id"`
	FolderName string       `json:"folder_name"`
	Counts     StatusCounts `json:"counts"`
}

// Summary 是 `GET /api/projects/{pid}/review-summary` 的响应。
type Summary struct {
	FolderID   uuid.UUID  `json:"folder_id"`
	FolderName string     `json:"folder_name"`
	ParentID   *uuid.UUID `json:"parent_id"`
	// Counts 是这个文件夹整棵子树；DirectCounts 是直接放在里面的文件。
	Counts       StatusCounts   `json:"counts"`
	DirectCounts StatusCounts   `json:"direct_counts"`
	Children     []SummaryChild `json:"children"`
}

// summaryTTL 是状态计数的缓存时间：实时事件一来几个客户端同时重新取，同一个（项目、文件夹、项目的数据版本
// events.Bus.Version）在这段时间里只算一次，同时来的合并成一次（internal/platform/memo）。发了事件就换键。
const summaryTTL = 5 * time.Second

// summaryKey 是状态计数缓存的键：根文件夹的 folder 是 uuid.Nil。
type summaryKey struct {
	project, folder uuid.UUID
	version         uint64
}

// Summary 是一个文件夹（nil 是根文件夹）和它每个子文件夹的子树状态计数。返回的结果是共用的，不能改。
func (s *Service) Summary(ctx context.Context, projectID uuid.UUID, folderID *uuid.UUID) (*Summary, error) {
	key := summaryKey{project: projectID, version: s.events.Version(projectID)}
	if folderID != nil {
		key.folder = *folderID
	}
	return s.summaries.Get(ctx, key, func() (*Summary, error) { return s.summary(ctx, projectID, folderID) })
}

// summary 是不经缓存的状态计数。
func (s *Service) summary(ctx context.Context, projectID uuid.UUID, folderID *uuid.UUID) (*Summary, error) {
	var out *Summary
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		sc, err := scope(ctx, tx, projectID, folderID)
		if err != nil {
			return err
		}
		lo, hi := sc.subtreeRange()
		buckets, err := statusBuckets(ctx, tx, projectID, sc.PathCode, lo, hi)
		if err != nil {
			return err
		}
		subs, err := subfolders(ctx, tx, sc.ID)
		if err != nil {
			return err
		}
		out = &Summary{FolderID: sc.ID, FolderName: sc.FolderName, ParentID: sc.ParentID, Children: []SummaryChild{}}
		for _, c := range buckets {
			out.Counts.merge(c)
		}
		out.DirectCounts.merge(buckets[sc.PathCode])
		for _, f := range subs {
			child := SummaryChild{FolderID: f.ID, FolderName: f.FolderName}
			child.Counts.merge(buckets[f.PathCode])
			out.Children = append(out.Children, child)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------- open 分配 ----------

// AssignmentReviewer 是范围里有 open 分配的审核人。
type AssignmentReviewer struct {
	ReviewerID  uuid.UUID `json:"reviewer_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Open        int       `json:"open"`
	CanReview   bool      `json:"can_review"`
	// Problems：disabled、no_review_menu、no_project_access；为空即 can_review。
	Problems []string `json:"problems"`
}

// OpenAssignment 是一个 open 的分配。
type OpenAssignment struct {
	AssignmentID      uuid.UUID `json:"assignment_id"`
	FileID            uuid.UUID `json:"file_id"`
	FolderFileID      uuid.UUID `json:"folder_file_id"`
	FolderID          uuid.UUID `json:"folder_id"`
	FolderPath        string    `json:"folder_path"`
	FileName          string    `json:"file_name"`
	SortNo            int       `json:"sort_no"`
	ReviewRound       int       `json:"review_round"`
	ReviewerID        uuid.UUID `json:"reviewer_id"`
	ReviewerName      string    `json:"reviewer_name"`
	ReviewerCanReview bool      `json:"reviewer_can_review"`
	AssignedBy        uuid.UUID `json:"assigned_by"`
	AssignedAt        time.Time `json:"assigned_at"`

	pathCode string
}

// OpenAssignments 是 `GET /api/projects/{pid}/review-assignments` 的响应。
type OpenAssignments struct {
	// Total 是符合 folder_id、reviewer_id 的 open 分配总数。
	Total int `json:"total"`
	// Reviewers 不受 reviewer_id 筛选影响，按显示名排。
	Reviewers  []AssignmentReviewer `json:"reviewers"`
	Items      []OpenAssignment     `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

// OpenAssignments 是 open 的分配（改派用）。
func (s *Service) OpenAssignments(ctx context.Context, projectID uuid.UUID, p ListParams, reviewer *uuid.UUID) (*OpenAssignments, error) {
	out := &OpenAssignments{Reviewers: []AssignmentReviewer{}}
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		sc, err := scope(ctx, tx, projectID, p.FolderID)
		if err != nil {
			return err
		}
		lo, hi := sc.subtreeRange()
		counts, err := openByReviewer(ctx, tx, projectID, lo, hi)
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(counts))
		for id, n := range counts {
			ids = append(ids, id)
			if reviewer == nil || *reviewer == id {
				out.Total += n
			}
		}
		facts, err := reviewerFactsOf(ctx, tx, projectID, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			f := facts[id]
			pr := f.problems()
			out.Reviewers = append(out.Reviewers, AssignmentReviewer{ReviewerID: id, Username: f.Username,
				DisplayName: f.DisplayName, Open: counts[id], CanReview: len(pr) == 0, Problems: pr})
		}
		slices.SortFunc(out.Reviewers, func(a, b AssignmentReviewer) int {
			if c := strings.Compare(a.DisplayName, b.DisplayName); c != 0 {
				return c
			}
			if c := strings.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username)); c != 0 {
				return c
			}
			return strings.Compare(a.ReviewerID.String(), b.ReviewerID.String())
		})
		items, err := openAssignments(ctx, tx, projectID, lo, hi, reviewer, p.After, p.Limit+1)
		if err != nil {
			return err
		}
		items, out.NextCursor = nextCursor(items, p.Limit, func(a OpenAssignment) keyset {
			return keyset{PathCode: a.pathCode, SortNo: a.SortNo, FileID: a.FileID}
		})
		paths, err := s.folders.Paths(ctx, tx, folderIDsOf(items, func(a OpenAssignment) uuid.UUID { return a.FolderID }))
		if err != nil {
			return err
		}
		for i := range items {
			items[i].FolderPath = paths[items[i].FolderID]
			if f := facts[items[i].ReviewerID]; f != nil {
				items[i].ReviewerCanReview = f.canReview()
			}
		}
		out.Items = items
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func folderIDsOf[T any](items []T, id func(T) uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		out = append(out, id(it))
	}
	return dedup(out)
}

// ---------- 待重扫 ----------

// LastReject 是最近一次驳回。
type LastReject struct {
	AssignmentID uuid.UUID `json:"assignment_id"`
	ReviewerID   uuid.UUID `json:"reviewer_id"`
	ReviewerName string    `json:"reviewer_name"`
	Reasons      []string  `json:"reasons"`
	Comment      *string   `json:"comment"`
	// SnapshotID、VersionNo 是驳回时审的快照和它的版本号。
	SnapshotID uuid.UUID `json:"snapshot_id"`
	VersionNo  int       `json:"version_no"`
	ReviewedAt time.Time `json:"reviewed_at"`
}

// RescanItem 是一个已驳回、等重扫的文件。
type RescanItem struct {
	FileID       uuid.UUID `json:"file_id"`
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FolderID     uuid.UUID `json:"folder_id"`
	FolderName   string    `json:"folder_name"`
	FolderPath   string    `json:"folder_path"`
	// FolderRemark 是直接所在文件夹的备注（原件位置）。
	FolderRemark *string `json:"folder_remark"`
	FileName     string  `json:"file_name"`
	SortNo       int     `json:"sort_no"`
	ReviewRound  int     `json:"review_round"`
	VersionNo    int     `json:"version_no"`
	// LastReject：找不到驳回记录（数据异常）时为 null。
	LastReject *LastReject `json:"last_reject"`

	pathCode string
}

// RescanList 是 `GET /api/projects/{pid}/rescans` 的响应。
type RescanList struct {
	Total      int          `json:"total"`
	Items      []RescanItem `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// Rescans 是已驳回、等重扫的文件。
func (s *Service) Rescans(ctx context.Context, projectID uuid.UUID, p ListParams) (*RescanList, error) {
	out := &RescanList{}
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		sc, err := scope(ctx, tx, projectID, p.FolderID)
		if err != nil {
			return err
		}
		lo, hi := sc.subtreeRange()
		if out.Total, err = rejectedCount(ctx, tx, projectID, lo, hi); err != nil {
			return err
		}
		items, err := rescanItems(ctx, tx, projectID, lo, hi, p.After, p.Limit+1)
		if err != nil {
			return err
		}
		items, out.NextCursor = nextCursor(items, p.Limit, func(it RescanItem) keyset {
			return keyset{PathCode: it.pathCode, SortNo: it.SortNo, FileID: it.FileID}
		})
		paths, err := s.folders.Paths(ctx, tx, folderIDsOf(items, func(it RescanItem) uuid.UUID { return it.FolderID }))
		if err != nil {
			return err
		}
		for i := range items {
			items[i].FolderPath = paths[items[i].FolderID]
		}
		out.Items = items
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------- 一个文件的全部分配 ----------

// FileReviewRow 是一个文件的一行分配（含已取消的）。
type FileReviewRow struct {
	AssignmentID   uuid.UUID `json:"assignment_id"`
	ReviewerID     uuid.UUID `json:"reviewer_id"`
	ReviewerName   string    `json:"reviewer_name"`
	AssignedBy     uuid.UUID `json:"assigned_by"`
	AssignedByName string    `json:"assigned_by_name"`
	AssignedAt     time.Time `json:"assigned_at"`
	// Status：open、done、cancelled。
	Status     string     `json:"status"`
	Result     *string    `json:"result"`
	SnapshotID *uuid.UUID `json:"snapshot_id"`
	// VersionNo 是所审快照的版本号；没审的为 null。
	VersionNo     *int       `json:"version_no"`
	RejectReasons []string   `json:"reject_reasons"`
	Comment       *string    `json:"comment"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
}

// FileReviews 是 `GET /api/files/{fid}/reviews` 的响应。
type FileReviews struct {
	FileID       uuid.UUID       `json:"file_id"`
	ProjectID    uuid.UUID       `json:"project_id"`
	FolderID     uuid.UUID       `json:"folder_id"`
	ReviewStatus string          `json:"review_status"`
	ReviewRound  int             `json:"review_round"`
	VersionNo    int             `json:"version_no"`
	Reviews      []FileReviewRow `json:"reviews"`
}

// FileReviews 是一个文件的全部分配和结果（按 id 升序，即分配的先后）。
func (s *Service) FileReviews(ctx context.Context, projectID, fileID uuid.UUID) (*FileReviews, error) {
	var out *FileReviews
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		list, err := rereadFiles(ctx, tx, projectID, []uuid.UUID{fileID})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return errFileNotFound
		}
		f := list[0]
		rows, err := fileReviewRows(ctx, tx, fileID)
		if err != nil {
			return err
		}
		out = &FileReviews{FileID: fileID, ProjectID: projectID, FolderID: f.FolderID, ReviewStatus: f.Status,
			ReviewRound: f.Round, VersionNo: f.VersionNo, Reviews: rows}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
