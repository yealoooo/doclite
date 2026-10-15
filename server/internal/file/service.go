// Package file 是文件：上传入库和预检（requirements 5.6）、看图（`files/batch`、`snapshots`，5.7）、文件日志（3.5）。
//
// 表：files（只有 id = file_id，硬性规则 1）、file_snapshots（只追加，硬性规则 2）、file_logs（硬性规则 9）。
// folders、folder_files 属于 internal/folder：上传时领位置、插入 folder_files 经 [Folders]（folder.Service 实现，
// app 里注入；folder 要用这里的 [LockFiles]、[WriteLogs]、[InsertCopy]，所以这个包不 import folder）。
//
// 给其他模块在自己的事务里用（像 audit.Write 一样是包级函数）：
//   - [LockFiles]：按 id 升序锁 files 行（每个文件的写锁，requirements 3.6）；
//   - [WriteLogs]：写 file_logs；
//   - [Current]、[CurrentOf]：文件的当前快照（max(version_no)）；
//   - [InsertCopy]：复制文件时新建 file_id 和 v1 copy 快照；[InsertSnapshot]：追加快照；
//   - [LockContent]：(项目, sha256) 的内容锁。
//
// 上传的流程见 upload.go；重扫（M3-1）复用 [Service.UploadBody]、[Service.Receive]、[KeysFor]、[Service.PutObjects]、
// [LockContent]、[Service.FindDuplicate]、[InsertSnapshot]。
package file

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/storage"
)

// 默认值。
const (
	DefaultMaxUploadBytes = 512 << 20
	DefaultMaxImagePixels = 300_000_000
	// DefaultIdleTimeout：上传的请求体这么久没收到数据按 400 invalid_input（requirements 5.6）。
	DefaultIdleTimeout = 60 * time.Second
)

// Options 是上传的配置。
type Options struct {
	// TmpDir 是临时目录（TMP_DIR）；原图边收边写到 TmpDir/uploads/upload-*.part。
	TmpDir string
	// MaxUploadBytes 是原图的大小上限（MAX_UPLOAD_BYTES），超过 413。
	MaxUploadBytes int64
	// MaxImagePixels 是像素总数上限（MAX_IMAGE_PIXELS），超过 400 invalid_image。
	MaxImagePixels int64
	// IdleTimeout 是请求体的空闲超时（测试里调短）。
	IdleTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.TmpDir == "" {
		o.TmpDir = filepath.Join(os.TempDir(), "doclite")
	}
	if o.MaxUploadBytes <= 0 {
		o.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if o.MaxImagePixels <= 0 {
		o.MaxImagePixels = DefaultMaxImagePixels
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	return o
}

// UploadDir 是放上传临时文件的目录。
func UploadDir(tmpDir string) string { return filepath.Join(tmpDir, "uploads") }

// Slot 是文件在文件夹里的位置：[Folders.ClaimSlot] 在事务里锁住文件夹行之后定下的名字和 sort_no。
type Slot struct {
	ProjectID uuid.UUID
	FolderID  uuid.UUID
	// FolderName、FolderPath 是文件夹现在的名字和从根文件夹起用 / 连接的路径（写日志用）。
	FolderName string
	FolderPath string
	// FileName 是入库后的名字：撞名时自动改为"名字 (n).扩展名"。
	FileName string
	// Renamed：FileName 和想要的名字不同。
	Renamed bool
	SortNo  int
}

// Folders 是文件夹模块给上传用的能力（folder.Service 实现）。
type Folders interface {
	// ProjectOf 是文件夹所在的项目；文件夹不存在或已删除 404。
	ProjectOf(ctx context.Context, folderID uuid.UUID) (uuid.UUID, error)
	// ClaimSlot 在事务里锁住目标文件夹行（不存在、已删除、不在这个项目 404），定下不重名的名字和 sort_no
	// （= 文件夹里未删除文件的 max + 1）。调用方必须在同一个事务里用 AddFile 插入。
	ClaimSlot(ctx context.Context, tx pgx.Tx, projectID, folderID uuid.UUID, name string) (Slot, error)
	// AddFile 按 slot 插入 folder_files（待分配），返回 folder_files 的 id。
	AddFile(ctx context.Context, tx pgx.Tx, slot Slot, fileID, createdBy uuid.UUID) (uuid.UUID, error)
	// PreviewName 是现在上传到这个文件夹会得到的名字（不加锁，预检用）。
	PreviewName(ctx context.Context, folderID uuid.UUID, name string) (string, error)
	// Path 是文件夹从根文件夹起用 / 连接的路径。
	Path(ctx context.Context, q db.Querier, folderID uuid.UUID) (string, error)
}

// Service 是文件的业务规则。
type Service struct {
	pool    *pgxpool.Pool
	events  *events.Bus
	auth    *auth.Service
	storage *storage.Service
	folders Folders
	opts    Options
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, bus *events.Bus, authSvc *auth.Service, storageSvc *storage.Service,
	folders Folders, opts Options) *Service {
	return &Service{pool: pool, events: bus, auth: authSvc, storage: storageSvc, folders: folders, opts: opts.withDefaults()}
}

// Options 是生效的配置（补了默认值）。
func (s *Service) Options() Options { return s.opts }
