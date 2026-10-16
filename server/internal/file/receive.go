package file

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"doclite/server/internal/platform/httpx"
	"doclite/shared/imagehdr"
	"doclite/shared/names"
)

// 上传的请求（requirements 5.6，用户决定 2026-09-28）：一个 multipart/form-data，字段按顺序各一次：
//
//	file_name       必填，文本：原始文件名（UTF-8 原文；文件名只认这个字段，不看各部分的 filename）
//	content_sha256  可选，文本：原图 sha256 的 64 位小写 hex（壳带，浏览器不带）
//	cover           可选，文件：客户端生成的封面 JPEG，≤ 256 KiB
//	file            必填，文件，最后一个：原图，≤ MAX_UPLOAD_BYTES
//
// 不是 multipart、缺字段、顺序不对、重复、多余、文本 > 4 KiB、请求体不完整或 60 秒收不到数据 → 400 invalid_input；
// 原图超限 413；封面超过 256 KiB 400 invalid_cover；sha 对不上 400 checksum_mismatch。
// 各部分的 Content-Type 不看（格式按魔数识别）。

// 上限。
const (
	MaxCoverBytes = 256 << 10
	maxTextBytes  = 4 << 10
	// drainSlack：出错时最多读掉 MAX_UPLOAD_BYTES 再加这么多（封面、文本、multipart 的头和分隔线）。
	drainSlack = 2 << 20
)

var fieldOrder = []string{"file_name", "content_sha256", "cover", "file"}

const fieldOrderHint = "字段应依次为 file_name、content_sha256（可选）、cover（可选）、file，每个最多一次"

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// 错误码。
const (
	CodeChecksumMismatch = "checksum_mismatch"
	CodeInvalidImage     = "invalid_image"
	CodeInvalidCover     = "invalid_cover"
	CodeDuplicateContent = "duplicate_content"
)

func coverError(format string, args ...any) *httpx.Error {
	return httpx.NewError(http.StatusBadRequest, CodeInvalidCover, "封面不合规："+fmt.Sprintf(format, args...)+
		"。客户端会去掉封面重传")
}

func tooLarge(max int64) *httpx.Error {
	return httpx.NewError(http.StatusRequestEntityTooLarge, httpx.CodePayloadTooLarge,
		fmt.Sprintf("文件太大：上限 %s", humanBytes(max)))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d 字节", n)
}

// Upload 是收完、检查过文件头和封面的一次上传：原图在临时文件里，用完调 Close 删掉。
type Upload struct {
	// FileName 是 file_name 字段（已按文件名规则校验）。
	FileName string
	// SHA256 是原图的 sha256（服务端算的）。
	SHA256 []byte
	Size   int64
	// Info 是原图的文件头。
	Info imagehdr.Info
	// Cover 是封面 JPEG（已校验）；没带时为 nil。
	Cover []byte

	tmp *os.File
}

// Close 关闭并删除临时文件。可以重复调用。
func (u *Upload) Close() {
	if u == nil || u.tmp == nil {
		return
	}
	name := u.tmp.Name()
	_ = u.tmp.Close()
	_ = os.Remove(name)
	u.tmp = nil
}

// Original 是原图的内容（从头读）。
func (u *Upload) Original() *io.SectionReader { return io.NewSectionReader(u.tmp, 0, u.Size) }

// Warnings 是响应的 warnings：DPI 未知 dpi_unknown，低于 200 low_dpi。
func (u *Upload) Warnings() []string { return warnings(u.Info.DPI) }

// LowDPI 是"DPI 低于这个值给提示"（参照 DA/T 31）。
const LowDPI = 200

func warnings(dpi int) []string {
	switch {
	case dpi <= 0:
		return []string{"dpi_unknown"}
	case dpi < LowDPI:
		return []string{"low_dpi"}
	}
	return []string{}
}

// errIdle 是请求体空闲超时（[Service.UploadBody] 的读取器返回）。
var errIdle = errors.New("上传的请求体超过 60 秒没有收到数据")

