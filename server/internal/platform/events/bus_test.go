package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestOverflowResync：积压满了丢掉积压的事件，换成一条 resync；之后的事件照常；别的项目不受影响。
func TestOverflowResync(t *testing.T) {
	b := NewBus()
	p, q := uuid.New(), uuid.New()
	slow := b.Subscribe(p)
	other := b.Subscribe(q)
	defer slow.Close()
	defer other.Close()

	for i := 0; i < SubscriptionBuffer; i++ {
		b.Publish(FoldersChanged(p, uuid.New()))
	}
	if len(slow.C) != SubscriptionBuffer || slow.resyncCount() != 0 {
		t.Fatalf("积压 %d，resync %d", len(slow.C), slow.resyncCount())
	}
	b.Publish(FoldersChanged(p, uuid.New()))
	if len(slow.C) != 1 || slow.resyncCount() != 1 {
		t.Fatalf("溢出后积压 %d，resync %d", len(slow.C), slow.resyncCount())
	}
	if ev := <-slow.C; ev.Type != TypeResync || ev.ProjectID != p {
		t.Fatalf("溢出后第一条：%+v", ev)
	}
	b.Publish(FoldersChanged(p, uuid.New()))
	if ev := <-slow.C; ev.Type != TypeFoldersChanged {
		t.Fatalf("之后的事件：%+v", ev)
	}
	if len(other.C) != 0 {
		t.Fatal("别的项目收到了事件")
	}

	slow.Close()
	if b.subscribers(p) != 0 {
		t.Fatal("Close 之后还在订阅")
	}
	b.Close()
	b.Publish(FoldersChanged(q, uuid.New()))
	select {
	case <-b.Done():
	default:
		t.Fatal("Close 之后 Done 没关")
	}
}

// TestEventJSON：只带 id；空列表不输出；catalog.changed 的 item_ids 空时输出 []、超过 500 个不带；
// files.changed 超过 500 个文件只带 folder_ids；id 去重。
func TestEventJSON(t *testing.T) {
	p, f := uuid.New(), uuid.New()
	js := func(ev Event) string {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := js(FoldersChanged(p, f, f)); got != `{"type":"folders.changed","project_id":"`+p.String()+`","folder_ids":["`+f.String()+`"]}` {
		t.Fatalf("folders.changed：%s", got)
	}
	if got := js(Resync(p)); got != `{"type":"resync","project_id":"`+p.String()+`"}` {
		t.Fatalf("resync：%s", got)
	}
	if got := js(CatalogChanged(p, []uuid.UUID{f}, nil)); !strings.Contains(got, `"item_ids":[]`) {
		t.Fatalf("catalog.changed 没有条目：%s", got)
	}
	many := make([]uuid.UUID, MaxIDs+1)
	for i := range many {
		many[i] = uuid.New()
	}
	if got := js(CatalogChanged(p, []uuid.UUID{f}, many)); strings.Contains(got, "item_ids") {
		t.Fatalf("catalog.changed 超过 500 个：%s", got)
	}
	if got := js(FilesChanged(p, []uuid.UUID{f}, many)); strings.Contains(got, "file_ids") || !strings.Contains(got, "folder_ids") {
		t.Fatalf("files.changed 超过 500 个：%s", got)
	}
	if got := js(FilesChanged(p, []uuid.UUID{f}, many[:MaxIDs])); strings.Count(got, ",") < MaxIDs {
		t.Fatalf("files.changed 500 个应全带：%d", len(got))
	}
}

// resyncCount 是这个订阅因为积压溢出收到 resync 的次数。
func (s *Subscription) resyncCount() int {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.resyncs
}

// subscribers 是某个项目当前的订阅数。
func (b *Bus) subscribers(projectID uuid.UUID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[projectID])
}
