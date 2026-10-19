package auth

import "strings"

// 菜单码，和迁移里 menus 的初始数据一致（requirements 第 4 节）。
// 新增菜单时：写一个新迁移插入 menus，同时给 admin 角色插入 role_menus（admin 的菜单在界面上不能改），
// 再在这里加常量。
const (
	MenuDashboard      = "dashboard"
	MenuFiles          = "files"
	MenuScan           = "scan"
	MenuReview         = "review"
	MenuReviewAssign   = "review_assign"
	MenuCatalog        = "catalog"
	MenuExport         = "export"
	MenuSystem         = "system" // 分组，不能分配给角色
	MenuSystemUsers    = "system.users"
	MenuSystemRoles    = "system.roles"
	MenuSystemProjects = "system.projects"
	MenuSystemStorage  = "system.storage"
	MenuSystemLicense  = "system.license"
	MenuSystemAudit    = "system.audit"
)

// AdminRoleCode 是内置的系统管理员角色。
const AdminRoleCode = "admin"

// SystemMenuPrefix 是系统管理菜单的前缀（防提权规则只管这类菜单）。
const SystemMenuPrefix = "system."

// IsSystemMenu 判断是不是系统管理菜单（`system.` 开头）。
func IsSystemMenu(code string) bool { return strings.HasPrefix(code, SystemMenuPrefix) }

// Menu 是一个菜单（`menus` 的一行）。
type Menu struct {
	MenuCode   string  `json:"menu_code"`
	ParentCode *string `json:"parent_code"`
	MenuName   string  `json:"menu_name"`
	MenuType   string  `json:"menu_type"`
	RoutePath  *string `json:"route_path"`
	SortNo     int     `json:"sort_no"`
}
