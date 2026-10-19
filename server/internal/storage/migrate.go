package storage

// 存储迁移（M7-2，requirements 第 11 节"存储迁移"，用户 2026-10-16）：把一个存储（源）里快照的原图和封面复制到另一个
// 存储（目标），按 sha256 核对一致之后改快照的位置——file_snapshots 的 storage_profile_id、storage_key、cover_key 三列，
// 这是"快照只追加"（硬性规则 2）唯一的例外，只在这里改。
//
// 接口（菜单 system.storage，migrate_handler.go）：POST …/{id}/migrate {target_profile_id} 建任务（源 = 这个存储）；
// GET …/{id}/migrate 查这个存储最近一次迁移（进度、失败清单）；POST …/{id}/migrate/cancel 取消。列表里每个存储另外带
// migration（最近一次迁出）和 incoming_migration（正在迁入）的摘要。
//
// 后台任务 job_type = storage_migrate（internal/platform/jobs 的执行器：领取、心跳、重试、启动时回收都是它的），一个
// 任务一行 background_jobs，没有业务表（不改表结构）：
//   - payload：源、目标（id 和建任务时的名字）；cancel（取消接口记下的）；state（断点：cursor、total、migrated、failed、
//     失败清单、audit_mark）。导出的 payload 写后不改，迁移的 state、cancel 由这里改，别的键不动；
//   - progress_done = migrated + failed、progress_total = total（快照数），和断点一起写（约每秒一次）；
//   - 最多执行 5 次（max_attempts；读写存储失败按执行器的退避重试，进程被杀后的回收也算一次）；优先级 -1（和导出一起
//     排队时导出先领）。
//
// 执行（migrate_run.go）：按 storage_key 的顺序一页一页取源存储里的对象（一个对象可能被几个快照共用：复制文件、以后的
// 恢复旧版都复用 key），几个 worker 并行处理：
//  1. 读出引用这个对象的全部快照（共用的一起处理，对象只复制一次）；
//  2. 原图从源存储读到临时文件（TMP_DIR/migrate/）同时算 sha256 → 和快照记的 sha256、大小核对 → 写到目标存储
//     （key = 目标的 path_prefix + `<项目编码>/<file_id>/v<n>.<ext>`，硬性规则 7；元数据原样带上，Cache-Control 按
//     目标存储的预签名有效期）→ 从目标存储读回来再算一次 sha256；封面同样（和源对象的 sha256 比）；
//  3. 都一致才在一个短事务里改位置：job 行（FOR SHARE，确认还归这次执行、没有要求取消）→ 目标存储行（FOR KEY SHARE，
//     确认还是可读写的）→ 内容锁（项目, sha256）→ 一条 UPDATE 改掉源存储里引用这个 key 的全部快照 → 每个快照一条审计
//     （target_type file_snapshot、action storage_migrate，detail 记改前改后的存储、key、封面 key）；
//  4. 核对不一致（源对象缺失、sha256 或大小不对、复制后读回来不一致、同一对象的快照内容不一致）：这个对象的快照都不改，
//     记进失败清单，接着迁别的；任务结束时报告失败数和清单，不算任务失败。
//  5. 约每秒写一次断点（结束、出错、取消、退出时也写）：cursor（按取出的顺序从头连续做完的最后一个 key）、失败清单、
//     migrated（数这次迁移的 storage_migrate 审计：和改位置同一个事务提交，进程在两次断点之间被杀也数得准）。重新执行时
//     从 cursor 之后接着取；已经迁走的快照不在源存储里了，自然不会再处理；断点之后没记下的失败会再试一次。
//  6. 取完了：数源存储里还剩的快照（remaining：核对失败的，以及复制了失败对象的新文件），写审计 migrate_end，job done。
// 开始（建任务）、取消、结束（完成、失败、取消）都写审计（target_type storage_profile、target_id 源存储）。
// **迁移不删源存储里的对象**（删不删由运维决定），导出物不迁（保留期满后自动删）。迁完的源存储建议先改为只读：旧的
// 导出物照常能下载，到期清理照常删它们（清理只跳过已停用存储里的），清理完再停用。导出过的存储一直被导出任务记着
// （export_tasks.storage_profile_id，done 的任务清理后也保留"当时存在哪"），这样的存储只能停用、不能删除。
//
// 并发（审查重点）：
//
//   - 共用对象：按 storage_key 分组处理，对象只复制一次；改位置是一条 `WHERE storage_profile_id = 源 AND storage_key = K`
//     的 UPDATE，引用它的快照一起改（封面按"旧封面 key → 新封面 key"对应；有快照的封面不在复制过的里面——理论上不会
//     有——就回滚，重读重来，再不行记失败）。
//   - 复制文件（复用源文件当前快照的对象）：复制的事务在存储配置行之后拿同一把内容锁（项目, sha256），锁内重读源快照
//     （folder.CopyFolderFile）。迁移改位置也先拿这把锁：复制先拿到时，迁移的 UPDATE 在它提交后才执行（READ COMMITTED，
//     拿到锁后的语句看得到它刚插入的快照），新快照一起改掉；迁移先拿到时，复制在锁内重读到的已经是目标存储的位置。
//     所以不会有新快照指向一个已经迁走的位置而被漏掉。
//   - 上传、重扫（查重拿内容锁，同项目同 sha256）：它们只写默认存储；断网重试命中返回已有文件，内容重复 409，都不新建
//     指向已有对象的快照。迁移要求源存储不是默认的（建任务 409 storage_is_default），迁移期间源存储不能设为默认
//     （409 invalid_state）；设默认时旧的默认存储行改用 FOR UPDATE，等正在写快照的上传、重扫（FOR KEY SHARE）提交，之后
//     的写入在锁内看到它已经不是默认（409 storage_changed，客户端重试）——所以建了迁移以后不会再有快照写进源存储。迁移
//     改位置和查重用同一把内容锁，查重命中正在迁的对象时两边排队，结论不变。
//   - 导出（冻结后执行时读原图）：执行开始时按快照现在的位置读；期间位置被改了，旧位置的对象还在（迁移不删源对象），新
//     位置的对象在改位置之前就核对过；源存储在快照都迁走之前不能停用（有快照引用 409 storage_in_use）。导出执行到一半
//     时迁移刚好结束、运维又马上停用了源存储，读原图失败按可重试的失败重试，下一次执行读的是新位置。
//   - 存储配置的修改：迁移进行中（排队、执行）源存储不能设为默认、不能删除；目标存储不能改为只读或停用、不能删除
//     （409 invalid_state）；源存储可以改只读（迁移只读它）。连接参数（地址、桶、密钥）可以改：每取一页对象之前重读
//     两个存储的配置、变了就换客户端，已经在复制的那几个对象用旧的连接做完（复制后读回核对，连错了核对不上或读写失败重试）。
//     执行时目标存储万一不再可读写（防御）：任务失败，已迁的不受影响。停用、删除照旧：还有快照引用 409 storage_in_use，
//     改位置的事务拿着目标存储行的 FOR KEY SHARE，和停用、删除的 FOR UPDATE 互斥。
//   - 取消：取消接口锁 job 行（FOR NO KEY UPDATE）记下 cancel，和改位置事务里的 FOR SHARE 互斥：取消接口返回之后不会
//     再改任何快照。排队中的直接 canceled；执行中的由执行器停下（心跳或 Interrupt），写完断点再 canceled。
//   - 同一时间整个系统只有一个迁移在排队或执行（一个迁移执行时占执行器的一个槽，两个会把默认的 2 个占满、导出一直
//     排队）：建任务时在 Service.mu（存储配置的写操作在进程内排队）里按库里的状态查。所以也不会有转圈、接力的迁移。
//
// 加锁顺序（requirements 3.6）：job 行 → 存储配置行 → 内容锁 → 快照行，是"……→ 存储配置行 → 内容锁"的尾部；改位置的
// 事务不拿树锁、files 行、folders 行，不会和拿着它们等内容锁的上传、复制形成环。job 行在最前，和导出一样（job 行 →
// 任务行）。

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/audit"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/jobs"
)

