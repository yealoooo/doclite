package upload

import "time"

// MaxRetryDelay 是自动重试间隔的上限（requirements 第 6 节：1 秒起翻倍，封顶 5 分钟）。
const MaxRetryDelay = 5 * time.Minute

// RetryDelay 是第 attempts 次临时失败之后等多久再试：1 秒 × 2^(attempts−1)，封顶 5 分钟，不加随机。
func RetryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return time.Second
	}
	if attempts > 20 {
		return MaxRetryDelay
	}
	return min(time.Second<<(attempts-1), MaxRetryDelay)
}
