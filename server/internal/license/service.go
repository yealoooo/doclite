package license

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 规则（requirements 第 11 节，用户 2026-10-16）：
//   - 没编进公钥（开发构建）时不检查许可证：谁都能登录，不能导入，状态 `dev`；
//   - 席位按登录会话数算（同一个人两处登录算 2），在线会话的定义和登录、请求时的检查在 auth/license.go；
//   - 持有内置 admin 角色的人总能登录（不受席位、到期、未导入的限制），他们的会话照样计数；
//   - 到期：非 admin 不能登录、已登录的下一次请求被拒（403 `license_expired`）；数据不动，导入新许可证立即恢复；
//   - 没导入（或库里的验不过）：只有 admin 能登录（403 `license_missing`）；
//   - 到期前 30 天起提醒（GET /api/me 的 license_notice，只给有 system.license 菜单的人）。
//
// 许可证的状态缓存在内存里（启动时读库验签，导入时刷新），登录和每个请求只看内存，不查库；时间每次现算。

// 状态（GET /api/system/license 的 status）。
const (
	StatusDev      = "dev"      // 开发构建（没编进公钥），不检查
	StatusMissing  = "missing"  // 还没导入
	StatusValid    = "valid"    // 有效
	StatusExpiring = "expiring" // 有效，30 天内到期
	StatusExpired  = "expired"  // 已到期
	StatusInvalid  = "invalid"  // 库里的许可证验不过（换了公钥、install_id 不符……），当作没导入
)

// ExpiringWithin：离到期不到这么久时提醒。
const ExpiringWithin = 30 * 24 * time.Hour

// 审计的 action（target_type = license，target_id = 许可证的 license_id）。
const (
	ActionImport  = "import"  // 第一次导入
	ActionReplace = "replace" // 替换已有的
)

// Options 是许可证的配置。
type Options struct {
	// PublicKey 是验签的公钥；nil 表示开发构建，不检查许可证。正式的公钥编进程序（[BuiltinPublicKey]），
	// 测试通过它注入测试公钥。
	PublicKey ed25519.PublicKey
	// Now 是时钟，只给测试用；nil 用 time.Now。
	Now func() time.Time
}

// Service 是许可证：install_id、导入、状态，以及登录和请求的限制（实现 auth.License）。
type Service struct {
	pool *pgxpool.Pool
	auth *auth.Service
	pub  ed25519.PublicKey
	now  func() time.Time

	mu         sync.RWMutex
	installID  string
	lic        *Payload // 验签通过的许可证；nil = 没导入或验不过
	importedAt time.Time
	importedBy *string
	invalid    string // 库里有许可证但验不过的原因
}

// NewService 建 Service。要在处理请求之前调 [Service.Init]。
func NewService(pool *pgxpool.Pool, authSvc *auth.Service, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{pool: pool, auth: authSvc, pub: opts.PublicKey, now: now}
}

// Dev 是不是开发构建（不检查许可证）。
func (s *Service) Dev() bool { return s.pub == nil }

// Init 启动时调用：生成 install_id（首次启动）、读导入的许可证并验签，放进内存。
func (s *Service) Init(ctx context.Context) error { return s.Reload(ctx) }

// Reload 重新从库里读 install_id 和许可证（Init 和测试用）。
func (s *Service) Reload(ctx context.Context) error {
	id, err := ensureInstallID(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("读写 install_id 失败：%w", err)
	}
	st, err := loadLicense(ctx, s.pool, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installID = id
	s.lic, s.invalid, s.importedAt, s.importedBy = nil, "", time.Time{}, nil
	if st == nil || s.pub == nil {
		return nil
	}
	f, herr := s.check(st.Text, id)
	if herr != nil {
		s.invalid = herr.Message
		return nil
	}
	s.lic, s.importedAt, s.importedBy = &f.Payload, st.ImportedAt, st.ImportedBy
	return nil
}

// InstallID 是这套系统的 install_id。
func (s *Service) InstallID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.installID
}

// snapshot 是内存里的状态和现在的时间。
func (s *Service) snapshot() (status string, p *Payload, now time.Time) {
	now = s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return statusOf(s.pub == nil, s.lic, s.invalid, now), s.lic, now
}

