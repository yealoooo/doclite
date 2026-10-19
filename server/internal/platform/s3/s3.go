// Package s3 是 S3 兼容存储的访问（aws-sdk-go-v2）：对象读写、预签名、测试连接。和业务无关：
// 连哪个存储、密钥是什么由 internal/storage 决定（存储配置），这里只拿 [Config]。
//
//   - endpoint 是 server 访问的地址；预签名用 public_endpoint（为空时同 endpoint）签，工作站拿 URL 直连 S3；
//   - 默认 path-style（`<endpoint>/<bucket>/<key>`，SeaweedFS 和多数自建 S3 都是），ForcePathStyle 为 false 时用
//     虚拟主机方式（`<scheme>://<bucket>.<host>/<key>`）；
//   - 只在需要时算校验和（RequestChecksumCalculationWhenRequired），兼容不支持 aws-chunked 的 S3 实现；
//   - 上传要 io.ReadSeeker（临时文件、bytes.Reader）：http 下 v4 签名要先算请求体的 sha256。
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// DefaultRegion 是不填 region 时用的值（SeaweedFS 不校验 region）。
const DefaultRegion = "us-east-1"

// Config 是连接一个存储的参数。SecretKey 是明文：不要打日志（String 不输出它）。
type Config struct {
	// Endpoint 是 server 访问的地址（[NormalizeEndpoint] 规整过：`http(s)://host[:port]`，没有路径）。
	Endpoint string
	// PublicEndpoint 是工作站访问的地址，预签名按它签；为空时同 Endpoint。
	PublicEndpoint string
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

// String 不输出 SecretKey。
func (c Config) String() string {
	return fmt.Sprintf("s3.Config{Endpoint:%s PublicEndpoint:%s Bucket:%s Region:%s AccessKey:%s ForcePathStyle:%t}",
		c.Endpoint, c.PublicEndpoint, c.Bucket, c.Region, c.AccessKey, c.ForcePathStyle)
}

// GoString 不输出 SecretKey。
func (c Config) GoString() string { return c.String() }

func (c Config) region() string {
	if c.Region == "" {
		return DefaultRegion
	}
	return c.Region
}

// publicEndpoint 是预签名用的地址。
func (c Config) publicEndpoint() string {
	if c.PublicEndpoint != "" {
		return c.PublicEndpoint
	}
	return c.Endpoint
}

// NormalizeEndpoint 检查并规整 S3 地址：`http://` 或 `https://` 开头、有主机名、只能是根路径（没有路径、查询参数、
// 片段和用户名密码）。返回 `scheme://host[:port]`（scheme 小写、去掉末尾的 `/`）；不合格时返回给人看的原因。
func NormalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	u, err := url.Parse(endpoint)
	if err != nil || u.Opaque != "" {
		return "", errors.New("不是合法的地址，应为 http://主机:端口 的形式")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("要以 http:// 或 https:// 开头")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("缺少主机名")
	}
	if u.User != nil {
		return "", errors.New("不能带用户名和密码")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(endpoint, "#") {
		return "", errors.New("只能是根路径（http://主机:端口），不能带路径和参数")
	}
	if p := u.Port(); p != "" {
		for _, c := range p {
			if c < '0' || c > '9' {
				return "", errors.New("端口不对")
			}
		}
	}
	return scheme + "://" + u.Host, nil
}

// Client 是一个存储的客户端（对象读写）。可以并发使用。
type Client struct {
	cfg Config
	api *awss3.Client
}

// httpClient：业务操作用的 HTTP 客户端（连接 10 秒超时；整体不设超时，大文件上传下载靠 ctx 控制）。
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
	},
}

// New 建客户端：只做本地检查，不访问存储。
func New(cfg Config) (*Client, error) {
	ep, err := NormalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint 不对：%w", err)
	}
	cfg.Endpoint = ep
	if cfg.PublicEndpoint != "" {
		if cfg.PublicEndpoint, err = NormalizeEndpoint(cfg.PublicEndpoint); err != nil {
			return nil, fmt.Errorf("public_endpoint 不对：%w", err)
		}
	}
	if cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("bucket、access_key、secret_key 不能为空")
	}
	api := awss3.New(awss3.Options{
		Region:                     cfg.region(),
		BaseEndpoint:               aws.String(cfg.Endpoint),
		UsePathStyle:               cfg.ForcePathStyle,
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		HTTPClient:                 httpClient,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		RetryMaxAttempts:           3,
	})
	return &Client{cfg: cfg, api: api}, nil
}

// Config 是客户端的连接参数（规整过）。
func (c *Client) Config() Config { return c.cfg }

// PutOptions 是上传对象时的可选项。
type PutOptions struct {
	ContentType  string
	CacheControl string
	// Metadata 是自定义元数据（x-amz-meta-*）；值要是 ASCII（原始文件名先 URL 编码）。
	Metadata map[string]string
}

// Put 上传一个对象（单次 PUT）。body 从当前位置读到末尾。
func (c *Client) Put(ctx context.Context, key string, body io.ReadSeeker, opts PutOptions) error {
	in := &awss3.PutObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key), Body: body, Metadata: opts.Metadata}
	if opts.ContentType != "" {
		in.ContentType = aws.String(opts.ContentType)
	}
	if opts.CacheControl != "" {
		in.CacheControl = aws.String(opts.CacheControl)
	}
	_, err := c.api.PutObject(ctx, in)
	return wrap("上传", key, err)
}

// Get 读一个对象；调用方负责关闭。不存在时返回的错误满足 errors.Is(err, ErrNotFound)。
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, wrap("读取", key, err)
	}
	return out.Body, nil
}

// ObjectInfo 是读对象时一起拿到的属性。
type ObjectInfo struct {
	// Size 是对象的大小（响应的 Content-Length；不知道时为 -1）。
	Size         int64
	ContentType  string
	CacheControl string
	// Metadata 是自定义元数据（x-amz-meta-*，键是小写的）。
	Metadata map[string]string
}

// GetWithInfo 读一个对象，同时返回它的属性（复制对象时原样带上元数据）；调用方负责关闭。不存在时返回的错误满足
// errors.Is(err, ErrNotFound)。
func (c *Client) GetWithInfo(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	out, err := c.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, ObjectInfo{}, wrap("读取", key, err)
	}
	info := ObjectInfo{Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		CacheControl: aws.ToString(out.CacheControl), Metadata: out.Metadata}
	if out.ContentLength == nil {
		info.Size = -1
	}
	return out.Body, info, nil
}

// Delete 删一个对象（不存在也算成功）。
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key)})
	return wrap("删除", key, err)
}

// List 列出 prefix 下的 key（最多 max 个，按 key 排序）。
func (c *Client) List(ctx context.Context, prefix string, max int) ([]string, error) {
	var keys []string
	p := awss3.NewListObjectsV2Paginator(c.api, &awss3.ListObjectsV2Input{Bucket: aws.String(c.cfg.Bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() && len(keys) < max {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, wrap("列出", prefix, err)
		}
		for _, o := range page.Contents {
			if len(keys) >= max {
				break
			}
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// ErrNotFound：对象不存在。
var ErrNotFound = errors.New("对象不存在")

// wrap 把 SDK 的错误变成带动作和 key 的错误；不存在时包含 ErrNotFound。
func wrap(action, key string, err error) error {
	if err == nil {
		return nil
	}
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return fmt.Errorf("s3 %s %s：%w", action, key, ErrNotFound)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("s3 %s %s 失败：%s（%s）", action, key, apiErr.ErrorCode(), apiErr.ErrorMessage())
	}
	return fmt.Errorf("s3 %s %s 失败：%w", action, key, err)
}
