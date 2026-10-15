package imagecache

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"

	"doclite/client/internal/tiles"
)

// ServeHTTP 处理取图地址（[Prefix] 下，见 path.go）。挂在壳的资产服务器上，和页面同源。
//
// 响应都是完整的（Windows 上 Wails 会把资产服务器的响应整块攒完再交给 WebView2，图片和块不受影响）。
// 都带 `Cache-Control: no-store`（WebView 自己不缓存：清空立即生效，换账号也看不到上一个人的图）和
// `X-Content-Type-Options: nosniff`；封面、原图、分块描述带 `X-Doclite-Cache: hit|miss`。
// 失败时响应体是 JSON `{code, message}`，另带 `X-Doclite-Error: <code>`：
//
//	404 bad_path 地址不对；not_found 不在来源记录里；no_url 没有下载地址；out_of_range 块超出范围
//	422 切不了块（too_large、timeout、unsupported_format、unsupported、corrupt），原图超过 512 MB（too_large）
//	502 下载失败（upstream 存储连不上、返回错误；integrity 内容和记录对不上）
//	503 unavailable 缓存不可用、切块期间被清空（可以重试）
//	500 io 本机读写出错
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed", message: "只支持 GET"})
		return
	}
	req, ok := ParsePath(r.URL.Path)
	if !ok {
		writeError(w, &httpError{status: http.StatusNotFound, code: "bad_path", message: "取图地址不对"})
		return
	}
	if req.Op == OpTest {
		writeBytes(w, "image/png", probePNG())
		return
	}
	src, ok := s.lookup(req.FileID, req.SnapshotID)
	if !ok {
		s.c.rejected.Add(1)
		writeError(w, &httpError{status: http.StatusNotFound, code: "not_found",
			message: "这张图不在客户端的来源记录里（图片信息要先经 batch 或 snapshots 接口取得）"})
		return
	}
	if src.URL(req.Op) == "" {
		writeError(w, &httpError{status: http.StatusNotFound, code: "no_url", message: "这张图没有下载地址"})
		return
	}
	switch req.Op {
	case OpCover:
		s.serveCover(w, r, src)
	case OpOriginal:
		s.serveOriginal(w, r, src)
	case OpDZI:
		s.serveDZI(w, r, src)
	case OpTile:
		s.serveTile(w, r, src, req)
	}
}

func (s *Service) count(hit bool) {
	if hit {
		s.c.hits.Add(1)
	} else {
		s.c.misses.Add(1)
	}
}

func (s *Service) fail(w http.ResponseWriter, e *httpError) {
	if e.status >= 500 {
		s.c.errors.Add(1)
	}
	writeError(w, e)
}

func (s *Service) serveCover(w http.ResponseWriter, r *http.Request, src Source) {
	data, hit, herr := s.cover(r.Context(), src)
	s.count(hit)
	switch {
	case herr != nil:
		s.fail(w, herr)
	case data != nil:
		w.Header().Set("X-Doclite-Cache", cacheHeader(hit))
		writeBytes(w, "image/jpeg", data)
	}
}

func (s *Service) serveOriginal(w http.ResponseWriter, r *http.Request, src Source) {
	path, release, hit, herr := s.original(r.Context(), src)
	s.count(hit)
	if herr != nil {
		s.fail(w, herr)
		return
	}
	if path == "" {
		return // 页面已经不要了
	}
	defer release()
	w.Header().Set("X-Doclite-Cache", cacheHeader(hit))
	if !s.serveFile(w, path, contentTypeOf(src.MimeType)) {
		s.fail(w, ioError(errors.New("原图文件不见了，请重试")))
	}
}

// serveFile 把一个文件作为完整的响应发出去。文件打不开时什么都不写，返回 false。
func (s *Service) serveFile(w http.ResponseWriter, path, contentType string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.fail(w, ioError(err))
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
	return true
}

