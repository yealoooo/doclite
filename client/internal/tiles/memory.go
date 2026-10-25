package tiles

import (
	"context"
	"sync"
)

// MemoryBudget 是整张解码大图的内存预算（1.5 GiB）：封面（internal/upload）和切块（internal/imagecache）共用，
// 同时在解码的几张图的估算（[DecodeEstimate]、[Plan.Estimate]）加起来不超过它，单张超过它的不解。
const MemoryBudget = 3 << 29

var memory = struct {
	mu   sync.Mutex
	used int64
	// freed 在有预算还回来时关闭、换一个新的（等预算的在它上面等，能同时看 ctx）。
	freed chan struct{}
}{freed: make(chan struct{})}

// AcquireMemory 等到预算里有 n 字节并占上（n 不能超过 MemoryBudget）。ctx 先取消时返回它的错误，什么也不占。
// 用完调 [ReleaseMemory]。
func AcquireMemory(ctx context.Context, n int64) error {
	for {
		memory.mu.Lock()
		if memory.used+n <= MemoryBudget {
			memory.used += n
			memory.mu.Unlock()
			return nil
		}
		freed := memory.freed
		memory.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ReleaseMemory 还回 [AcquireMemory] 占的 n 字节。
func ReleaseMemory(n int64) {
	memory.mu.Lock()
	memory.used -= n
	close(memory.freed)
	memory.freed = make(chan struct{})
	memory.mu.Unlock()
}
