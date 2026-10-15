package upload

import (
	"encoding/json"
	"time"
)

// Status 是队列项的状态（web/src/lib/scan-queue.ts 的 ItemStatus）：等待写入完成、排队（含等重试的）、上传中、
// 已入库、内容重复（已跳过）、失败。
type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusQueued    Status = "queued"
	StatusUploading Status = "uploading"
	StatusDone      Status = "done"
	StatusDuplicate Status = "duplicate"
	StatusFailed    Status = "failed"
)

// 队列项的来源：热文件夹、壳里的上传按钮。
const (
	SourceHotfolder = "hotfolder"
	SourceManual    = "manual"
)

// 壳在本地记的错误代码（web/src/views/scan/status.ts 的 LOCAL_ERRORS；其余是 server 的 code）。
const (
	ErrCodeFileMissing    = "file_missing"
	ErrCodeFileUnreadable = "file_unreadable"
	ErrCodeFileChanged    = "file_changed"
	ErrCodeFileLocked     = "file_locked"
	ErrCodeIncomplete     = "incomplete_file"
	ErrCodeFolderRejected = "folder_rejected"
	ErrCodeTargetNotFound = "target_not_found"
	ErrCodeNetwork        = "network"
	ErrCodeInternal       = "shell_internal"
)

// Item 是队列里的一项（一个本地文件），JSON 是 web/src/lib/scan-queue.ts 的 QueueItem。时间都是 Unix 毫秒。
type Item struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	ServerURL string `json:"server_url"`
	LocalPath string `json:"local_path"`
	FileName  string `json:"file_name"`
	// RelDir 是相对热文件夹根目录（或上传按钮选的文件夹）的子路径，`/` 连接；根目录里的为空。
	RelDir         string   `json:"rel_dir"`
	TargetFolderID string   `json:"target_folder_id"`
	FolderID       *string  `json:"folder_id"`
	Size           int64    `json:"size"`
	MtimeNS        int64    `json:"mtime_ns"`
	SHA256         *string  `json:"sha256"`
	Status         Status   `json:"status"`
	Attempts       int      `json:"attempts"`
	NextAttemptAt  *int64   `json:"next_attempt_at"`
	FileID         *string  `json:"file_id"`
	ServerFileName *string  `json:"server_file_name"`
	SortNo         *int64   `json:"sort_no"`
	CoverStatus    *string  `json:"cover_status"`
	CoverReason    *string  `json:"cover_reason"`
	ErrorCode      *string  `json:"error_code"`
	ErrorMessage   *string  `json:"error_message"`
	Warnings       []string `json:"warnings"`
	// DuplicateOf 是内容重复时已有文件的位置 `{file_id, folder_path, file_name}`，没有时为 null。
	DuplicateOf json.RawMessage `json:"duplicate_of"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
	FinishedAt  *int64          `json:"finished_at"`
}

// Stats 是各状态的数量（只算当前服务器的项）。
type Stats struct {
	Waiting   int `json:"waiting"`
	Queued    int `json:"queued"`
	Uploading int `json:"uploading"`
	Done      int `json:"done"`
	Duplicate int `json:"duplicate"`
	Failed    int `json:"failed"`
	// Retrying 是排队里正在等重试的（已经算在 Queued 里）。
	Retrying int `json:"retrying"`
	Total    int `json:"total"`
}

// Pause 是整个队列暂停的原因（401 会话失效、426 版本太低）。
type Pause struct {
	Reason  string          `json:"reason"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
	Since   int64           `json:"since"`
}

// 暂停的原因。
const (
	PauseAuth    = "auth_required"
	PauseUpgrade = "upgrade_required"
)

// QueueStatus 是 upload_status 的结果和 upload:state 事件的内容。
type QueueStatus struct {
	ServerURL     *string  `json:"server_url"`
	Paused        *Pause   `json:"paused"`
	PausedSources []string `json:"paused_sources"`
	Stats         Stats    `json:"stats"`
	// Running 是正在上传的子文件夹数（≤ 2）。
	Running int `json:"running"`
}

// HotConfig 是热文件夹的配置（存在队列库里，一行）。
type HotConfig struct {
	WatchDir       string `json:"watch_dir"`
	TargetFolderID string `json:"target_folder_id"`
	// ServerURL 是目标文件夹所属的服务器。
	ServerURL        string `json:"server_url"`
	ScanIntervalSecs int    `json:"scan_interval_secs"`
	// Running：壳重启后是否自动开始。
	Running bool `json:"running"`
}

// HotStatus 是 hotfolder_* 命令的结果和 hotfolder:state 事件的内容。
type HotStatus struct {
	Config  *HotConfig `json:"config"`
	Running bool       `json:"running"`
	// Watching：目录事件监控建起来了（false 时只靠定时的全量扫描）。
	Watching   bool    `json:"watching"`
	Error      *string `json:"error"`
	LastScanAt *int64  `json:"last_scan_at"`
	Scans      int     `json:"scans"`
}

func nowMS() int64 { return time.Now().UnixMilli() }

func strPtr(s string) *string { return &s }

func int64Ptr(v int64) *int64 { return &v }
