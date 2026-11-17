// Package protocol 定义后端与 agent 之间的全部消息，以及双方共用的结果结构。
package protocol

import "encoding/json"

// Version 是 agent/server 的版本号，构建时可用 -ldflags 覆盖。
var Version = "0.2.0-dev"

// ProtocolVersion 是消息协议版本。后端只给协议版本相同的 agent 派发任务。
const ProtocolVersion = 2

const (
	// agent → server
	TypeHello = "hello"
	TypeEvent = "event"
	TypeProbe = "probe"
	// server → agent
	TypeStart  = "start"
	TypeStage  = "stage"
	TypeFinish = "finish"
	TypeCancel = "cancel"
	TypeSync   = "sync"
)

// Envelope 只用于先读出 type，再按类型解析。
type Envelope struct {
	Type string `json:"type"`
}

type HarnessInfo struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	LoggedIn  bool   `json:"logged_in"`
	Detail    string `json:"detail,omitempty"`
}

// Ready 表示 harness 可以直接使用。
func (h HarnessInfo) Ready() bool { return h.Installed && h.LoggedIn }

// BrowserInfo 是无头浏览器（UI 走查）能力的探测结果。
type BrowserInfo struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type HostInfo struct {
	AgentVersion string        `json:"agent_version"`
	Protocol     int           `json:"protocol"`
	Hostname     string        `json:"hostname"`
	OS           string        `json:"os"`
	Arch         string        `json:"arch"`
	Git          string        `json:"git,omitempty"`
	Docker       string        `json:"docker,omitempty"`
	Harnesses    []HarnessInfo `json:"harnesses"`
	Browser      BrowserInfo   `json:"browser"`
}

// Harness 返回指定 harness 的探测结果。
func (h *HostInfo) Harness(name string) (HarnessInfo, bool) {
	if h == nil {
		return HarnessInfo{}, false
	}
	for _, x := range h.Harnesses {
		if x.Name == name {
			return x, true
		}
	}
	return HarnessInfo{}, false
}

type Hello struct {
	Type string   `json:"type"`
	Host HostInfo `json:"host"`
	// KnownRuns 是 agent 本地事件日志里有记录的测试。
	KnownRuns []string `json:"known_runs"`
	// Active 是 agent 当前持有工作区的测试（空表示空闲）。
	Active string `json:"active,omitempty"`
}

type Probe struct {
	Type string   `json:"type"`
	Host HostInfo `json:"host,omitempty"`
}

// 事件种类。
const (
	KindAccepted     = "accepted"      // 已接收 start，开始建工作区
	KindReady        = "ready"         // 工作区就绪，Commit 给出 SHA
	KindStageStarted = "stage_started" // 开始执行 StageID
	KindLog          = "log"           // 一行日志，StageID 可为空（工作区准备、收尾）
	KindStageResult  = "stage_result"  // 阶段结束，Stage 非空
	KindClosed       = "closed"        // 收尾完成（终态），Closed 非空
)

type Event struct {
	Type    string        `json:"type"`
	RunID   string        `json:"run_id"`
	Seq     int64         `json:"seq"`
	Kind    string        `json:"kind"`
	TS      int64         `json:"ts"` // unix 毫秒
	StageID string        `json:"stage_id,omitempty"`
	Commit  string        `json:"commit,omitempty"`
	Text    string        `json:"text,omitempty"`
	Stage   *StageResult  `json:"stage,omitempty"`
	Closed  *ClosedResult `json:"closed,omitempty"`
}

// 阶段结果状态。
const (
	StageOK       = "ok"
	StageError    = "error"
	StageTimeout  = "timeout"
	StageCanceled = "canceled"
)

type StageResult struct {
	Status     string          `json:"status"`
	Error      string          `json:"error,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"` // harness 的结构化输出
	CostUSD    float64         `json:"cost_usd,omitempty"`
	DurationMS int64           `json:"duration_ms"`
	Artifacts  []string        `json:"artifacts,omitempty"` // 已上传的产物文件名
	Service    *ServiceStatus  `json:"service,omitempty"`   // service 阶段的结果
}

