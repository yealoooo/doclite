// Package catalog 是归类（requirements 5.9、5.10）：归类树（节点）、档号和件号、档案条目（著录、状态、定稿）、
// 归类设置；挂接和页序（pages.go）、归类信息（placement.go）、项目看板（summary.go）。
//
// 加锁（requirements 3.6）：
//   - 归类树锁（project.LockCatalogTree）共享：建节点、改名节点，条目的增删改移和状态动作、批量定稿、挂接；
//     排他：移动或删除节点子树、改 category_code、category_type，按新模板重新生成档号；
//   - 然后锁节点行（新建子节点锁父节点；新建条目、分配或改件号锁所在节点，移动条目锁目标节点；移动节点锁自己、
//     原父节点、新父节点，按 id 升序），再锁条目行（按 id 升序），（共享锁下写档号时）拿档号的咨询锁
//     （numbering.go 的 lockArchiveNo）；
//   - 只有挂上、移出文件（PUT 页、带 file_ids 新建、删除有页的条目）在归类的锁和行之后再拿文件树锁（共享）和这些
//     文件的 files 行（见 pages.go）。审核不拿归类锁、不锁条目；拿着文件树锁的事务从不等归类的锁和行，归类也不等审核。
//
// 检查顺序：菜单 403 → 项目 404/403 → 参数 400 → 资源 404 → 状态 409（item_locked / invalid_state）→
// row_version 409 `conflict` → 著录 400 → 前置条件 409。
//
// 每个成功的写操作在同一个事务里写审计（archive_category、archive_item），提交后发 catalog.changed；没有实际变化
// 不写库、不审计、不发事件。
package catalog

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/archiveno"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
)

var (
	errCategoryNotFound = httpx.NotFound("归类节点")
	errItemNotFound     = httpx.NotFound("条目")
)

// Service 是归类的业务规则。
type Service struct {
	pool   *pgxpool.Pool
	events *events.Bus
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, bus *events.Bus) *Service {
	return &Service{pool: pool, events: bus}
}

// ProjectOfCategory 是未删除节点所在的项目；节点不存在或已删除 404（项目已删除由 auth.InProject 按 404 处理）。
func (s *Service) ProjectOfCategory(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	pid, err := categoryProject(ctx, s.pool, id)
	if err != nil {
		return uuid.Nil, err
	}
	if pid == uuid.Nil {
		return uuid.Nil, errCategoryNotFound
	}
	return pid, nil
}

// ProjectOfItem 是未删除条目所在的项目；条目不存在或已删除 404。
func (s *Service) ProjectOfItem(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	pid, err := itemProject(ctx, s.pool, id)
	if err != nil {
		return uuid.Nil, err
	}
	if pid == uuid.Nil {
		return uuid.Nil, errItemNotFound
	}
	return pid, nil
}

// normalizeCategoryName：去掉首尾空白后 1–100 个字符，不能有控制字符；同一层可以重名。
func normalizeCategoryName(v string) (string, error) {
	name := strings.TrimSpace(v)
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxCategoryNameChars {
		return "", httpx.Invalid("归类节点的名称不能为空，最长 %d 个字符", MaxCategoryNameChars)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", httpx.Invalid("归类节点的名称不能有控制字符")
	}
	return name, nil
}

// checkCategoryType：只能是 fonds、category、year、retention、org、custom（root 只在建项目时建）。
func checkCategoryType(v string) error {
	if v == string(archiveno.Root) {
		return httpx.Invalid("不能新建根节点，也不能把节点改成根节点（根节点建项目时就有了）")
	}
	for _, t := range archiveno.AssignableTypes {
		if string(t) == v {
			return nil
		}
	}
	return httpx.Invalid("归类节点的类型只能是 fonds、category、year、retention、org、custom")
}
