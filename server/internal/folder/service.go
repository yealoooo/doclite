// Package folder 是文件目录（requirements 5.5）：文件夹树（parent_id + path_code）和整理（新建、改名、备注、移动、删除
// 文件夹；文件的改名、移动、复制、删除和批量移动、删除）。
//
// 表：folders、folder_files（界面上的"文件"：位置、名字、sort_no、审核状态）。文件目录只管位置和名字（硬性规则 3）：
// 移动、改名只改这两张表，对象、快照、审核、归类不动。
//
// 规则：
//   - 名称 1–255 字，不含 \ / : * ? " < > |、控制字符，首尾不空白、不以点结尾、不是 Windows 保留名（platform/names）；
//     老名字移动、复制时不重校验；
//   - 重名（同文件夹、不分大小写、不算已删除）：明确改名撞名 409 conflict；上传、移动、复制文件撞名自动改为
//     "名字 (n).扩展名"；新建、移动文件夹撞名 409（新建带 if_exists: "return" 时返回已有的）；
//   - 文件的 sort_no = 目标文件夹未删除文件的 max + 1（锁住文件夹行之后取）；
//   - 根文件夹不能改名、移动、删除（409 root_folder_locked），可改备注；移进自己的子树 409 tree_cycle；
//     目标不在这个项目或已删除 404；
//   - 删除是软删除：删文件夹连子树；已归类的文件不能删（409 file_cataloged）；取消进行中的分配；每个文件记 delete；
//   - 加锁（requirements 3.6）：文件树锁（建、改名文件夹、文件的操作共享；移动文件夹、删子树排他）→ files 行（id 升序）
//     → folders 行（id 升序）→ 存储配置行；
//   - 文件夹的变更写审计（target_type folder），文件的操作写 file_logs；没有实际变化不写库、不审计、不发事件；
//   - 提交后发 folders.changed、files.changed。
//
// 给上传（internal/file）用的 ClaimSlot、AddFile、PreviewName、Path、ProjectOf 实现 file.Folders。
// 给审核（internal/review）用的 SetReviewStatus、SetReviewState 改 folder_files 的审核状态和轮次（review_state.go）。
package folder

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/auth"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/project"
	"doclite/server/internal/storage"
)

const maxRemarkRunes = 500

var (
	errFolderNotFound = httpx.NotFound("文件夹")
	errTargetNotFound = httpx.NotFound("目标文件夹")
	errFileNotFound   = httpx.NotFound("文件")
)

func rootLocked() error {
	return httpx.Conflict("root_folder_locked", "根文件夹不能改名、移动或删除（名称跟着项目名称走），备注可以改")
}

// Service 是文件目录的业务规则。
type Service struct {
	pool    *pgxpool.Pool
	events  *events.Bus
	auth    *auth.Service
	storage *storage.Service
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, bus *events.Bus, authSvc *auth.Service, storageSvc *storage.Service) *Service {
	return &Service{pool: pool, events: bus, auth: authSvc, storage: storageSvc}
}

var _ file.Folders = (*Service)(nil)

// ProjectOf 是文件夹所在的项目；文件夹不存在或已删除 404（项目已删除由项目授权检查报 404）。
func (s *Service) ProjectOf(ctx context.Context, folderID uuid.UUID) (uuid.UUID, error) {
	pid, ok, err := liveFolderProject(ctx, s.pool, folderID)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, errFolderNotFound
	}
	return pid, nil
}

// ProjectOfEntry 是文件（folder_files 的 id）所在的项目；不存在、已删除（含所在文件夹）404。
func (s *Service) ProjectOfEntry(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	es, err := liveEntries(ctx, s.pool, []uuid.UUID{id})
	if err != nil {
		return uuid.Nil, err
	}
	e := es[id]
	if e == nil {
		return uuid.Nil, errFileNotFound
	}
	return e.ProjectID, nil
}

// Path 是文件夹从根文件夹起用 / 连接的路径（日志 detail 的 folder_path、duplicate_content 的 details）。
func (s *Service) Path(ctx context.Context, q db.Querier, folderID uuid.UUID) (string, error) {
	paths, err := folderPaths(ctx, q, []uuid.UUID{folderID})
	if err != nil {
		return "", err
	}
	return paths[folderID], nil
}

// Paths 是一批文件夹的路径（按 id）。
func (s *Service) Paths(ctx context.Context, q db.Querier, folderIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	return folderPaths(ctx, q, folderIDs)
}

