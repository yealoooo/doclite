package apitest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// RoleID 按 role_code 查角色 id。
func (e *Env) RoleID(code string) uuid.UUID {
	e.t.Helper()
	return QueryValue[uuid.UUID](e, "SELECT id FROM roles WHERE role_code = $1", code)
}

// UserID 按登录名查用户 id。
func (e *Env) UserID(username string) uuid.UUID {
	e.t.Helper()
	return QueryValue[uuid.UUID](e, "SELECT id FROM users WHERE lower(username) = lower($1)", username)
}

// CreateRole 用管理员建一个自建角色，返回 id。
func (e *Env) CreateRole(code string, menus ...string) uuid.UUID {
	e.t.Helper()
	if menus == nil {
		menus = []string{}
	}
	return e.Admin().Post("/api/system/roles", map[string]any{
		"role_code": code, "role_name": code, "menu_codes": menus,
	}).Expect(http.StatusCreated).ID()
}

// CreateUser 用管理员建一个用户（密码 DefaultPassword），角色按 role_code 给出，返回 id。
func (e *Env) CreateUser(username string, roleCodes ...string) uuid.UUID {
	e.t.Helper()
	ids := []uuid.UUID{}
	for _, c := range roleCodes {
		ids = append(ids, e.RoleID(c))
	}
	return e.Admin().Post("/api/system/users", map[string]any{
		"username": username, "display_name": username, "password": DefaultPassword, "role_ids": ids,
	}).Expect(http.StatusCreated).ID()
}

// persona 是权限表里的一列：一个专门的用户和他已登录的客户端。
type persona struct {
	userID uuid.UUID
	client *Client
}

var builtinRoles = map[string]bool{"admin": true, "manager": true, "scanner": true, "reviewer": true, "cataloger": true}

// As 返回某类用户的已登录客户端（同一个 Env 里复用）：
//   - 内置角色码（admin、manager、scanner、reviewer、cataloger）：有这一个内置角色的用户；
//   - `none`：没有任何角色的用户；
//   - 其他写法是菜单码，多个用 `+` 连起来（`system.users`、`catalog+review_assign`）：
//     有一个只含这些菜单的自建角色的用户。
func (e *Env) As(name string) *Client {
	e.t.Helper()
	e.mu.Lock()
	p := e.personas[name]
	e.mu.Unlock()
	if p != nil {
		return p.client
	}
	username := personaCode(name)
	var id uuid.UUID
	switch {
	case builtinRoles[name]:
		id = e.CreateUser(username, name)
	case name == "none":
		id = e.CreateUser(username)
	default:
		e.CreateRole(username, strings.Split(name, "+")...)
		id = e.CreateUser(username, username)
	}
	p = &persona{userID: id, client: e.Login(username, DefaultPassword)}
	e.mu.Lock()
	e.personas[name] = p
	e.mu.Unlock()
	return p.client
}

// PersonaID 是 As(name) 那个用户的 id。
func (e *Env) PersonaID(name string) uuid.UUID {
	e.t.Helper()
	e.As(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.personas[name].userID
}

// personaCode 把列名变成合法的角色码和登录名（`^[a-z][a-z0-9_]{0,31}$`）。
func personaCode(name string) string {
	code := "p_" + strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, strings.ToLower(name))
	if len(code) > 32 {
		sum := sha256.Sum256([]byte(name))
		code = "p_" + hex.EncodeToString(sum[:])[:16]
	}
	return code
}

var placeholder = regexp.MustCompile(`\{[a-z_]+\}`)

// CheckPerms 按一张紧凑的"接口 × 角色"表检查菜单权限。每个模块写一张，例如：
//
//		CheckPerms(t, env, `
//		                                         anon  admin  system.users  system.roles  manager  none
//		GET    /api/system/users                  -      Y         Y             -          -       -
//		POST   /api/auth/login                    Y      Y         Y             Y          Y       Y
//		`)
//
//	  - 第一行是列名：`anon` 表示没登录；其余见 [Env.As]（内置角色码、`none`、菜单码用 `+` 连起来）。
//	  - 每一行：方法、路径、每列一格。`Y` 表示菜单权限放行（状态码不是 401，也不是 403 `forbidden`；
//	    参数不对、找不到之类都算放行）；`-` 表示拒绝：登录了是 403 `forbidden`，`anon` 列是 401 `unauthorized`。
//	  - 没写 `anon` 列时，每一行都另外检查一次没登录是 401。
//	  - 路径里的 `{id}` 之类换成随机 UUID；POST、PUT、PATCH 发 `{}`，不会真的改到数据。
//	  - `#` 开头的行是注释。
func CheckPerms(t *testing.T, e *Env, table string) {
	t.Helper()
	var cols []string
	type row struct {
		method, path string
		cells        []string
		line         int
	}
	var rows []row
	for i, line := range strings.Split(table, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if cols == nil {
			cols = f
			continue
		}
		if len(f) != len(cols)+2 {
			t.Fatalf("权限表第 %d 行有 %d 格，表头有 %d 列：%s", i+1, len(f)-2, len(cols), line)
		}
		rows = append(rows, row{method: f[0], path: f[1], cells: f[2:], line: i + 1})
	}
	hasAnon := false
	for _, c := range cols {
		if c == "anon" {
			hasAnon = true
		}
	}

	check := func(r row, col, cell string, c *Client) {
		t.Helper()
		path := placeholder.ReplaceAllStringFunc(r.path, func(string) string { return uuid.NewString() })
		var body any
		if r.method == "POST" || r.method == "PUT" || r.method == "PATCH" {
			body = map[string]any{}
		}
		res := c.Do(r.method, path, body)
		denied := res.Status == http.StatusUnauthorized || (res.Status == http.StatusForbidden && res.Code() == "forbidden")
		switch cell {
		case "Y":
			if denied {
				t.Errorf("%s %s [%s]：期望放行，实际 %d %s", r.method, r.path, col, res.Status, res.Code())
			}
		case "-":
			want, code := http.StatusForbidden, "forbidden"
			if col == "anon" {
				want, code = http.StatusUnauthorized, "unauthorized"
			}
			if res.Status != want || res.Code() != code {
				t.Errorf("%s %s [%s]：期望 %d %s，实际 %d %s", r.method, r.path, col, want, code, res.Status, res.Code())
			}
		default:
			t.Fatalf("权限表第 %d 行 [%s] 的格子只能是 Y 或 -，实际是 %q", r.line, col, cell)
		}
	}

	for ci, col := range cols {
		var c *Client
		if col == "anon" {
			c = e.Anon()
		} else {
			c = e.As(col)
		}
		for _, r := range rows {
			check(r, col, r.cells[ci], c)
		}
	}
	if !hasAnon {
		anon := e.Anon()
		for _, r := range rows {
			check(r, "anon", "-", anon)
		}
	}
}
