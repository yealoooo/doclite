package shell

// M6-4：`--smoke` 的主流程（web/src/lib/smoke-flow.ts）：壳里上传、热文件夹、审核看大图、归类、导出下载。只在自检进程里，
// 设置了 DOCLITE_SMOKE_FLOW_SAMPLES 才跑：
//
//	DOCLITE_SMOKE_FLOW_SAMPLES  本机的样本文件夹（单页的 JPEG、PNG、TIFF，按文件名取前 2 × COUNT 个）
//	DOCLITE_SMOKE_FLOW_PROJECT  项目编码；不设时取第一个能进的项目
//	DOCLITE_SMOKE_FLOW_FOLDER   放自检文件夹的上级（从项目根起，/ 分隔，没有就建），默认「客户端自检」
//	DOCLITE_SMOKE_FLOW_COUNT    壳里上传、热文件夹各传几个，默认 3（1–20）
//	DOCLITE_SMOKE_FLOW_FORMAT   导出格式 pdf（默认）或 images
//
// 每跑一次在 <数据目录>/smoke/flow/<本次>/ 下复制样本，往每个文件里写一段本次独有的注释（JPEG 的 COM 段、PNG 的
// tEXt 块、TIFF 末尾），内容在项目里不会重复。系统对话框和系统浏览器在自检里都不出现：
//   - pick_and_enqueue_upload：用复制好的「壳里上传」文件夹代替系统对话框；
//   - hotfolder_pick_dir：用复制好的空文件夹代替系统对话框，smoke_flow_drop 再往里放文件（模拟扫描软件）；
//   - open_external：不打开系统浏览器，只记下地址；smoke_external_check 用 HTTP 下载它、核对 zip。

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"doclite/client/internal/proxy"
	"doclite/client/internal/upload"
)

const smokeFlowSamplesEnv = "DOCLITE_SMOKE_FLOW_SAMPLES"

// 自检里复制样本用的子文件夹名（壳里上传的那一个也是服务器上建的文件夹名）。
const (
	smokeFlowUploadDir = "壳里上传"
	smokeFlowHotDir    = "热文件夹"
	smokeFlowHotSub    = "盒 01"
)

// SmokeFlow 是 smoke_config 的 flow（web/src/lib/shell.ts 的 SmokeConfig['flow']）。
type SmokeFlow struct {
	Enabled bool    `json:"enabled"`
	Project *string `json:"project"`
	Folder  string  `json:"folder"`
	Count   int     `json:"count"`
	Format  string  `json:"format"`
}

func smokeFlowConfig() SmokeFlow {
	c := SmokeFlow{Folder: "客户端自检", Count: 3, Format: "pdf"}
	if os.Getenv(smokeFlowSamplesEnv) == "" {
		return c
	}
	c.Enabled = true
	c.Project = envPtr("DOCLITE_SMOKE_FLOW_PROJECT")
	if v := strings.Trim(strings.TrimSpace(os.Getenv("DOCLITE_SMOKE_FLOW_FOLDER")), "/"); v != "" {
		c.Folder = v
	}
	if n, err := strconv.Atoi(os.Getenv("DOCLITE_SMOKE_FLOW_COUNT")); err == nil && n >= 1 && n <= 20 {
		c.Count = n
	}
	if os.Getenv("DOCLITE_SMOKE_FLOW_FORMAT") == "images" {
		c.Format = "images"
	}
	return c
}

// smokeFlowState 是一次自检的主流程用到的本机文件夹（smoke_flow_prepare 建好）。
type smokeFlowState struct {
	run       string
	dir       string
	uploadDir string
	hotDir    string
	hotFiles  []string // 还没放进热文件夹的样本
	externals []string // open_external 收到的地址
}

// FlowPrepared 是 smoke_flow_prepare 的结果。
type FlowPrepared struct {
	Run         string   `json:"run"`
	UploadDir   string   `json:"upload_dir"`
	UploadFiles []string `json:"upload_files"`
	HotDir      string   `json:"hot_dir"`
	HotFiles    []string `json:"hot_files"`
}

