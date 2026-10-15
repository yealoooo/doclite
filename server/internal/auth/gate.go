package auth

import (
	"context"
	"net/http"
	"time"

	"doclite/server/internal/platform/httpx"
)

// SessionCookie 是会话 Cookie 的名字。
const SessionCookie = "doclite_session"

// Gate 生成接口的访问控制中间件（硬性规则 10：每个接口声明自己需要的菜单码，中间件统一校验）：
//
//	r.Handle("GET /api/health", gate.Public(), h.health)                 // 不用登录
//	r.Handle("GET /api/me", gate.Login(), h.me)                          // 登录即可
//	r.Handle("GET /api/system/users", gate.AnyMenu(MenuSystemUsers), …)  // 有其中一个菜单即可
//	r.Handle("POST /api/archive-items/{id}/unlock",
//	         gate.AllMenus(MenuCatalog, MenuReviewAssign), …)            // 每个菜单都要有
//
// 没登录或会话失效 401 `unauthorized`；菜单不够 403 `forbidden`。菜单检查在解析参数之前
// （requirements 第 4 节的检查顺序：菜单 403 → 项目 404/403 → 参数 400 → …）。
// 项目授权（能不能进这个项目）由 M1-3 加在 handler 里。
type Gate struct {
	svc *Service
}

// NewGate 建 Gate。
func NewGate(svc *Service) *Gate { return &Gate{svc: svc} }

type principalKey struct{}

// Current 取当前登录的用户。只能在 Login、AnyMenu、AllMenus 保护的 handler 里用。
func Current(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// Public 不检查登录。
func (g *Gate) Public() httpx.Middleware {
	return func(next http.Handler) http.Handler { return next }
}

// Login 要求登录。
func (g *Gate) Login() httpx.Middleware {
	return g.require(nil)
}

// AnyMenu 要求有 codes 里的任意一个菜单。
func (g *Gate) AnyMenu(codes ...string) httpx.Middleware {
	if len(codes) == 0 {
		panic("auth: AnyMenu 至少要一个菜单码")
	}
	return g.require(func(menus map[string]bool) bool {
		for _, c := range codes {
			if menus[c] {
				return true
			}
		}
		return false
	})
}

// AllMenus 要求 codes 里的菜单都有。
func (g *Gate) AllMenus(codes ...string) httpx.Middleware {
	if len(codes) == 0 {
		panic("auth: AllMenus 至少要一个菜单码")
	}
	return g.require(func(menus map[string]bool) bool {
		for _, c := range codes {
			if !menus[c] {
				return false
			}
		}
		return true
	})
}

func (g *Gate) require(allowed func(map[string]bool) bool) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := g.svc.Authenticate(r.Context(), SessionToken(r))
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if allowed != nil {
				menus, err := g.svc.MenuCodes(r.Context(), p)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				if !allowed(menus) {
					httpx.WriteError(w, r, httpx.Forbidden())
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		})
	}
}

// SessionToken 取请求 Cookie 里的会话令牌；没有时为空。
func SessionToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// setSessionCookie 下发会话 Cookie：HttpOnly、SameSite=Lax、Path=/，Max-Age 等于会话有效期，Secure 按配置。
func setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// clearSessionCookie 让浏览器删掉会话 Cookie（同名同属性、空值、Max-Age=0）。
func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}
