// Package auth 是登录、会话、菜单权限、项目授权检查（project.go）。
//
// 会话规则（requirements 5.1）：
//   - HttpOnly Cookie `doclite_session`（SameSite=Lax、Path=/，Secure 可配），值是 32 字节随机数的 base64url；
//     库里只存它的 sha256；
//   - 有效期从登录起算、不续期；到期、撤销（退出、停用、重置密码）、用户停用后立即失效（401）；
//   - last_seen_at 每分钟最多写一次；
//   - 登录读 `X-Client-Type`（browser 缺省、desktop，其他 400），记进会话。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

const (
	// lastSeenInterval：距离上次记录超过这么久才更新 last_seen_at。
	lastSeenInterval  = time.Minute
	maxUserAgentRunes = 512
)

// Options 是 auth 的配置。
type Options struct {
	SessionTTL   time.Duration
	CookieSecure bool
	Password     PasswordParams
}

// Service 是登录和会话的业务规则。
type Service struct {
	pool      *pgxpool.Pool
	opts      Options
	hasher    *hasher
	dummyHash string
}

// NewService 建 Service。会预先算一个占位哈希（登录名不存在时拿它做一次校验，让"用户不存在"和"密码错"耗时相同）。
func NewService(pool *pgxpool.Pool, opts Options) (*Service, error) {
	if opts.Password == (PasswordParams{}) {
		opts.Password = DefaultPasswordParams
	}
	s := &Service{pool: pool, opts: opts, hasher: newHasher(opts.Password)}
	dummy, err := s.hasher.Hash(context.Background(), "doclite-dummy-password")
	if err != nil {
		return nil, err
	}
	s.dummyHash = dummy
	return s, nil
}

// HashPassword 计算密码哈希（新建用户、重置密码、初始管理员用）。排队超时返回 503 `busy`。
func (s *Service) HashPassword(ctx context.Context, password string) (string, error) {
	return s.hasher.Hash(ctx, password)
}

// RevokeUserSessions 在调用方的事务里撤销某个用户所有没撤销的会话（停用、重置密码），返回撤销的个数。
func (s *Service) RevokeUserSessions(ctx context.Context, q db.Querier, userID uuid.UUID) (int64, error) {
	return revokeUserSessions(ctx, q, userID)
}

// ClientInfo 是发起登录的客户端。
type ClientInfo struct {
	// ClientType 取自请求头 X-Client-Type：browser（缺省）或 desktop。
	ClientType string
	// IP 是 TCP 对端地址（不信任 X-Forwarded-For）。
	IP *netip.Addr
	// UserAgent 截到 512 个字符。
	UserAgent *string
}

// ParseClientInfo 读请求的客户端信息。X-Client-Type 不是 browser、desktop 时 400 `invalid_input`。
func ParseClientInfo(r *http.Request) (ClientInfo, error) {
	ci := ClientInfo{ClientType: "browser"}
	if v := r.Header.Get("X-Client-Type"); v != "" {
		switch t := strings.ToLower(strings.TrimSpace(v)); t {
		case "browser", "desktop":
			ci.ClientType = t
		default:
			return ci, httpx.Invalid("客户端类型只能是 browser 或 desktop，收到的是：%s", truncate(v, 32))
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		addr := ap.Addr().Unmap()
		ci.IP = &addr
	}
	if ua := r.UserAgent(); ua != "" {
		ua = truncate(ua, maxUserAgentRunes)
		ci.UserAgent = &ua
	}
	return ci, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// LoginResult 是登录成功的结果：令牌写进 Cookie，库里只有它的哈希。
type LoginResult struct {
	Token     string
	UserID    uuid.UUID
	ExpiresAt time.Time
}

var (
	errInvalidCredentials = httpx.NewError(http.StatusUnauthorized, httpx.CodeInvalidCredentials, "登录名或密码错误")
	errUserDisabled       = httpx.NewError(http.StatusForbidden, httpx.CodeUserDisabled, "账号已停用，请联系管理员")
)

// Login 校验登录名和密码，新建会话：
//   - 登录名不存在和密码错都是 401 `invalid_credentials`，耗时相同；
//   - 密码对但用户已停用 403 `user_disabled`；
//   - 写会话前在事务里锁住用户行再核对一次密码哈希和状态：校验之后才提交的重置密码、停用同样生效。
func (s *Service) Login(ctx context.Context, username, password string, ci ClientInfo) (*LoginResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, httpx.Invalid("请输入登录名和密码")
	}
	cred, err := findCredentials(ctx, s.pool, username)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		if _, err := s.hasher.Verify(ctx, password, s.dummyHash); err != nil {
			return nil, err
		}
		return nil, errInvalidCredentials
	}
	ok, err := s.hasher.Verify(ctx, password, cred.PasswordHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errInvalidCredentials
	}
	if cred.Status != "active" {
		return nil, errUserDisabled
	}

	token, hash, err := newToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := newSession{
		ID:         db.NewID(),
		UserID:     cred.UserID,
		TokenHash:  hash,
		ClientType: ci.ClientType,
		IP:         ci.IP,
		UserAgent:  ci.UserAgent,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.opts.SessionTTL),
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		curHash, status, found, err := lockUserForLogin(ctx, tx, cred.UserID)
		if err != nil {
			return err
		}
		if !found || curHash != cred.PasswordHash {
			return errInvalidCredentials // 校验之后密码被重置了
		}
		if status != "active" {
			return errUserDisabled // 校验之后被停用了
		}
		if err := insertSession(ctx, tx, sess); err != nil {
			return err
		}
		return setLastLogin(ctx, tx, cred.UserID, now)
	})
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, UserID: cred.UserID, ExpiresAt: sess.ExpiresAt}, nil
}

