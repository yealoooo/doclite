package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"doclite/server/internal/platform/httpx"
)

// 密码哈希：argon2id，库里存 PHC 字符串 `$argon2id$v=19$m=19456,t=2,p=1$<盐>$<哈希>`（盐和哈希是不带填充的
// 标准 base64）。参数随哈希一起保存，校验时按哈希里的参数算，以后调参数不影响已有的密码。
//
// 一次计算要十几到几十毫秒 CPU 和 19 MiB 内存，所以全进程同时进行的计算数不超过 CPU 核数（2–16）；
// 排队超过 5 秒返回 503 `busy`（requirements 5.1）。

// PasswordParams 是 argon2id 的参数。
type PasswordParams struct {
	// Memory 是内存（KiB）。
	Memory  uint32
	Time    uint32
	Threads uint8
	KeyLen  uint32
	SaltLen int
}

// DefaultPasswordParams 是生产用的参数（OWASP 推荐的最低配置，和上一版一样）：m = 19 MiB、t = 2、p = 1。
// 测试里可以换成更小的参数加快速度（见 apitest）。
var DefaultPasswordParams = PasswordParams{Memory: 19 * 1024, Time: 2, Threads: 1, KeyLen: 32, SaltLen: 16}

const (
	hashQueueTimeout = 5 * time.Second
	// maxHashMemory 是校验时接受的最大内存参数（1 GiB），库里的哈希被改坏时不至于把内存吃光。
	maxHashMemory = 1 << 20
)

var b64 = base64.RawStdEncoding

// hasher 计算和校验密码哈希，限制同时进行的计算数。
type hasher struct {
	params PasswordParams
	slots  chan struct{}
}

func newHasher(p PasswordParams) *hasher {
	n := min(max(runtime.NumCPU(), 2), 16)
	return &hasher{params: p, slots: make(chan struct{}, n)}
}

// run 排队拿到名额后执行 fn；排队超过 5 秒返回 503 `busy`。
func (h *hasher) run(ctx context.Context, fn func()) error {
	timer := time.NewTimer(hashQueueTimeout)
	defer timer.Stop()
	select {
	case h.slots <- struct{}{}:
	case <-timer.C:
		return httpx.Busy()
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.slots }()
	fn()
	return nil
}

// Hash 计算密码的 PHC 字符串。
func (h *hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	var key []byte
	p := h.params
	if err := h.run(ctx, func() { key = argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify 校验密码。密码不对返回 false；哈希本身格式不对（库里的数据坏了）返回错误。
func (h *hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	ph, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	var key []byte
	if err := h.run(ctx, func() {
		key = argon2.IDKey([]byte(password), ph.salt, ph.time, ph.memory, ph.threads, uint32(len(ph.hash)))
	}); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, ph.hash) == 1, nil
}

type phc struct {
	memory, time uint32
	threads      uint8
	salt, hash   []byte
}

var errBadHash = errors.New("库里的密码哈希格式不对")

func parsePHC(s string) (phc, error) {
	var p phc
	parts := strings.Split(s, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, errBadHash
	}
	var m, t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &threads); err != nil {
		return p, errBadHash
	}
	if m == 0 || m > maxHashMemory || t == 0 || threads == 0 {
		return p, errBadHash
	}
	salt, err1 := b64.DecodeString(parts[4])
	hash, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) == 0 || len(hash) == 0 {
		return p, errBadHash
	}
	return phc{memory: m, time: t, threads: threads, salt: salt, hash: hash}, nil
}
