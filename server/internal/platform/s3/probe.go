package s3

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// 测试连接（requirements 5.4）。不写入任何对象，只读：
//
//  1. 用这对密钥 List（limit 1）一个不存在的目录 `<path_prefix>doclite-connection-test/`：能同时发现连不上、
//     密钥错、桶不存在；
//  2. List 成功还不够（对什么请求都回 200 的普通 HTTP 服务也能"成功"）：再用**故意签错的**请求 List 一次，
//     对端必须按 S3 的方式拒绝（403 + S3 错误码），才说明它是 S3 而且校验签名；
//  3. public_endpoint 和 endpoint 不同时：用 public_endpoint 签一个不存在的 key 的预签名 GET，从 server 访问，
//     应得 404 NoSuchKey。
//
// 不跟随重定向；每个响应体最多读 64 KiB；只回简短摘要（状态码、S3 错误码、本地的网络错误），不回显对端的响应。

// 测试连接的结论（ConnectionTestResult.result）。
const (
	ResultOK               = "ok"
	ResultUnreachable      = "unreachable"
	ResultAuthFailed       = "auth_failed"
	ResultBucketNotFound   = "bucket_not_found"
	ResultSecretUnreadable = "secret_unreadable" // 只由 storage 模块给出（密钥解不开，没有访问存储）
	ResultOther            = "other"
)

// public_endpoint 检查的结论。
const (
	PublicOK          = "ok"
	PublicUnreachable = "unreachable"
	PublicRejected    = "rejected"
)

// probeDir 是测试连接列的目录（接在 path_prefix 后面），正常情况下不存在。
const probeDir = "doclite-connection-test/"

// maxProbeBody 是测试连接时每个响应体最多读的字节数（S3 的 List（limit 1）和错误响应都只有几百字节）。
const maxProbeBody = 64 << 10

// wrongSecretSuffix 接在 secret_key 后面，让签名一定不对。
const wrongSecretSuffix = "-doclite-signature-check"

const maxDetailChars = 300

// TestReport 是测试连接的结果（接口 `ConnectionTestResult` 的字段）。
type TestReport struct {
	OK      bool   `json:"ok"`
	Result  string `json:"result"`
	Message string `json:"message"`
	// Detail 是排查用的简短摘要；成功时为 null。
	Detail              *string      `json:"detail"`
	ElapsedMS           int64        `json:"elapsed_ms"`
	PublicEndpointCheck *PublicCheck `json:"public_endpoint_check"`
}

