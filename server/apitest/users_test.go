package apitest

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

type userItem struct {
	ID          uuid.UUID  `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Roles       []struct {
		ID       uuid.UUID `json:"id"`
		RoleCode string    `json:"role_code"`
		RoleName string    `json:"role_name"`
	} `json:"roles"`
}

func (u userItem) roleCodes() []string {
	out := []string{}
	for _, r := range u.Roles {
		out = append(out, r.RoleCode)
	}
	return out
}

type userPage struct {
	Items    []userItem `json:"items"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

func auditActions(env *Env, targetID uuid.UUID) []string {
	return QueryValue[[]string](env, "SELECT coalesce(array_agg(action ORDER BY id), '{}') FROM audit_logs WHERE target_id = $1", targetID)
}

func TestUsersCRUD(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	reviewer := env.RoleID("reviewer")

	// 新建：去首尾空白、201、带角色。
	var u userItem
	admin.Post("/api/system/users", map[string]any{
		"username": " Alice ", "display_name": " 爱丽丝 ", "password": "secret1", "role_ids": []uuid.UUID{reviewer, reviewer},
	}).Expect(201).JSON(&u)
	if u.Username != "Alice" || u.DisplayName != "爱丽丝" || u.Status != "active" || u.LastLoginAt != nil ||
		len(u.Roles) != 1 || u.Roles[0].ID != reviewer || u.Roles[0].RoleName != "审核员" {
		t.Fatalf("新建的用户：%+v", u)
	}
	path := "/api/system/users/" + u.ID.String()

	// 登录名不区分大小写唯一；各种不合规则的输入。
	admin.Post("/api/system/users", map[string]any{"username": "alice", "display_name": "x", "password": "secret1"}).ExpectError(409, "conflict")
	for _, body := range []map[string]any{
		{"username": "a b", "display_name": "x", "password": "secret1"},
		{"username": "", "display_name": "x", "password": "secret1"},
		{"username": string(make([]rune, 65)), "display_name": "x", "password": "secret1"},
		{"username": "bob", "display_name": " ", "password": "secret1"},
		{"username": "bob", "display_name": "x", "password": "12345"},
		{"username": "bob", "display_name": "x", "password": "secret1", "role_ids": []uuid.UUID{uuid.New()}},
		{"username": "bob", "display_name": "x", "password": "secret1", "role_ids": []string{"not-a-uuid"}},
		{"username": 1, "display_name": "x", "password": "secret1"},
	} {
		admin.Post("/api/system/users", body).ExpectError(400, "invalid_input")
	}

	// 详情。
	admin.Get(path).Expect(200).JSON(&u)
	admin.Get("/api/system/users/"+uuid.NewString()).ExpectError(404, "not_found")

	// 列表：按登录名排序（不区分大小写）、关键字、分页。
	env.CreateUser("bob")
	env.CreateUser("carol_x")
	var page userPage
	admin.Get("/api/system/users").Expect(200).JSON(&page)
	if page.Total != 4 || page.Page != 1 || page.PageSize != 20 || len(page.Items) != 4 ||
		page.Items[0].Username != "admin" || page.Items[1].Username != "Alice" || page.Items[2].Username != "bob" {
		t.Fatalf("用户列表：%+v", page)
	}
	admin.Get("/api/system/users?keyword=%E7%88%B1").Expect(200).JSON(&page) // "爱"
	if page.Total != 1 || page.Items[0].Username != "Alice" {
		t.Fatalf("按姓名搜：%+v", page)
	}
	admin.Get("/api/system/users?keyword=_").Expect(200).JSON(&page) // _ 不当通配符
	if page.Total != 1 || page.Items[0].Username != "carol_x" {
		t.Fatalf("关键字里的 _ 应按原文匹配：%+v", page)
	}
	admin.Get("/api/system/users?page=2&page_size=3").Expect(200).JSON(&page)
	if page.Total != 4 || len(page.Items) != 1 || page.Items[0].Username != "carol_x" || page.PageSize != 3 {
		t.Fatalf("分页：%+v", page)
	}
	admin.Get("/api/system/users?page_size=101").ExpectError(400, "invalid_input")
	admin.Get("/api/system/users?page=0").ExpectError(400, "invalid_input")
	admin.Get("/api/system/users?page=x").ExpectError(400, "invalid_input")

	// 修改：空修改不写库；改名撞了 409。
	before := u.UpdatedAt
	admin.Patch(path, map[string]any{}).Expect(200).JSON(&u)
	if !u.UpdatedAt.Equal(before) {
		t.Fatal("空修改不应刷新 updated_at")
	}
	admin.Patch(path, map[string]any{"display_name": "爱丽丝 2"}).Expect(200).JSON(&u)
	if u.DisplayName != "爱丽丝 2" || u.Username != "Alice" {
		t.Fatalf("修改后：%+v", u)
	}
	admin.Patch(path, map[string]any{"username": "ADMIN"}).ExpectError(409, "conflict")
	admin.Patch(path, map[string]any{"username": "a b"}).ExpectError(400, "invalid_input")

	// 重置密码：204，撤销他的会话，旧密码失效。
	alice := env.Login("alice", "secret1")
	alice.Get("/api/me").Expect(200)
	admin.Post(path+"/reset-password", map[string]any{"password": "123"}).ExpectError(400, "invalid_input")
	admin.Post(path+"/reset-password", map[string]any{"password": "new-secret"}).Expect(204)
	alice.Get("/api/me").ExpectError(401, "unauthorized")
	env.Anon().Post("/api/auth/login", map[string]any{"username": "alice", "password": "secret1"}).ExpectError(401, "invalid_credentials")
	alice = env.Login("alice", "new-secret")

	// 改角色：整体替换，按 role_code 排序；缺 role_ids 400；角色没变不写库。
	admin.Put(path+"/roles", map[string]any{"role_ids": []uuid.UUID{env.RoleID("scanner"), env.RoleID("cataloger")}}).Expect(200).JSON(&u)
	if got := u.roleCodes(); len(got) != 2 || got[0] != "cataloger" || got[1] != "scanner" {
		t.Fatalf("改角色后：%v", got)
	}
	admin.Put(path+"/roles", map[string]any{"role_ids": []uuid.UUID{env.RoleID("cataloger"), env.RoleID("scanner")}}).Expect(200)
	admin.Put(path+"/roles", map[string]any{}).ExpectError(400, "invalid_input")
	admin.Put(path+"/roles", map[string]any{"role_ids": []uuid.UUID{uuid.New()}}).ExpectError(400, "invalid_input")
	admin.Put("/api/system/users/"+uuid.NewString()+"/roles", map[string]any{"role_ids": []uuid.UUID{}}).ExpectError(404, "not_found")
	// 改了角色，菜单马上变（不用重新登录）。
	var menus []myMenu
	alice.Get("/api/me/menus").Expect(200).JSON(&menus)
	if len(menus) != 3 || menus[0].MenuCode != "files" || menus[1].MenuCode != "scan" || menus[2].MenuCode != "catalog" {
		t.Fatalf("改角色后的菜单：%+v", menus)
	}

	// 停用、启用；状态没变不写库。
	admin.Post(path+"/disable", nil).Expect(200).JSON(&u)
	if u.Status != "disabled" {
		t.Fatalf("停用后：%+v", u)
	}
	admin.Post(path+"/disable", nil).Expect(200)
	admin.Post(path+"/enable", nil).Expect(200).JSON(&u)
	if u.Status != "active" {
		t.Fatalf("启用后：%+v", u)
	}
	admin.Post("/api/system/users/"+uuid.NewString()+"/disable", nil).ExpectError(404, "not_found")

	// 审计：每个实际的修改一条，不记密码。
	got := auditActions(env, u.ID)
	want := []string{"create", "update", "reset_password", "set_roles", "disable", "enable"}
	if len(got) != len(want) {
		t.Fatalf("审计：%v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("审计：%v，期望 %v", got, want)
		}
	}
	if QueryValue[int](env, "SELECT count(*) FROM audit_logs WHERE detail::text LIKE '%secret%'") != 0 {
		t.Fatal("审计里不能有密码")
	}
}

func TestUserGuards(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	adminID := env.UserID("admin")

	// 不能停用自己。
	admin.Post("/api/system/users/"+adminID.String()+"/disable", nil).ExpectError(409, "cannot_disable_self")
	// 不能拿掉最后一个启用的管理员的 admin 角色。
	admin.Put("/api/system/users/"+adminID.String()+"/roles", map[string]any{"role_ids": []uuid.UUID{}}).ExpectError(409, "last_admin")

	// 有第二个管理员时可以；第二个管理员停用了初始管理员之后，自己就是最后一个。
	a2 := env.CreateUser("admin2", "admin")
	admin2 := env.Login("admin2", DefaultPassword)
	admin2.Post("/api/system/users/"+adminID.String()+"/disable", nil).Expect(200)
	admin.Get("/api/me").ExpectError(401, "unauthorized")
	admin2.Put("/api/system/users/"+a2.String()+"/roles", map[string]any{"role_ids": []uuid.UUID{env.RoleID("manager")}}).ExpectError(409, "last_admin")
	// 停用的管理员不算：给他去掉 admin 不受限制。
	admin2.Put("/api/system/users/"+adminID.String()+"/roles", map[string]any{"role_ids": []uuid.UUID{}}).Expect(200)
	// 最后一个管理员也不能停用自己。
	admin2.Post("/api/system/users/"+a2.String()+"/disable", nil).ExpectError(409, "cannot_disable_self")
}

// TestPrivilegeEscalation 是防提权的四条规则（requirements 第 4 节）：操作人是有 system.users、system.roles 的非 admin。
func TestPrivilegeEscalation(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()
	env.CreateRole("user_admin", "system.users", "system.roles")
	env.CreateRole("storage_admin", "system.storage")
	env.CreateUser("ua", "user_admin")
	ua := env.Login("ua", DefaultPassword)
	adminID := env.UserID("admin")
	plainID := env.CreateUser("plain", "reviewer")
	saID := env.CreateUser("sa", "storage_admin")
	userPath := func(id uuid.UUID) string { return "/api/system/users/" + id.String() }

	// 规则 1：授予、收回 admin，修改 admin 用户，都要是 admin。
	ua.Post("/api/system/users", map[string]any{
		"username": "x1", "display_name": "x1", "password": "secret1", "role_ids": []uuid.UUID{env.RoleID("admin")},
	}).ExpectError(403, "admin_required")
	ua.Put(userPath(plainID)+"/roles", map[string]any{"role_ids": []uuid.UUID{env.RoleID("admin")}}).ExpectError(403, "admin_required")
	ua.Patch(userPath(adminID), map[string]any{"display_name": "x"}).ExpectError(403, "admin_required")
	ua.Post(userPath(adminID)+"/reset-password", map[string]any{"password": "hacked1"}).ExpectError(403, "admin_required")
	ua.Post(userPath(adminID)+"/disable", nil).ExpectError(403, "admin_required")
	ua.Put(userPath(adminID)+"/roles", map[string]any{"role_ids": []uuid.UUID{}}).ExpectError(403, "admin_required")

	// 规则 2：增减任何角色的 system.* 菜单都要是 admin（哪怕操作人自己有这个菜单）。
	ua.Post("/api/system/roles", map[string]any{"role_code": "r_sys", "role_name": "r", "menu_codes": []string{"system.users"}}).ExpectError(403, "admin_required")
	rFiles := ua.Post("/api/system/roles", map[string]any{"role_code": "r_files", "role_name": "r", "menu_codes": []string{"files"}}).Expect(201).ID()
	ua.Put("/api/system/roles/"+rFiles.String()+"/menus", map[string]any{"menu_codes": []string{"files", "system.users"}}).ExpectError(403, "admin_required")
	ua.Put("/api/system/roles/"+rFiles.String()+"/menus", map[string]any{"menu_codes": []string{"files", "scan"}}).Expect(200)
	ua.Delete("/api/system/roles/"+env.RoleID("storage_admin").String()).ExpectError(403, "admin_required")
	ua.Patch("/api/system/roles/"+env.RoleID("storage_admin").String(), map[string]any{"role_name": "改名不涉及菜单"}).Expect(200)
	ua.Delete("/api/system/roles/" + rFiles.String()).Expect(204)

	// 规则 3：新加的角色里的 system.* 必须是操作人菜单的子集。
	ua.Post("/api/system/users", map[string]any{
		"username": "x2", "display_name": "x2", "password": "secret1", "role_ids": []uuid.UUID{env.RoleID("storage_admin")},
	}).ExpectError(403, "admin_required")
	ua.Put(userPath(plainID)+"/roles", map[string]any{"role_ids": []uuid.UUID{env.RoleID("reviewer"), env.RoleID("storage_admin")}}).ExpectError(403, "admin_required")
	ua.Put(userPath(plainID)+"/roles", map[string]any{"role_ids": []uuid.UUID{env.RoleID("reviewer"), env.RoleID("user_admin")}}).Expect(200)

	// 规则 4：被改的用户现有的 system.* 必须是操作人菜单的子集。
	ua.Patch(userPath(saID), map[string]any{"display_name": "x"}).ExpectError(403, "admin_required")
	ua.Post(userPath(saID)+"/reset-password", map[string]any{"password": "hacked1"}).ExpectError(403, "admin_required")
	ua.Post(userPath(saID)+"/disable", nil).ExpectError(403, "admin_required")
	ua.Put(userPath(saID)+"/roles", map[string]any{"role_ids": []uuid.UUID{}}).ExpectError(403, "admin_required")

	// 没有越权的修改照常可以：收回原有的非 admin 角色、改资料、停用。
	ua.Put(userPath(plainID)+"/roles", map[string]any{"role_ids": []uuid.UUID{}}).Expect(200)
	ua.Patch(userPath(plainID), map[string]any{"display_name": "普通"}).Expect(200)
	ua.Post(userPath(plainID)+"/disable", nil).Expect(200)

	// admin 不受限制。
	admin.Patch(userPath(saID), map[string]any{"display_name": "存储管理员"}).Expect(http.StatusOK)
	admin.Put("/api/system/roles/"+env.RoleID("user_admin").String()+"/menus", map[string]any{"menu_codes": []string{"system.users"}}).Expect(200)
	// 操作人的权限按当时的库算：user_admin 刚被去掉 system.roles，ua 马上不能管理角色。
	ua.Get("/api/system/menus").ExpectError(403, "forbidden")
}