// MigrateJobType 是存储迁移在 background_jobs.job_type 里的值。
const MigrateJobType = "storage_migrate"

// 迁移的审计动作（target_type storage_profile，target_id 源存储）。
const (
	actionMigrateStart  = "migrate_start"
	actionMigrateCancel = "migrate_cancel"
	actionMigrateEnd    = "migrate_end"
)

// 默认值。
const (
	defaultMigrateWorkers      = 4
	defaultMigrateBatch        = 32
	migrateMaxAttempts         = 5
	migrateFailuresKept        = 200 // 失败清单最多记这么多个对象（失败数照样全数）
	migrateFailureSnaps        = 20  // 失败清单里每个对象最多列这么多个快照（另记总数）
	migrateObjectRetries       = 3   // 一个对象读写存储暂时失败时重试几次
	defaultMigrateRetryBackoff = time.Second
	migratePayloadVersion      = 1
)

// MigrateOptions 是迁移执行的配置。零值用默认值。
type MigrateOptions struct {
	// TmpDir 是 TMP_DIR（复制原图的临时文件在 TmpDir/migrate/ 下）；为空时用系统临时目录下的 doclite。
	TmpDir string
	// Workers 是一个迁移任务里同时复制几个对象（默认 4）。
	Workers int
	// BatchSize 是每次从库里取几个对象（一页，默认 32）。
	BatchSize int
	// RetryBackoff 是一个对象读写存储暂时失败后，第一次重试前等多久（默认 1 秒），之后每次 × 4，一共重试 3 次（1、4、
	// 16 秒）；还不行就停下这次执行，交给执行器按它的退避重试。
	RetryBackoff time.Duration
}