// PublicCheck 是 public_endpoint 的检查。
type PublicCheck struct {
	Result     string `json:"result"`
	Message    string `json:"message"`
	HTTPStatus *int   `json:"http_status"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

// probeClient：不跟随重定向，连接 5 秒、整体 10 秒超时，不用代理。
var probeClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableKeepAlives:     true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// probeResponse 是一次探测请求的结果。
type probeResponse struct {
	status  int
	code    string // S3 错误码（`<Error><Code>`），没有时为空
	listing bool   // 响应体是 S3 的 List 结果（`<ListBucketResult>`）
	tooBig  bool
}

// TestConnection 测试 cfg 能不能用（path_prefix 已校验）。连接失败不是错误，结论在报告里。
func TestConnection(ctx context.Context, cfg Config, pathPrefix string) TestReport {
	start := time.Now()
	var report TestReport
	if ep, err := NormalizeEndpoint(cfg.Endpoint); err != nil {
		report = failed(ResultOther, "endpoint 不对："+err.Error(), err.Error())
	} else {
		cfg.Endpoint = ep
		report = testList(ctx, cfg, pathPrefix)
	}
	report.ElapsedMS = time.Since(start).Milliseconds()
	report.OK = report.Result == ResultOK
	if report.OK && cfg.PublicEndpoint != "" {
		if pub, err := NormalizeEndpoint(cfg.PublicEndpoint); err == nil && pub != cfg.Endpoint {
			report.PublicEndpointCheck = checkPublicEndpoint(ctx, cfg, pub, pathPrefix)
		}
	}
	if report.Detail != nil {
		d := scrub(truncateRunes(*report.Detail, maxDetailChars), cfg.SecretKey)
		report.Detail = &d
	}
	return report
}

func failed(result, message, detail string) TestReport {
	return TestReport{Result: result, Message: message, Detail: &detail}
}

func testList(ctx context.Context, cfg Config, pathPrefix string) TestReport {
	res, err := listProbe(ctx, cfg, cfg.SecretKey, pathPrefix)
	if err != nil {
		return networkFailure(cfg, err)
	}
	if !res.listing || res.status != http.StatusOK {
		return classify(cfg, res)
	}
	// 第 2 步：故意签错。
	wrong, err := listProbe(ctx, cfg, cfg.SecretKey+wrongSecretSuffix, pathPrefix)
	if err != nil {
		return networkFailure(cfg, err)
	}
	switch {
	case wrong.status == http.StatusForbidden && wrong.code != "":
		return TestReport{Result: ResultOK, Message: fmt.Sprintf("连接成功：能用这对密钥访问 %s 上的桶 %s", cfg.Endpoint, cfg.Bucket)}
	case wrong.status == http.StatusOK:
		return failed(ResultOther,
			fmt.Sprintf("%s 接受了故意签错的请求：它不校验签名，不是 S3 服务（或者前面的代理把请求拦下来了）", cfg.Endpoint),
			"签错的请求也返回了成功")
	default:
		s := summary(wrong)
		return failed(ResultOther,
			fmt.Sprintf("%s 对故意签错的请求返回了 %s，没有按 S3 的方式拒绝：它可能不是 S3 服务", cfg.Endpoint, s),
			"签错的请求："+s)
	}
}

// listProbe 发一个签好名的 ListObjectsV2（max-keys=1）。
func listProbe(ctx context.Context, cfg Config, secret, pathPrefix string) (*probeResponse, error) {
	raw, err := objectURL(cfg.Endpoint, cfg.Bucket, "", cfg.ForcePathStyle)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("max-keys", "1")
	q.Set("prefix", pathPrefix+probeDir)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw+"?"+strings.ReplaceAll(q.Encode(), "+", "%20"), nil)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(nil)
	payload := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", payload)
	creds := aws.Credentials{AccessKeyID: cfg.AccessKey, SecretAccessKey: secret}
	if err := newSigner().SignHTTP(ctx, creds, req, payload, "s3", cfg.region(), time.Now().UTC()); err != nil {
		return nil, err
	}
	return doProbe(req)
}

func doProbe(req *http.Request) (*probeResponse, error) {
	res, err := probeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxProbeBody+1))
	if err != nil {
		return nil, err
	}
	out := &probeResponse{status: res.StatusCode}
	if len(body) > maxProbeBody {
		out.tooBig = true
		return out, nil
	}
	var root struct {
		XMLName xml.Name
		Code    string `xml:"Code"`
	}
	if xml.Unmarshal(body, &root) == nil {
		switch root.XMLName.Local {
		case "ListBucketResult":
			out.listing = true
		case "Error":
			out.code = cleanCode(root.Code)
		}
	}
	return out, nil
}

// cleanCode 只留 S3 错误码里的字母数字，最长 64 个字符。
func cleanCode(code string) string {
	var b strings.Builder
	for _, c := range code {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

func summary(r *probeResponse) string {
	switch {
	case r.tooBig:
		return fmt.Sprintf("HTTP %d，响应体超过 %d KiB", r.status, maxProbeBody>>10)
	case r.code != "":
		return fmt.Sprintf("HTTP %d，S3 错误码 %s", r.status, r.code)
	case r.listing:
		return fmt.Sprintf("HTTP %d，S3 的列表响应", r.status)
	default:
		return fmt.Sprintf("HTTP %d，不是 S3 格式的响应", r.status)
	}
}

// networkFailure：请求没发出去或没收到响应（拒绝连接、DNS、TLS、超时）。
func networkFailure(cfg Config, err error) TestReport {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return failed(ResultUnreachable, fmt.Sprintf("连接 %s 超时：10 秒内没有响应", cfg.Endpoint), "超时")
	}
	cause := rootCause(err)
	return failed(ResultUnreachable,
		fmt.Sprintf("连不上 %s（地址或端口不对、服务没启动、网络不通，或 https 证书有问题）：%s", cfg.Endpoint, cause),
		"发请求失败："+cause)
}

// rootCause 是错误链最里面的那一层（去掉 Go 加的 "Get \"http://…\":" 这类前缀）。
func rootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

// classify：第 1 步收到了响应，但不是成功的 List。
func classify(cfg Config, r *probeResponse) TestReport {
	pathHint := ""
	if cfg.ForcePathStyle {
		pathHint = "；如果这个存储要求用虚拟主机方式（bucket.host）访问，试试关闭 path-style"
	}
	ep, bucket := cfg.Endpoint, cfg.Bucket
	s := summary(r)
	switch r.code {
	case "NoSuchBucket":
		return failed(ResultBucketNotFound,
			fmt.Sprintf("桶 %s 不存在：请先在存储上建桶（SeaweedFS：weed shell 里执行 s3.bucket.create -name %s）", bucket, bucket), s)
	case "InvalidAccessKeyId":
		return failed(ResultAuthFailed, "密钥不对：存储上没有这个 access_key", s)
	case "SignatureDoesNotMatch":
		return failed(ResultAuthFailed, "密钥不对：签名校验失败，secret_key 和 access_key 对不上", s)
	case "AccessDenied":
		return failed(ResultAuthFailed, fmt.Sprintf("存储拒绝访问：这对密钥没有访问桶 %s 的权限，或者密钥不对", bucket), s)
	case "RequestTimeTooSkewed":
		return failed(ResultOther, "server 和存储的时钟相差太大，请求被拒绝：请校准两边的时间", s)
	case "AuthorizationHeaderMalformed":
		return failed(ResultOther, fmt.Sprintf("存储不接受这个签名：region（%s）可能不对", cfg.region()), s)
	case "PermanentRedirect", "TemporaryRedirect":
		return failed(ResultOther, fmt.Sprintf("存储要求换一个地址访问（%s）：检查 endpoint 和 region（%s）%s", r.code, cfg.region(), pathHint), s)
	case "InvalidBucketName":
		return failed(ResultOther, fmt.Sprintf("存储不接受桶名 %s%s", bucket, pathHint), s)
	case "":
	default:
		if r.status == http.StatusForbidden {
			return failed(ResultOther, fmt.Sprintf("存储拒绝访问（HTTP 403，错误码 %s），原因不明%s", r.code, pathHint), s)
		}
		return failed(ResultOther, fmt.Sprintf("存储返回了错误 %s（HTTP %d）", r.code, r.status), s)
	}
	switch {
	case r.tooBig:
		return failed(ResultOther, fmt.Sprintf("%s 返回的内容超过 %d KiB，不像 S3 的响应：它可能不是 S3 服务", ep, maxProbeBody>>10), s)
	case r.status >= 300 && r.status < 400:
		return failed(ResultOther, fmt.Sprintf("%s 返回了重定向（HTTP %d），测试连接不跟随重定向：请直接填最终的地址（例如 http 被重定向到了 https）", ep, r.status), s)
	case r.status == http.StatusOK:
		return failed(ResultOther, fmt.Sprintf("%s 返回的内容不是 S3 的列表响应：它可能不是 S3 服务", ep), s)
	case r.status == http.StatusForbidden:
		return failed(ResultOther, fmt.Sprintf("%s 返回 403，但不是 S3 格式的错误：它可能不是 S3 服务，或者中间的代理拦截了请求%s", ep, pathHint), s)
	case r.status == http.StatusNotFound:
		return failed(ResultOther, fmt.Sprintf("%s 能连上，但返回 404：它可能不是 S3 服务", ep), s)
	case r.status >= 500:
		return failed(ResultOther, fmt.Sprintf("存储返回 HTTP %d（服务端错误）：存储可能还没启动完，或者出了故障，请稍后再试", r.status), s)
	default:
		return failed(ResultOther, fmt.Sprintf("%s 返回 HTTP %d，不是 S3 的响应：它可能不是 S3 服务", ep, r.status), s)
	}
}

// checkPublicEndpoint：用 public_endpoint 签一个不存在的对象的预签名 GET，从 server 访问，期望 404 NoSuchKey。
func checkPublicEndpoint(ctx context.Context, cfg Config, public, pathPrefix string) *PublicCheck {
	start := time.Now()
	out := &PublicCheck{}
	defer func() { out.ElapsedMS = time.Since(start).Milliseconds() }()
	key := pathPrefix + probeDir + randomName() + ".txt"
	p, err := presign(public, cfg, key, time.Now().UTC(), 5*time.Minute, PresignOptions{})
	if err != nil {
		out.Result, out.Message = PublicRejected, "public_endpoint "+public+" 不能用来签名："+err.Error()
		return out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		out.Result, out.Message = PublicRejected, "public_endpoint "+public+" 不能用来签名："+err.Error()
		return out
	}
	res, err := doProbe(req)
	if err != nil {
		out.Result = PublicUnreachable
		out.Message = fmt.Sprintf("server 访问不到 public_endpoint %s（%s）。工作站和 server 不在同一个网络时这可能是正常的，"+
			"请在工作站上确认能打开这个地址", public, rootCause(err))
		return out
	}
	status := res.status
	out.HTTPStatus = &status
	switch {
	case status == http.StatusNotFound && res.code == "NoSuchKey":
		out.Result = PublicOK
		out.Message = fmt.Sprintf("public_endpoint %s 可用：server 按它签名的 URL 通过了存储的校验（收到预期的 404 NoSuchKey）", public)
	case res.code == "NoSuchBucket":
		out.Result = PublicRejected
		out.Message = fmt.Sprintf("public_endpoint %s 上没有桶 %s：它可能不是同一个 S3 存储", public, cfg.Bucket)
	case status == http.StatusForbidden && res.code == "SignatureDoesNotMatch":
		out.Result = PublicRejected
		out.Message = fmt.Sprintf("public_endpoint %s 不认按它签名的 URL（签名校验失败）：前面可能有改写 Host 的代理，"+
			"或者它不是同一个 S3 存储", public)
	default:
		out.Result = PublicRejected
		out.Message = fmt.Sprintf("public_endpoint %s 能连上，但返回的是 %s，不是预期的 404 NoSuchKey：它可能不是同一个 S3 存储",
			public, summary(res))
	}
	return out
}

func randomName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// scrub 去掉摘要里可能出现的 secret_key（防御性的：摘要本来只有状态码和错误码）。
func scrub(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}
