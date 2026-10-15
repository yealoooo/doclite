package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// 流式上传（导出物，硬性规则 14：边生成边上传）：
//   - Write 的数据先攒进一片（partSize），攒够一片就交给后台上传，同时继续攒下一片；同一时刻最多一片在传、一片在攒，
//     内存约 2 × partSize；
//   - 第一片攒满时才发起分片上传（CreateMultipartUpload）；总大小不到一片时 Complete 用一次 PutObject；
//   - 出错、取消时调 Abort：中止分片上传（AbortMultipartUpload，S3 删掉已传的分片），不留下对象。Complete 失败时
//     自己中止。没完成也没中止的分片上传（进程被杀）留在 S3 里，看不见、不占 key，由运维清理（SeaweedFS：
//     `s3.clean.uploads`）。

// 分片大小的范围（S3 的规定：除最后一片外每片至少 5 MiB，最多 10000 片）。
const (
	DefaultPartSize = 8 << 20
	MinPartSize     = 5 << 20
	MaxPartSize     = 512 << 20
	MaxParts        = 10000
)

// PartSizeFor 是预计 total 字节的对象该用的分片大小：默认 8 MiB，预计很大时放大到"total × 1.25 ÷ 9000"（留余量，
// 不超过 10000 片），最多 512 MiB。
func PartSizeFor(total int64) int64 {
	size := int64(DefaultPartSize)
	if need := total/9000*5/4 + 1; need > size {
		size = need
	}
	return min(size, MaxPartSize)
}

// Upload 是一次流式上传，用 [Client.NewUpload] 建。不能并发调用。
type Upload struct {
	c        *Client
	ctx      context.Context
	key      string
	opts     PutOptions
	partSize int

	buf      []byte // 正在攒的这一片
	spare    []byte // 传完的那一片的缓冲区，下一片复用
	uploadID string
	nextPart int32
	parts    []types.CompletedPart
	inflight chan partResult // 正在后台上传的那一片的结果；没有时为 nil

	size int64
	err  error
	done bool
}

type partResult struct {
	no   int32
	etag *string
	data []byte
	err  error
}

// NewUpload 开始上传 key（还不访问存储）。partSize 不大于 0 时用 8 MiB，大于 512 MiB 时按 512 MiB。真实的 S3 要求
// 除最后一片外每片至少 5 MiB（MinPartSize）：正式代码用 [PartSizeFor]，更小的只给测试用（SeaweedFS 不查）。
func (c *Client) NewUpload(ctx context.Context, key string, partSize int64, opts PutOptions) *Upload {
	if partSize <= 0 {
		partSize = DefaultPartSize
	}
	partSize = min(partSize, MaxPartSize)
	return &Upload{c: c, ctx: ctx, key: key, opts: opts, partSize: int(partSize)}
}

// Size 是已经写进来的字节数。
func (u *Upload) Size() int64 { return u.size }

// Write 写数据：攒够一片就交给后台上传。返回的错误是之前的分片上传失败、ctx 取消，之后的 Write 都返回同一个错误。
func (u *Upload) Write(p []byte) (int, error) {
	if u.err != nil {
		return 0, u.err
	}
	if u.done {
		return 0, errors.New("s3: 上传已经结束")
	}
	n := 0
	for len(p) > 0 {
		if u.buf == nil {
			u.buf = u.takeBuffer()
		}
		k := min(u.partSize-len(u.buf), len(p))
		u.buf = append(u.buf, p[:k]...)
		p = p[k:]
		n += k
		u.size += int64(k)
		if len(u.buf) == u.partSize {
			if err := u.sendPart(); err != nil {
				u.err = err
				return n, err
			}
		}
	}
	return n, nil
}

func (u *Upload) takeBuffer() []byte {
	if u.spare != nil {
		b := u.spare[:0]
		u.spare = nil
		return b
	}
	return make([]byte, 0, u.partSize)
}