// ServiceStatus 描述 agent 托管的被测服务。
type ServiceStatus struct {
	Running   bool   `json:"running"`
	URL       string `json:"url,omitempty"`        // 健康检查或前端地址
	LogFile   string `json:"log_file,omitempty"`   // 运行时上的服务日志路径
	StartCmd  string `json:"start_cmd,omitempty"`  // 实际使用的启动命令
	LogTail   string `json:"log_tail,omitempty"`   // 失败时的最后几行输出
	HealthURL string `json:"health_url,omitempty"` // 实际检查的地址
}

// ClosedResult 是收尾结果。Error 非空表示测试被中断（agent 重启、工作区准备失败等）。
type ClosedResult struct {
	Canceled       bool   `json:"canceled,omitempty"`
	Error          string `json:"error,omitempty"`
	SourceModified bool   `json:"source_modified"`
	Diff           string `json:"diff,omitempty"`
}

// ---- server → agent ----

type Start struct {
	Type    string `json:"type"`
	RunID   string `json:"run_id"`
	RepoURL string `json:"repo_url"`
	Branch  string `json:"branch"`
}

// 阶段执行方式。
const (
	StageKindHarness = "harness"
	StageKindService = "service"
)

type Stage struct {
	Type           string `json:"type"`
	RunID          string `json:"run_id"`
	StageID        string `json:"stage_id"`
	Kind           string `json:"kind"`
	Role           string `json:"role"`
	TimeoutMinutes int    `json:"timeout_minutes"`

	// harness 阶段
	Harness string          `json:"harness,omitempty"`
	Prompt  string          `json:"prompt,omitempty"`
	Schema  json.RawMessage `json:"schema,omitempty"`
	Browser bool            `json:"browser,omitempty"` // 挂载无头浏览器工具

	// service 阶段
	Service *ServiceSpec `json:"service,omitempty"`
}

type ServiceSpec struct {
	SetupCmd  string `json:"setup_cmd"`
	StartCmd  string `json:"start_cmd"`
	HealthURL string `json:"health_url"`
	AppURL    string `json:"app_url"`
}

type Finish struct {
	Type  string `json:"type"`
	RunID string `json:"run_id"`
}

type Cancel struct {
	Type  string `json:"type"`
	RunID string `json:"run_id"`
}

type SyncRun struct {
	RunID   string `json:"run_id"`
	LastSeq int64  `json:"last_seq"`
}

type Sync struct {
	Type string    `json:"type"`
	Runs []SyncRun `json:"runs"`
}

// ---- 测试报告 ----

const (
	ACPass         = "pass"
	ACFail         = "fail"
	ACUnverifiable = "unverifiable"
	ACDisputed     = "disputed" // 红蓝双方最终意见不一致
)

// Challenge 是红队对某条验收结论的一次质疑，以及它后来怎么解决的。
type Challenge struct {
	Round          int    `json:"round"`
	Kind           string `json:"kind"`
	Claim          string `json:"claim"`
	SuggestedCheck string `json:"suggested_check,omitempty"`
	ProposedStatus string `json:"proposed_status,omitempty"`
	Response       string `json:"response,omitempty"`   // 蓝队复测时的回应
	Resolution     string `json:"resolution,omitempty"` // 复测后的状态
}

type ACResult struct {
	ID         string      `json:"id"`
	Criterion  string      `json:"criterion"`
	Method     string      `json:"method,omitempty"` // cli / api / ui / code
	Status     string      `json:"status"`
	Evidence   string      `json:"evidence"`
	Reasoning  string      `json:"reasoning"`
	Confidence string      `json:"confidence,omitempty"` // high / medium / low
	Challenges []Challenge `json:"challenges,omitempty"`
}

// Finding 是验收标准以外的额外发现（不改变验收结论）。
type Finding struct {
	Severity string `json:"severity"` // critical / high / medium / low
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Evidence string `json:"evidence"`
	Source   string `json:"source,omitempty"` // 由哪个阶段发现
}

type Report struct {
	Summary string     `json:"summary"`
	ACs     []ACResult `json:"acs"`
}

const (
	VerdictPass    = "pass"
	VerdictFail    = "fail"
	VerdictPartial = "partial"
)

// Verdict 由逐条结论汇总得出，不由模型直接给出。
func (r Report) Verdict() string {
	if len(r.ACs) == 0 {
		return VerdictPartial
	}
	partial := false
	for _, a := range r.ACs {
		switch a.Status {
		case ACPass:
		case ACFail:
			return VerdictFail
		default:
			partial = true
		}
	}
	if partial {
		return VerdictPartial
	}
	return VerdictPass
}