// FlowDropped 是 smoke_flow_drop 的结果。
type FlowDropped struct {
	Sub   string   `json:"sub"`
	Files []string `json:"files"`
}

// ExternalCheck 是 smoke_external_check 的结果：壳收到的下载地址和用 HTTP 下载核对的结果（预签名地址本身不写进结果）。
type ExternalCheck struct {
	// Opened：自检期间 open_external 收到过几个地址。
	Opened             int        `json:"opened"`
	URLHost            string     `json:"url_host"`
	URLPath            string     `json:"url_path"`
	Status             int        `json:"status"`
	ContentType        string     `json:"content_type"`
	ContentDisposition string     `json:"content_disposition"`
	FileName           string     `json:"file_name"`
	Size               int64      `json:"size"`
	SHA256             string     `json:"sha256"`
	Entries            []ZipEntry `json:"entries"`
	EntryCount         int        `json:"entry_count"`
	MS                 int64      `json:"ms"`
}

// ZipEntry 是 zip 里的一项。
type ZipEntry struct {
	Name string `json:"name"`
	Size uint64 `json:"size"`
}

// registerSmokeFlowCommands 登记自检主流程的命令（不是自检时返回 invalid_request）。
func (s *Shell) registerSmokeFlowCommands() {
	handle(s, "smoke_flow_prepare", func(context.Context, noArgs) (any, error) {
		if s.smoke == nil {
			return nil, proxy.InvalidRequest("不是自检模式")
		}
		return s.smoke.flowPrepare()
	})
	handle(s, "smoke_flow_drop", func(context.Context, noArgs) (any, error) {
		if s.smoke == nil {
			return nil, proxy.InvalidRequest("不是自检模式")
		}
		return s.smoke.flowDrop()
	})
	handle(s, "smoke_external_check", func(ctx context.Context, a struct {
		URL      string `json:"url"`
		FileName string `json:"fileName"`
		Size     int64  `json:"size"`
	}) (any, error) {
		if s.smoke == nil {
			return nil, proxy.InvalidRequest("不是自检模式")
		}
		return s.smoke.externalCheck(ctx, a.URL, a.FileName, a.Size)
	})
}

// flowState 返回主流程的状态；还没有时为 nil。
func (r *smokeRun) flowState() *smokeFlowState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flow
}

// flowUploadPaths：自检里 pick_and_enqueue_upload 代替系统对话框选的路径（没有准备主流程时返回 false）。
func (r *smokeRun) flowUploadPaths(mode string) ([]string, bool) {
	f := r.flowState()
	if f == nil || f.uploadDir == "" {
		return nil, false
	}
	if mode == "folder" {
		return []string{f.uploadDir}, true
	}
	ents, err := os.ReadDir(f.uploadDir)
	if err != nil {
		return nil, true
	}
	var out []string
	for _, e := range ents {
		out = append(out, filepath.Join(f.uploadDir, e.Name()))
	}
	return out, true
}

// flowHotDir：自检里 hotfolder_pick_dir 代替系统对话框选的目录。
func (r *smokeRun) flowHotDir() (string, bool) {
	f := r.flowState()
	if f == nil || f.hotDir == "" {
		return "", false
	}
	return f.hotDir, true
}

// recordExternal：自检里 open_external 不打开系统浏览器，只记下地址。
func (r *smokeRun) recordExternal(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flow == nil {
		r.flow = &smokeFlowState{}
	}
	r.flow.externals = append(r.flow.externals, url)
}

