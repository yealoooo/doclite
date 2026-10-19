package imagecache

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"doclite/client/internal/proxy"
)

// 来源记录（requirements 第 6 节"图片缓存"）：壳只用这个快照最近一次出现在 `POST /api/files/batch` 或
// `GET /api/files/{fid}/snapshots` 响应里的预签名 URL 下载，不接受页面给的地址。命中缓存也要求快照在记录里——
// 本机缓存是这台电脑上所有账号共用的，记录说明当前账号刚从 server 拿到过这张图。
// 记录只在内存里；登录成功、退出登录、换服务器时清空。
//
// 已知的小缺口：记录是在响应回来时记的（会话代理的 Observe），不知道请求是什么时候发出的。退出登录、再登录的
// 那一瞬间还在路上的 batch 响应会在登录之后才记下（上一版按会话序号丢掉这种响应）。

// maxSources 是最多记多少个快照；超出时丢掉最早记下的 1/10。
const maxSources = 10_000

// Source 是一个快照的来源记录。
type Source struct {
	FileID      string
	ProjectID   string
	SnapshotID  string
	CoverURL    string // 空表示没有封面或生成不了 URL
	OriginalURL string // 空表示生成不了 URL
	FileSize    int64
	SHA256      string // 小写 hex
	MimeType    string
}

// URL 是某种图的下载地址（cover、original；分块用原图的）。
func (s Source) URL(op string) string {
	if op == OpCover {
		return s.CoverURL
	}
	return s.OriginalURL
}

type sourceRec struct {
	Source
	seq uint64
}

// Sources 是来源记录。零值可用。
type Sources struct {
	mu     sync.Mutex
	server string
	seq    uint64
	m      map[string]*sourceRec // snapshot_id → 记录
}

// batchPath 是 `POST /api/files/batch`。
const batchPath = "/api/files/batch"

// imageFields 是 batch、snapshots 里和取图有关的字段（server/internal/file/view.go 的 ImageFields）。
type imageFields struct {
	SnapshotID    string  `json:"snapshot_id"`
	FileSize      int64   `json:"file_size"`
	MimeType      string  `json:"mime_type"`
	ContentSHA256 string  `json:"content_sha256"`
	CoverURL      *string `json:"cover_url"`
	OriginalURL   *string `json:"original_url"`
}

// Observe 看会话代理的一个响应（server 是当时配置的服务器）：登录成功、退出登录时清空；batch、snapshots 的 200
// 响应记下里面的快照。
func (s *Sources) Observe(server, method, path string, status int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncServer(server)
	switch {
	case method == http.MethodPost && path == proxy.LoginPath:
		if status >= 200 && status < 300 {
			s.clearLocked()
		}
	case method == http.MethodPost && path == proxy.LogoutPath:
		// 不论结果（代理这时已经清空了 Cookie 罐）。
		s.clearLocked()
	case status != http.StatusOK:
	case method == http.MethodPost && path == batchPath:
		var resp struct {
			Items []struct {
				FileID    string `json:"file_id"`
				ProjectID string `json:"project_id"`
				imageFields
			} `json:"items"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return
		}
		for _, it := range resp.Items {
			s.putLocked(it.FileID, it.ProjectID, it.imageFields)
		}
	case method == http.MethodGet && isSnapshotsPath(path):
		var resp struct {
			FileID    string        `json:"file_id"`
			ProjectID string        `json:"project_id"`
			Snapshots []imageFields `json:"snapshots"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return
		}
		for _, it := range resp.Snapshots {
			s.putLocked(resp.FileID, resp.ProjectID, it)
		}
	}
}

// isSnapshotsPath：`/api/files/<fid>/snapshots`。
func isSnapshotsPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/api/files/")
	if !ok {
		return false
	}
	fid, tail, ok := strings.Cut(rest, "/")
	return ok && tail == "snapshots" && IsUUID(fid)
}

// IsSourceRequest：这个请求的响应里有图片来源（`POST /api/files/batch`、`GET /api/files/<fid>/snapshots`），
// 要交给 [Service.Observe]（cmd/devshell 也用它挑要看的响应）。
func IsSourceRequest(method, path string) bool {
	return (method == http.MethodPost && path == batchPath) || (method == http.MethodGet && isSnapshotsPath(path))
}

// Lookup 取一个快照的记录（要求 file_id 也对得上）。server 和记录所属的不同时（换了服务器）先清空。
func (s *Sources) Lookup(server, fileID, snapshotID string) (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncServer(server)
	r, ok := s.m[snapshotID]
	if !ok || r.FileID != fileID {
		return Source{}, false
	}
	return r.Source, true
}

func (s *Sources) syncServer(server string) {
	if server != s.server {
		s.server = server
		s.clearLocked()
	}
}

func (s *Sources) clearLocked() {
	s.m = nil
}

func (s *Sources) putLocked(fileID, projectID string, f imageFields) {
	fid, ok1 := normUUID(fileID)
	sid, ok2 := normUUID(f.SnapshotID)
	pid, ok3 := normUUID(projectID)
	if !ok1 || !ok2 || !ok3 {
		return
	}
	if s.m == nil {
		s.m = map[string]*sourceRec{}
	}
	s.seq++
	src := Source{FileID: fid, ProjectID: pid, SnapshotID: sid, FileSize: f.FileSize,
		SHA256: strings.ToLower(f.ContentSHA256), MimeType: f.MimeType}
	if f.CoverURL != nil {
		src.CoverURL = *f.CoverURL
	}
	if f.OriginalURL != nil {
		src.OriginalURL = *f.OriginalURL
	}
	s.m[sid] = &sourceRec{Source: src, seq: s.seq}
	if len(s.m) > maxSources {
		s.dropOldestLocked(len(s.m) / 10)
	}
}

func (s *Sources) dropOldestLocked(n int) {
	recs := make([]*sourceRec, 0, len(s.m))
	for _, r := range s.m {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].seq < recs[j].seq })
	for _, r := range recs[:n] {
		delete(s.m, r.SnapshotID)
	}
}