// Receive 读上传的 multipart 请求：原图流式写进临时文件同时算 sha256，再读原图的文件头、校验封面。
// 出错时临时文件已删除；成功时调用方用完 Close。重扫（M3-1）用同一个函数读请求。
//
// 路由要套 [Service.UploadBody]（出错时先读完剩余的请求体再返回、空闲超时）。
func (s *Service) Receive(r *http.Request) (*Upload, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, httpx.Invalid("上传要用 multipart/form-data（%s）", fieldOrderHint)
	}
	u := &Upload{}
	ok := false
	defer func() {
		if !ok {
			u.Close()
		}
	}()
	var clientSHA string
	next := 0 // fieldOrder 里下一个允许的位置
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, bodyError(err)
		}
		name := part.FormName()
		idx := indexOf(fieldOrder, name)
		switch {
		case idx < 0:
			return nil, httpx.Invalid("不认识的字段 %q：%s", name, fieldOrderHint)
		case next == 0 && idx != 0:
			return nil, httpx.Invalid("第一个字段要是 file_name：%s", fieldOrderHint)
		case idx < next:
			return nil, httpx.Invalid("字段 %s 重复或顺序不对：%s", name, fieldOrderHint)
		}
		next = idx + 1
		switch name {
		case "file_name":
			v, err := readText(part, name)
			if err != nil {
				return nil, err
			}
			if err := names.ValidateFileName(v); err != nil {
				return nil, httpx.Invalid("%s", err.Error())
			}
			u.FileName = v
		case "content_sha256":
			v, err := readText(part, name)
			if err != nil {
				return nil, err
			}
			if !shaPattern.MatchString(v) {
				return nil, httpx.Invalid("content_sha256 要是 64 位小写十六进制")
			}
			clientSHA = v
		case "cover":
			data, err := io.ReadAll(io.LimitReader(part, MaxCoverBytes+1))
			if err != nil {
				return nil, bodyError(err)
			}
			if len(data) > MaxCoverBytes {
				return nil, coverError("超过 %s", humanBytes(MaxCoverBytes))
			}
			u.Cover = data
		case "file":
			if err := s.spool(u, part); err != nil {
				return nil, err
			}
			if _, err := mr.NextPart(); err != io.EOF {
				if err != nil {
					return nil, bodyError(err)
				}
				return nil, httpx.Invalid("file 后面不能再有字段：%s", fieldOrderHint)
			}
		}
		if name == "file" {
			break
		}
	}
	if next < len(fieldOrder) {
		return nil, httpx.Invalid("缺少原图（file 字段）：%s", fieldOrderHint)
	}
	if clientSHA != "" && clientSHA != hex.EncodeToString(u.SHA256) {
		return nil, httpx.NewError(http.StatusBadRequest, CodeChecksumMismatch,
			"收到的文件和 content_sha256 对不上（传输中损坏或文件在上传时被改动），请重传")
	}
	// ParseUpload：另外拒绝导出 PDF 做不了的罕见编码（算术编码的 JPEG、JPEG 2000 压缩的 TIFF……），扫描员当场知道。
	info, err := imagehdr.ParseUpload(u.tmp, u.Size, s.opts.MaxImagePixels)
	if err != nil {
		return nil, imageError(err)
	}
	u.Info = info
	if u.Cover != nil {
		if err := ValidateCover(u.Cover, info); err != nil {
			return nil, err
		}
	}
	ok = true
	return u, nil
}

// spool 把原图写进临时文件，同时算 sha256、数字节；超过上限 413。
func (s *Service) spool(u *Upload, part io.Reader) error {
	dir := UploadDir(s.opts.TmpDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建上传临时目录 %s：%w", dir, err)
	}
	f, err := os.CreateTemp(dir, "upload-*.part")
	if err != nil {
		return fmt.Errorf("建上传临时文件：%w", err)
	}
	u.tmp = f
	h := sha256.New()
	max := s.opts.MaxUploadBytes
	buf := make([]byte, 256<<10)
	var n int64
	for {
		k, rerr := part.Read(buf)
		if k > 0 {
			n += int64(k)
			if n > max {
				return tooLarge(max)
			}
			h.Write(buf[:k])
			if _, err := f.Write(buf[:k]); err != nil {
				return fmt.Errorf("写上传临时文件：%w", err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return bodyError(rerr)
		}
	}
	u.Size = n
	u.SHA256 = h.Sum(nil)
	return nil
}

func readText(part io.Reader, name string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, maxTextBytes+1))
	if err != nil {
		return "", bodyError(err)
	}
	if len(data) > maxTextBytes {
		return "", httpx.Invalid("字段 %s 太长（上限 4 KiB）", name)
	}
	if !utf8.Valid(data) {
		return "", httpx.Invalid("字段 %s 不是 UTF-8 文本", name)
	}
	return string(data), nil
}

// bodyError：读请求体出错（客户端断开、格式不对、空闲超时）都按 400 invalid_input。
func bodyError(err error) error {
	if errors.Is(err, errIdle) {
		return httpx.Invalid("%s，请重传", errIdle.Error())
	}
	return httpx.Invalid("上传的请求体不完整或格式不对（%s）", strings.TrimPrefix(err.Error(), "multipart: "))
}

