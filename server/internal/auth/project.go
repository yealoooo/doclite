package auth

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 项目授权检查（requirements 第 4 节）：
//   - 能进项目 = admin，或在 project_members 里，或拥有 project_roles 里的某个角色；
//   - 项目不存在或已删除 404，没授权 403 `forbidden`；
//   - 检查顺序：菜单 403 → 项目 404/403 → 参数 400 → …；进不了项目的人带错参数也是 403。
//
// 以项目 id 为参数的接口：
//
//	r.Handle("GET /api/projects/{pid}/review-settings", g.InProject(g.Login(), auth.PathProject("pid")), h.reviewSettings)
//	r.Handle("POST /api/projects/{pid}/review-assignments",
//	         g.InProject(g.AnyMenu(auth.MenuReviewAssign), auth.PathProject("pid")), h.assign)
//
// 以资源 id 为参数的接口：自己写一个 ProjectResolver 反查项目（找不到、已删除、所在文件夹已删除都返回 404），
// 项目已删除由 InProject 统一按 404 处理：
//
//	func (h *Handler) folderProject(r *http.Request) (uuid.UUID, error) {
//		id, err := httpx.PathUUID(r, "id")
//		if err != nil { return uuid.Nil, err }
//		return h.svc.ProjectOfFolder(r.Context(), id) // 不存在或已删除 → httpx.NotFound("文件夹")
//	}
//	r.Handle("GET /api/folders/{id}", g.InProject(g.AnyMenu(auth.MenuScan, auth.MenuFiles), h.folderProject), h.get)
//
// handler 里用 auth.ProjectID(ctx) 取检查过的项目 id。请求体里带项目或资源的（批量接口），在 handler / service 里调
// [Service.CheckProjectAccess] 或 [Service.AccessibleProjects]。

// ProjectResolver 从请求里找出要访问的项目 id（解析路径参数、按资源 id 反查）。资源不存在返回 404。
type ProjectResolver func(r *http.Request) (uuid.UUID, error)

// PathProject 是"路径参数 name 就是项目 id"的 ProjectResolver。不是合法的 UUID 时 400。
func PathProject(name string) ProjectResolver {
	return func(r *http.Request) (uuid.UUID, error) { return httpx.PathUUID(r, name) }
}

type projectKey struct{}

// ProjectID 是 InProject 检查过的项目 id。
func ProjectID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(projectKey{}).(uuid.UUID)
	return id
}

// InProject 在 access（Login、AnyMenu）之后检查项目授权：resolve 找出项目，项目不存在或已删除 404，
// 没授权 403 `forbidden`；通过后把项目 id 放进 context（[ProjectID]）。
func (g *Gate) InProject(access httpx.Middleware, resolve ProjectResolver) httpx.Middleware {
	if access == nil || resolve == nil {
		panic("auth: InProject 的 access 和 resolve 都要给")
	}
	return func(next http.Handler) http.Handler {
		check := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := Current(r.Context())
			if p == nil {
				panic("auth: InProject 要在 Login、AnyMenu 之后")
			}
			pid, err := resolve(r)
			if err == nil {
				err = g.svc.CheckProjectAccess(r.Context(), p.UserID, pid)
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), projectKey{}, pid)))
		})
		return access(check)
	}
}

var (
	errProjectNotFound  = httpx.NotFound("项目")
	errProjectForbidden = httpx.NewError(http.StatusForbidden, httpx.CodeForbidden, "你没有这个项目的权限")
)

// CheckProjectAccess：项目不存在或已删除 404，用户进不了 403 `forbidden`，能进返回 nil。
func (s *Service) CheckProjectAccess(ctx context.Context, userID, projectID uuid.UUID) error {
	alive, allowed, found, err := projectAccess(ctx, s.pool, userID, projectID)
	if err != nil {
		return err
	}
	if !found || !alive {
		return errProjectNotFound
	}
	if !allowed {
		return errProjectForbidden
	}
	return nil
}

// AccessibleProjects 是 ids 里用户能进的未删除项目（批量接口按它过滤，进不了的当作不存在）。
func (s *Service) AccessibleProjects(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	return accessibleProjects(ctx, s.pool, userID, ids)
}

// RecheckProject 复核会话和项目授权（实时通道每 60 秒一次）：令牌对应的会话仍有效、用户仍能进这个项目时返回 nil。
func (s *Service) RecheckProject(ctx context.Context, token string, projectID uuid.UUID) error {
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		return err
	}
	return s.CheckProjectAccess(ctx, p.UserID, projectID)
}

// IsAdminSQL 是"用户 userExpr 有内置的 admin 角色"的 SQL 条件（userExpr 是 SQL 表达式，如 `$1`、`u.id`）。
// 别的模块的只读查询拼进去用，条件只写这一份。
func IsAdminSQL(userExpr string) string {
	return `EXISTS (SELECT 1 FROM user_roles aur JOIN roles ar ON ar.id = aur.role_id
          WHERE aur.user_id = ` + userExpr + ` AND ar.role_code = '` + AdminRoleCode + `')`
}

// ProjectAccessSQL 是"用户 userExpr 能进项目 projectExpr"的 SQL 条件（admin，或个人授权，或角色授权；requirements
// 第 4 节"项目授权"）。别的模块的只读查询拼进去用，条件只写这一份。
func ProjectAccessSQL(userExpr, projectExpr string) string {
	return `(` + IsAdminSQL(userExpr) + `
  OR EXISTS (SELECT 1 FROM project_members apm WHERE apm.project_id = ` + projectExpr + ` AND apm.user_id = ` + userExpr + `)
  OR EXISTS (SELECT 1 FROM project_roles apr JOIN user_roles aur ON aur.role_id = apr.role_id
             WHERE apr.project_id = ` + projectExpr + ` AND aur.user_id = ` + userExpr + `))`
}

// accessPredicate 是"用户 $1 能进项目 p"。
var accessPredicate = ProjectAccessSQL("$1", "p.id")

// projectAccess 是 auth 对 projects、project_members、project_roles、user_roles、roles 的只读查询（跨模块的读）。
func projectAccess(ctx context.Context, q db.Querier, userID, projectID uuid.UUID) (alive, allowed, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT p.deleted_at IS NULL, `+accessPredicate+` FROM projects p WHERE p.id = $2`,
		userID, projectID).Scan(&alive, &allowed)
	if db.IsNoRows(err) {
		return false, false, false, nil
	}
	return alive, allowed, err == nil, err
}

func accessibleProjects(ctx context.Context, q db.Querier, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT p.id FROM projects p WHERE p.id = ANY($2) AND p.deleted_at IS NULL AND `+accessPredicate,
		userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
