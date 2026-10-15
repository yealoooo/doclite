package apitest

import (
	"strings"
	"testing"
)

// TestPermsSession 是会话接口的菜单权限：不用登录的、登录即可的。
func TestPermsSession(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                         anon  admin  none
	GET    /api/health         Y     Y     Y
	POST   /api/auth/login     Y     Y     Y
	GET    /api/me             -     Y     Y
	GET    /api/me/menus       -     Y     Y
	GET    /api/projects       -     Y     Y
	`)
}

// TestPermsSystem 是用户、角色接口的菜单权限。
func TestPermsSystem(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	CheckPerms(t, env, `
	                                          admin  system.users  system.roles  manager  scanner  reviewer  cataloger  none
	GET    /api/system/users                    Y        Y             -            -        -        -         -        -
	POST   /api/system/users                    Y        Y             -            -        -        -         -        -
	GET    /api/system/users/{id}               Y        Y             -            -        -        -         -        -
	PATCH  /api/system/users/{id}               Y        Y             -            -        -        -         -        -
	POST   /api/system/users/{id}/disable       Y        Y             -            -        -        -         -        -
	POST   /api/system/users/{id}/enable        Y        Y             -            -        -        -         -        -
	POST   /api/system/users/{id}/reset-password Y       Y             -            -        -        -         -        -
	PUT    /api/system/users/{id}/roles         Y        Y             -            -        -        -         -        -
	GET    /api/system/roles                    Y        Y             Y            -        -        -         -        -
	POST   /api/system/roles                    Y        -             Y            -        -        -         -        -
	GET    /api/system/roles/{id}               Y        -             Y            -        -        -         -        -
	PATCH  /api/system/roles/{id}               Y        -             Y            -        -        -         -        -
	DELETE /api/system/roles/{id}               Y        -             Y            -        -        -         -        -
	PUT    /api/system/roles/{id}/menus         Y        -             Y            -        -        -         -        -
	GET    /api/system/menus                    Y        -             Y            -        -        -         -        -
	`)
}

// TestBuiltinRoleMenus：内置角色的菜单（requirements 第 4 节的表），多个角色取并集。
func TestBuiltinRoleMenus(t *testing.T) {
	t.Parallel()
	env := NewEnv(t)
	want := map[string]string{
		"manager":   "dashboard,files,scan,review,review_assign,catalog,export",
		"scanner":   "files,scan",
		"reviewer":  "files,review",
		"cataloger": "files,catalog",
		"none":      "",
	}
	for name, menus := range want {
		var got []myMenu
		env.As(name).Get("/api/me/menus").Expect(200).JSON(&got)
		var codes []string
		for _, m := range got {
			codes = append(codes, m.MenuCode)
		}
		if strings.Join(codes, ",") != menus {
			t.Errorf("%s 的菜单：%v，期望 %s", name, codes, menus)
		}
	}
	// 多个角色取并集，按 sort_no 排序，没有分组本身；system.* 带 parent_code。
	env.CreateUser("multi", "reviewer", "cataloger")
	env.CreateRole("ua", "system.users")
	env.CreateUser("multi2", "scanner", "ua")
	var got []myMenu
	env.Login("multi", DefaultPassword).Get("/api/me/menus").Expect(200).JSON(&got)
	if len(got) != 3 || got[0].MenuCode != "files" || got[1].MenuCode != "review" || got[2].MenuCode != "catalog" {
		t.Fatalf("并集：%+v", got)
	}
	env.Login("multi2", DefaultPassword).Get("/api/me/menus").Expect(200).JSON(&got)
	if len(got) != 3 || got[2].MenuCode != "system.users" || got[2].ParentCode == nil || *got[2].ParentCode != "system" ||
		got[2].RoutePath == nil || *got[2].RoutePath != "/system/users" || got[2].MenuName != "用户" {
		t.Fatalf("system.users：%+v", got)
	}
}