// Logout 撤销令牌对应的会话；令牌无效或会话早已失效时什么都不做。
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return revokeSession(ctx, s.pool, hashToken(token))
}

// Principal 是当前登录的用户和会话。
type Principal struct {
	UserID           uuid.UUID
	Username         string
	DisplayName      string
	SessionID        uuid.UUID
	SessionExpiresAt time.Time
	ClientType       string
	// menus 是菜单码（所有角色的并集），第一次用到时查库，同一个请求里只查一次。
	menus map[string]bool
}

// Authenticate 按 Cookie 里的令牌识别当前用户。令牌不存在、会话过期或已撤销、用户已停用都是 401 `unauthorized`。
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, httpx.Unauthorized()
	}
	row, err := findSession(ctx, s.pool, hashToken(token))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if row == nil || row.Revoked || !now.Before(row.ExpiresAt) || row.UserStatus != "active" {
		return nil, httpx.Unauthorized()
	}
	if now.Sub(row.LastSeenAt) >= lastSeenInterval {
		if err := touchSession(ctx, s.pool, row.SessionID); err != nil {
			return nil, err
		}
	}
	return &Principal{
		UserID:           row.UserID,
		Username:         row.Username,
		DisplayName:      row.DisplayName,
		SessionID:        row.SessionID,
		SessionExpiresAt: row.ExpiresAt,
		ClientType:       row.ClientType,
	}, nil
}

// MenuCodes 是当前用户的菜单码（所有角色的并集）；同一个请求里只查一次库。
func (s *Service) MenuCodes(ctx context.Context, p *Principal) (map[string]bool, error) {
	if p.menus == nil {
		m, err := menuCodesOfUser(ctx, s.pool, p.UserID)
		if err != nil {
			return nil, err
		}
		p.menus = m
	}
	return p.menus, nil
}

// MyMenus 是用户的菜单（所有角色的并集，不含分组），按 sort_no 排序。
func (s *Service) MyMenus(ctx context.Context, userID uuid.UUID) ([]Menu, error) {
	return menusOfUser(ctx, s.pool, userID)
}

// AllMenus 是全部菜单（含分组），按 sort_no 排序。
func (s *Service) AllMenus(ctx context.Context) ([]Menu, error) {
	return allMenus(ctx, s.pool)
}

// Me 是 `GET /api/me` 和登录的响应。
type Me struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	// Roles 按 role_code 排序。
	Roles []MeRole `json:"roles"`
}

// MeRole 是 Me 里的一个角色。
type MeRole struct {
	RoleCode string `json:"role_code"`
	RoleName string `json:"role_name"`
}

// GetMe 读当前用户（含角色）。
func (s *Service) GetMe(ctx context.Context, userID uuid.UUID) (*Me, error) {
	me, err := findMe(ctx, s.pool, userID)
	if err != nil {
		return nil, err
	}
	roles, err := rolesOfUser(ctx, s.pool, userID)
	if err != nil {
		return nil, err
	}
	me.Roles = make([]MeRole, 0, len(roles))
	for _, r := range roles {
		me.Roles = append(me.Roles, MeRole{RoleCode: r.RoleCode, RoleName: r.RoleName})
	}
	return me, nil
}

// newToken 生成会话令牌（32 字节随机数的 base64url，无填充，43 个字符）和它的 sha256。
func newToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

// hashToken 是令牌原文（字符串的 UTF-8 字节）的 sha256，即 user_sessions.token_hash。
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
