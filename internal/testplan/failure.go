package testplan

import "strings"

// 阶段失败分类。分类由平台按错误文本判定，不依赖模型的自我描述。
const (
	FailureTransient    = "transient"     // 网关抖动、限流、连接中断：可重试
	FailureAuth         = "auth"          // 凭据问题：重试也没用
	FailureDeadline     = "deadline"      // 到点超时（阶段超过时限）：最多续跑一次
	FailureModel        = "model"         // 模型服务拦截：换模型重试一次
	FailureService      = "service"       // 启动服务的可恢复失败：重试一次
	FailureServiceFatal = "service_fatal" // 启动服务的确定性失败（脚本 / 配置错误）：不重试
	FailureOther        = "other"         // 其它失败：重试一次
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

// modelMarkers 是模型服务拦截的特征：同一段内容再问一次多半还是被拦，优先换模型。
var modelMarkers = []string{"flagged", "cybersecurity", "content policy", "safety system", "安全策略"}

// serviceFatalMarkers 是启动服务的确定性失败：重跑同一条命令结果不会变。
var serviceFatalMarkers = []string{"安装依赖失败", "启动服务失败", "缺少服务配置", "地址不合法", "端口占位符"}

// serviceRetryMarkers 是启动服务的可恢复失败：健康检查超时、进程短暂退出、端口暂时占用。
var serviceRetryMarkers = []string{"健康检查", "服务进程", "端口"}

// ClassifyFailure 返回失败分类。
func ClassifyFailure(stageStatus, errText string) string {
	msg := strings.ToLower(errText)
	if strings.Contains(msg, "阶段超过时限") || (stageStatus == StatusTimeout && strings.Contains(msg, "超过时限")) {
		return FailureDeadline
	}
	// 服务阶段的错误可能带脚本输出尾部，里面出现的 401 / token 不能当作凭据问题，
	// 必须先按服务特征归类。
	for _, m := range serviceFatalMarkers {
		if strings.Contains(errText, m) {
			return FailureServiceFatal
		}
	}
	for _, m := range serviceRetryMarkers {
		if strings.Contains(errText, m) {
			return FailureService
		}
	}
	for _, m := range authMarkers {
		if strings.Contains(msg, m) {
			return FailureAuth
		}
	}
	for _, m := range modelMarkers {
		if strings.Contains(msg, m) {
			return FailureModel
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
func RetryableFailure(kind string) bool {
	switch kind {
	case FailureTransient, FailureOther, FailureDeadline, FailureModel, FailureService:
		return true
	}
	return false
}

// MaxRetriesFor 返回这类失败最多重试几次；-1 表示沿用设置里的重试上限。
func MaxRetriesFor(kind string) int {
	switch kind {
	case FailureDeadline, FailureModel, FailureService:
		return 1 // 到点超时 / 模型拦截 / 服务可恢复失败：只给一次续跑机会
	case FailureAuth, FailureServiceFatal:
		return 0
	}
	return -1
}

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
