package s3

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// 预签名（requirements 5.7，硬性规则 12）：
//   - 签名时间取 **UTC+8 当天 0 点**（不看容器的 TZ），同一天里同一张图的 URL 不变，工作站和浏览器能缓存；
//   - 有效期 = url_expire_hours×3600 + 86400 秒，封顶 7 天（S3 的上限）：签名时间在当天 0 点，多加一天保证
//     任何时刻拿到的 URL 至少还有 url_expire_hours 小时可用；
//   - 用存储的 public_endpoint（为空时 endpoint）签，工作站直连 S3，图片不经过 server，密钥不下发。

// MaxPresignExpiry 是预签名 URL 有效期的上限（S3 规定最多 7 天）。
const MaxPresignExpiry = 7 * 24 * time.Hour

// signZone 是按天取整用的时区：UTC+8（固定，不看容器的 TZ）。
var signZone = time.FixedZone("UTC+8", 8*3600)

// SignTime 是 now 所在的 UTC+8 自然日的 0 点（预签名的签名时间），以 UTC 表示。
func SignTime(now time.Time) time.Time {
	t := now.In(signZone)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, signZone).UTC()
}

// PresignExpiry 是预签名 URL 的有效期：url_expire_hours×3600 + 86400 秒，最多 7 天。
func PresignExpiry(urlExpireHours int) time.Duration {
	d := time.Duration(urlExpireHours)*time.Hour + 24*time.Hour
	if d > MaxPresignExpiry {
		d = MaxPresignExpiry
	}
	return d
}

// Presigned 是一个预签名 URL。
type Presigned struct {
	URL string
	// ExpiresAt 是 URL 失效的时刻（签名时间 + 有效期）。
	ExpiresAt time.Time
}

// unsignedPayload：预签名的 GET 不签请求体。
const unsignedPayload = "UNSIGNED-PAYLOAD"

// newSigner 每次签名新建一个签名器：SDK 的签名器按 access_key + 日期缓存派生的签名密钥，**不看 secret_key**，
// 共用一个签名器时，改了 secret_key（access_key 不变）或测试连接故意签错，都会拿到缓存里旧密钥算的签名。
func newSigner() *v4.Signer {
	return v4.NewSigner(func(o *v4.SignerOptions) {
		// S3 的 canonical URI 不再二次编码（objectURL 已经按 S3 的规则编码过）。
		o.DisableURIPathEscaping = true
	})
}

// PresignGet 给 key 生成预签名的 GET URL：按 cfg 的 public_endpoint 签，签名时间 SignTime(now)，
// 有效期 PresignExpiry(urlExpireHours)。只做本地计算，不访问存储。
func PresignGet(cfg Config, key string, urlExpireHours int, now time.Time) (Presigned, error) {
	return presign(cfg.publicEndpoint(), cfg, key, SignTime(now), PresignExpiry(urlExpireHours))
}

// presign 按 endpoint 签一个 GET。
func presign(endpoint string, cfg Config, key string, signTime time.Time, expiry time.Duration) (Presigned, error) {
	if key == "" {
		return Presigned{}, fmt.Errorf("s3: 预签名的 key 不能为空")
	}
	raw, err := objectURL(endpoint, cfg.Bucket, key, cfg.ForcePathStyle)
	if err != nil {
		return Presigned{}, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Presigned{}, fmt.Errorf("s3: 预签名的地址不对：%w", err)
	}
	q := u.Query()
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(expiry/time.Second), 10))
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return Presigned{}, err
	}
	creds := aws.Credentials{AccessKeyID: cfg.AccessKey, SecretAccessKey: cfg.SecretKey}
	signed, _, err := newSigner().PresignHTTP(context.Background(), creds, req, unsignedPayload, "s3", cfg.region(), signTime)
	if err != nil {
		return Presigned{}, fmt.Errorf("s3: 预签名失败：%w", err)
	}
	return Presigned{URL: signed, ExpiresAt: signTime.Add(expiry)}, nil
}

// objectURL 是对象的地址（未签名）：path-style `<endpoint>/<bucket>/<key>`，否则 `<scheme>://<bucket>.<host>/<key>`；
// key 按 S3 的规则逐段编码（保留 `/`）。bucket 为空时是桶本身（`<endpoint>/<bucket>` 或 `<scheme>://<bucket>.<host>/`）。
func objectURL(endpoint, bucket, key string, pathStyle bool) (string, error) {
	ep, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return "", fmt.Errorf("s3: 地址 %q 不对：%w", endpoint, err)
	}
	if pathStyle {
		if key == "" {
			return ep + "/" + bucket, nil
		}
		return ep + "/" + bucket + "/" + escapeKey(key), nil
	}
	scheme, host, _ := strings.Cut(ep, "://")
	return scheme + "://" + bucket + "." + host + "/" + escapeKey(key), nil
}

// escapeKey 按 S3（SigV4）的规则编码 key：字母、数字和 `-_.~` 不变，`/` 保留，其他字节 %XX（大写）。
func escapeKey(key string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