// Migrator 是存储迁移：建任务、查进度、取消（接口），和后台任务的执行（JobHandler）。
type Migrator struct {
	svc  *Service
	jobs *jobs.Executor
	opts MigrateOptions
	log  *slog.Logger
}

// NewMigrator 建 Migrator。app 里还要 Register(MigrateJobType, m.JobHandler())、OnStart(清临时目录)。
func NewMigrator(svc *Service, executor *jobs.Executor, opts MigrateOptions, log *slog.Logger) *Migrator {
	if opts.TmpDir == "" {
		opts.TmpDir = filepath.Join(os.TempDir(), "doclite")
	}
	if opts.Workers <= 0 {
		opts.Workers = defaultMigrateWorkers
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultMigrateBatch
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = defaultMigrateRetryBackoff
	}
	if log == nil {
		log = slog.Default()
	}
	return &Migrator{svc: svc, jobs: executor, opts: opts, log: log}
}

// MigrateTmpDir 是迁移用的临时目录：TMP_DIR 下的 migrate/。
func MigrateTmpDir(base string) string {
	if base == "" {
		base = filepath.Join(os.TempDir(), "doclite")
	}
	return filepath.Join(base, "migrate")
}

// SweepMigrateTmp 启动时删掉迁移临时目录里上次留下的文件（进程被杀时没删的）。执行器启动时调用。
func SweepMigrateTmp(base string, log *slog.Logger) {
	dir := MigrateTmpDir(base)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && log != nil {
			log.Warn("清理迁移临时目录失败", "path", filepath.Join(dir, e.Name()), "error", err)
		}
	}
	if len(entries) > 0 && log != nil {
		log.Info("清理了上次留下的迁移临时文件", "count", len(entries))
	}
}

// ---------------------------------------------------------------------------
// 类型

// Migration 是一次迁移（接口 `GET …/{id}/migrate` 的响应；列表里的 migration、incoming_migration 是不带 remaining、
// failures 的摘要）。数量都按快照算。
type Migration struct {
	ID                uuid.UUID `json:"id"`
	SourceProfileID   uuid.UUID `json:"source_profile_id"`
	SourceProfileName string    `json:"source_profile_name"`
	TargetProfileID   uuid.UUID `json:"target_profile_id"`
	TargetProfileName string    `json:"target_profile_name"`
	// Status 是后台任务的状态：queued（排队中；attempts > 0 时是等待重试，原因在 error_message）、running、done、failed、
	// canceled。
	Status string `json:"status"`
	// CancelRequested：执行中已经要求取消（正在停下，之后变成 canceled）。
	CancelRequested bool `json:"cancel_requested"`
	// Total 是要迁的快照数（建任务时源存储里的快照；迁移期间复制出来一起迁的会让它变大）；完成时 = migrated + failed。
	Total    int `json:"total"`
	Migrated int `json:"migrated"`
	// Failed 是核对不一致、没有改位置的快照数；FailedObjects 是这些快照用到的对象数。
	Failed        int        `json:"failed"`
	FailedObjects int        `json:"failed_objects"`
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"max_attempts"`
	ErrorMessage  *string    `json:"error_message"`
	CreatedBy     uuid.UUID  `json:"created_by"`
	CreatedByName *string    `json:"created_by_name"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	// Remaining 是源存储里现在还有的快照数（只在详情里；为 0 时源存储可以停用）。
	Remaining *int `json:"remaining,omitempty"`
	// Failures 是失败清单（只在详情里，按对象，最多 200 个；FailuresTruncated 表示还有没列出的）。
	Failures          []MigrationFailure `json:"failures,omitempty"`
	FailuresTruncated bool               `json:"failures_truncated"`
}

