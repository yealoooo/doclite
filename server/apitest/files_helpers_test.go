package apitest

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"doclite/server/internal/app"
	"doclite/server/internal/file"
)

// 文件目录、上传测试的支撑。

// newUploadEnv 建能上传的测试环境：临时目录是这个测试自己的（返回它，测试里检查用完是否清空），默认存储是
// 这个测试的 S3 前缀。opts 可以改上传的配置（上限调小）。
func newUploadEnv(t *testing.T, opts ...func(*file.Options)) (*Env, string) {
	t.Helper()
	tmp := t.TempDir()
	env := NewEnv(t, func(o *app.Options) {
		o.Upload = file.Options{TmpDir: tmp}
		for _, f := range opts {
			f(&o.Upload)
		}
	})
	env.UseS3()
	return env, tmp
}

// sampleBytes 读 server/testdata/samples 里的样本。
func sampleBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "samples", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// unique 往图片里加一段注释（图片本身不变），让内容和别的上传不同：JPEG 在 SOI 和 APPn 之后插 COM 段，PNG 在 IEND
// 之前插 tEXt 块，TIFF 在末尾追加。
func unique(data []byte, tag string) []byte {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		i := 2
		for i+4 <= len(data) && data[i] == 0xFF && data[i+1] >= 0xE0 && data[i+1] <= 0xEF {
			i += 2 + int(binary.BigEndian.Uint16(data[i+2:]))
		}
		seg := []byte{0xFF, 0xFE, 0, 0}
		binary.BigEndian.PutUint16(seg[2:], uint16(len(tag)+2))
		out := append([]byte{}, data[:i]...)
		out = append(append(out, seg...), tag...)
		return append(out, data[i:]...)
	case len(data) > 8 && bytes.HasPrefix(data, []byte("\x89PNG")):
		body := append([]byte("tEXt"), append([]byte("Comment\x00"), tag...)...)
		chunk := make([]byte, 4, 12+len(body))
		binary.BigEndian.PutUint32(chunk, uint32(len(body)-4))
		chunk = append(chunk, body...)
		chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
		at := len(data) - 12
		out := append([]byte{}, data[:at]...)
		return append(append(out, chunk...), data[at:]...)
	}
	return append(append([]byte{}, data...), tag...)
}

// coverJPEG 编一张 w×h 的灰度 JPEG（Go 的编码器不写 EXIF，以 FF D9 结尾）。
func coverJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// coverFor 是原图（转正后 w×h）的合规封面：长边 min(400, 原图长边)，长宽比一致。
func coverFor(t *testing.T, w, h int) []byte {
	t.Helper()
	long := min(400, max(w, h))
	short := int(math.Round(float64(min(w, h)) * float64(long) / float64(max(w, h))))
	if w >= h {
		return coverJPEG(t, long, short)
	}
	return coverJPEG(t, short, long)
}

// part 是 multipart 的一个字段。
type part struct {
	name string
	data []byte
	file bool
}

func textPart(name, v string) part        { return part{name: name, data: []byte(v)} }
func filePart(name string, b []byte) part { return part{name: name, data: b, file: true} }