// FolderItem 是 `GET /api/folders/{id}` 等的响应：文件夹本身和祖先链（面包屑）。
type FolderItem struct {
	ID         uuid.UUID  `json:"id"`
	ProjectID  uuid.UUID  `json:"project_id"`
	ParentID   *uuid.UUID `json:"parent_id"`
	FolderName string     `json:"folder_name"`
	Remark     *string    `json:"remark"`
	PathCode   string     `json:"path_code"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	// Ancestors 从根文件夹到父文件夹，不含自己；根文件夹的为空。
	Ancestors []Ancestor `json:"ancestors"`
}

// readItem 在 q 上读未删除的文件夹和祖先链；不存在或已删除 404。
func readItem(ctx context.Context, q db.Querier, id uuid.UUID) (*FolderItem, error) {
	f, err := getFolder(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if f == nil || f.Deleted {
		return nil, errFolderNotFound
	}
	as, err := ancestors(ctx, q, f.ProjectID, f.PathCode)
	if err != nil {
		return nil, err
	}
	return &FolderItem{ID: f.ID, ProjectID: f.ProjectID, ParentID: f.ParentID, FolderName: f.FolderName, Remark: f.Remark,
		PathCode: f.PathCode, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt, Ancestors: as}, nil
}

// readOnly 在一个只读的可重复读事务里执行 fn（几条查询看到同一个快照）。
func (s *Service) readOnly(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	return fn(tx)
}

// Get 读文件夹和祖先链。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*FolderItem, error) {
	var out *FolderItem
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readItem(ctx, tx, id)
		return err
	})
	return out, err
}

// Children 是 `GET /api/folders/{id}/children` 的响应：都按 sort_no、名称排序，不分页，不带图片地址。
type Children struct {
	Folders []SubfolderEntry `json:"folders"`
	Files   []FileEntry      `json:"files"`
}

// Children 列出未删除的直接子文件夹和文件（同一个快照）。
func (s *Service) Children(ctx context.Context, id uuid.UUID) (*Children, error) {
	out := &Children{}
	err := s.readOnly(ctx, func(tx pgx.Tx) error {
		f, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if f == nil || f.Deleted {
			return errFolderNotFound
		}
		if out.Folders, err = listSubfolders(ctx, tx, id); err != nil {
			return err
		}
		out.Files, err = listFiles(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeRemark：去掉首尾空白，空串为 nil；最长 500 字；不能有控制字符（换行、回车、制表符可以）。
func normalizeRemark(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	r := strings.TrimSpace(*v)
	if r == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(r) > maxRemarkRunes {
		return nil, httpx.Invalid("备注最长 %d 个字", maxRemarkRunes)
	}
	for _, c := range r {
		if unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' {
			return nil, httpx.Invalid("备注不能包含控制字符")
		}
	}
	return &r, nil
}

func validFolderName(name string) error {
	if err := names.ValidateFolderName(name); err != nil {
		return httpx.Invalid("%s", err.Error())
	}
	return nil
}

func nameConflict(name string) error {
	return httpx.Conflict("", "同一个文件夹里已有名为「"+name+"」的文件夹（不区分大小写）")
}

// CreateInput 是新建文件夹的输入。
type CreateInput struct {
	ParentID   uuid.UUID
	FolderName string
	Remark     *string
	// IfExists 为 "return" 时遇到同名返回已有的文件夹（created 为 false），否则 409 conflict。
	IfExists string
}

// Create 新建子文件夹。检查顺序：上级文件夹不存在 404 → 项目授权 403 → 名称、备注 400 → 同名 409 / 编号用完 409 tree_full。
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in CreateInput) (*FolderItem, bool, error) {
	if in.IfExists != "" && in.IfExists != "return" {
		return nil, false, httpx.Invalid(`if_exists 只能是 "return"`)
	}
	if in.ParentID == uuid.Nil {
		return nil, false, httpx.Invalid("缺少 parent_id")
	}
	pid, err := s.ProjectOf(ctx, in.ParentID)
	if err != nil {
		return nil, false, err
	}
	if err := s.auth.CheckProjectAccess(ctx, userID, pid); err != nil {
		return nil, false, err
	}
	if err := validFolderName(in.FolderName); err != nil {
		return nil, false, err
	}
	remark, err := normalizeRemark(in.Remark)
	if err != nil {
		return nil, false, err
	}
	var out *FolderItem
	created := false
	err = s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, pid, false); err != nil {
			return err
		}
		locked, err := lockFolders(ctx, tx, in.ParentID)
		if err != nil {
			return err
		}
		parent := locked[in.ParentID]
		if parent == nil || parent.Deleted || parent.ProjectID != pid {
			return httpx.NotFound("上级文件夹")
		}
		sib, err := siblingNamed(ctx, tx, parent.ID, in.FolderName, uuid.Nil)
		if err != nil {
			return err
		}
		if sib != nil {
			if in.IfExists == "return" {
				out, err = readItem(ctx, tx, sib.ID)
				return err
			}
			return nameConflict(in.FolderName)
		}
		ppath, n, err := allocateChild(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
		code, err := childPath(ppath, n)
		if err != nil {
			return err
		}
		f := &folderRow{ID: db.NewID(), ProjectID: pid, ParentID: &parent.ID, FolderName: in.FolderName, PathCode: code, Remark: remark}
		if err := insertFolder(ctx, tx, f, n, userID); err != nil {
			if db.UniqueViolation(err, "ux_folders_name") {
				return nameConflict(in.FolderName)
			}
			return err
		}
		path, err := s.Path(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		after := map[string]any{"folder_name": f.FolderName, "folder_path": path, "parent_id": parent.ID, "path_code": code}
		if remark != nil {
			after["remark"] = *remark
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &pid, OperatorID: audit.Operator(userID),
			TargetType: audit.TargetFolder, TargetID: f.ID, Action: "create", Detail: map[string]any{"after": after}}); err != nil {
			return err
		}
		if out, err = readItem(ctx, tx, f.ID); err != nil {
			return err
		}
		created = true
		ev.Add(events.FoldersChanged(pid, f.ID, parent.ID))
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// UpdateInput 是修改文件夹的输入；nil 表示不改。Remark 传空串表示清空；ParentID 是移动（等于现在的父文件夹时不移动）。
type UpdateInput struct {
	FolderName *string
	Remark     *string
	ParentID   *uuid.UUID
}

// Update 改名、改备注、移动（项目 projectID 的授权已经查过）。没有变化时不写审计、不发事件。
func (s *Service) Update(ctx context.Context, userID, projectID, id uuid.UUID, in UpdateInput) (*FolderItem, error) {
	if in.FolderName != nil {
		if err := validFolderName(*in.FolderName); err != nil {
			return nil, err
		}
	}
	var remark *string
	if in.Remark != nil {
		var err error
		if remark, err = normalizeRemark(in.Remark); err != nil {
			return nil, err
		}
	}
	var out *FolderItem
	err := s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		// 移动文件夹拿文件树排他锁（子树换编码，requirements 3.6），改名、改备注拿共享锁。
		if err := project.LockFileTree(ctx, tx, projectID, in.ParentID != nil); err != nil {
			return err
		}
		cur, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur == nil || cur.Deleted || cur.ProjectID != projectID {
			return errFolderNotFound
		}
		lockIDs := []uuid.UUID{id}
		if cur.ParentID != nil {
			lockIDs = append(lockIDs, *cur.ParentID)
		}
		if in.ParentID != nil {
			lockIDs = append(lockIDs, *in.ParentID)
		}
		locked, err := lockFolders(ctx, tx, lockIDs...)
		if err != nil {
			return err
		}
		f := locked[id]
		if f == nil || f.Deleted {
			return errFolderNotFound
		}
		isRoot := f.ParentID == nil
		newName := f.FolderName
		if in.FolderName != nil {
			newName = *in.FolderName
		}
		rename := newName != f.FolderName
		remarkChange := in.Remark != nil && !equalPtr(remark, f.Remark)
		move := in.ParentID != nil && (isRoot || *in.ParentID != *f.ParentID)
		if isRoot && (rename || move) {
			return rootLocked()
		}
		var target *folderRow
		if move {
			target = locked[*in.ParentID]
			if target == nil || target.Deleted || target.ProjectID != projectID {
				return errTargetNotFound
			}
			if inSubtree(target.PathCode, f.PathCode) {
				return httpx.Conflict("tree_cycle", "不能把文件夹移到它自己或它下面的子文件夹里")
			}
		}
		if !rename && !remarkChange && !move {
			out, err = readItem(ctx, tx, id)
			return err
		}
		destParent := f.ParentID
		if move {
			destParent = &target.ID
		}
		if rename || move {
			sib, err := siblingNamed(ctx, tx, *destParent, newName, f.ID)
			if err != nil {
				return err
			}
			if sib != nil {
				return nameConflict(newName)
			}
		}
		oldPath, err := s.Path(ctx, tx, id)
		if err != nil {
			return err
		}
		before, after := map[string]any{}, map[string]any{}
		oldParent, oldCode := f.ParentID, f.PathCode
		sortNo := 0
		if move {
			ppath, n, err := allocateChild(ctx, tx, target.ID)
			if err != nil {
				return err
			}
			code, err := childPath(ppath, n)
			if err != nil {
				return err
			}
			if err := rebaseSubtree(ctx, tx, projectID, f.PathCode, code); err != nil {
				return err
			}
			f.ParentID, f.PathCode, sortNo = &target.ID, code, n
			before["parent_id"], after["parent_id"] = oldParent, target.ID
			before["path_code"], after["path_code"] = oldCode, code
		}
		if rename {
			before["folder_name"], after["folder_name"] = f.FolderName, newName
			f.FolderName = newName
		}
		if remarkChange {
			before["remark"], after["remark"] = f.Remark, remark
			f.Remark = remark
		}
		if err := updateFolder(ctx, tx, f, sortNo); err != nil {
			if db.UniqueViolation(err, "ux_folders_name") {
				return nameConflict(newName)
			}
			return err
		}
		if rename || move {
			newPath, err := s.Path(ctx, tx, id)
			if err != nil {
				return err
			}
			before["folder_path"], after["folder_path"] = oldPath, newPath
		}
		action := "update"
		if move {
			action = "move"
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(userID),
			TargetType: audit.TargetFolder, TargetID: id, Action: action,
			Detail: map[string]any{"before": before, "after": after}}); err != nil {
			return err
		}
		changed := []uuid.UUID{id}
		if oldParent != nil {
			changed = append(changed, *oldParent)
		}
		if move {
			changed = append(changed, target.ID)
		}
		ev.Add(events.FoldersChanged(projectID, changed...))
		out, err = readItem(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Delete 软删除文件夹和整棵子树（项目 projectID 的授权已经查过）。子树里有已归类的文件 409 file_cataloged，
// 什么都不改；否则子树里的文件夹和文件都打上 deleted_at，进行中的分配改成 cancelled，每个文件记一条 delete。
func (s *Service) Delete(ctx context.Context, userID, projectID, id uuid.UUID) error {
	return s.events.InTx(ctx, s.pool, func(tx pgx.Tx, ev *events.Pending) error {
		if err := project.LockFileTree(ctx, tx, projectID, true); err != nil {
			return err
		}
		f, err := getFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if f == nil || f.Deleted || f.ProjectID != projectID {
			return errFolderNotFound
		}
		if f.ParentID == nil {
			return rootLocked()
		}
		// 先按 id 升序锁住子树里文件的 files 行，再检查已归类（挂接锁同一行，并发也不漏）。
		fileIDs, err := subtreeLiveFileIDs(ctx, tx, projectID, f.PathCode)
		if err != nil {
			return err
		}
		if err := file.LockFiles(ctx, tx, fileIDs); err != nil {
			return err
		}
		cataloged, err := catalogedAmong(ctx, tx, fileIDs)
		if err != nil {
			return err
		}
		if len(cataloged) > 0 {
			example, err := fileNameOf(ctx, tx, cataloged[0])
			if err != nil {
				return err
			}
			return httpx.Conflict("file_cataloged", "文件夹里有 "+itoa(len(cataloged))+" 个已归类的文件（例如「"+example+
				"」），不能删除")
		}
		folderIDs, err := subtreeLiveFolderIDs(ctx, tx, projectID, f.PathCode)
		if err != nil {
			return err
		}
		locked, err := lockFolders(ctx, tx, folderIDs...)
		if err != nil {
			return err
		}
		paths, err := folderPaths(ctx, tx, folderIDs)
		if err != nil {
			return err
		}
		if err := softDeleteFolders(ctx, tx, folderIDs, userID); err != nil {
			return err
		}
		deleted, err := softDeleteFilesIn(ctx, tx, folderIDs, userID)
		if err != nil {
			return err
		}
		deletedIDs := make([]uuid.UUID, len(deleted))
		logs := make([]file.Log, len(deleted))
		for i, d := range deleted {
			deletedIDs[i] = d.FileID
			logs[i] = file.Log{ProjectID: projectID, FileID: d.FileID, Action: file.ActionDelete, OperatorID: userID,
				Detail: map[string]any{"file_name": d.FileName, "folder": locked[d.FolderID].FolderName, "folder_path": paths[d.FolderID]}}
		}
		if err := cancelOpenReviews(ctx, tx, deletedIDs); err != nil {
			return err
		}
		if err := file.WriteLogs(ctx, tx, logs...); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ProjectID: &projectID, OperatorID: audit.Operator(userID),
			TargetType: audit.TargetFolder, TargetID: id, Action: "delete",
			Detail: map[string]any{"before": map[string]any{"folder_name": f.FolderName, "folder_path": paths[id],
				"folder_count": len(folderIDs), "file_count": len(deleted)}}}); err != nil {
			return err
		}
		ev.Add(events.FoldersChanged(projectID, append([]uuid.UUID{*f.ParentID}, folderIDs...)...))
		if len(deleted) > 0 {
			ev.Add(events.FilesChanged(projectID, folderIDs, deletedIDs))
		}
		return nil
	})
}