// Active：排队中或执行中。
func (m *Migration) Active() bool {
	return m.Status == string(jobs.Queued) || m.Status == string(jobs.Running)
}

func (m *Migration) fill(p *migratePayload) {
	m.SourceProfileID, m.TargetProfileID = p.SourceProfileID, p.TargetProfileID
	m.SourceProfileName, m.TargetProfileName = p.SourceProfileName, p.TargetProfileName
	m.CancelRequested = p.Cancel != nil && m.Status == string(jobs.Running)
	st := p.State
	m.Total, m.Migrated, m.Failed, m.FailedObjects = st.Total, st.Migrated, st.Failed, st.FailedObjects
	m.Failures, m.FailuresTruncated = st.Failures, st.FailuresTruncated
}

// MigrationFailure 是核对不一致、没有迁的一个对象和引用它的快照。
type MigrationFailure struct {
	// StorageKey 是源存储里的原图 key。
	StorageKey string `json:"storage_key"`
	// ReasonCode：source_missing（源存储里没有原图）、cover_missing（没有封面）、sha256_mismatch、size_mismatch（原图和
	// 快照记的不一致）、verify_failed（复制到目标存储后读回来不一致）、inconsistent（引用同一对象的快照记的内容不一致，
	// 或者封面对不上）。
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
	// Snapshots 是引用这个对象的快照（最多列 20 个）；SnapshotCount 是总数。
	Snapshots     []FailedSnapshot `json:"snapshots"`
	SnapshotCount int              `json:"snapshot_count"`
	// FailedAt 是发现的时间。
	FailedAt time.Time `json:"failed_at"`
}

// FailedSnapshot 是失败清单里的一个快照（名字、位置是失败时的）。
type FailedSnapshot struct {
	SnapshotID  uuid.UUID `json:"snapshot_id"`
	FileID      uuid.UUID `json:"file_id"`
	VersionNo   int       `json:"version_no"`
	ProjectCode *string   `json:"project_code"`
	FileName    *string   `json:"file_name"`
	// FolderPath 从项目的根文件夹起，/ 连接。
	FolderPath  *string `json:"folder_path"`
	FileDeleted bool    `json:"file_deleted"`
}

// migratePayload 是迁移任务的 background_jobs.payload。
type migratePayload struct {
	PayloadVersion    int       `json:"payload_version"`
	SourceProfileID   uuid.UUID `json:"source_profile_id"`
	TargetProfileID   uuid.UUID `json:"target_profile_id"`
	SourceProfileName string    `json:"source_profile_name"`
	TargetProfileName string    `json:"target_profile_name"`
	// Cancel 是取消接口记下的（谁、什么时候）；没有表示没要求取消。
	Cancel *migrateCancel `json:"cancel,omitempty"`
	State  migrateState   `json:"state"`
}

type migrateCancel struct {
	RequestedAt time.Time `json:"requested_at"`
	RequestedBy uuid.UUID `json:"requested_by"`
}

// migrateState 是断点（约每秒写一次）。
type migrateState struct {
	// Cursor：源存储里 storage_key 不大于它的对象都处理过了（迁走了，或者记进了失败清单）。
	Cursor        string `json:"cursor"`
	Total         int    `json:"total"`
	Migrated      int    `json:"migrated"`
	Failed        int    `json:"failed"`
	FailedObjects int    `json:"failed_objects"`
	// AuditMark：migrated 数到了哪条审计（这次迁移 id 不大于它的 storage_migrate 审计都数过了）。
	AuditMark         int64              `json:"audit_mark"`
	Failures          []MigrationFailure `json:"failures"`
	FailuresTruncated bool               `json:"failures_truncated"`
	// Remaining 是结束时源存储里还剩的快照数。
	Remaining *int `json:"remaining,omitempty"`
}

// ---------------------------------------------------------------------------
// 接口

var errNoMigration = httpx.NewError(http.StatusNotFound, httpx.CodeNotFound, "这个存储还没有迁移记录")

