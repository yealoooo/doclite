package export

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/google/uuid"

	"doclite/server/internal/export/bundle"
	"doclite/server/internal/platform/s3"
	"doclite/server/internal/storage"
)

// source 是从 S3 读原图的 bundle.PageSource：按页的快照所在的存储连（同一次执行里每个存储一个客户端）；
// Open 是整个 GetObject，ReadAt 是 Range GetObject（只有 Inspect 用）。
type source struct {
	storage *storage.Service
	mu      sync.Mutex
	clients map[uuid.UUID]*s3.Client
}

func newSource(st *storage.Service) *source {
	return &source{storage: st, clients: map[uuid.UUID]*s3.Client{}}
}

var _ bundle.PageSource = (*source)(nil)

func (s *source) client(ctx context.Context, profileID uuid.UUID) (*s3.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[profileID]; c != nil {
		return c, nil
	}
	c, err := s.storage.Client(ctx, profileID)
	if err != nil {
		return nil, err
	}
	s.clients[profileID] = c
	return c, nil
}

// Open 读整个原图。
func (s *source) Open(ctx context.Context, p bundle.Page) (io.ReadCloser, error) {
	c, err := s.client(ctx, p.StorageProfileID)
	if err != nil {
		return nil, err
	}
	return c.Get(ctx, p.StorageKey)
}

// ReadAt 读原图从 off 起的 len(b) 个字节；对象比这短时读到多少算多少，返回 io.EOF。
func (s *source) ReadAt(ctx context.Context, p bundle.Page, b []byte, off int64) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c, err := s.client(ctx, p.StorageProfileID)
	if err != nil {
		return 0, err
	}
	rc, err := c.GetRange(ctx, p.StorageKey, off, int64(len(b)))
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	n, err := io.ReadFull(rc, b)
	if errors.Is(err, io.ErrUnexpectedEOF) || (err == io.EOF && n == 0) {
		return n, io.EOF
	}
	return n, err
}
