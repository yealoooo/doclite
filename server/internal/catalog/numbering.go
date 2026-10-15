package catalog

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/platform/db"
	"doclite/shared/archiveno"
)

// 档号和件号（requirements 5.9"档号和件号"，用户决定 Q2；撞号和分档是总控定的，见 requirements 第 10 节）：
//   - 档号按项目现在的模板和条目所在节点的祖先链生成（archiveno），缺级留空；
//   - **撞号**：生成的档号已经被项目里别的未删除条目占着时，自动生成和重算都把档号留空、不报错；只有显式改件号
//     （PATCH 带 item_no）时 409 `conflict`；
//   - **批量重算**（[renumberSubtree]：改节点编码、类型、移动节点、按新模板重新生成）：范围外的和已定稿的先占着
//     现在的号；范围里的分档先到先得——① 有号、号不变 ② 有号、要换号 ③ 原来没号，档内按 id 升序；后到的撞号留空。
//     结果是确定的，重复执行不变；
//   - 唯一索引 ux_archive_items_no 不能延迟检查：写的时候先把"旧号是别人新号"的置空，再写新号；
//   - 共享归类树锁下写档号（新建、移动条目、改件号）先拿这个档号的咨询锁（[lockArchiveNo]），锁内查占用：两个事务
//     同时生成同一个档号时后到的等前一个提交，再查就能看到，不会撞唯一索引。批量重算拿归类树排他锁，不用它。

// lockClassArchiveNo 是档号咨询锁的类别（6000 迁移和测试模板库、6001 文件树、6002 归类树、6003 内容锁、
// 6004 任务领取）。它在归类的行锁之后、文件树锁之前拿。带页新建时，拿到 6005 之后还会拿文件树锁和 files 行；
// 拿着文件树锁或 files 行的事务不能再拿 6005。
const lockClassArchiveNo = 6005

