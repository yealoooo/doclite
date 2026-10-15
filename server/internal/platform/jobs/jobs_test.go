package jobs

import (
	"errors"
	"testing"
	"time"
)

func TestRetryDelayAndMessages(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 30 * time.Second, 2: 2 * time.Minute, 3: 8 * time.Minute, 5: time.Hour} {
		if got := RetryDelay(30*time.Second, attempt); got != want {
			t.Errorf("RetryDelay(30s, %d) = %s，期望 %s", attempt, got, want)
		}
	}
	for d, want := range map[time.Duration]string{10 * time.Millisecond: "1 秒", 30 * time.Second: "30 秒", 2 * time.Minute: "2 分钟",
		150 * time.Second: "2 分 30 秒", time.Hour: "1 小时", 65 * time.Minute: "1 小时 5 分钟"} {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%s) = %q，期望 %q", d, got, want)
		}
	}
	if got := FailureMessage(1, "读原图失败", 30*time.Second); got != "第 1 次执行失败，30 秒后自动重试：读原图失败" {
		t.Error(got)
	}
	if got := FailureMessage(3, "读原图失败", 0); got != "已经执行了 3 次，都没有成功。最后一次：读原图失败" {
		t.Error(got)
	}
	if got := FailureMessage(1, "原图损坏", 0); got != "原图损坏" {
		t.Error(got)
	}
	if !Retryable(Retry(errors.New("x"))) || Retryable(errors.New("x")) || Retryable(nil) {
		t.Error("Retryable")
	}
}
