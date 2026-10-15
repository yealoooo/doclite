package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/s3"
)

// 给其他模块用：按存储配置连 S3、写快照前的检查、给快照生成图片地址。

// ObjectRef 是一个快照的对象（file_snapshots 的 storage_profile_id、storage_key、cover_key）。
type ObjectRef struct {
	StorageProfileID uuid.UUID
	StorageKey       string
	// CoverKey 为 nil 表示没有封面。
	CoverKey *string
}

// URLError 是生成不了图片地址的原因。
type URLError struct {
	// Code：storage_unavailable（存储已停用、不存在）、secret_unreadable（密钥解不开）、presign_failed（签名失败）。
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ImageURLs 是一个快照的图片地址（接口 SnapshotImageFields 里的 cover_url、original_url、url_expires_at、url_error）。
// 可以直接嵌进响应结构体。
type ImageURLs struct {
	// CoverURL：没有封面、或生成不了地址时为 null。
	CoverURL *string `json:"cover_url"`
	// OriginalURL：生成不了地址时为 null。
	OriginalURL  *string    `json:"original_url"`
	URLExpiresAt *time.Time `json:"url_expires_at"`
	// URLError 只在生成不了地址时出现。
	URLError *URLError `json:"url_error,omitempty"`
}

// ImageURLs 给一批快照生成预签名的图片地址（requirements 5.7），结果和 refs 一一对应。权限由调用方先查。
//   - 按快照所在存储的 public_endpoint 签，签名时间是 UTC+8 当天 0 点，同一天里同一张图的地址不变；
//   - 存储已停用（或不存在）：两个地址为 null，url_error storage_unavailable；只读的照常读；
//   - 密钥解不开：url_error secret_unreadable；签名失败：presign_failed。
//
// 只在查库出错时返回错误。M2-1 的 `files/batch`、`snapshots` 用它。
func (s *Service) ImageURLs(ctx context.Context, refs []ObjectRef) ([]ImageURLs, error) {
	out := make([]ImageURLs, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, 4)
	seen := map[uuid.UUID]bool{}
	for _, r := range refs {
		if !seen[r.StorageProfileID] {
			seen[r.StorageProfileID] = true
			ids = append(ids, r.StorageProfileID)
		}
	}
	rows, err := getRows(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	type target struct {
		cfg   s3.Config
		hours int
		err   *URLError
	}
	targets := map[uuid.UUID]*target{}
	for _, id := range ids {
		r := rows[id]
		t := &target{}
		switch {
		case r == nil || r.Status == StatusDisabled:
			t.err = &URLError{Code: CodeStorageUnavailable, Message: "文件所在的存储已停用，看不了图片"}
		default:
			secret, err := openSecret(s.mk, r.ID, r.SecretKeyEnc)
			if err != nil {
				t.err = &URLError{Code: CodeSecretUnreadable, Message: unreadableMessage(r, err)}
			} else {
				t.cfg, t.hours = s3Config(r, secret), r.URLExpireHours
			}
		}
		targets[id] = t
	}
	now := s.now()
	for i, ref := range refs {
		t := targets[ref.StorageProfileID]
		if t.err != nil {
			out[i].URLError = t.err
			continue
		}
		orig, err := s3.PresignGet(t.cfg, ref.StorageKey, t.hours, now)
		if err != nil {
			out[i].URLError = &URLError{Code: "presign_failed", Message: "生成图片地址失败：" + err.Error()}
			continue
		}
		out[i].OriginalURL = &orig.URL
		exp := orig.ExpiresAt
		out[i].URLExpiresAt = &exp
		if ref.CoverKey != nil && *ref.CoverKey != "" {
			cover, err := s3.PresignGet(t.cfg, *ref.CoverKey, t.hours, now)
			if err != nil {
				out[i] = ImageURLs{URLError: &URLError{Code: "presign_failed", Message: "生成图片地址失败：" + err.Error()}}
				continue
			}
			out[i].CoverURL = &cover.URL
		}
	}
	return out, nil
}

// Client 按存储配置建 S3 客户端（读对象：导出、复制时确认源存储）。存储已停用或不存在 409 `storage_unavailable`；
// 密钥解不开 500 `secret_unreadable`。
func (s *Service) Client(ctx context.Context, profileID uuid.UUID) (*s3.Client, error) {
	r, err := getRow(ctx, s.pool, profileID)
	if err != nil {
		return nil, err
	}
	if r == nil || r.Status == StatusDisabled {
		return nil, httpx.Conflict(CodeStorageUnavailable, "文件所在的存储已停用")
	}
	return s.clientOf(r)
}

func (s *Service) clientOf(r *row) (*s3.Client, error) {
	secret, err := openSecret(s.mk, r.ID, r.SecretKeyEnc)
	if err != nil {
		return nil, errSecretUnreadable(r, err)
	}
	return s3.New(s3Config(r, secret))
}

// Target 是写新对象的去处：默认存储。
type Target struct {
	ProfileID uuid.UUID
	// PathPrefix 是 key 前缀（空或以 / 结尾），key = PathPrefix + 项目编码 + "/" + file_id + "/v<no>.<ext>"（硬性规则 7）。
	PathPrefix     string
	URLExpireHours int
	Client         *s3.Client
}

// DefaultTarget 是当前的默认存储（上传、重扫、导出写对象用）。没有默认存储 409 `storage_unavailable`；
// 密钥解不开 500 `secret_unreadable`。之后写快照的事务里还要用 [Service.LockForSnapshot] 确认它仍是默认。
func (s *Service) DefaultTarget(ctx context.Context) (*Target, error) {
	r, err := getDefaultRow(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpx.Conflict(CodeStorageUnavailable, "还没有默认存储，请管理员到系统管理的存储配置里设置")
	}
	c, err := s.clientOf(r)
	if err != nil {
		return nil, err
	}
	return &Target{ProfileID: r.ID, PathPrefix: r.PathPrefix, URLExpireHours: r.URLExpireHours, Client: c}, nil
}

// LockForSnapshot 在写快照的事务里调用（插入 file_snapshots 之前）：锁住存储配置行（FOR KEY SHARE，和停用、删除互斥），
// 确认它仍是 active 而且仍是默认，否则 409 `storage_changed`（传对象期间换了默认存储或改了状态，客户端重试即可）。
// 加锁顺序见 requirements 3.6（存储配置行在 folders 行之后、内容锁之前）。
func (s *Service) LockForSnapshot(ctx context.Context, tx pgx.Tx, profileID uuid.UUID) error {
	status, isDefault, found, err := lockForSnapshot(ctx, tx, profileID)
	if err != nil {
		return err
	}
	if !found || status != StatusActive || !isDefault {
		return httpx.Conflict(CodeStorageChanged, "上传期间默认存储变了，请重试")
	}
	return nil
}

// LockForReuse 在复用已有对象的事务里调用（复制文件：新快照指向源快照的 key，不写字节）：锁住源快照所在的存储配置行
// （FOR KEY SHARE，和停用、删除互斥），只要求它不是 disabled（只读的也行，不要求是默认），否则 409
// `storage_unavailable`。加锁顺序同 [Service.LockForSnapshot]。
func (s *Service) LockForReuse(ctx context.Context, tx pgx.Tx, profileID uuid.UUID) error {
	status, _, found, err := lockForSnapshot(ctx, tx, profileID)
	if err != nil {
		return err
	}
	if !found || status == StatusDisabled {
		return httpx.Conflict(CodeStorageUnavailable, "源文件所在的存储已停用，不能复制")
	}
	return nil
}

// PresignDownload 给导出物生成下载地址（requirements 5.11"下载"）：按存储的 public_endpoint 签，签名时间按天取整
// （UTC+8），有效期按这个存储的 url_expire_hours，带 response-content-disposition（disposition 为空时不带）。
// 存储已停用或不存在 409 `storage_unavailable`；密钥解不开 500 `secret_unreadable`。
func (s *Service) PresignDownload(ctx context.Context, profileID uuid.UUID, key, disposition string) (s3.Presigned, error) {
	r, err := getRow(ctx, s.pool, profileID)
	if err != nil {
		return s3.Presigned{}, err
	}
	if r == nil || r.Status == StatusDisabled {
		return s3.Presigned{}, httpx.Conflict(CodeStorageUnavailable, "导出物所在的存储已停用，不能下载")
	}
	secret, err := openSecret(s.mk, r.ID, r.SecretKeyEnc)
	if err != nil {
		return s3.Presigned{}, errSecretUnreadable(r, err)
	}
	return s3.PresignGetWith(s3Config(r, secret), key, r.URLExpireHours, s.now(), s3.PresignOptions{ContentDisposition: disposition})
}
