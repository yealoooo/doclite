// Package imagecache 是壳的取图协议、本机图片缓存和大图切块（requirements 第 6 节"图片缓存""大图切块"，
// 5.7 的查看器）：
//
//   - 取图挂在壳的资产服务器上（[Prefix]，和页面同源，见 path.go）。封面、原图先查本机缓存，没有就用来源记录里的
//     预签名 URL 下载、校验、存下；分块描述和块由原图切出（tilegen.go）。
//   - 只认来源记录（source.go）：命中缓存也要求快照在记录里。
//   - 缓存按 snapshot_id 存，LRU 淘汰（store.go）。
//
// 页面那一侧见 web/src/lib/image-url.ts、image-cache.ts、viewer/load-original.ts。
package imagecache

import (
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
)

// Config 是缓存的参数。
type Config struct {
	// Dir 是缓存目录（数据目录下的 image-cache；自检是 smoke/image-cache）。
	Dir string
	Log *slog.Logger
	// Server 返回当前配置的服务器（来源记录按服务器隔离：换了服务器就清空）。
	Server func() string
}

// Service 是取图和缓存。
type Service struct {
	dir    string
	log    *slog.Logger
	server func() string

	sources Sources
	// store 为 nil 时缓存不可用（目录建不了、索引写不了）：图片照常下载显示，只是不存。
	store    *store
	storeErr string
	tmpDir   string

	client *http.Client
	dlSem  chan struct{}

	mu      sync.Mutex
	flights map[string]*flight

	tiler *tiler
	c     counters
}

type counters struct {
	hits, misses, downloads, downloadBytes, rejected, errors, tilings, tilingFailures atomic.Int64
}

// New 打开缓存。缓存目录用不了时也返回可用的 Service（不落盘），原因在 Stats 里。
func New(cfg Config) *Service {
	s := &Service{
		dir:     cfg.Dir,
		log:     cfg.Log,
		server:  cfg.Server,
		client:  newDownloadClient(),
		dlSem:   make(chan struct{}, maxDownloads),
		flights: map[string]*flight{},
	}
	st, err := openStore(cfg.Dir, cfg.Log)
	if err != nil {
		s.storeErr = err.Error()
		cfg.Log.Warn("本机图片缓存不可用，图片只下载、不存", "dir", cfg.Dir, "err", err)
	} else {
		s.store = st
		s.tmpDir = st.tmpDir()
	}
	s.tiler = newTiler(s)
	cfg.Log.Info("本机图片缓存", "dir", cfg.Dir, "available", s.store != nil)
	return s
}

// Observe 交给会话代理：看每个 api_fetch 的响应，记来源（见 source.go）。
func (s *Service) Observe(server, method, path string, status int, body []byte) {
	s.sources.Observe(server, method, path, status, body)
}

// lookup 取来源记录（按当前服务器）。
func (s *Service) lookup(fileID, snapshotID string) (Source, bool) {
	return s.sources.Lookup(s.server(), fileID, snapshotID)
}

// Close 退出时把索引写盘。
func (s *Service) Close() {
	if s.store != nil {
		s.store.close()
	}
}

// Counters 是壳启动以来的计数（web/src/lib/image-cache.ts 的 CacheCounters）。
type Counters struct {
	Hits           int64 `json:"hits"`
	Misses         int64 `json:"misses"`
	Downloads      int64 `json:"downloads"`
	DownloadBytes  int64 `json:"download_bytes"`
	Rejected       int64 `json:"rejected"`
	Errors         int64 `json:"errors"`
	Tilings        int64 `json:"tilings"`
	TilingFailures int64 `json:"tiling_failures"`
}

// ProjectUsage 是一个项目的占用。
type ProjectUsage struct {
	ProjectID     string `json:"project_id"`
	TotalBytes    int64  `json:"total_bytes"`
	FileCount     int    `json:"file_count"`
	CoverCount    int    `json:"cover_count"`
	OriginalCount int    `json:"original_count"`
	TilesCount    int    `json:"tiles_count"`
}

// Stats 是 cache_stats 的结果（web/src/lib/image-cache.ts 的 CacheStats）。
type Stats struct {
	Dir        string         `json:"dir"`
	Available  bool           `json:"available"`
	Error      *string        `json:"error"`
	TotalBytes int64          `json:"total_bytes"`
	FileCount  int            `json:"file_count"`
	LimitBytes int64          `json:"limit_bytes"`
	Projects   []ProjectUsage `json:"projects"`
	Counters   Counters       `json:"counters"`
}

// Stats 统计占用和计数。
func (s *Service) Stats() Stats {
	st := Stats{Dir: s.dir, Available: s.store != nil, LimitBytes: DefaultLimit, Projects: []ProjectUsage{}}
	if s.store != nil {
		st.TotalBytes, st.FileCount, st.LimitBytes, st.Projects = s.store.usage()
	} else {
		msg := s.storeErr
		st.Error = &msg
	}
	st.Counters = Counters{
		Hits:           s.c.hits.Load(),
		Misses:         s.c.misses.Load(),
		Downloads:      s.c.downloads.Load(),
		DownloadBytes:  s.c.downloadBytes.Load(),
		Rejected:       s.c.rejected.Load(),
		Errors:         s.c.errors.Load(),
		Tilings:        s.c.tilings.Load(),
		TilingFailures: s.c.tilingFailures.Load(),
	}
	return st
}

// Clear 清空一个项目的缓存（projectID 为空表示全部）。缓存不可用时什么都不删。
func (s *Service) Clear(projectID string) ClearReport {
	if s.store == nil {
		return ClearReport{}
	}
	rep := s.store.clear(projectID)
	s.log.Info("清空本机图片缓存", "project", projectID, "removed", rep.RemovedFiles, "bytes", rep.RemovedBytes,
		"failed", rep.FailedFiles)
	return rep
}

// SetLimit 改上限（调用方已核对范围），超出时立即淘汰。缓存不可用时返回 false。
func (s *Service) SetLimit(limit int64) bool {
	if s.store == nil {
		return false
	}
	s.store.setLimit(limit)
	s.log.Info("修改本机图片缓存上限", "limit", limit)
	return true
}