// flowPrepare 复制样本：前 COUNT 个到「壳里上传」，后 COUNT 个留给热文件夹（smoke_flow_drop）。
func (r *smokeRun) flowPrepare() (*FlowPrepared, error) {
	cfg := smokeFlowConfig()
	if !cfg.Enabled {
		return nil, proxy.InvalidRequest("没有设置 " + smokeFlowSamplesEnv)
	}
	samples, err := flowSamples(os.Getenv(smokeFlowSamplesEnv))
	if err != nil {
		return nil, proxy.InvalidRequest(err.Error())
	}
	if len(samples) < 2*cfg.Count {
		return nil, proxy.InvalidRequest(fmt.Sprintf("%s 里只有 %d 个 JPEG、PNG、TIFF，要 %d 个", smokeFlowSamplesEnv, len(samples), 2*cfg.Count))
	}
	run := "自检 " + time.Now().Format("20060102-150405")
	dir := filepath.Join(r.s.cfg.DataDir, "flow", run)
	st := &smokeFlowState{run: run, dir: dir, uploadDir: filepath.Join(dir, smokeFlowUploadDir), hotDir: filepath.Join(dir, smokeFlowHotDir)}
	for _, d := range []string{st.uploadDir, st.hotDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, proxy.Internal("建不了自检的文件夹：" + err.Error())
		}
	}
	out := &FlowPrepared{Run: run, UploadDir: smokeFlowUploadDir, HotDir: st.hotDir}
	for i, src := range samples[:cfg.Count] {
		name, err := copyUnique(src, st.uploadDir, fmt.Sprintf("doclite smoke %s upload %d", run, i+1))
		if err != nil {
			return nil, proxy.Internal(err.Error())
		}
		out.UploadFiles = append(out.UploadFiles, name)
	}
	st.hotFiles = samples[cfg.Count : 2*cfg.Count]
	for _, src := range st.hotFiles {
		out.HotFiles = append(out.HotFiles, filepath.Base(src))
	}
	r.mu.Lock()
	if r.flow != nil {
		st.externals = r.flow.externals
	}
	r.flow = st
	r.mu.Unlock()
	r.s.log.Info("自检：主流程的样本已复制", "run", run, "dir", dir, "upload", len(out.UploadFiles), "hot", len(out.HotFiles))
	return out, nil
}

// flowDrop 往热文件夹的子文件夹里放样本（像扫描软件那样一张张写进去）。
func (r *smokeRun) flowDrop() (*FlowDropped, error) {
	st := r.flowState()
	if st == nil || st.hotDir == "" {
		return nil, proxy.InvalidRequest("先调 smoke_flow_prepare")
	}
	sub := filepath.Join(st.hotDir, smokeFlowHotSub)
	if err := os.MkdirAll(sub, 0o700); err != nil {
		return nil, proxy.Internal("建不了热文件夹的子文件夹：" + err.Error())
	}
	out := &FlowDropped{Sub: smokeFlowHotSub}
	for i, src := range st.hotFiles {
		name, err := copyUnique(src, sub, fmt.Sprintf("doclite smoke %s hotfolder %d", st.run, i+1))
		if err != nil {
			return nil, proxy.Internal(err.Error())
		}
		out.Files = append(out.Files, name)
		time.Sleep(150 * time.Millisecond)
	}
	return out, nil
}