// Descriptor 是分块描述（web/src/lib/image-url.ts 的 TileSourceDescriptor）：Image 是 DZI 的 JSON 写法，
// OpenSeadragon 直接认；其余字段给页面用。
type Descriptor struct {
	Image struct {
		Xmlns    string `json:"xmlns"`
		URL      string `json:"Url"`
		Format   string `json:"Format"`
		Overlap  string `json:"Overlap"`
		TileSize string `json:"TileSize"`
		Size     struct {
			Width  string `json:"Width"`
			Height string `json:"Height"`
		} `json:"Size"`
	} `json:"Image"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	TileSize   int        `json:"tile_size"`
	Overlap    int        `json:"overlap"`
	Levels     int        `json:"levels"`
	MaxLevel   int        `json:"max_level"`
	Format     string     `json:"format"`
	Mode       tiles.Mode `json:"mode"`
	TileCount  int        `json:"tile_count"`
	TilesBytes int64      `json:"tiles_bytes"`
	Streamed   bool       `json:"streamed"`
	ElapsedMs  int64      `json:"elapsed_ms"`
	Hit        bool       `json:"hit"`
}

// TileURLPrefix 是一张图的块地址前缀（以 / 结尾），块是 `<前缀><级>/<x>_<y>.<jpg|png>`。
func TileURLPrefix(fileID, snapshotID string) string {
	return Prefix + OpTile + "/" + fileID + "/" + snapshotID + "/"
}

func descriptor(src Source, m *tiles.Result, hit bool) Descriptor {
	var d Descriptor
	d.Image.Xmlns = "http://schemas.microsoft.com/deepzoom/2008"
	d.Image.URL = TileURLPrefix(src.FileID, src.SnapshotID)
	d.Image.Format = m.Format
	d.Image.Overlap = strconv.Itoa(tiles.Overlap)
	d.Image.TileSize = strconv.Itoa(tiles.TileSize)
	d.Image.Size.Width = strconv.Itoa(m.Width)
	d.Image.Size.Height = strconv.Itoa(m.Height)
	d.Width, d.Height = m.Width, m.Height
	d.TileSize, d.Overlap = tiles.TileSize, tiles.Overlap
	d.Levels, d.MaxLevel = m.MaxLevel+1, m.MaxLevel
	d.Format, d.Mode = m.Format, m.Mode
	d.TileCount, d.TilesBytes, d.ElapsedMs = m.TileCount, m.Bytes, m.ElapsedMs
	d.Hit = hit
	return d
}

func (s *Service) serveDZI(w http.ResponseWriter, r *http.Request, src Source) {
	meta, _, hit, herr := s.tiler.ensure(r.Context(), src)
	if herr != nil {
		s.fail(w, herr)
		return
	}
	if meta == nil {
		return
	}
	data, _ := json.Marshal(descriptor(src, meta, hit))
	w.Header().Set("X-Doclite-Cache", cacheHeader(hit))
	writeBytes(w, "application/json", data)
}

func (s *Service) serveTile(w http.ResponseWriter, r *http.Request, src Source, req Request) {
	for attempt := 0; attempt < 2; attempt++ {
		meta, dir, _, herr := s.tiler.ensure(r.Context(), src)
		if herr != nil {
			s.fail(w, herr)
			return
		}
		if meta == nil {
			return
		}
		lw, lh := tiles.LevelSize(meta.Width, meta.Height, meta.MaxLevel, min(req.Level, meta.MaxLevel))
		cols, rows := tiles.Grid(lw, lh)
		if req.Level > meta.MaxLevel || req.X >= cols || req.Y >= rows || req.Ext != meta.Format {
			writeError(w, &httpError{status: http.StatusNotFound, code: "out_of_range", message: "没有这一块"})
			return
		}
		ct := "image/jpeg"
		if meta.Format == "png" {
			ct = "image/png"
		}
		if s.serveFile(w, tileFile(dir, req.Level, req.X, req.Y, meta.Format), ct) {
			return
		}
		// 块文件不见了（被淘汰、清空，或被别的程序删了）：删掉这一套，重切一次。
		s.log.Warn("块文件不见了，重切", "snapshot_id", src.SnapshotID, "level", req.Level, "x", req.X, "y", req.Y)
		s.store.remove(entryKey(src.SnapshotID, KindTiles))
	}
	s.fail(w, ioError(errors.New("块文件不见了")))
}

func cacheHeader(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

func writeBytes(w http.ResponseWriter, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, e *httpError) {
	data, _ := json.Marshal(map[string]string{"code": e.code, "message": e.message})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Doclite-Error", e.code)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(e.status)
	_, _ = w.Write(data)
}

var (
	probeOnce sync.Once
	probeData []byte
)

// probePNG 是自检 img 一步的测试图：64×64 的渐变。
func probePNG() []byte {
	probeOnce.Do(func() {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 160, A: 255})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		probeData = buf.Bytes()
	})
	return probeData
}