// imageError 把文件头的错误分类成接口的错误（M1-2 的须知）。
func imageError(err error) error {
	switch {
	case errors.Is(err, imagehdr.ErrUnsupported):
		return httpx.NewError(http.StatusUnsupportedMediaType, httpx.CodeUnsupportedMedia, err.Error()+"：只收 JPEG、PNG、TIFF")
	case errors.Is(err, imagehdr.ErrMultiPage):
		return httpx.NewError(http.StatusBadRequest, CodeInvalidImage, err.Error()+"：不收多页 TIFF，请先拆成单页再上传")
	case errors.Is(err, imagehdr.ErrInvalid), errors.Is(err, imagehdr.ErrTooLarge), errors.Is(err, imagehdr.ErrUnsupportedEncoding):
		return httpx.NewError(http.StatusBadRequest, CodeInvalidImage, err.Error())
	}
	return err
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// UploadBody 是上传路由的中间件，套在访问控制外面（httpx.Chain(svc.UploadBody(), gate…)）：
//   - 请求体空闲超时：每次读之前把连接的读截止时间推到 60 秒之后，超时按 400 invalid_input；读完请求体时清掉；
//   - 写响应之前先读掉剩余的请求体（最多 MAX_UPLOAD_BYTES + 2 MiB）：业务错误（包括鉴权、找不到文件夹）提前返回时，
//     客户端还在发原图，不读完它看到的是"连接被重置"，而不是错误响应（requirements 5.6）。
func (s *Service) UploadBody() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := &idleBody{rc: http.NewResponseController(w), body: r.Body, idle: s.opts.IdleTimeout,
				budget: s.opts.MaxUploadBytes + drainSlack}
			r.Body = body
			next.ServeHTTP(&drainWriter{ResponseWriter: w, body: body, log: httpx.LoggerFrom(r.Context())}, r)
		})
	}
}

// idleBody 给请求体加空闲超时，并记下读了多少。
type idleBody struct {
	rc     *http.ResponseController
	body   io.ReadCloser
	idle   time.Duration
	budget int64
	read   int64
	eof    bool
	// extended 是上次推截止时间的时刻：multipart 每次只读几 KiB，隔一会儿（1 秒，空闲时间很短时是它的 1/4）才再推一次，
	// 截止时间相应多给这么久。
	extended time.Time
}

func (b *idleBody) Read(p []byte) (int, error) {
	if b.eof {
		return 0, io.EOF
	}
	step := min(time.Second, b.idle/4)
	if now := time.Now(); now.Sub(b.extended) >= step {
		b.extended = now
		_ = b.rc.SetReadDeadline(now.Add(b.idle + step))
	}
	n, err := b.body.Read(p)
	b.read += int64(n)
	if err == io.EOF {
		b.eof = true
		// 请求体读完了：清掉截止时间，免得之后（传 S3、写库期间）连接的后台读超时。
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	var ne net.Error
	if err != nil && (errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())) {
		err = errIdle
	}
	return n, err
}

func (b *idleBody) Close() error { return b.body.Close() }

// drain 读掉剩余的请求体（最多到 budget）。
func (b *idleBody) drain() (complete bool) {
	if b.eof {
		return true
	}
	left := b.budget - b.read
	if left <= 0 {
		return false
	}
	_, err := io.CopyN(io.Discard, b, left)
	return err == io.EOF || b.eof
}

// drainWriter 在第一次写响应之前读完剩余的请求体。
type drainWriter struct {
	http.ResponseWriter
	body    *idleBody
	log     *slog.Logger
	drained bool
}

func (w *drainWriter) drain() {
	if w.drained {
		return
	}
	w.drained = true
	if !w.body.drain() {
		w.log.Debug("上传的请求体太大或中断，没有读完，连接会被关闭")
	}
}

func (w *drainWriter) WriteHeader(code int) {
	w.drain()
	w.ResponseWriter.WriteHeader(code)
}

func (w *drainWriter) Write(b []byte) (int, error) {
	w.drain()
	return w.ResponseWriter.Write(b)
}

func (w *drainWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// SweepTmp 启动时清掉上一次运行留下的上传临时文件（TMP_DIR/uploads/upload-*.part）。单台服务器、单个进程，
// 不用考虑别的实例正在用（硬性规则 16）。删不掉的记警告、接着删别的。
func SweepTmp(tmpDir string, log *slog.Logger) {
	matches, err := filepath.Glob(filepath.Join(UploadDir(tmpDir), "upload-*.part"))
	if err != nil {
		return
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("删除上次留下的上传临时文件失败", "path", m, "error", err)
		}
	}
	if len(matches) > 0 {
		log.Info("清掉了上次留下的上传临时文件", "count", len(matches))
	}
}
