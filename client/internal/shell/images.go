package shell

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"doclite/client/internal/imagecache"
	"doclite/client/internal/proxy"
)

// 取图协议、本机图片缓存、大图切块（M6-3，实现在 internal/imagecache、internal/tiles）在壳里的接线：
//   - 取图挂在资产服务器上（imagecache.Prefix，和页面同源，见 assets.go）；
//   - 会话代理的每个响应交给缓存记来源（batch、snapshots；登录、退出、换服务器清空）；
//   - 命令 cache_stats、cache_clear、cache_set_limit、prepare_tiles。

// registerImageCommands 打开缓存、接上会话代理，登记缓存和切块的命令（registerCommands 最后调用）。
func (s *Shell) registerImageCommands() {
	s.images = imagecache.New(imagecache.Config{
		Dir:    filepath.Join(s.cfg.DataDir, "image-cache"),
		Log:    s.log,
		Server: s.proxy.Server,
	})
	s.proxy.Observe(func(req *proxy.Request, status int, body []byte) {
		s.images.Observe(s.proxy.Server(), req.Method, req.Path, status, body)
	})

	handle(s, "cache_stats", func(context.Context, noArgs) (any, error) { return s.images.Stats(), nil })

	handle(s, "cache_clear", func(_ context.Context, a struct {
		ProjectID *string `json:"projectId"`
	}) (any, error) {
		pid := ""
		if a.ProjectID != nil {
			pid = strings.ToLower(strings.TrimSpace(*a.ProjectID))
			if !imagecache.IsUUID(pid) {
				return nil, proxy.InvalidRequest("projectId 不是项目 id")
			}
		}
		return s.images.Clear(pid), nil
	})

	handle(s, "cache_set_limit", func(_ context.Context, a struct {
		LimitBytes *int64 `json:"limitBytes"`
	}) (any, error) {
		if a.LimitBytes == nil || *a.LimitBytes < imagecache.MinLimit || *a.LimitBytes > imagecache.MaxLimit {
			return nil, proxy.InvalidRequest("上限要在 100 MB 到 1 TB 之间")
		}
		if !s.images.SetLimit(*a.LimitBytes) {
			return nil, proxy.Internal("本机图片缓存不可用，改不了上限")
		}
		return s.images.Stats(), nil
	})

	handle(s, "prepare_tiles", func(_ context.Context, a struct {
		FileID     string `json:"fileId"`
		SnapshotID string `json:"snapshotId"`
	}) (any, error) {
		res, ok := s.images.PrepareTiles(a.FileID, a.SnapshotID)
		if !ok {
			return nil, proxy.InvalidRequest("这张图不在客户端的来源记录里（图片信息要先经 batch 或 snapshots 接口取得）")
		}
		return res, nil
	})
}

// imageSmokeConfig：自检里取图、缓存、切块的参数（环境变量，沿用上一版的名字），并打开 images、csp 两个能力。
//
//	DOCLITE_SMOKE_CACHE          cold（默认，先清空自检的缓存，要求全部未命中）或 warm（要求全部命中）
//	DOCLITE_SMOKE_IMG_PROJECT    项目编码，默认第一个能进的项目
//	DOCLITE_SMOKE_IMG_FOLDER     从项目根起的文件夹路径（/ 分隔），默认按层找第一个有文件的
//	DOCLITE_SMOKE_IMG_COUNT      取前几个文件，默认 8（1–100）
//	DOCLITE_SMOKE_TILES_PROJECT  切块用的项目编码，默认同 IMG_PROJECT
//	DOCLITE_SMOKE_TILES_FOLDER   切块用的文件夹，默认按层找第一个同时有文件名含 g4、lzw 的 TIFF 的
//	DOCLITE_SMOKE_TILES_FILES    实测用：逗号分隔的文件名（要和 TILES_FOLDER 一起给），改为切这几个
func (s *Shell) imageSmokeConfig(c *SmokeConfig) {
	c.Capabilities.Images = true
	c.Capabilities.CSP = true
	env := func(name string) *string {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return &v
		}
		return nil
	}
	if os.Getenv("DOCLITE_SMOKE_CACHE") == "warm" {
		c.ImageCache.Mode = "warm"
	}
	c.ImageCache.Project = env("DOCLITE_SMOKE_IMG_PROJECT")
	c.ImageCache.Folder = env("DOCLITE_SMOKE_IMG_FOLDER")
	if n, err := strconv.Atoi(os.Getenv("DOCLITE_SMOKE_IMG_COUNT")); err == nil && n >= 1 && n <= 100 {
		c.ImageCache.Count = n
	}
	c.Tiles.Project = env("DOCLITE_SMOKE_TILES_PROJECT")
	c.Tiles.Folder = env("DOCLITE_SMOKE_TILES_FOLDER")
	if files := env("DOCLITE_SMOKE_TILES_FILES"); files != nil && c.Tiles.Folder != nil {
		for _, f := range strings.Split(*files, ",") {
			if f = strings.TrimSpace(f); f != "" {
				c.Tiles.Files = append(c.Tiles.Files, f)
			}
		}
	}
}
