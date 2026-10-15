package imagecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// 下载的上限（上一版 design §4.5 的细则）。
const (
	// maxDownloads：不同的图最多同时下载几张。
	maxDownloads = 16
	// idleTimeout：等响应头、读响应体时多久收不到数据算中断。
	idleTimeout = 30 * time.Second
	// 总时限。
	coverTimeout    = 2 * time.Minute
	originalTimeout = 30 * time.Minute
	// maxCoverBytes：封面最大 4 MB（server 只收 ≤ 256 KiB 的，这里放宽）。
	maxCoverBytes = 4 << 20
	// maxOriginalBytes：原图最大 512 MiB（server MAX_UPLOAD_BYTES 的默认值）。
	maxOriginalBytes = 512 << 20
)

// httpError 是取图失败：状态码、错误码（页面读 X-Doclite-Error，或分块描述的 JSON 里的 code）、说明。
type httpError struct {
	status  int
	code    string
	message string
}

func (e *httpError) Error() string { return fmt.Sprintf("%d %s: %s", e.status, e.code, e.message) }

func upstream(format string, args ...any) *httpError {
	return &httpError{status: http.StatusBadGateway, code: "upstream", message: fmt.Sprintf(format, args...)}
}

func integrity(format string, args ...any) *httpError {
	return &httpError{status: http.StatusBadGateway, code: "integrity", message: fmt.Sprintf(format, args...)}
}

func ioError(err error) *httpError {
	return &httpError{status: http.StatusInternalServerError, code: "io", message: "本机读写出错：" + err.Error()}
}

func newDownloadClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		// 存储在内网，不走系统代理（和会话代理一样）。
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   maxDownloads,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: idleTimeout,
		// 原样拿字节（大小、sha256 要和快照记录一致）。
		DisableCompression: true,
	}}
}

// flight 是一次下载（同一张图的并发请求共用）。done 关闭后 err、data、path、temp 不再变。
type flight struct {
	done chan struct{}
	err  *httpError
	// data：封面的字节。
	data []byte
	// path：原图的文件；temp 表示它是临时文件（没存进缓存），最后一个使用者用完后删掉。
	path string
	temp bool

	// 以下受 Service.mu 保护。
	refs     int
	finished bool
}

// join 加入（或发起）一张图的下载。用完调 release。
func (s *Service) join(kind string, src Source) *flight {
	key := entryKey(src.SnapshotID, kind)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flights[key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		s.flights[key] = f
		go s.runDownload(key, kind, src, f)
	}
	f.refs++
	return f
}

func (s *Service) release(f *flight) {
	s.mu.Lock()
	f.refs--
	drop := f.finished && f.refs == 0 && f.temp
	s.mu.Unlock()
	if drop {
		_ = os.Remove(f.path)
	}
}

// wait 等下载结束；ctx 先取消时返回 false（下载照常做完，别的请求还能用）。
func wait(ctx context.Context, f *flight) bool {
	select {
	case <-f.done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) runDownload(key, kind string, src Source, f *flight) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("下载图片时出错", "key", key, "panic", r)
			f.err = &httpError{status: http.StatusInternalServerError, code: "internal", message: fmt.Sprint(r)}
		}
		s.mu.Lock()
		delete(s.flights, key)
		f.finished = true
		drop := f.refs == 0 && f.temp
		s.mu.Unlock()
		close(f.done)
		if drop {
			_ = os.Remove(f.path)
		}
	}()
	s.dlSem <- struct{}{}
	defer func() { <-s.dlSem }()

	var tok token
	if s.store != nil {
		tok = s.store.token(src.ProjectID)
	}
	started := time.Now()
	var n int64
	if kind == KindCover {
		f.data, f.err = s.downloadCover(src.CoverURL)
		n = int64(len(f.data))
		if f.err == nil {
			s.storeCover(src, f.data, tok)
		}
	} else {
		f.path, f.temp, n, f.err = s.downloadOriginal(src, tok)
	}
	if f.err != nil {
		s.log.Warn("下载图片失败", "kind", kind, "file_id", src.FileID, "snapshot_id", src.SnapshotID, "err", f.err)
		return
	}
	s.c.downloads.Add(1)
	s.c.downloadBytes.Add(n)
	s.log.Debug("下载图片", "kind", kind, "snapshot_id", src.SnapshotID, "bytes", n, "ms", time.Since(started).Milliseconds())
}