// Start 建迁移任务：把存储 sourceID 里的原图和封面迁到 targetID。
//   - 404：源存储、目标存储不存在；400：没给目标、目标就是源、两个是同一个位置（地址、桶、前缀都相同）；
//   - 409 storage_not_active：目标不是可读写的；storage_is_default：源是默认存储（新上传还会写进去）；
//   - 409 invalid_state：已有迁移在排队或执行（整个系统同时只有一个）、源存储里没有快照；
//   - 500 secret_unreadable：两个存储的密钥有解不开的。
func (m *Migrator) Start(ctx context.Context, operatorID, sourceID, targetID uuid.UUID) (*Migration, error) {
	s := m.svc
	s.mu.Lock()
	defer s.mu.Unlock()
	jobID := db.NewID()
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		src, err := lockRow(ctx, tx, sourceID, false)
		if err != nil {
			return err
		}
		if src == nil {
			return errNotFound
		}
		if targetID == uuid.Nil {
			return httpx.Invalid("要选一个目标存储（target_profile_id）")
		}
		if targetID == sourceID {
			return httpx.Invalid("目标存储不能是这个存储自己")
		}
		dst, err := lockRow(ctx, tx, targetID, false)
		if err != nil {
			return err
		}
		if dst == nil {
			return httpx.NotFound("目标存储配置")
		}
		if sameLocation(src, dst) {
			return httpx.Invalid("目标存储和这个存储是同一个位置（地址、桶和统一前缀都相同），不用迁移")
		}
		if dst.Status != StatusActive {
			return httpx.Conflict(CodeStorageNotActive, "目标存储要是可读写（启用）的")
		}
		if src.IsDefault {
			return httpx.Conflict(CodeStorageIsDefault, "这是默认存储，新上传的文件还会写进来；请先把别的存储（比如目标存储）设为默认，再迁移")
		}
		for _, r := range []*row{src, dst} {
			if _, err := openSecret(s.mk, r.ID, r.SecretKeyEnc); err != nil {
				return errSecretUnreadable(r, err)
			}
		}
		// 同一时间整个系统只有一个迁移在排队或执行（一个迁移执行时占一个执行槽，两个就把默认的 2 个占满，导出一直排队）。
		// 进程内由 Service.mu 排队，这里再按库里的状态查。
		active, err := activeMigrations(ctx, tx)
		if err != nil {
			return err
		}
		if len(active) > 0 {
			return httpx.InvalidState("已有存储迁移在进行，请等它完成或取消")
		}
		total, err := countSnapshots(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		if total == 0 {
			return httpx.InvalidState("这个存储里没有文件，不用迁移")
		}
		// audit_mark 从现在的最大审计 id 起：这次迁移的审计都在它之后，补数时不用从头扫审计。
		mark, err := maxAuditID(ctx, tx)
		if err != nil {
			return err
		}
		p := &migratePayload{PayloadVersion: migratePayloadVersion, SourceProfileID: sourceID, TargetProfileID: targetID,
			SourceProfileName: src.ProfileName, TargetProfileName: dst.ProfileName,
			State: migrateState{Total: total, AuditMark: mark, Failures: []MigrationFailure{}}}
		if err := insertMigrationJob(ctx, tx, jobID, p, total, migrateMaxAttempts, operatorID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
			TargetID: sourceID, Action: actionMigrateStart, Detail: map[string]any{
				"job_id": jobID, "profile_name": src.ProfileName, "target_profile_id": targetID,
				"target_profile_name": dst.ProfileName, "total": total}})
	})
	if err != nil {
		return nil, err
	}
	m.log.Info("存储迁移：已建任务", "job_id", jobID, "source", sourceID, "target", targetID)
	m.jobs.Wake()
	return m.Latest(ctx, sourceID)
}

// sameLocation：地址、桶、统一前缀都相同（复制到同一个位置没有意义，key 也会一样）。
func sameLocation(a, b *row) bool {
	return a.Endpoint == b.Endpoint && a.Bucket == b.Bucket && a.PathPrefix == b.PathPrefix
}

