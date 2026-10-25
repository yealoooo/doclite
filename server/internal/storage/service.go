// Package storage 是存储配置（requirements 5.4）：S3 兼容存储的连接信息、加密的密钥、状态规则、测试连接、
// 首次启动登记默认存储；另外给其他模块用：按存储配置连 S3（[Service.Client]、[Service.DefaultTarget]）、
// 写快照前的检查（[Service.LockForSnapshot]）、给快照生成图片地址（[Service.ImageURLs]）。
//
// 规则：
//   - secret_key 用 MASTER_KEY 加密（secret.go），绑定行 id；接口、审计、日志里永远没有明文和密文；
//     PATCH 不传或传空串表示不改；解不开时 secret_readable=false，用到时 500 `secret_unreadable`，界面提示重填；
//   - 状态 active ⇄ readonly → disabled（可再启用）：只有 active 能设默认、只能有一个默认；默认的不能改只读、停用、
//     删除（409 `storage_is_default`）；有文件（快照、OCR 结果）的不能停用（409 `storage_in_use`，可以改只读）；
//     只有非默认、没被任何数据引用的能删；设默认的不是 active 409 `storage_not_active`；
//   - 写操作在进程内排队（mu，requirements 3.6 的可简化：存储配置表锁用进程内互斥），再锁要改的行；
//   - 新建、修改、删除、设默认都写审计（target_type storage_profile）；没有实际变化不写库、不审计；
//   - 存储迁移（M7-2，migrate.go）：迁移进行中源存储不能设为默认、不能删除，目标存储不能改为只读或停用、不能删除
//     （409 invalid_state）；设默认时旧的默认存储行用 FOR UPDATE，等正在写快照的上传、重扫提交。
package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/config"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/names"
	"doclite/server/internal/platform/s3"
)

// 存储状态。
const (
	StatusActive   = "active"
	StatusReadonly = "readonly"
	StatusDisabled = "disabled"
)

// 错误码。
const (
	CodeStorageNotActive   = "storage_not_active"
	CodeStorageIsDefault   = "storage_is_default"
	CodeStorageInUse       = "storage_in_use"
	CodeStorageUnavailable = "storage_unavailable"
	CodeStorageChanged     = "storage_changed"
	CodeSecretUnreadable   = "secret_unreadable"
)

var errNotFound = httpx.NotFound("存储配置")

// Service 是存储配置的业务规则。
type Service struct {
	pool *pgxpool.Pool
	mk   config.MasterKey
	// mu：存储配置的写操作（新建、修改、删除、设默认、首次登记）在进程内排队。
	mu sync.Mutex
	// now 是当前时间（预签名按天取整用）。
	now func() time.Time
	// cache 是存储配置行的进程内缓存（见 cachedRows）。
	cache profileCache
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, mk config.MasterKey) *Service {
	return &Service{pool: pool, mk: mk, now: time.Now}
}

