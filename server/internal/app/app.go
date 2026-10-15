// Package app 组装各模块、注册路由、启动和优雅退出。
//
// 新模块接入：在 New 里建它的 Service、Handler，调 Handler.Routes(api, gate) 注册路由。
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/auth"
	"doclite/server/internal/config"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
	"doclite/server/internal/role"
	"doclite/server/internal/user"
)

// Version 是 server 的版本，构建时可以用 -ldflags "-X doclite/server/internal/app.Version=…" 覆盖。
var Version = "0.1.0"

// Options 是组装用的配置（从 config.Config 来；测试里直接给）。
type Options struct {
	SessionTTL     time.Duration
	CookieSecure   bool
	Password       auth.PasswordParams // 零值表示用 auth.DefaultPasswordParams
	ClientVersions config.ClientVersions
	// WebDir 是网页构建产物目录；为空时不托管网页（`/api` 以外的路径一律 404）。
	WebDir string
}

// App 是组装好的 server。
type App struct {
	log     *slog.Logger
	handler http.Handler

	Auth     *auth.Service
	Users    *user.Service
	Roles    *role.Service
	Projects *project.Service
}

// New 组装各模块。数据库迁移要在这之前执行（Run 里先迁移）。
func New(pool *pgxpool.Pool, opts Options, log *slog.Logger) (*App, error) {
	authSvc, err := auth.NewService(pool, auth.Options{
		SessionTTL:   opts.SessionTTL,
		CookieSecure: opts.CookieSecure,
		Password:     opts.Password,
	})
	if err != nil {
		return nil, err
	}
	roleSvc := role.NewService(pool, authSvc)
	userSvc := user.NewService(pool, authSvc, roleSvc)
	projectSvc := project.NewService(pool)

	gate := auth.NewGate(authSvc)
	api := httpx.NewRouter()
	api.Handle("GET /api/health", gate.Public(), health)
	auth.NewHandler(authSvc).Routes(api, gate)
	user.NewHandler(userSvc).Routes(api, gate)
	role.NewHandler(roleSvc).Routes(api, gate)
	project.NewHandler(projectSvc).Routes(api, gate)

	// /api 的最外层是客户端版本检查（先于鉴权）；/api 以外是网页。
	apiHandler := auth.ClientVersionCheck(opts.ClientVersions)(api)
	root := http.NewServeMux()
	root.Handle("/api", apiHandler)
	root.Handle("/api/", apiHandler)
	if opts.WebDir != "" {
		root.Handle("/", httpx.WebHandler(opts.WebDir))
	} else {
		root.Handle("/", http.NotFoundHandler())
	}

	return &App{
		log:      log,
		handler:  httpx.Logging(log)(root),
		Auth:     authSvc,
		Users:    userSvc,
		Roles:    roleSvc,
		Projects: projectSvc,
	}, nil
}

// Handler 是整个 server 的 http.Handler（含请求日志）。
func (a *App) Handler() http.Handler { return a.handler }

// EnsureInitialAdmin 首次启动时建初始管理员（库里没有用户时）。
func (a *App) EnsureInitialAdmin(ctx context.Context, admin *config.InitAdmin) error {
	if admin == nil {
		return a.Users.EnsureInitialAdmin(ctx, "", "", "", false, a.log)
	}
	return a.Users.EnsureInitialAdmin(ctx, admin.Username, admin.DisplayName, admin.Password, true, a.log)
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// health 是 `GET /api/health`：进程活着就返回 200，不用登录，不检查客户端版本。
func health(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: Version})
	return nil
}