// idleBody：每读到数据就把空闲计时器拨回去；30 秒收不到数据时计时器取消整个请求。
type idleBody struct {
	r io.Reader
	t *time.Timer
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.t.Reset(idleTimeout)
	}
	return n, err
}

// open 发起 GET（预签名 URL，不带 Cookie 和壳的请求头），只接受 200。返回的 cleanup 必须调用。
func (s *Service) open(url string, total time.Duration) (io.Reader, *http.Response, func(), *httpError) {
	ctx, cancel := context.WithTimeout(context.Background(), total)
	idle := time.AfterFunc(idleTimeout, cancel)
	fail := func(e *httpError) (io.Reader, *http.Response, func(), *httpError) {
		idle.Stop()
		cancel()
		return nil, nil, nil, e
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return fail(upstream("下载地址不对"))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fail(upstream("连接存储超时"))
		}
		return fail(upstream("连不上存储：%v", err))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			return fail(upstream("存储拒绝访问（地址可能已过期）"))
		}
		return fail(upstream("存储返回 %d", resp.StatusCode))
	}
	idle.Reset(idleTimeout)
	cleanup := func() {
		idle.Stop()
		cancel()
		resp.Body.Close()
	}
	return &idleBody{r: resp.Body, t: idle}, resp, cleanup, nil
}

func readFailed(err error) *httpError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return upstream("下载超时（%d 秒收不到数据，或超过总时限）", int(idleTimeout/time.Second))
	}
	return upstream("下载中途断开：%v", err)
}

// downloadCover 下载封面并校验：存储说是 JPEG，不超过 4 MB、和 Content-Length 一致，内容以 FF D8 FF 开头、
// FF D9 结尾（之后最多 4 KiB 的 0）。防止代理或配错的地址返回 200 的网页被当成封面存下来。
func (s *Service) downloadCover(url string) ([]byte, *httpError) {
	body, resp, cleanup, herr := s.open(url, coverTimeout)
	if herr != nil {
		return nil, herr
	}
	defer cleanup()
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(ct, "image/jpeg") {
		return nil, integrity("存储返回的封面类型是 %q，不是 JPEG", ct)
	}
	if resp.ContentLength > maxCoverBytes {
		return nil, integrity("封面太大（%d 字节）", resp.ContentLength)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxCoverBytes+1))
	if err != nil {
		return nil, readFailed(err)
	}
	if len(data) > maxCoverBytes {
		return nil, integrity("封面太大")
	}
	if resp.ContentLength >= 0 && int64(len(data)) != resp.ContentLength {
		return nil, upstream("封面只下载到 %d 字节，应为 %d", len(data), resp.ContentLength)
	}
	if !validJPEG(data) {
		return nil, integrity("下载到的封面不是完整的 JPEG")
	}
	return data, nil
}

// validJPEG：以 FF D8 FF 开头，以 FF D9 结尾（之后最多允许 4 KiB 的 0）。
func validJPEG(b []byte) bool {
	if !bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) {
		return false
	}
	end := len(b)
	for end > 0 && b[end-1] == 0 && len(b)-end < 4096 {
		end--
	}
	return end >= 5 && b[end-2] == 0xff && b[end-1] == 0xd9
}

// storeCover 把封面存进缓存（大于上限一半的不存；缓存不可用时不存）。
func (s *Service) storeCover(src Source, data []byte, tok token) {
	if s.store == nil || int64(len(data)) > s.store.currentLimit()/2 {
		return
	}
	tmp, err := os.CreateTemp(s.tmpDir, src.SnapshotID+".cover.*.part")
	if err != nil {
		s.log.Warn("写封面的临时文件失败", "err", err)
		return
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		s.log.Warn("写封面的临时文件失败", "err", errors.Join(werr, cerr))
		return
	}
	e := &entry{FileID: src.FileID, SnapshotID: src.SnapshotID, ProjectID: src.ProjectID, Kind: KindCover,
		Size: int64(len(data))}
	stored, err := s.store.put(e, name, tok)
	if !stored {
		_ = os.Remove(name)
		if err != nil {
			s.log.Warn("封面存不进缓存", "err", err)
		}
	}
}