// externalCheck 核对 open_external 收到的地址：就是页面拿到的下载地址；用 HTTP 下载（不经会话代理、不带 Cookie，
// 和系统浏览器一样直连存储），核对状态、Content-Disposition 里的文件名、大小，按 zip 读出目录。
func (r *smokeRun) externalCheck(ctx context.Context, url, fileName string, size int64) (*ExternalCheck, error) {
	r.mu.Lock()
	var opened []string
	dir := r.s.cfg.DataDir
	if r.flow != nil {
		opened = append(opened, r.flow.externals...)
		if r.flow.dir != "" {
			dir = r.flow.dir
		}
	}
	r.mu.Unlock()
	if len(opened) == 0 || opened[len(opened)-1] != url {
		return nil, proxy.InvalidRequest(fmt.Sprintf("壳最后收到的地址不是这个下载地址（收到 %d 个）", len(opened)))
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, proxy.InvalidRequest("下载地址不对：" + err.Error())
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, &proxy.Error{Kind: proxy.KindNetwork, Message: "下载失败：" + err.Error()}
	}
	defer resp.Body.Close()
	out := &ExternalCheck{Opened: len(opened), URLHost: req.URL.Host, URLPath: req.URL.Path, Status: resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"), ContentDisposition: resp.Header.Get("Content-Disposition")}
	if _, params, err := mime.ParseMediaType(out.ContentDisposition); err == nil {
		out.FileName = params["filename"]
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, proxy.InvalidRequest(fmt.Sprintf("下载返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	tmp := filepath.Join(dir, "download.zip")
	f, err := os.Create(tmp)
	if err != nil {
		return nil, proxy.Internal("建不了临时文件：" + err.Error())
	}
	defer os.Remove(tmp)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, &proxy.Error{Kind: proxy.KindNetwork, Message: "下载中断：" + err.Error()}
	}
	out.Size = n
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	out.MS = time.Since(started).Milliseconds()
	if fileName != "" && out.FileName != fileName {
		return nil, proxy.InvalidRequest(fmt.Sprintf("Content-Disposition 的文件名是「%s」，应为「%s」", out.FileName, fileName))
	}
	if size > 0 && n != size {
		return nil, proxy.InvalidRequest(fmt.Sprintf("下载了 %d 字节，接口说是 %d 字节", n, size))
	}
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return nil, proxy.InvalidRequest("下载的不是 zip：" + err.Error())
	}
	defer zr.Close()
	out.EntryCount = len(zr.File)
	for _, zf := range zr.File {
		if len(out.Entries) < 100 {
			out.Entries = append(out.Entries, ZipEntry{Name: zf.Name, Size: zf.UncompressedSize64})
		}
	}
	return out, nil
}

// flowSamples 列出样本文件夹里支持的图片（按文件名）。
func flowSamples(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读不了 %s：%v", smokeFlowSamplesEnv, err)
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && upload.SupportedImage(e.Name()) && !upload.IgnoredName(e.Name(), false) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// copyUnique 把样本复制到 dir（文件名不变），并写进一段这次独有的注释，让内容在项目里不重复。
func copyUnique(src, dir, marker string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("读不了样本 %s：%v", src, err)
	}
	data, err = withMarker(data, marker)
	if err != nil {
		return "", fmt.Errorf("样本 %s：%v", filepath.Base(src), err)
	}
	name := filepath.Base(src)
	// 先写临时名再改名，热文件夹不会看到写了一半的文件（看到了也会等写完）。
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("写不了 %s：%v", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return "", fmt.Errorf("改名失败：%v", err)
	}
	return name, nil
}

// withMarker 往图片里写一段注释：JPEG 在 SOI 后插 COM 段，PNG 在 IHDR 后插 tEXt 块，TIFF 加在文件末尾（不在任何
// IFD 引用的范围里）。像素不变。
func withMarker(data []byte, marker string) ([]byte, error) {
	switch {
	case len(data) > 4 && data[0] == 0xFF && data[1] == 0xD8:
		seg := []byte{0xFF, 0xFE, 0, 0}
		binary.BigEndian.PutUint16(seg[2:], uint16(len(marker)+2))
		seg = append(seg, marker...)
		return append(append(append([]byte{}, data[:2]...), seg...), data[2:]...), nil
	case len(data) > 33 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		// 签名 8 字节 + IHDR（长度 4、类型 4、数据 13、CRC 4）= 33 字节。
		body := append([]byte("tEXt"), append([]byte("Comment\x00"), marker...)...)
		chunk := make([]byte, 4, 12+len(body))
		binary.BigEndian.PutUint32(chunk, uint32(len(body)-4))
		chunk = append(chunk, body...)
		chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
		return append(append(append([]byte{}, data[:33]...), chunk...), data[33:]...), nil
	case len(data) > 8 && (bytes.Equal(data[:4], []byte("II*\x00")) || bytes.Equal(data[:4], []byte("MM\x00*"))):
		return append(append([]byte{}, data...), marker...), nil
	}
	return nil, errors.New("不是 JPEG、PNG、TIFF")
}
