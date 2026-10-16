package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// 许可证对登录和请求的限制（requirements 第 11 节，M7-1）。规则在 internal/license，这里只在登录和每个请求时问它：
//   - 持有内置 admin 角色的人不受限制（会话照样计数）；
//   - 别人登录：许可证没导入、验不过 403 `license_missing`，已到期 403 `license_expired`，在线会话数（含 admin 的）
//     ≥ 席位数 403 `license_seats_full`；
//   - 别人已登录的请求：许可证到期（或没有有效的）时 403 `license_expired`（`license_missing`）。只看内存里的状态。
//
// 在线会话 = 没撤销、没到期、最近 30 分钟有活动（last_seen_at；打开的实时通道每次复核会话时也算活动）。

// OnlineWindow：last_seen_at 在这么久之内的会话算在线。
const OnlineWindow = 30 * time.Minute

// License 是许可证的限制（internal/license 实现）。没设置时不检查。
type License interface {
	// LoginLimit：非 admin 登录时、写会话之前调用。返回 error 时拒绝登录；seats > 0 时还要求在线会话数 < seats。
	LoginLimit() (seats int, err error)
	// RequestCheck：非 admin 的每个已登录请求调用（只看内存里的状态）。
	RequestCheck() error
	// Notice 是给管理许可证的人（有 system.license 菜单）看的提醒，放进 GET /api/me 的 license_notice；
	// 没有要提醒的时返回 nil。
	Notice() any
}

// SetLicense 设置许可证的限制（app 组装时调用一次，在开始处理请求之前）。
func (s *Service) SetLicense(l License) { s.license = l }

// lockClassSeats 是席位检查的咨询锁：检查在线会话数和写会话串行，两个人同时登录不会一起占到最后一个席位。
const lockClassSeats = 6011

func seatsFull(seats, online int) error {
	return httpx.NewError(http.StatusForbidden, httpx.CodeLicenseSeatsFull,
		"在线人数已达到许可证的上限，请等别人退出后再登录，或联系管理员").
		WithDetails(map[string]int{"max_users": seats, "online_sessions": online})
}

// checkLoginLicense 在登录的事务里（已锁住用户行）检查许可证；admin 不受限制。
func (s *Service) checkLoginLicense(ctx context.Context, q db.Querier, cred *credentials) error {
	if s.license == nil {
		return nil
	}
	seats, limitErr := s.license.LoginLimit()
	if limitErr == nil && seats <= 0 {
		return nil // 不检查（开发版）
	}
	admin, err := userIsAdmin(ctx, q, cred.UserID)
	if err != nil || admin {
		return err
	}
	if limitErr != nil {
		return limitErr
	}
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, 0)`, lockClassSeats); err != nil {
		return err
	}
	online, err := countOnlineSessions(ctx, q)
	if err != nil {
		return err
	}
	if online >= seats {
		return seatsFull(seats, online)
	}
	return nil
}

// CountOnlineSessions 是当前在线的会话数（含 admin 的）。
func (s *Service) CountOnlineSessions(ctx context.Context) (int, error) {
	return countOnlineSessions(ctx, s.pool)
}

func countOnlineSessions(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM user_sessions
WHERE revoked_at IS NULL AND expires_at > now() AND last_seen_at > now() - make_interval(secs => $1)`,
		OnlineWindow.Seconds()).Scan(&n)
	return n, err
}

// userIsAdmin：用户有没有内置的 admin 角色（跨模块的只读查询）。
func userIsAdmin(ctx context.Context, q db.Querier, userID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1 AND r.role_code = '`+AdminRoleCode+`')`, userID).Scan(&ok)
	return ok, err
}
