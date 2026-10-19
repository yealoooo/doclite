package apitest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/app"
	"doclite/server/internal/platform/events"
)

// sseConn 是一条打开的 SSE 连接：后台逐行读，事件放进 events，注释（保活）计数，结束时关闭 done。
type sseConn struct {
	events   chan map[string]any
	comments chan struct{}
	done     chan struct{}
	cancel   context.CancelFunc
}

// openStreamStatus 发起 GET /api/projects/{pid}/stream，只看状态码和错误码（成功时马上断开）。
func openStreamStatus(t *testing.T, c *Client, pid uuid.UUID) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := streamRequest(t, ctx, c, pid)
	defer res.Body.Close()
	if res.StatusCode == 200 {
		if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("Content-Type：%s", ct)
		}
		return 200, ""
	}
	var body struct{ Code string }
	data, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(data, &body)
	return res.StatusCode, body.Code
}

func streamRequest(t *testing.T, ctx context.Context, c *Client, pid uuid.UUID) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/api/projects/"+pid.String()+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// openStream 打开实时通道，要求 200。测试结束时断开。
func openStream(t *testing.T, c *Client, pid uuid.UUID) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	res := streamRequest(t, ctx, c, pid)
	if res.StatusCode != 200 {
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		cancel()
		t.Fatalf("打开实时通道：%d %s", res.StatusCode, data)
	}
	s := &sseConn{events: make(chan map[string]any, 100), comments: make(chan struct{}, 100), done: make(chan struct{}), cancel: cancel}
	t.Cleanup(cancel)
	go func() {
		defer close(s.done)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if len(data) > 0 {
					var ev map[string]any
					if json.Unmarshal([]byte(strings.Join(data, "\n")), &ev) == nil {
						s.events <- ev
					}
					data = nil
				}
			case strings.HasPrefix(line, ":"):
				select {
				case s.comments <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	return s
}

// next 等下一条事件（最多 3 秒）。
func (s *sseConn) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case ev := <-s.events:
		return ev
	case <-s.done:
		t.Fatal("实时通道断开了，没等到事件")
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没等到事件")
	}
	return nil
}

// closedWithin 断言连接在 d 内被 server 断开。
func (s *sseConn) closedWithin(t *testing.T, d time.Duration, why string) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(d):
		t.Fatalf("%s：连接没有断开", why)
	}
}

func fastStream(o *app.Options) {
	o.Stream = events.StreamOptions{KeepAlive: 50 * time.Millisecond, RecheckEvery: 150 * time.Millisecond}
}

// TestProjectStream：保活、提交后发事件（只带 id，只推本项目）、复核会话和授权（失效即断）。
func TestProjectStream(t *testing.T) {
	t.Parallel()
	env := NewEnv(t, fastStream)
	admin := env.Admin()
	d := env.CreateProject("E1")
	d2 := env.CreateProject("E2")
	scannerID := env.PersonaID("scanner")
	env.GrantProject(d.ID, scannerID)
	scanner := env.As("scanner")

	s := openStream(t, scanner, d.ID)
	// 保活注释。
	select {
	case <-s.comments:
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到保活注释")
	}

	path := "/api/system/projects/" + d.ID.String()
	// 改设置、改状态、版本冲突（回滚）、别的项目改名：都不发（本项目）。
	admin.Patch(path, map[string]any{"settings": map[string]any{"x": 1}, "status": "archived"}).Expect(200)
	admin.Patch(path, map[string]any{"project_name": "不会生效", "expected_updated_at": "2000-01-01T00:00:00Z"}).ExpectError(409, "conflict")
	admin.Patch("/api/system/projects/"+d2.ID.String(), map[string]any{"project_name": "别的项目"}).Expect(200)
	// 改名：提交后发 folders.changed（根文件夹）。
	admin.Patch(path, map[string]any{"project_name": "新名字"}).Expect(200)
	ev := s.next(t)
	if ev["type"] != "folders.changed" || ev["project_id"] != d.ID.String() ||
		len(ev["folder_ids"].([]any)) != 1 || ev["folder_ids"].([]any)[0] != d.RootFolderID.String() || ev["file_ids"] != nil {
		t.Fatalf("事件：%v", ev)
	}

	// 总线发的事件原样到达（只带 id；catalog.changed 的 item_ids 空也输出）。
	env.App.Events.Publish(events.CatalogChanged(d.ID, []uuid.UUID{d.CategoryRootID}, nil), events.CatalogChanged(d2.ID, nil, nil))
	ev = s.next(t)
	if ev["type"] != "catalog.changed" || ev["item_ids"] == nil || len(ev["item_ids"].([]any)) != 0 {
		t.Fatalf("catalog.changed：%v", ev)
	}

	// 复核：撤销授权后在复核间隔内断开，重连 403。
	env.GrantProject(d.ID)
	s.closedWithin(t, 2*time.Second, "撤销授权")
	if st, code := openStreamStatus(t, scanner, d.ID); st != 403 || code != "forbidden" {
		t.Fatalf("撤销后重连：%d %s", st, code)
	}

	// 退出登录：会话失效后断开。
	other := env.Login(AdminUsername, AdminPassword)
	s2 := openStream(t, other, d.ID)
	other.Post("/api/auth/logout", nil).Expect(204)
	s2.closedWithin(t, 2*time.Second, "退出登录")

	// 删除项目：断开。
	s3 := openStream(t, admin, d2.ID)
	admin.Delete("/api/system/projects/" + d2.ID.String()).Expect(204)
	s3.closedWithin(t, 2*time.Second, "删除项目")

	// server 退出（总线关闭）：所有连接断开。
	s4 := openStream(t, admin, d.ID)
	env.App.Events.Close()
	s4.closedWithin(t, 2*time.Second, "总线关闭")
}
