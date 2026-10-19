package apitest

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type roleItem struct {
	ID          uuid.UUID `json:"id"`
	RoleCode    string    `json:"role_code"`
	RoleName    string    `json:"role_name"`
	Description *string   `json:"description"`
	IsBuiltin   bool      `json:"is_builtin"`
	MenuCodes   []string  `json:"menu_codes"`
	UserCount   int       `json:"user_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type menuItem struct {
	MenuCode   string  `json:"menu_code"`
	ParentCode *string `json:"parent_code"`
	MenuName   string  `json:"menu_name"`
	MenuType   string  `json:"menu_type"`
	RoutePath  *string `json:"route_path"`
	SortNo     int     `json:"sort_no"`
}

func TestRolesCRUD(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	admin := env.Admin()

	// 内置角色按固定顺序在前；admin 有全部菜单（按 sort_no）。
	var roles []roleItem
	admin.Get("/api/system/roles").Expect(200).JSON(&roles)
	var codes []string
	for _, r := range roles {
		codes = append(codes, r.RoleCode)
	}
	if strings.Join(codes, ",") != "admin,manager,scanner,reviewer,cataloger" {
		t.Fatalf("角色顺序：%v", codes)
	}
	wantAdminMenus := "dashboard,files,scan,review,review_assign,catalog,export,system.users,system.roles,system.projects,system.storage,system.license,system.audit"
	if a := roles[0]; !a.IsBuiltin || a.UserCount != 1 || strings.Join(a.MenuCodes, ",") != wantAdminMenus || a.RoleName != "系统管理员" {
		t.Fatalf("admin 角色：%+v", a)
	}

	// 新建：去空白、空说明为 null、菜单去重并按 sort_no 排序。
	var r roleItem
	admin.Post("/api/system/roles", map[string]any{
		"role_code": " qc_lead ", "role_name": " 质检组长 ", "description": "  ", "menu_codes": []string{"review", "files", "files"},
	}).Expect(201).JSON(&r)
	if r.RoleCode != "qc_lead" || r.RoleName != "质检组长" || r.Description != nil || r.IsBuiltin || r.UserCount != 0 ||
		strings.Join(r.MenuCodes, ",") != "files,review" {
		t.Fatalf("新建的角色：%+v", r)
	}
	path := "/api/system/roles/" + r.ID.String()
	admin.Post("/api/system/roles", map[string]any{"role_code": "qc_lead", "role_name": "x"}).ExpectError(409, "conflict")
	for _, body := range []map[string]any{
		{"role_code": "QC", "role_name": "x"},
		{"role_code": "2qc", "role_name": "x"},
		{"role_code": "qc-lead", "role_name": "x"},
		{"role_code": "a" + strings.Repeat("b", 32), "role_name": "x"},
		{"role_code": "qc2", "role_name": " "},
		{"role_code": "qc2", "role_name": "x", "menu_codes": []string{"system"}},
		{"role_code": "qc2", "role_name": "x", "menu_codes": []string{"nope"}},
		{"role_code": "qc2", "role_name": "x", "description": strings.Repeat("说", 501)},
	} {
		admin.Post("/api/system/roles", body).ExpectError(400, "invalid_input")
	}

	admin.Get("/api/system/roles").Expect(200).JSON(&roles)
	if len(roles) != 6 || roles[5].RoleCode != "qc_lead" {
		t.Fatalf("自建角色应排在内置角色后面：%+v", roles)
	}
	admin.Get(path).Expect(200)
	admin.Get("/api/system/roles/"+uuid.NewString()).ExpectError(404, "not_found")

	// 修改名称、说明；空字符串清空说明；空修改不写库；内置角色也能改名。
	admin.Patch(path, map[string]any{"role_name": "质检", "description": " 管质检 "}).Expect(200).JSON(&r)
	if r.RoleName != "质检" || r.Description == nil || *r.Description != "管质检" {
		t.Fatalf("修改后：%+v", r)
	}
	before := r.UpdatedAt
	admin.Patch(path, map[string]any{"role_name": "质检"}).Expect(200).JSON(&r)
	if !r.UpdatedAt.Equal(before) {
		t.Fatal("没有变化时不应刷新 updated_at")
	}
	admin.Patch(path, map[string]any{"description": ""}).Expect(200).JSON(&r)
	if r.Description != nil {
		t.Fatalf("说明应被清空：%+v", r)
	}
	admin.Patch("/api/system/roles/"+env.RoleID("manager").String(), map[string]any{"role_name": "负责人"}).Expect(200)
	admin.Patch("/api/system/roles/"+uuid.NewString(), map[string]any{"role_name": "x"}).ExpectError(404, "not_found")

	// 改菜单。admin 的菜单不能改。
	admin.Put(path+"/menus", map[string]any{"menu_codes": []string{"scan", "files"}}).Expect(200).JSON(&r)
	if strings.Join(r.MenuCodes, ",") != "files,scan" {
		t.Fatalf("改菜单后：%v", r.MenuCodes)
	}
	admin.Put(path+"/menus", map[string]any{}).ExpectError(400, "invalid_input")
	admin.Put(path+"/menus", map[string]any{"menu_codes": []string{"system"}}).ExpectError(400, "invalid_input")
	admin.Put("/api/system/roles/"+env.RoleID("admin").String()+"/menus", map[string]any{"menu_codes": []string{"files"}}).ExpectError(409, "admin_menus_locked")

	// 用户数；删除自建角色时用户的这个角色一起去掉。
	uid := env.CreateUser("qc1", "qc_lead", "reviewer")
	admin.Get(path).Expect(200).JSON(&r)
	if r.UserCount != 1 {
		t.Fatalf("user_count：%d", r.UserCount)
	}
	admin.Delete("/api/system/roles/"+env.RoleID("manager").String()).ExpectError(409, "builtin_role")
	admin.Delete("/api/system/roles/"+uuid.NewString()).ExpectError(404, "not_found")
	admin.Delete(path).Expect(204)
	admin.Get(path).ExpectError(404, "not_found")
	var u userItem
	admin.Get("/api/system/users/" + uid.String()).Expect(200).JSON(&u)
	if got := u.roleCodes(); len(got) != 1 || got[0] != "reviewer" {
		t.Fatalf("删除角色后用户的角色：%v", got)
	}

	got := auditActions(env, r.ID)
	if strings.Join(got, ",") != "create,update,update,set_menus,delete" {
		t.Fatalf("角色的审计：%v", got)
	}

	// 全部菜单（含分组）。
	var menus []menuItem
	admin.Get("/api/system/menus").Expect(200).JSON(&menus)
	if len(menus) != 14 || menus[0].MenuCode != "dashboard" || menus[7].MenuCode != "system" || menus[7].MenuType != "group" ||
		menus[8].ParentCode == nil || *menus[8].ParentCode != "system" {
		t.Fatalf("全部菜单：%+v", menus)
	}
}