// lockArchiveNo 在事务里拿（项目, 档号）的咨询锁，到事务结束为止。不同档号折叠到同一个键只是多等一会儿。
func lockArchiveNo(ctx context.Context, tx db.Querier, projectID uuid.UUID, archiveNo string) error {
	h := fnv.New32a()
	h.Write(projectID[:])
	h.Write([]byte(archiveNo))
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`, lockClassArchiveNo, int32(h.Sum32()))
	return err
}

// chainNode 是祖先链上的一个节点（从根往下）。
type chainNode struct {
	PathCode string
	Type     string
	Code     string
	Name     string
}

// chain 是一个节点的祖先链（含它自己），从根往下。
type chain []chainNode

func (c chain) nodes() []archiveno.Node {
	out := make([]archiveno.Node, len(c))
	for i, n := range c {
		out[i] = archiveno.Node{Type: archiveno.CategoryType(n.Type), Code: n.Code}
	}
	return out
}

// path 是从根起用 `/` 连接的名称。
func (c chain) path() string {
	parts := make([]string, len(c))
	for i, n := range c {
		parts[i] = n.Name
	}
	return strings.Join(parts, "/")
}

// pathPrefixes 是 path_code 的各级前缀（从根到自己）。
func pathPrefixes(path string) []string {
	var out []string
	for i := 6; i <= len(path); i += 6 {
		out = append(out, path[:i])
	}
	return out
}

// subtreeUpper 是子树区间的上界：前缀末尾的点换成 `/`（path_code >= path AND path_code < upper）。
func subtreeUpper(path string) string { return path[:len(path)-1] + "/" }

// render 按模板生成档号；缺级时返回空串和缺的类型。tmpl 不能为 nil。
func render(tmpl *archiveno.Template, c chain, itemNo int) (string, []archiveno.CategoryType) {
	return tmpl.Render(c.nodes(), itemNo)
}

func missingText(missing []archiveno.CategoryType) string {
	labels := make([]string, len(missing))
	for i, t := range missing {
		labels[i] = t.Label()
	}
	return strings.Join(labels, "、")
}

// ---------------------------------------------------------------------------
// 分档先到先得（纯函数）
// ---------------------------------------------------------------------------

// candidate 是批量重算里的一个条目。
type candidate struct {
	ID        uuid.UUID
	Current   *string // 现在的档号
	Generated string  // 按现在的模板生成的档号；缺级为空串
}

// assignment 是一个条目的分配结果。
type assignment struct {
	ID        uuid.UUID
	Current   *string
	New       *string
	Generated string
	// Holder 是撞号时占着 Generated 的条目（New 为 nil）。
	Holder uuid.UUID
}

func (a assignment) changed() bool { return !equalStr(a.Current, a.New) }

// conflicted：生成得出、但被别的条目占着而留空。
func (a assignment) conflicted() bool { return a.Generated != "" && a.New == nil }

func equalStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// allocate 给候选条目分配档号。taken 是范围外的、已定稿的条目占着的号（档号 → 条目），不会被改动。
// 分档：① 有号、号不变 ② 有号、要换号 ③ 原来没号；档内按 id 升序；生成的号已经被占（范围外，或者前面的条目
// 刚拿到）就留空。返回值按 id 升序。
func allocate(cands []candidate, taken map[string]uuid.UUID) []assignment {
	tier := func(c candidate) int {
		switch {
		case c.Current != nil && *c.Current == c.Generated:
			return 0
		case c.Current != nil:
			return 1
		}
		return 2
	}
	order := slices.Clone(cands)
	slices.SortFunc(order, func(a, b candidate) int {
		if ta, tb := tier(a), tier(b); ta != tb {
			return ta - tb
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	held := make(map[string]uuid.UUID, len(taken)+len(cands))
	for k, v := range taken {
		held[k] = v
	}
	out := make([]assignment, 0, len(cands))
	for _, c := range order {
		a := assignment{ID: c.ID, Current: c.Current, Generated: c.Generated}
		if c.Generated != "" {
			if holder, ok := held[c.Generated]; ok {
				a.Holder = holder
			} else {
				no := c.Generated
				a.New = &no
				held[no] = c.ID
			}
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b assignment) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	return out
}

// ---------------------------------------------------------------------------
// 批量重算（拿着归类树排他锁调用）
// ---------------------------------------------------------------------------

// renumberResult 是一次批量重算的结果。
type renumberResult struct {
	// Changed 是档号变了的条目（含变成空的）。
	Changed []uuid.UUID
	// Conflicts 是范围里因为撞号档号留空的未定稿条目数（这次变成空的和原来就空、这次还是空的都算）。
	Conflicts int
	// SkippedLocked 是范围里已定稿、没有动的条目数。
	SkippedLocked int
}

// renumberSubtree 按项目现在的模板重算 path 子树里未删除、未定稿条目的档号（path 是子树根现在的 path_code）。
// 档号变了的 row_version + 1，各写一条 archive_item / renumber 审计（detail 带 cause，撞号的带 conflict）。
// 模板解析不了而有要重算的条目时 409 `invalid_state`（what 是给人看的动作，例如"移动节点"）。
func renumberSubtree(ctx context.Context, tx pgx.Tx, projectID, operatorID uuid.UUID, path string, st *settings,
	cause map[string]any, what string) (renumberResult, error) {
	var res renumberResult
	rows, locked, err := subtreeItemsForRenumber(ctx, tx, projectID, path)
	if err != nil {
		return res, err
	}
	res.SkippedLocked = locked
	if len(rows) == 0 {
		return res, nil
	}
	if st.tmpl == nil {
		return res, st.templateInvalid(what)
	}
	nodes, err := chainNodesForSubtree(ctx, tx, projectID, path)
	if err != nil {
		return res, err
	}
	chains := map[string]chain{}
	chainFor := func(p string) chain {
		if c, ok := chains[p]; ok {
			return c
		}
		var c chain
		for _, pre := range pathPrefixes(p) {
			if n, ok := nodes[pre]; ok {
				c = append(c, n)
			}
		}
		chains[p] = c
		return c
	}
	cands := make([]candidate, len(rows))
	own := map[string]bool{} // 候选自己现在持有的号（唯一索引保证别人不会占着）
	for i, r := range rows {
		no, _ := render(st.tmpl, chainFor(r.CategoryPath), r.ItemNo)
		cands[i] = candidate{ID: r.ID, Current: r.ArchiveNo, Generated: no}
		if r.ArchiveNo != nil {
			own[*r.ArchiveNo] = true
		}
	}
	var probe []string
	seen := map[string]bool{}
	for _, c := range cands {
		if c.Generated != "" && !own[c.Generated] && !seen[c.Generated] {
			seen[c.Generated] = true
			probe = append(probe, c.Generated)
		}
	}
	taken, err := archiveNoHolders(ctx, tx, projectID, probe)
	if err != nil {
		return res, err
	}
	result := allocate(cands, taken)

	newNos := map[string]bool{}
	var changed []assignment
	for _, a := range result {
		if a.conflicted() {
			res.Conflicts++
		}
		if a.changed() {
			changed = append(changed, a)
			if a.New != nil {
				newNos[*a.New] = true
			}
		}
	}
	if len(changed) == 0 {
		return res, nil
	}
	// 先把"旧号正好是别人的新号"的置空（互换、轮换时才有），再写新号。
	var reused []uuid.UUID
	ids := make([]uuid.UUID, len(changed))
	nos := make([]*string, len(changed))
	entries := make([]auditEntry, len(changed))
	for i, a := range changed {
		if a.Current != nil && newNos[*a.Current] {
			reused = append(reused, a.ID)
		}
		ids[i], nos[i] = a.ID, a.New
		detail := map[string]any{
			"before": map[string]any{"archive_no": a.Current},
			"after":  map[string]any{"archive_no": a.New},
			"cause":  cause,
		}
		if a.conflicted() {
			detail["conflict"] = map[string]any{"archive_no": a.Generated, "item_id": a.Holder}
		}
		entries[i] = auditEntry{TargetType: audit.TargetArchiveItem, TargetID: a.ID, Action: "renumber", Detail: detail}
		res.Changed = append(res.Changed, a.ID)
	}
	if err := writeArchiveNos(ctx, tx, operatorID, reused, ids, nos); err != nil {
		return res, err
	}
	if err := insertAudits(ctx, tx, projectID, operatorID, entries); err != nil {
		return res, err
	}
	return res, nil
}

// ArchiveNoReason 是档号为空的原因（详情接口读的时候算，不存）。
type ArchiveNoReason struct {
	// Code：incomplete 缺级、conflict 撞号、not_generated 生成得出但还没写进来、template_invalid 模板解析不了。
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Missing []string `json:"missing,omitempty"`
	// ArchiveNo：conflict、not_generated 时按现在的模板生成得出的档号。
	ArchiveNo          *string    `json:"archive_no,omitempty"`
	ConflictItemID     *uuid.UUID `json:"conflict_item_id,omitempty"`
	ConflictCategoryID *uuid.UUID `json:"conflict_category_id,omitempty"`
	ConflictItemNo     *int       `json:"conflict_item_no,omitempty"`
}

// emptyReason 算档号为空的原因。
func emptyReason(ctx context.Context, q db.Querier, st *settings, projectID, itemID uuid.UUID, c chain, itemNo int) (*ArchiveNoReason, error) {
	if st.tmpl == nil {
		return &ArchiveNoReason{Code: "template_invalid", Message: "项目的档号模板不对：" + strings.TrimPrefix(st.templateErr, "档号模板不对：")}, nil
	}
	no, missing := render(st.tmpl, c, itemNo)
	if no == "" {
		levels := make([]string, len(missing))
		for i, t := range missing {
			levels[i] = string(t)
		}
		return &ArchiveNoReason{Code: "incomplete", Missing: levels,
			Message: "所在归类节点的上级缺档号模板用到的：" + missingText(missing)}, nil
	}
	h, err := archiveNoHolder(ctx, q, projectID, no, itemID)
	if err != nil {
		return nil, err
	}
	if h != nil {
		return &ArchiveNoReason{Code: "conflict", ArchiveNo: &no, ConflictItemID: &h.ID, ConflictCategoryID: &h.CategoryID,
			ConflictItemNo: &h.ItemNo,
			Message:        fmt.Sprintf("按模板生成的档号“%s”已经被另一个条目（第 %d 件）占用；改件号或调整归类节点、档号模板后才有档号", no, h.ItemNo)}, nil
	}
	return &ArchiveNoReason{Code: "not_generated", ArchiveNo: &no,
		Message: fmt.Sprintf("按现在的模板应该是“%s”，还没生成；用“按新模板重新生成档号”补上", no)}, nil
}
