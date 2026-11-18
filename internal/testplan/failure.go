package testplan

import "strings"

// 阶段失败分类。分类由平台按错误文本判定，不依赖模型的自我描述。
const (
	FailureTransient = "transient" // 网关抖动、限流、连接中断：可重试
	FailureAuth      = "auth"      // 凭据问题：重试也没用
	FailureOther     = "other"     // 其它失败：重试一次
)

// transientMarkers 是可恢复失败的特征。
var transientMarkers = []string{
	"stream disconnected", "upstream provider rejected", "connection reset", "econnreset",
	"rate limit", "too many requests", "overloaded", "service unavailable", "bad gateway",
	"gateway timeout", "502", "503", "504", "429", "timeout", "deadline exceeded",
	"context deadline", "connection refused", "temporary failure", "eof",
}

// authMarkers 是凭据类失败的特征，重试没有意义。
var authMarkers = []string{
	"401", "403", "invalid api key", "unauthorized", "forbidden", "not logged in",
	"authentication", "no credentials", "api key", "令牌",
}

// ClassifyFailure 返回失败分类。
func ClassifyFailure(stageStatus, errText string) string {
	msg := strings.ToLower(errText)
	for _, m := range authMarkers {
		if strings.Contains(msg, m) {
			return FailureAuth
		}
	}
	if stageStatus == StatusTimeout {
		return FailureTransient
	}
	for _, m := range transientMarkers {
		if strings.Contains(msg, m) {
			return FailureTransient
		}
	}
	return FailureOther
}

// RetryableFailure 表示这类失败值得重试。
func RetryableFailure(kind string) bool { return kind == FailureTransient || kind == FailureOther }

// RetryDelaySeconds 返回第 attempt 次重试（attempt 从 2 开始）前需要等待的秒数：
// 首次用配置值，之后翻倍，上限 8 分钟。
func RetryDelaySeconds(base, attempt int) int {
	if base <= 0 {
		base = 60
	}
	d := base
	for i := 2; i < attempt; i++ {
		d *= 2
	}
	return min(d, 8*60)
}

// 重试相关的默认值（设置里没填时使用）。
const (
	DefaultStageRetries   = 5
	MaxStageRetries       = 20
	DefaultRetryDelaySecs = 60
)