func statusOf(dev bool, p *Payload, invalid string, now time.Time) string {
	switch {
	case dev:
		return StatusDev
	case p == nil && invalid != "":
		return StatusInvalid
	case p == nil:
		return StatusMissing
	case !now.Before(p.ExpiresAt):
		return StatusExpired
	case p.ExpiresAt.Sub(now) <= ExpiringWithin:
		return StatusExpiring
	}
	return StatusValid
}

var (
	errMissing = httpx.NewError(http.StatusForbidden, httpx.CodeLicenseMissing,
		"系统还没有导入有效的许可证，暂时只有管理员能登录，请联系管理员")
	errExpired = httpx.NewError(http.StatusForbidden, httpx.CodeLicenseExpired,
		"系统的许可证已到期，暂时只有管理员能登录，请联系管理员导入新的许可证")
)

// LoginLimit 实现 auth.License：非 admin 登录时的限制。
func (s *Service) LoginLimit() (int, error) {
	status, p, _ := s.snapshot()
	switch status {
	case StatusDev:
		return 0, nil
	case StatusValid, StatusExpiring:
		return int(p.MaxUsers), nil
	case StatusExpired:
		return 0, errExpired
	}
	return 0, errMissing
}

// RequestCheck 实现 auth.License：非 admin 已登录的请求。
func (s *Service) RequestCheck() error {
	switch status, _, _ := s.snapshot(); status {
	case StatusExpired:
		return errExpired
	case StatusMissing, StatusInvalid:
		return errMissing
	}
	return nil
}

// Notice 是 GET /api/me 的 license_notice（快到期、已到期、没导入、无效时）。
type Notice struct {
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
	// DaysLeft 是离到期还有几天（不足一天按一天；已到期、没有许可证时为空）。
	DaysLeft *int `json:"days_left"`
}

// Notice 实现 auth.License。
func (s *Service) Notice() any {
	status, p, now := s.snapshot()
	switch status {
	case StatusDev, StatusValid:
		return nil
	}
	n := &Notice{Status: status}
	if p != nil {
		exp := p.ExpiresAt
		n.ExpiresAt = &exp
		n.DaysLeft = daysLeft(p.ExpiresAt, now)
	}
	return n
}

func daysLeft(expires, now time.Time) *int {
	d := expires.Sub(now)
	if d <= 0 {
		return nil
	}
	n := int((d + 24*time.Hour - 1) / (24 * time.Hour))
	return &n
}