// downloadOriginal 下载原图（边写临时文件边算 sha256），核对大小和 sha256，能存进缓存就存。
// 返回文件路径和它是不是临时文件（没存进缓存）。
func (s *Service) downloadOriginal(src Source, tok token) (string, bool, int64, *httpError) {
	if src.FileSize <= 0 || src.FileSize > maxOriginalBytes {
		return "", false, 0, &httpError{status: http.StatusUnprocessableEntity, code: "too_large",
			message: fmt.Sprintf("原图 %d MB，超过客户端的上限 %d MB", src.FileSize>>20, maxOriginalBytes>>20)}
	}
	body, resp, cleanup, herr := s.open(src.OriginalURL, originalTimeout)
	if herr != nil {
		return "", false, 0, herr
	}
	defer cleanup()
	if resp.ContentLength >= 0 && resp.ContentLength != src.FileSize {
		return "", false, 0, integrity("存储上的原图是 %d 字节，记录的是 %d 字节", resp.ContentLength, src.FileSize)
	}
	tmp, err := os.CreateTemp(s.tmpDir, src.SnapshotID+".original.*.part")
	if err != nil {
		return "", false, 0, ioError(err)
	}
	name := tmp.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, src.FileSize+1))
	cerr := tmp.Close()
	failed := func(e *httpError) (string, bool, int64, *httpError) {
		_ = os.Remove(name)
		return "", false, 0, e
	}
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return failed(ioError(err))
		}
		return failed(readFailed(err))
	}
	if cerr != nil {
		return failed(ioError(cerr))
	}
	if n != src.FileSize {
		return failed(integrity("原图下载到 %d 字节，记录的是 %d 字节", n, src.FileSize))
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != src.SHA256 {
		return failed(integrity("原图的 sha256 和记录的不一致"))
	}
	if s.store != nil && n <= s.store.currentLimit()/2 {
		e := &entry{FileID: src.FileID, SnapshotID: src.SnapshotID, ProjectID: src.ProjectID, Kind: KindOriginal,
			Ext: extOf(src.MimeType), Size: n}
		stored, err := s.store.put(e, name, tok)
		if stored {
			return s.store.path(e), false, n, nil
		}
		if err != nil {
			s.log.Warn("原图存不进缓存", "err", err)
		}
	}
	return name, true, n, nil
}

// extOf 是原图在缓存里的扩展名（按快照的 mime_type）。
func extOf(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/tiff":
		return "tif"
	}
	return "bin"
}

// contentTypeOf 是原图响应的 Content-Type：只认三种，其他一律 application/octet-stream。
func contentTypeOf(mime string) string {
	switch mime {
	case "image/jpeg", "image/png", "image/tiff":
		return mime
	}
	return "application/octet-stream"
}

// cover 取封面：缓存里有就读出来（命中），没有就下载（同一张的并发请求共用一次）。
func (s *Service) cover(ctx context.Context, src Source) ([]byte, bool, *httpError) {
	if s.store != nil {
		key := entryKey(src.SnapshotID, KindCover)
		if _, p, ok := s.store.lookup(key); ok {
			data, err := os.ReadFile(p)
			if err == nil {
				return data, true, nil
			}
			s.log.Warn("缓存里的封面读不出来，重新下载", "path", p, "err", err)
			s.store.remove(key)
		}
	}
	f := s.join(KindCover, src)
	defer s.release(f)
	if !wait(ctx, f) {
		return nil, false, nil
	}
	return f.data, false, f.err
}

// original 取原图文件：缓存里有就给缓存的路径（命中），没有就下载。用完调 release（临时文件在最后一个使用者
// 用完后删掉）。ctx 先取消时返回空路径、没有错误。
func (s *Service) original(ctx context.Context, src Source) (path string, release func(), hit bool, herr *httpError) {
	if s.store != nil {
		key := entryKey(src.SnapshotID, KindOriginal)
		if _, p, ok := s.store.lookup(key); ok {
			if _, err := os.Stat(p); err == nil {
				return p, func() {}, true, nil
			}
			s.store.remove(key)
		}
	}
	f := s.join(KindOriginal, src)
	if !wait(ctx, f) {
		s.release(f)
		return "", nil, false, nil
	}
	if f.err != nil {
		s.release(f)
		return "", nil, false, f.err
	}
	return f.path, func() { s.release(f) }, false, nil
}