// multipartBody 按给的顺序拼 multipart/form-data。
func multipartBody(t *testing.T, parts ...part) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		var dst io.Writer
		var err error
		if p.file {
			dst, err = w.CreateFormFile(p.name, "blob")
		} else {
			dst, err = w.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dst.Write(p.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// PostRaw 发一个原样的请求体。
func (c *Client) PostRaw(path, contentType string, body []byte) *Response {
	c.t.Helper()
	req, err := http.NewRequest("POST", c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	res, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("POST %s：%v", path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return &Response{t: c.t, method: "POST", path: path, Status: res.StatusCode, Header: res.Header, Body: data}
}

// UploadParts 按给的字段上传到文件夹。
func (c *Client) UploadParts(folderID uuid.UUID, parts ...part) *Response {
	c.t.Helper()
	body, ct := multipartBody(c.t, parts...)
	return c.PostRaw("/api/folders/"+folderID.String()+"/files", ct, body)
}

// Upload 上传一个文件（file_name、cover?、file）。
func (c *Client) Upload(folderID uuid.UUID, name string, data, cover []byte) *Response {
	c.t.Helper()
	parts := []part{textPart("file_name", name)}
	if cover != nil {
		parts = append(parts, filePart("cover", cover))
	}
	return c.UploadParts(folderID, append(parts, filePart("file", data))...)
}

// ingestResponse 是上传的响应。
type ingestResponse struct {
	FileID       uuid.UUID `json:"file_id"`
	SnapshotID   uuid.UUID `json:"snapshot_id"`
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileName     string    `json:"file_name"`
	Renamed      bool      `json:"renamed"`
	SortNo       int       `json:"sort_no"`
	VersionNo    int       `json:"version_no"`
	WidthPx      *int      `json:"width_px"`
	HeightPx     *int      `json:"height_px"`
	DPI          *int      `json:"dpi"`
	ColorMode    *string   `json:"color_mode"`
	FileSize     int64     `json:"file_size"`
	HasCover     bool      `json:"has_cover"`
	Warnings     []string  `json:"warnings"`
}

// uploadOK 上传一个 JPEG 样本（加了 tag 的注释，内容唯一，不带封面），要求 201。
func uploadOK(t *testing.T, c *Client, folderID uuid.UUID, name, tag string) ingestResponse {
	t.Helper()
	data := unique(sampleBytes(t, "jpeg-progressive.jpg"), tag)
	return Decode[ingestResponse](c.Upload(folderID, name, data, nil).Expect(http.StatusCreated))
}

// folderItem 是 `GET /api/folders/{id}` 的响应。
type folderItem struct {
	ID         uuid.UUID  `json:"id"`
	ProjectID  uuid.UUID  `json:"project_id"`
	ParentID   *uuid.UUID `json:"parent_id"`
	FolderName string     `json:"folder_name"`
	Remark     *string    `json:"remark"`
	PathCode   string     `json:"path_code"`
	Ancestors  []struct {
		ID         uuid.UUID `json:"id"`
		FolderName string    `json:"folder_name"`
		PathCode   string    `json:"path_code"`
	} `json:"ancestors"`
}

// mkdir 建子文件夹，要求 201。
func mkdir(c *Client, parent uuid.UUID, name string) folderItem {
	c.t.Helper()
	return Decode[folderItem](c.Post("/api/folders", map[string]any{"parent_id": parent, "folder_name": name}).Expect(http.StatusCreated))
}

// children 是 `GET /api/folders/{id}/children` 的响应。
type childrenResponse struct {
	Folders []struct {
		ID          uuid.UUID `json:"id"`
		FolderName  string    `json:"folder_name"`
		Remark      *string   `json:"remark"`
		PathCode    string    `json:"path_code"`
		FolderCount int       `json:"folder_count"`
		FileCount   int       `json:"file_count"`
	} `json:"folders"`
	Files []struct {
		FolderFileID uuid.UUID `json:"folder_file_id"`
		FileID       uuid.UUID `json:"file_id"`
		FileName     string    `json:"file_name"`
		SortNo       int       `json:"sort_no"`
		ReviewStatus string    `json:"review_status"`
		ReviewRound  int       `json:"review_round"`
		VersionNo    int       `json:"version_no"`
		Cataloged    bool      `json:"cataloged"`
	} `json:"files"`
}

func listChildren(c *Client, id uuid.UUID) childrenResponse {
	c.t.Helper()
	return Decode[childrenResponse](c.Get("/api/folders/" + id.String() + "/children").Expect(http.StatusOK))
}

// fileNames 是 children 里文件的名字（按列表顺序）。
func (r childrenResponse) fileNames() []string {
	var out []string
	for _, f := range r.Files {
		out = append(out, f.FileName)
	}
	return out
}

// catalog 把文件挂到项目归类根下的一个新条目（直接写库）：条目封存，来源文件夹是 source（不是文件所在的文件夹，
// 所以文件所在的文件夹不封存，只测"已归类的文件删不掉"）。
func (e *Env) catalog(pid, source, fileID uuid.UUID) {
	e.t.Helper()
	e.Exec(`WITH it AS (
  INSERT INTO archive_items (id, project_id, category_id, item_level, item_no, created_by, source_folder_id)
  SELECT gen_random_uuid(), $1, c.id, 'item', (SELECT count(*) + 1 FROM archive_items WHERE project_id = $1), u.id, $3
  FROM archive_categories c, users u WHERE c.project_id = $1 AND c.parent_id IS NULL AND u.username = 'admin'
  RETURNING id, created_by)
INSERT INTO archive_item_files (project_id, archive_item_id, file_id, page_no, linked_by)
SELECT $1, it.id, $2, 1, it.created_by FROM it`, pid, fileID, source)
}

// openReview 给文件加一行 open 的分配（直接写库，分配接口在 M3-1），返回它的 id。
func (e *Env) openReview(pid, fileID uuid.UUID) uuid.UUID {
	e.t.Helper()
	id := uuid.Must(uuid.NewV7())
	e.Exec(`INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by)
SELECT $1, $2, $3, u.id, u.id FROM users u WHERE u.username = 'admin'`, id, pid, fileID)
	return id
}

// uploadTmpFiles 是临时目录里还在的上传临时文件。
func uploadTmpFiles(t *testing.T, tmp string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(file.UploadDir(tmp), "*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