// Info 是许可证的内容（显示用）。
type Info struct {
	LicenseID string    `json:"license_id"`
	Company   string    `json:"company"`
	Edition   string    `json:"edition"`
	MaxUsers  int       `json:"max_users"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func infoOf(p *Payload) *Info {
	if p == nil {
		return nil
	}
	return &Info{LicenseID: p.LicenseID, Company: p.Company, Edition: p.Edition, MaxUsers: int(p.MaxUsers),
		IssuedAt: p.IssuedAt, ExpiresAt: p.ExpiresAt}
}

// View 是 GET、PUT /api/system/license 的响应。
type View struct {
	Status    string `json:"status"`
	InstallID string `json:"install_id"`
	// License 是导入的、验签通过的许可证；开发构建、没导入、验不过时为空。
	License  *Info `json:"license"`
	DaysLeft *int  `json:"days_left"`
	// OnlineSessions 是当前在线的登录会话数（含 admin 的）。
	OnlineSessions int `json:"online_sessions"`
	// InvalidReason 是库里的许可证验不过的原因（status = invalid 时）。
	InvalidReason *string    `json:"invalid_reason"`
	ImportedAt    *time.Time `json:"imported_at"`
	ImportedBy    *string    `json:"imported_by"`
}

// Get 是许可证的状态。
func (s *Service) Get(ctx context.Context) (*View, error) {
	online, err := s.auth.CountOnlineSessions(ctx)
	if err != nil {
		return nil, err
	}
	status, p, now := s.snapshot()
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := &View{Status: status, InstallID: s.installID, License: infoOf(p), OnlineSessions: online}
	if p != nil {
		v.DaysLeft = daysLeft(p.ExpiresAt, now)
		at := s.importedAt
		v.ImportedAt, v.ImportedBy = &at, s.importedBy
	}
	if status == StatusInvalid {
		reason := s.invalid
		v.InvalidReason = &reason
	}
	return v, nil
}

// cst 是显示日期用的时区（UTC+8）。
var cst = time.FixedZone("UTC+8", 8*3600)

func invalidLicense(reason, msg string) *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, httpx.CodeInvalidLicense, msg).
		WithDetails(map[string]string{"reason": reason})
}

// check 解析、验签、核对 install_id（不看是否过期）。
func (s *Service) check(text, installID string) (*File, *httpx.Error) {
	f, err := Decode(text)
	if err != nil {
		var fe *FormatError
		if errors.As(err, &fe) {
			return nil, invalidLicense("format", "不是有效的许可证文件（"+fe.Reason+"）")
		}
		return nil, invalidLicense("format", "不是有效的许可证文件")
	}
	if f.Version != 1 {
		return nil, invalidLicense("format", fmt.Sprintf("不支持这个版本的许可证文件（version %d）", f.Version))
	}
	if Verify(s.pub, f) != nil {
		return nil, invalidLicense("signature", "许可证的签名校验没有通过：文件被改动过，或者不是为这个系统签发的")
	}
	if _, err := uuid.Parse(f.Payload.LicenseID); err != nil || f.Payload.MaxUsers == 0 {
		return nil, invalidLicense("format", "不是有效的许可证文件（编号或席位数不对）")
	}
	if !strings.EqualFold(f.Payload.HardwareID, installID) {
		return nil, httpx.NewError(http.StatusBadRequest, httpx.CodeLicenseMismatch,
			"这份许可证是为另一套系统签发的（install_id 不符），请把本页显示的 install_id 发给供应商重新签发").
			WithDetails(map[string]string{"install_id": installID, "license_install_id": f.Payload.HardwareID})
	}
	return f, nil
}

var errDevImport = httpx.Conflict(httpx.CodeInvalidState, "开发版不检查许可证，不用导入")

// Import 导入（或替换）许可证：验签、核对 install_id、不能已经过期；写库、写审计，立即生效。和已导入的完全相同时
// 什么都不做（不写库、不审计）。
func (s *Service) Import(ctx context.Context, operator *auth.Principal, text string) (*View, error) {
	if s.Dev() {
		return nil, errDevImport
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, httpx.Invalid("请粘贴许可证文件的内容，或选择 .lic 文件")
	}
	if len(text) > 64<<10 {
		return nil, invalidLicense("format", "不是有效的许可证文件（太大）")
	}
	installID := s.InstallID()
	f, herr := s.check(text, installID)
	if herr != nil {
		return nil, herr
	}
	if !s.now().Before(f.Payload.ExpiresAt) {
		return nil, httpx.NewError(http.StatusBadRequest, httpx.CodeLicenseFileExpired,
			"这份许可证已于 "+f.Payload.ExpiresAt.In(cst).Format("2006-01-02 15:04")+" 到期，请导入新的许可证").
			WithDetails(map[string]any{"expires_at": f.Payload.ExpiresAt})
	}
	var importedAt time.Time
	changed := true
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockInstallID(ctx, tx); err != nil {
			return err
		}
		old, err := loadLicense(ctx, tx, true)
		if err != nil {
			return err
		}
		var before *File
		if old != nil {
			before, _ = Decode(old.Text)
		}
		if before != nil && bytes.Equal(Canonical(before.Payload), Canonical(f.Payload)) &&
			bytes.Equal(before.Signature, f.Signature) {
			changed = false
			return nil
		}
		if importedAt, err = saveLicense(ctx, tx, text+"\n", operator.UserID); err != nil {
			return err
		}
		action, detail := ActionImport, map[string]any{"after": auditInfo(&f.Payload)}
		if old != nil {
			action = ActionReplace
			if before != nil {
				detail["before"] = auditInfo(&before.Payload)
			}
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operator.UserID),
			TargetType: audit.TargetLicense,
			TargetID:   uuid.MustParse(f.Payload.LicenseID),
			Action:     action,
			Detail:     detail,
		})
	})
	if err != nil {
		return nil, err
	}
	if changed {
		by := operator.DisplayName
		s.mu.Lock()
		p := f.Payload
		s.lic, s.invalid, s.importedAt, s.importedBy = &p, "", importedAt, &by
		s.mu.Unlock()
	}
	return s.Get(ctx)
}

// auditInfo 是审计 detail 里的许可证摘要。
func auditInfo(p *Payload) map[string]any {
	return map[string]any{
		"license_id": p.LicenseID, "company": p.Company, "edition": p.Edition, "max_users": p.MaxUsers,
		"issued_at": p.IssuedAt, "expires_at": p.ExpiresAt,
	}
}
