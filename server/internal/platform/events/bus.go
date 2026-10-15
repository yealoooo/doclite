package events

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/db"
)

// SubscriptionBuffer 是每个订阅最多积压的事件数；再多就丢掉积压的、换成一条 resync。
const SubscriptionBuffer = 256

// Bus 是进程内的事件总线：按项目分组的订阅者。零值不能用，用 [NewBus]。
type Bus struct {
	mu     sync.Mutex
	subs   map[uuid.UUID]map[*Subscription]struct{}
	done   chan struct{}
	closed bool
}

// NewBus 建事件总线。
func NewBus() *Bus {
	return &Bus{subs: map[uuid.UUID]map[*Subscription]struct{}{}, done: make(chan struct{})}
}

// Subscription 是一个订阅（一条 SSE 连接）：从 C 读本项目的事件，用完调 Close。
type Subscription struct {
	bus       *Bus
	projectID uuid.UUID
	ch        chan Event
	// C 是事件；积压溢出时里面是一条 resync，后面接着新的事件。
	C <-chan Event
	// resyncs 是积压溢出的次数（读写都在 bus.mu 下）。
	resyncs int
	closed  bool
}

// Subscribe 订阅一个项目的事件。总线已关闭时返回的订阅不会收到事件（调用方应同时看 [Bus.Done]）。
func (b *Bus) Subscribe(projectID uuid.UUID) *Subscription {
	ch := make(chan Event, SubscriptionBuffer)
	s := &Subscription{bus: b, projectID: projectID, ch: ch, C: ch}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		s.closed = true
		return s
	}
	set := b.subs[projectID]
	if set == nil {
		set = map[*Subscription]struct{}{}
		b.subs[projectID] = set
	}
	set[s] = struct{}{}
	return s
}

// Close 取消订阅。可以重复调用。
func (s *Subscription) Close() {
	b := s.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if set := b.subs[s.projectID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(b.subs, s.projectID)
		}
	}
}

// Resyncs 是这个订阅因为积压溢出收到 resync 的次数（测试、日志用）。
func (s *Subscription) Resyncs() int {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.resyncs
}

// Publish 把事件发给对应项目的订阅者，不阻塞。事务里的写操作要在提交之后才调用（见 [Bus.InTx]）。
func (b *Bus) Publish(evs ...Event) {
	if len(evs) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, ev := range evs {
		for s := range b.subs[ev.ProjectID] {
			s.deliver(ev)
		}
	}
}

// deliver 在 bus.mu 下调用（发送方只有持锁的 Publish，所以清空之后一定放得下 resync）。
func (s *Subscription) deliver(ev Event) {
	select {
	case s.ch <- ev:
		return
	default:
	}
	// 积压满了：丢掉积压的事件和这一条，换成一条 resync（客户端整体刷新，丢的事件不用补）。
	for drained := false; !drained; {
		select {
		case <-s.ch:
		default:
			drained = true
		}
	}
	s.resyncs++
	s.ch <- Resync(s.projectID)
}

// Done 在总线关闭（server 退出）时关闭，SSE 连接据此结束，免得优雅退出一直等它们。
func (b *Bus) Done() <-chan struct{} { return b.done }

// Close 关闭总线：之后的 Publish 什么都不做，Done 关闭。可以重复调用。
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	close(b.done)
	b.subs = map[uuid.UUID]map[*Subscription]struct{}{}
}

// Subscribers 是某个项目当前的订阅数（测试用）。
func (b *Bus) Subscribers(projectID uuid.UUID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[projectID])
}

// Pending 收集一个事务里要发的事件，提交之后由 [Bus.InTx] 发出。
type Pending struct {
	evs []Event
}

// Add 登记要发的事件（提交后才发）。
func (p *Pending) Add(evs ...Event) { p.evs = append(p.evs, evs...) }

// InTx 是带事件的 db.InTx：fn 里用 out.Add 登记事件，事务提交成功后一起发出；fn 返回错误、panic 或提交失败时不发。
func (b *Bus) InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx, out *Pending) error) error {
	out := &Pending{}
	if err := db.InTx(ctx, pool, func(tx pgx.Tx) error { return fn(tx, out) }); err != nil {
		return err
	}
	b.Publish(out.evs...)
	return nil
}
