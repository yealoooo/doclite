package upload

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// Body 是一次上传（或重扫）的请求体：multipart/form-data，字段按序各一次（requirements 5.6，用户决定 2026-09-28）：
// `file_name`、`content_sha256`、`cover`（可选）、`file`（最后）。原图不读进内存：请求体是"前面几段 + 原图 + 结尾"
// 拼起来的流，长度事先算好（Content-Length），server 能边收边算 sha。
type Body struct {
	ContentType string
	Length      int64
	prefix      []byte
	suffix      []byte
	fileSize    int64
}

// NewBody 准备请求体。sha256 是原图 sha256 的 64 位小写 hex；cover 为 nil 表示不带封面；fileSize 是原图的字节数
// （发送时正好读这么多）。
func NewBody(fileName, sha256 string, cover []byte, fileSize int64) (*Body, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("file_name", fileName); err != nil {
		return nil, err
	}
	if err := w.WriteField("content_sha256", sha256); err != nil {
		return nil, err
	}
	if cover != nil {
		part, err := w.CreatePart(fileHeader("cover", "cover.jpg", "image/jpeg"))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(cover); err != nil {
			return nil, err
		}
	}
	if _, err := w.CreatePart(fileHeader("file", fileName, "application/octet-stream")); err != nil {
		return nil, err
	}
	suffix := []byte("\r\n--" + w.Boundary() + "--\r\n")
	return &Body{
		ContentType: w.FormDataContentType(),
		Length:      int64(buf.Len()) + fileSize + int64(len(suffix)),
		prefix:      buf.Bytes(),
		suffix:      suffix,
		fileSize:    fileSize,
	}, nil
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "", "\n", "")

func fileHeader(field, fileName, contentType string) textproto.MIMEHeader {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, quoteEscaper.Replace(fileName)))
	h.Set("Content-Type", contentType)
	return h
}

// ErrFileChanged：发送原图时读到的字节数和准备时的大小不一样（文件在上传过程中被改了）。
var ErrFileChanged = errors.New("文件在上传过程中被改动了")

// Reader 返回请求体的流：file 从当前位置起正好读 fileSize 个字节（文件变短了报 [ErrFileChanged]）。
func (b *Body) Reader(file io.Reader) io.Reader {
	return io.MultiReader(bytes.NewReader(b.prefix), &exactReader{r: file, left: b.fileSize}, bytes.NewReader(b.suffix))
}

// exactReader 正好读 left 个字节：提前读到文件末尾说明文件变短了。
type exactReader struct {
	r    io.Reader
	left int64
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.left {
		p = p[:e.left]
	}
	n, err := e.r.Read(p)
	e.left -= int64(n)
	if err == io.EOF {
		if e.left > 0 {
			return n, ErrFileChanged
		}
		return n, io.EOF
	}
	return n, err
}
