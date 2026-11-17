// Package protocol 定义后端与 agent 之间的全部消息，以及双方共用的结果结构。
package protocol

import "encoding/json"

// Version 是 agent/server 的版本号，构建时可用 -ldflags 覆盖。
var Version = "0.1.0-dev"

// ---- agent → server ----

const (
	TypeHello = "hello"
	TypeEvent = "event"
	TypeProbe = "probe"
	// server → agent
	TypeStart  = "start"
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

type HostInfo struct {
	AgentVersion string        `json:"agent_version"`
	Hostname     string        `json:"hostname"`
	OS           string        `json:"os"`
	Arch         string        `json:"arch"`
	Git          string        `json:"git,omitempty"`
	Docker       string        `json:"docker,omitempty"`
	Harnesses    []HarnessInfo `json:"harnesses"`
}

type Hello struct {
	Type string   `json:"type"`
	Host HostInfo `json:"host"`
	// KnownRuns 是 agent 本地事件日志里有记录的测试。
	KnownRuns []string `json:"known_runs"`
	// Running 是 agent 当前正在执行的测试（空表示空闲）。
	Running string `json:"running,omitempty"`
}

type Probe struct {
	Type string   `json:"type"`
	Host HostInfo `json:"host,omitempty"`
}

// 事件种类。
const (
	KindAccepted = "accepted" // agent 已接收任务
	KindPhase    = "phase"    // 进入某个阶段，Phase 字段给出 preparing / running
	KindCommit   = "commit"   // 分支已解析成 commit，Commit 字段给出 SHA
	KindLog      = "log"
	KindResult   = "result" // 终态，Result 字段非空
)

type Event struct {
	Type   string     `json:"type"`
	RunID  string     `json:"run_id"`
	Seq    int64      `json:"seq"`
	Kind   string     `json:"kind"`
	TS     int64      `json:"ts"` // unix 毫秒
	Phase  string     `json:"phase,omitempty"`
	Commit string     `json:"commit,omitempty"`
	Text   string     `json:"text,omitempty"`
	Result *RunResult `json:"result,omitempty"`
}

// RunResult 是一次测试的最终结果。Status 取 completed / error / canceled。
type RunResult struct {
	Status         string          `json:"status"`
	Error          string          `json:"error,omitempty"`
	Report         *Report         `json:"report,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"` // harness 原始结构化输出（解析失败时留痕）
	SourceModified bool            `json:"source_modified"`
	Diff           string          `json:"diff,omitempty"`
	CostUSD        float64         `json:"cost_usd,omitempty"`
}

// ---- server → agent ----

type Start struct {
	Type           string          `json:"type"`
	RunID          string          `json:"run_id"`
	RepoURL        string          `json:"repo_url"`
	Branch         string          `json:"branch"`
	Harness        string          `json:"harness"`
	Prompt         string          `json:"prompt"`
	Schema         json.RawMessage `json:"schema"`
	TimeoutMinutes int             `json:"timeout_minutes"`
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
)

type ACResult struct {
	ID        string `json:"id"`
	Criterion string `json:"criterion"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence"`
	Reasoning string `json:"reasoning"`
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