// Profile 是 `/api/system/storage-profiles` 的一条存储配置。永远不含 secret_key。
type Profile struct {
	ID             uuid.UUID `json:"id"`
	ProfileName    string    `json:"profile_name"`
	StorageType    string    `json:"storage_type"`
	Endpoint       string    `json:"endpoint"`
	PublicEndpoint *string   `json:"public_endpoint"`
	Bucket         string    `json:"bucket"`
	Region         string    `json:"region"`
	PathPrefix     string    `json:"path_prefix"`
	AccessKey      string    `json:"access_key"`
	// HasSecret 总是 true：secret_key 已保存（不返回）。
	HasSecret bool `json:"has_secret"`
	// SecretReadable：当前 MASTER_KEY 能解开保存的 secret_key；false 时要重填。
	SecretReadable bool      `json:"secret_readable"`
	ForcePathStyle bool      `json:"force_path_style"`
	URLExpireHours int       `json:"url_expire_hours"`
	Status         string    `json:"status"`
	IsDefault      bool      `json:"is_default"`
	Remark         *string   `json:"remark"`
	InUse          bool      `json:"in_use"`
	Deletable      bool      `json:"deletable"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// Migration 是这个存储最近一次迁出（M7-2，摘要：不带 remaining、failures）；没有迁过为 null。
	Migration *Migration `json:"migration"`
	// IncomingMigration 是正在往这个存储迁入的迁移（排队中、执行中；摘要）；没有为 null。
	IncomingMigration *Migration `json:"incoming_migration"`
}

func (s *Service) profileOf(r *row, u usage) Profile {
	_, err := openSecret(s.mk, r.ID, r.SecretKeyEnc)
	return Profile{
		ID: r.ID, ProfileName: r.ProfileName, StorageType: r.StorageType, Endpoint: r.Endpoint, PublicEndpoint: r.PublicEndpoint,
		Bucket: r.Bucket, Region: r.Region, PathPrefix: r.PathPrefix, AccessKey: r.AccessKey,
		HasSecret: true, SecretReadable: err == nil, ForcePathStyle: r.ForcePathStyle, URLExpireHours: r.URLExpireHours,
		Status: r.Status, IsDefault: r.IsDefault, Remark: r.Remark,
		InUse: u.HasContents, Deletable: !r.IsDefault && !u.HasReferences,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// List 是全部存储配置：默认的在最前，其余按创建时间。
func (s *Service) List(ctx context.Context) ([]Profile, error) {
	rows, err := listRows(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	latest, incoming, err := migrationSummaries(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		p := s.profileOf(r.row, r.usage)
		p.Migration, p.IncomingMigration = latest[p.ID], incoming[p.ID]
		out = append(out, p)
	}
	return out, nil
}

// Get 读一条；不存在 404。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Profile, error) {
	r, u, err := getRowWithUsage(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errNotFound
	}
	p := s.profileOf(r, u)
	latest, incoming, err := migrationSummaries(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	p.Migration, p.IncomingMigration = latest[id], incoming[id]
	return &p, nil
}

// ConnectionInput 是连接参数（新建、测试未保存的配置）。nil 表示没传，用默认值。
type ConnectionInput struct {
	Endpoint       string  `json:"endpoint"`
	PublicEndpoint *string `json:"public_endpoint"`
	Bucket         string  `json:"bucket"`
	Region         *string `json:"region"`
	PathPrefix     *string `json:"path_prefix"`
	AccessKey      string  `json:"access_key"`
	SecretKey      string  `json:"secret_key"`
	ForcePathStyle *bool   `json:"force_path_style"`
}

// connection 是校验过的连接参数。
type connection struct {
	endpoint       string
	publicEndpoint *string
	bucket         string
	region         string
	pathPrefix     string
	accessKey      string
	secretKey      string
	forcePathStyle bool
}

func (c connection) s3Config() s3.Config {
	cfg := s3.Config{Endpoint: c.endpoint, Bucket: c.bucket, Region: c.region, AccessKey: c.accessKey,
		SecretKey: c.secretKey, ForcePathStyle: c.forcePathStyle}
	if c.publicEndpoint != nil {
		cfg.PublicEndpoint = *c.publicEndpoint
	}
	return cfg
}

func validateConnection(in ConnectionInput) (connection, error) {
	var c connection
	var err error
	if c.endpoint, err = normalizeEndpoint("endpoint", in.Endpoint); err != nil {
		return c, err
	}
	if in.PublicEndpoint != nil {
		if c.publicEndpoint, err = normalizePublicEndpoint(*in.PublicEndpoint); err != nil {
			return c, err
		}
	}
	if c.bucket, err = normalizeBucket(in.Bucket); err != nil {
		return c, err
	}
	c.region = s3.DefaultRegion
	if in.Region != nil {
		if c.region, err = normalizeRegion(*in.Region); err != nil {
			return c, err
		}
	}
	c.pathPrefix = defaultPathPrefix
	if in.PathPrefix != nil {
		if c.pathPrefix, err = normalizePathPrefix(*in.PathPrefix); err != nil {
			return c, err
		}
	}
	if c.accessKey, err = normalizeAccessKey(in.AccessKey); err != nil {
		return c, err
	}
	if c.secretKey, err = normalizeSecretKey(in.SecretKey); err != nil {
		return c, err
	}
	c.forcePathStyle = true
	if in.ForcePathStyle != nil {
		c.forcePathStyle = *in.ForcePathStyle
	}
	return c, nil
}

// CreateInput 是新建的输入。
type CreateInput struct {
	ConnectionInput
	ProfileName    string  `json:"profile_name"`
	URLExpireHours *int    `json:"url_expire_hours"`
	Remark         *string `json:"remark"`
}

// newRow 校验新建的输入，生成要写入的一行（secret_key 已加密）。
func (s *Service) newRow(in CreateInput) (*row, error) {
	name, err := normalizeProfileName(in.ProfileName)
	if err != nil {
		return nil, err
	}
	c, err := validateConnection(in.ConnectionInput)
	if err != nil {
		return nil, err
	}
	hours := defaultURLExpireHours
	if in.URLExpireHours != nil {
		if hours, err = checkURLExpireHours(*in.URLExpireHours); err != nil {
			return nil, err
		}
	}
	var remark *string
	if in.Remark != nil {
		if remark, err = names.OptionalText(in.Remark, maxRemarkChars, "备注"); err != nil {
			return nil, httpx.Invalid("%s", err)
		}
	}
	r := &row{ID: db.NewID(), ProfileName: name, StorageType: "s3", Endpoint: c.endpoint, PublicEndpoint: c.publicEndpoint,
		Bucket: c.bucket, Region: c.region, PathPrefix: c.pathPrefix, AccessKey: c.accessKey,
		ForcePathStyle: c.forcePathStyle, URLExpireHours: hours, Status: StatusActive, Remark: remark}
	if r.SecretKeyEnc, err = sealSecret(s.mk, r.ID, c.secretKey); err != nil {
		return nil, err
	}
	return r, nil
}

// Create 新建（active，不是默认）。不测试连接（界面可以先测）。
func (s *Service) Create(ctx context.Context, operatorID uuid.UUID, in CreateInput) (*Profile, error) {
	r, err := s.newRow(in)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.forgetRows()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertRow(ctx, tx, r); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
			TargetID: r.ID, Action: "create", Detail: map[string]any{"after": auditFields(r)}})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, r.ID)
}

// auditFields 是审计里记录的字段（不含密钥和密文）。
func auditFields(r *row) map[string]any {
	return map[string]any{
		"profile_name": r.ProfileName, "endpoint": r.Endpoint, "public_endpoint": r.PublicEndpoint, "bucket": r.Bucket,
		"region": r.Region, "path_prefix": r.PathPrefix, "access_key": r.AccessKey, "force_path_style": r.ForcePathStyle,
		"url_expire_hours": r.URLExpireHours, "status": r.Status, "is_default": r.IsDefault, "remark": r.Remark,
	}
}

// diffFields 只留变了的字段。
func diffFields(before, after map[string]any) (map[string]any, map[string]any) {
	b, a := map[string]any{}, map[string]any{}
	for k, av := range after {
		bv := before[k]
		if fmt.Sprint(deref(bv)) != fmt.Sprint(deref(av)) {
			b[k], a[k] = bv, av
		}
	}
	return b, a
}

func deref(v any) any {
	if p, ok := v.(*string); ok {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	return v
}

// UpdateInput 是修改的输入：nil 表示不改。PublicEndpoint、Remark、PathPrefix 传空串表示清空；
// SecretKey 传空串表示不改。
type UpdateInput struct {
	ProfileName    *string `json:"profile_name"`
	Endpoint       *string `json:"endpoint"`
	PublicEndpoint *string `json:"public_endpoint"`
	Bucket         *string `json:"bucket"`
	Region         *string `json:"region"`
	PathPrefix     *string `json:"path_prefix"`
	AccessKey      *string `json:"access_key"`
	SecretKey      *string `json:"secret_key"`
	ForcePathStyle *bool   `json:"force_path_style"`
	URLExpireHours *int    `json:"url_expire_hours"`
	Status         *string `json:"status"`
	Remark         *string `json:"remark"`
}

// changes 是校验过的修改。
type changes struct {
	in             UpdateInput
	profileName    string
	endpoint       string
	publicEndpoint *string
	bucket         string
	region         string
	pathPrefix     string
	accessKey      string
	secretKey      string // 空表示不改
	urlExpireHours int
	status         string
	remark         *string
}

func validateUpdate(in UpdateInput) (*changes, error) {
	c := &changes{in: in}
	var err error
	if in.ProfileName != nil {
		if c.profileName, err = normalizeProfileName(*in.ProfileName); err != nil {
			return nil, err
		}
	}
	if in.Endpoint != nil {
		if c.endpoint, err = normalizeEndpoint("endpoint", *in.Endpoint); err != nil {
			return nil, err
		}
	}
	if in.PublicEndpoint != nil {
		if c.publicEndpoint, err = normalizePublicEndpoint(*in.PublicEndpoint); err != nil {
			return nil, err
		}
	}
	if in.Bucket != nil {
		if c.bucket, err = normalizeBucket(*in.Bucket); err != nil {
			return nil, err
		}
	}
	if in.Region != nil {
		if c.region, err = normalizeRegion(*in.Region); err != nil {
			return nil, err
		}
	}
	if in.PathPrefix != nil {
		if c.pathPrefix, err = normalizePathPrefix(*in.PathPrefix); err != nil {
			return nil, err
		}
	}
	if in.AccessKey != nil {
		if c.accessKey, err = normalizeAccessKey(*in.AccessKey); err != nil {
			return nil, err
		}
	}
	if in.SecretKey != nil && *in.SecretKey != "" {
		if c.secretKey, err = normalizeSecretKey(*in.SecretKey); err != nil {
			return nil, err
		}
	}
	if in.URLExpireHours != nil {
		if c.urlExpireHours, err = checkURLExpireHours(*in.URLExpireHours); err != nil {
			return nil, err
		}
	}
	if in.Status != nil {
		if c.status, err = checkStatus(*in.Status); err != nil {
			return nil, err
		}
	}
	if in.Remark != nil {
		if c.remark, err = names.OptionalText(in.Remark, maxRemarkChars, "备注"); err != nil {
			return nil, httpx.Invalid("%s", err)
		}
	}
	return c, nil
}

// apply 把修改套到 before 的副本上（不含密钥）。
func (c *changes) apply(before *row) *row {
	r := *before
	in := c.in
	if in.ProfileName != nil {
		r.ProfileName = c.profileName
	}
	if in.Endpoint != nil {
		r.Endpoint = c.endpoint
	}
	if in.PublicEndpoint != nil {
		r.PublicEndpoint = c.publicEndpoint
	}
	if in.Bucket != nil {
		r.Bucket = c.bucket
	}
	if in.Region != nil {
		r.Region = c.region
	}
	if in.PathPrefix != nil {
		r.PathPrefix = c.pathPrefix
	}
	if in.AccessKey != nil {
		r.AccessKey = c.accessKey
	}
	if in.ForcePathStyle != nil {
		r.ForcePathStyle = *in.ForcePathStyle
	}
	if in.URLExpireHours != nil {
		r.URLExpireHours = c.urlExpireHours
	}
	if in.Status != nil {
		r.Status = c.status
	}
	if in.Remark != nil {
		r.Remark = c.remark
	}
	return &r
}

// Update 修改存储配置（含状态）。已经有文件的存储也可以改地址、桶和密钥（例如存储换了 IP）；改 path_prefix
// 只影响以后新写入的 key（快照里存的是完整的 key）。
func (s *Service) Update(ctx context.Context, operatorID, id uuid.UUID, in UpdateInput) (*Profile, error) {
	c, err := validateUpdate(in)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.forgetRows()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := lockRow(ctx, tx, id, c.status == StatusDisabled)
		if err != nil {
			return err
		}
		if before == nil {
			return errNotFound
		}
		after := c.apply(before)
		secretChanged := false
		if c.secretKey != "" {
			// 和原来的一样不算修改；原来的解不开时一律重新加密保存（换了 MASTER_KEY 之后的恢复办法）。
			if old, err := openSecret(s.mk, id, before.SecretKeyEnc); err != nil || old != c.secretKey {
				if after.SecretKeyEnc, err = sealSecret(s.mk, id, c.secretKey); err != nil {
					return err
				}
				secretChanged = true
			}
		}
		if after.Status != before.Status {
			if err := s.checkStatusChange(ctx, tx, before, after.Status); err != nil {
				return err
			}
			if after.Status != StatusActive {
				if _, asTarget, err := migrationGuard(ctx, tx, id); err != nil {
					return err
				} else if asTarget {
					return httpx.InvalidState("正在往这个存储迁移文件，不能改为只读或停用；等迁移结束或先取消")
				}
			}
		}
		b, a := diffFields(auditFields(before), auditFields(after))
		if len(a) == 0 && !secretChanged {
			return nil // 没有变化
		}
		if secretChanged {
			a["secret_key_changed"] = true
		}
		if err := updateRow(ctx, tx, after); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
			TargetID: id, Action: "update", Detail: map[string]any{"before": b, "after": a}})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// checkStatusChange：默认存储只能是 active；里面还有文件的不能停用。
func (s *Service) checkStatusChange(ctx context.Context, tx pgx.Tx, before *row, to string) error {
	if before.IsDefault && to != StatusActive {
		return httpx.Conflict(CodeStorageIsDefault, "默认存储不能改为只读或停用，请先把别的存储设为默认")
	}
	if to == StatusDisabled {
		u, err := usageOf(ctx, tx, before.ID)
		if err != nil {
			return err
		}
		if u.HasContents {
			return httpx.Conflict(CodeStorageInUse, "还有文件存在这个存储里，不能停用；可以改为只读：不再写入新文件，已有文件照常读取")
		}
	}
	return nil
}

// Delete 删除。默认存储、被引用的存储不能删（表上没有 deleted_at，删之前的配置留在审计里）。
func (s *Service) Delete(ctx context.Context, operatorID, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.forgetRows()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := lockRow(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r == nil {
			return errNotFound
		}
		if r.IsDefault {
			return httpx.Conflict(CodeStorageIsDefault, "默认存储不能删除，请先把别的存储设为默认")
		}
		if asSource, asTarget, err := migrationGuard(ctx, tx, id); err != nil {
			return err
		} else if asSource || asTarget {
			return httpx.InvalidState("这个存储正在迁移中，不能删除；等迁移结束或先取消")
		}
		u, err := usageOf(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.HasReferences {
			return httpx.Conflict(CodeStorageInUse, "这个存储还被文件快照、导出任务或 OCR 结果引用，不能删除；不再使用可以改为只读或停用")
		}
		if err := deleteRow(ctx, tx, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
			TargetID: id, Action: "delete", Detail: map[string]any{"before": auditFields(r)}})
	})
}

// SetDefault 设为默认存储（之后新上传的文件写到这里）。只有 active 的可以；已经是默认时什么都不做。
func (s *Service) SetDefault(ctx context.Context, operatorID, id uuid.UUID) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.forgetRows()
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := lockRow(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if r == nil {
			return errNotFound
		}
		if r.Status != StatusActive {
			return httpx.Conflict(CodeStorageNotActive, "只有启用状态的存储能设为默认")
		}
		if r.IsDefault {
			return nil
		}
		if asSource, _, err := migrationGuard(ctx, tx, id); err != nil {
			return err
		} else if asSource {
			return httpx.InvalidState("这个存储正在迁出，设为默认后新文件又会写回来；等迁移结束或先取消")
		}
		old, err := getDefaultRow(ctx, tx)
		if err != nil {
			return err
		}
		if old != nil {
			// FOR UPDATE（和写快照的 FOR KEY SHARE 冲突）：等正在往旧的默认存储写快照的上传、重扫提交；之后的写入在锁内
			// 看到它已经不是默认（409 storage_changed，客户端重试）。存储迁移靠这一点：源存储不是默认之后不会再有快照写进去。
			if _, err := lockRow(ctx, tx, old.ID, true); err != nil {
				return err
			}
			if err := setDefaultFlag(ctx, tx, old.ID, false); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
				TargetID: old.ID, Action: "unset_default",
				Detail: map[string]any{"before": map[string]any{"is_default": true}, "after": map[string]any{"is_default": false}}}); err != nil {
				return err
			}
		}
		if err := setDefaultFlag(ctx, tx, id, true); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
			TargetID: id, Action: "set_default",
			Detail: map[string]any{"before": map[string]any{"is_default": false}, "after": map[string]any{"is_default": true}}})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// TestNew 测试还没保存的配置。输入不合格 400；连接失败照样返回报告。
func (s *Service) TestNew(ctx context.Context, in ConnectionInput) (*s3.TestReport, error) {
	c, err := validateConnection(in)
	if err != nil {
		return nil, err
	}
	rep := s3.TestConnection(ctx, c.s3Config(), c.pathPrefix)
	return &rep, nil
}

// TestSaved 测试已保存的配置；in 是编辑表单里还没保存的修改（只用连接相关的字段），不带 secret_key 时用保存的。
// 保存的解不开时结论是 secret_unreadable，不访问存储。
func (s *Service) TestSaved(ctx context.Context, id uuid.UUID, in UpdateInput) (*s3.TestReport, error) {
	c, err := validateUpdate(UpdateInput{Endpoint: in.Endpoint, PublicEndpoint: in.PublicEndpoint, Bucket: in.Bucket,
		Region: in.Region, PathPrefix: in.PathPrefix, AccessKey: in.AccessKey, SecretKey: in.SecretKey, ForcePathStyle: in.ForcePathStyle})
	if err != nil {
		return nil, err
	}
	cur, err := getRow(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errNotFound
	}
	p := c.apply(cur)
	secret := c.secretKey
	if secret == "" {
		if secret, err = openSecret(s.mk, id, cur.SecretKeyEnc); err != nil {
			return &s3.TestReport{Result: s3.ResultSecretUnreadable, Message: unreadableMessage(cur, err)}, nil
		}
	}
	cfg := s3Config(p, secret)
	rep := s3.TestConnection(ctx, cfg, p.PathPrefix)
	return &rep, nil
}

func s3Config(r *row, secret string) s3.Config {
	cfg := s3.Config{Endpoint: r.Endpoint, Bucket: r.Bucket, Region: r.Region, AccessKey: r.AccessKey,
		SecretKey: secret, ForcePathStyle: r.ForcePathStyle}
	if r.PublicEndpoint != nil {
		cfg.PublicEndpoint = *r.PublicEndpoint
	}
	return cfg
}

func unreadableMessage(r *row, err error) string {
	return fmt.Sprintf("存储“%s”保存的 secret_key 解不开：%v。请恢复原来的 MASTER_KEY，或在存储配置里重新填写 secret_key",
		r.ProfileName, err)
}

// EnsureInitial 首次启动时登记默认存储：库里一条存储配置都没有时，按 S3_* 登记一条 active 的默认存储
// （名称 InitialProfileName，审计的操作人为空）。已经有存储配置时什么都不做；都没有又没配置 S3_* 时打警告。
// 另外检查已保存的 secret_key 能不能用当前 MASTER_KEY 解开，解不开的打警告。
func (s *Service) EnsureInitial(ctx context.Context, init *config.InitialStorage, log *slog.Logger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.forgetRows()
	n, err := countRows(ctx, s.pool)
	if err != nil {
		return err
	}
	if n > 0 {
		rows, err := listRows(ctx, s.pool)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := openSecret(s.mk, r.row.ID, r.row.SecretKeyEnc); err != nil {
				log.Warn("存储配置的 secret_key 解不开", "profile", r.row.ProfileName, "reason", err.Error())
			}
		}
		return nil
	}
	if init == nil {
		log.Warn("还没有存储配置，也没有设置 S3_*：上传会失败，请到系统管理的存储配置里新建一条并设为默认")
		return nil
	}
	in := CreateInput{
		ConnectionInput: ConnectionInput{Endpoint: init.Endpoint, Bucket: init.Bucket, AccessKey: init.AccessKey,
			SecretKey: init.SecretKey, ForcePathStyle: init.ForcePathStyle},
		ProfileName:    InitialProfileName,
		URLExpireHours: init.URLExpireHours,
	}
	if init.PublicEndpoint != "" {
		in.PublicEndpoint = &init.PublicEndpoint
	}
	if init.Region != "" {
		in.Region = &init.Region
	}
	if init.PathPrefix != "" {
		in.PathPrefix = &init.PathPrefix
	}
	r, err := s.newRow(in)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return fmt.Errorf("S3_* 不对，没能登记默认存储：%s", he.Message)
		}
		return err
	}
	r.IsDefault = true
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if n, err := countRows(ctx, tx); err != nil || n > 0 {
			return err
		}
		if err := insertRow(ctx, tx, r); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{TargetType: audit.TargetStorageProfile, TargetID: r.ID, Action: "create",
			Detail: map[string]any{"source": "init_storage", "after": auditFields(r)}})
	})
	if err != nil {
		return err
	}
	log.Info("已按 S3_* 登记默认存储", "profile", r.ProfileName, "endpoint", r.Endpoint, "bucket", r.Bucket)
	return nil
}

// errSecretUnreadable 是 500 `secret_unreadable`。
func errSecretUnreadable(r *row, err error) *httpx.Error {
	return httpx.NewError(http.StatusInternalServerError, CodeSecretUnreadable, unreadableMessage(r, err))
}