// Latest 是存储 sourceID 最近一次迁移（详情：带 remaining、失败清单）。存储不存在 404；没有迁移记录 404。
func (m *Migrator) Latest(ctx context.Context, sourceID uuid.UUID) (*Migration, error) {
	r, err := getRow(ctx, m.svc.pool, sourceID)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errNotFound
	}
	mg, err := latestMigration(ctx, m.svc.pool, sourceID)
	if err != nil {
		return nil, err
	}
	if mg == nil {
		return nil, errNoMigration
	}
	n, err := countSnapshots(ctx, m.svc.pool, sourceID)
	if err != nil {
		return nil, err
	}
	mg.Remaining = &n
	if mg.Failures == nil {
		mg.Failures = []MigrationFailure{}
	}
	return mg, nil
}

// Cancel 取消存储 sourceID 正在进行的迁移：排队中（含等待重试）的直接 canceled；执行中的记下取消、叫执行器停下（写完
// 断点后 canceled，期间 cancel_requested 为 true）。已经迁的快照不退回。没有进行中的迁移、已经在取消 409 invalid_state。
func (m *Migrator) Cancel(ctx context.Context, operatorID, sourceID uuid.UUID) (*Migration, error) {
	var interrupt uuid.UUID
	err := db.InTx(ctx, m.svc.pool, func(tx pgx.Tx) error {
		r, err := getRow(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		if r == nil {
			return errNotFound
		}
		jobID, status, p, err := lockActiveMigration(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		if jobID == uuid.Nil {
			return httpx.InvalidState("这个存储没有进行中的迁移")
		}
		if p.Cancel != nil {
			return httpx.InvalidState("迁移已经在取消了")
		}
		p.Cancel = &migrateCancel{RequestedAt: time.Now().UTC(), RequestedBy: operatorID}
		if err := setPayloadKey(ctx, tx, jobID, "cancel", p.Cancel); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{OperatorID: audit.Operator(operatorID), TargetType: audit.TargetStorageProfile,
			TargetID: sourceID, Action: actionMigrateCancel, Detail: map[string]any{"job_id": jobID,
				"profile_name": r.ProfileName, "target_profile_id": p.TargetProfileID, "target_profile_name": p.TargetProfileName,
				"before": map[string]any{"status": status}, "after": map[string]any{"status": string(jobs.Canceled)}}}); err != nil {
			return err
		}
		if status == string(jobs.Running) {
			interrupt = jobID
			return nil
		}
		// 排队中（没人在执行；含失败后等待重试的）：直接取消，最后的断点和结束的审计也在这里写。
		if _, err := tx.Exec(ctx, `UPDATE background_jobs SET status = 'canceled', finished_at = now(), runner_id = NULL WHERE id = $1`,
			jobID); err != nil {
			return err
		}
		if err := closeState(ctx, tx, jobID, p); err != nil {
			return err
		}
		return m.writeEnd(ctx, tx, jobID, p, string(jobs.Canceled), nil)
	})
	if err != nil {
		return nil, err
	}
	if interrupt != uuid.Nil {
		m.jobs.Interrupt(interrupt)
	}
	return m.Latest(ctx, sourceID)
}

// writeEnd 写结束的审计（migrate_end，操作人为空表示系统）：结果、数量、剩下的快照、失败原因。
func (m *Migrator) writeEnd(ctx context.Context, q db.Querier, jobID uuid.UUID, p *migratePayload, result string, message *string) error {
	remaining, err := countSnapshots(ctx, q, p.SourceProfileID)
	if err != nil {
		return err
	}
	d := map[string]any{"job_id": jobID, "result": result, "profile_name": p.SourceProfileName, "target_profile_id": p.TargetProfileID,
		"target_profile_name": p.TargetProfileName, "total": p.State.Total, "migrated": p.State.Migrated,
		"failed": p.State.Failed, "failed_objects": p.State.FailedObjects, "remaining": remaining}
	if message != nil {
		d["error_message"] = *message
	}
	return audit.Write(ctx, q, audit.Entry{TargetType: audit.TargetStorageProfile, TargetID: p.SourceProfileID,
		Action: actionMigrateEnd, Detail: d})
}

// ---------------------------------------------------------------------------
// 给存储配置的修改用（调用方拿着 Service.mu）

// migrationGuard：存储 id 在进行中的迁移里当源（asSource）、当目标（asTarget）。
func migrationGuard(ctx context.Context, q db.Querier, id uuid.UUID) (asSource, asTarget bool, err error) {
	active, err := activeMigrations(ctx, q)
	if err != nil {
		return false, false, err
	}
	for _, a := range active {
		asSource = asSource || a.SourceID == id
		asTarget = asTarget || a.TargetID == id
	}
	return asSource, asTarget, nil
}
