// Package domain 定义平台的全部业务对象。
//
// 纪律：本包及 ledger/store/api 不允许出现任何被测项目的技术名词
// （语言、框架、数据库名）。平台只认 Area 这一个抽象；
// 「代码怎么映射到 Area」「验证怎么执行」分别属于 agent 与 verifier。
package domain

import (
	"encoding/json"
	"time"
)

// Area 是变更与验收条件之间唯一的连接点。
type Area struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Disposition 是需求在发版时的处置。
type Disposition string

const (
	DispositionActive  Disposition = "active"  // 随版本发布，必须验证通过
	DispositionClosed  Disposition = "closed"  // 代码在但开关关闭
	DispositionRemoved Disposition = "removed" // 已从版本摘除
)

type Requirement struct {
	ID          int64       `json:"id"`
	Key         string      `json:"key"` // 外部需求系统里的编号
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Disposition Disposition `json:"disposition"`
	CreatedAt   time.Time   `json:"created_at"`
	ACs         []AC        `json:"acs"`
}

// AC 是一条验收条件。Verifier 为空表示「无法自动验证」。
type AC struct {
	ID            int64           `json:"id"`
	RequirementID int64           `json:"requirement_id"`
	Seq           int             `json:"seq"`
	Text          string          `json:"text"`
	Areas         []string        `json:"areas"`
	Verifier      string          `json:"verifier"`
	Params        json.RawMessage `json:"params"`
}

// ChangeSet 是一个提测单位（例如一个候选版本）。Seq 决定先后。
type ChangeSet struct {
	ID           int64           `json:"id"`
	Seq          int64           `json:"seq"`
	Ref          string          `json:"ref"`    // 版本名
	Digest       string          `json:"digest"` // 构建产物摘要：证据绑定的是它
	TouchedAreas []string        `json:"touched_areas"`
	Analysis     json.RawMessage `json:"analysis"` // 智能体的原始分析，留痕
	CreatedAt    time.Time       `json:"created_at"`
}

// Evidence 是一次验证器执行的结果，绑定到 (AC, ChangeSet)。
type Evidence struct {
	ID          int64     `json:"id"`
	ACID        int64     `json:"ac_id"`
	ChangeSetID int64     `json:"changeset_id"`
	Verifier    string    `json:"verifier"`
	Passed      bool      `json:"passed"`
	Output      string    `json:"output"`
	CreatedAt   time.Time `json:"created_at"`
}

// ACStatus 是某条 AC 在某个变更集上的状态。
type ACStatus string

const (
	StatusVerified     ACStatus = "verified"     // 有仍然有效的通过证据
	StatusFailed       ACStatus = "failed"       // 最近的有效证据是失败
	StatusStale        ACStatus = "stale"        // 有过证据，但被后续变更推翻
	StatusUntested     ACStatus = "untested"     // 从未执行
	StatusUnverifiable ACStatus = "unverifiable" // 没有绑定验证器
)

type ACVerdict struct {
	AC       AC        `json:"ac"`
	Status   ACStatus  `json:"status"`
	Evidence *Evidence `json:"evidence,omitempty"`
	// CarriedFrom 非空表示结论沿用自更早的变更集。
	CarriedFrom string `json:"carried_from,omitempty"`
	// InvalidatedBy 非空表示被哪个变更集推翻。
	InvalidatedBy string `json:"invalidated_by,omitempty"`
}

type RequirementVerdict struct {
	Requirement Requirement `json:"requirement"`
	ACs         []ACVerdict `json:"acs"`
	Blocking    bool        `json:"blocking"`
}

// Verdict 是平台对一个变更集的裁决。Releasable 由证据算出，不由模型给出。
type Verdict struct {
	ChangeSet    ChangeSet            `json:"changeset"`
	Releasable   bool                 `json:"releasable"`
	Requirements []RequirementVerdict `json:"requirements"`
	Counts       map[ACStatus]int     `json:"counts"`
}