// sendPart 把攒好的这一片交给后台上传（先等上一片传完）。
func (u *Upload) sendPart() error {
	if err := u.wait(); err != nil {
		return err
	}
	if err := u.ctx.Err(); err != nil {
		return err
	}
	if u.uploadID == "" {
		in := &awss3.CreateMultipartUploadInput{Bucket: aws.String(u.c.cfg.Bucket), Key: aws.String(u.key), Metadata: u.opts.Metadata}
		if u.opts.ContentType != "" {
			in.ContentType = aws.String(u.opts.ContentType)
		}
		if u.opts.CacheControl != "" {
			in.CacheControl = aws.String(u.opts.CacheControl)
		}
		out, err := u.c.api.CreateMultipartUpload(u.ctx, in)
		if err != nil {
			return wrap("开始分片上传", u.key, err)
		}
		u.uploadID = aws.ToString(out.UploadId)
	}
	if u.nextPart >= MaxParts {
		return fmt.Errorf("s3 上传 %s：超过 %d 片（%d 字节）", u.key, MaxParts, u.size)
	}
	u.nextPart++
	data := u.buf
	u.buf = nil
	ch := make(chan partResult, 1)
	u.inflight = ch
	go func(no int32) {
		out, err := u.c.api.UploadPart(u.ctx, &awss3.UploadPartInput{
			Bucket: aws.String(u.c.cfg.Bucket), Key: aws.String(u.key), UploadId: aws.String(u.uploadID),
			PartNumber: aws.Int32(no), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
		})
		r := partResult{no: no, data: data, err: err}
		if err == nil {
			r.etag = out.ETag
		}
		ch <- r
	}(u.nextPart)
	return nil
}

// wait 等正在后台上传的那一片传完。
func (u *Upload) wait() error {
	if u.inflight == nil {
		return nil
	}
	r := <-u.inflight
	u.inflight = nil
	u.spare = r.data
	if r.err != nil {
		return wrap(fmt.Sprintf("上传第 %d 片", r.no), u.key, r.err)
	}
	u.parts = append(u.parts, types.CompletedPart{ETag: r.etag, PartNumber: aws.Int32(r.no)})
	return nil
}

// Complete 传完剩下的数据、完成上传，返回对象的大小。失败时自己中止（不留对象）。
func (u *Upload) Complete() (int64, error) {
	if u.done {
		return 0, errors.New("s3: 上传已经结束")
	}
	err := u.err
	if err == nil {
		err = u.complete()
	}
	if err != nil {
		_ = u.Abort()
		return 0, err
	}
	u.done = true
	return u.size, nil
}

func (u *Upload) complete() error {
	if u.uploadID == "" {
		// 不到一片：一次 PUT。
		if err := u.c.Put(u.ctx, u.key, bytes.NewReader(u.buf), u.opts); err != nil {
			return err
		}
		u.buf = nil
		return nil
	}
	if len(u.buf) > 0 {
		if err := u.sendPart(); err != nil {
			return err
		}
	}
	if err := u.wait(); err != nil {
		return err
	}
	_, err := u.c.api.CompleteMultipartUpload(u.ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: aws.String(u.c.cfg.Bucket), Key: aws.String(u.key), UploadId: aws.String(u.uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: u.parts},
	})
	return wrap("完成分片上传", u.key, err)
}

// abortTimeout 是中止分片上传最多等多久（ctx 已经取消时也要中止，用单独的超时）。
const abortTimeout = 30 * time.Second

// Abort 中止上传：等正在传的那一片结束，再中止分片上传（S3 删掉已传的分片）。ctx 已经取消也照样中止。
// 已经完成或中止过的什么都不做。
func (u *Upload) Abort() error {
	if u.done {
		return nil
	}
	u.done = true
	_ = u.wait()
	u.buf, u.spare = nil, nil
	if u.uploadID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(u.ctx), abortTimeout)
	defer cancel()
	_, err := u.c.api.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket: aws.String(u.c.cfg.Bucket), Key: aws.String(u.key), UploadId: aws.String(u.uploadID),
	})
	return wrap("中止分片上传", u.key, err)
}

// PendingUploads 是 prefix 下没完成也没中止的分片上传的 key（运维检查、测试用）。
func (c *Client) PendingUploads(ctx context.Context, prefix string) ([]string, error) {
	out, err := c.api.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String(c.cfg.Bucket), Prefix: aws.String(prefix)})
	if err != nil {
		return nil, wrap("列出分片上传", prefix, err)
	}
	keys := make([]string, 0, len(out.Uploads))
	for _, u := range out.Uploads {
		keys = append(keys, aws.ToString(u.Key))
	}
	return keys, nil
}

// GetRange 读对象从 off 起的 n 个字节（Range 请求）；对象比 off+n 短时读到的少。调用方负责关闭。
func (c *Client) GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if off < 0 || n <= 0 {
		return nil, fmt.Errorf("s3 读取 %s：范围不对（%d, %d）", key, off, n)
	}
	out, err := c.api.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key), Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+n-1)),
	})
	if err != nil {
		return nil, wrap("读取", key, err)
	}
	return out.Body, nil
}
