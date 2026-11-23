package testplan

import (
	"encoding/json"
	"fmt"

	"verdict/internal/linear"
	"verdict/internal/protocol"
)

// 阶段角色。
const (
	RolePlan    = "plan"
	RoleService = "service"
	RoleVerify  = "verify"
	RoleUI      = "ui"
	RoleShots   = "shots"
	RoleRed     = "red"
	RoleRecheck = "recheck"
	RoleExplore = "explore"
	RoleJudge   = "judge"
)

// 阶段记录状态（ok/error/timeout/canceled 来自 agent，skipped 由平台写入）。
const (
	StatusPending  = "pending"
	StatusSent     = "sent"
	StatusRunning  = "running"
	StatusOK       = protocol.StageOK
	StatusError    = protocol.StageError
	StatusTimeout  = protocol.StageTimeout
	StatusCanceled = protocol.StageCanceled
	StatusSkipped  = "skipped"
	// StatusRetryWait 表示阶段失败但可重试，正在退避等待。
	StatusRetryWait = "retry_wait"
)

// Done 表示阶段已有结果。
func Done(status string) bool {
	switch status {
	case StatusOK, StatusError, StatusTimeout, StatusCanceled, StatusSkipped:
		return true
	}
	return false
}

// Partial 表示阶段没有正常结束，但留下了可用的结构化结果（来自模型的进度交卷文件）。
func Partial(r StageRecord) bool {
	return (r.Status == StatusTimeout || r.Status == StatusError) && len(r.Output) > 0
}

// StageRecord 是一个已经创建的阶段及其结果。
type StageRecord struct {
	ID         string                  `json:"id"`
	Seq        int                     `json:"seq"`
	Role       string                  `json:"role"`
	Round      int                     `json:"round"`
	Kind       string                  `json:"kind"`
	Harness    string                  `json:"harness"`
	Status     string                  `json:"status"`
	Note       string                  `json:"note"`
	Error      string                  `json:"error"`
	Output     json.RawMessage         `json:"output,omitempty"`
	CostUSD    float64                 `json:"cost_usd"`
	DurationMS int64                   `json:"duration_ms"`
	Service    *protocol.ServiceStatus `json:"service,omitempty"`
	Artifacts  []string                `json:"artifacts"`
	// ArtifactIssues 是 agent 对产物的基础检查结果，只作提示。
	ArtifactIssues []protocol.ArtifactIssue `json:"artifact_issues,omitempty"`
}

type RepoInfo struct {
	TestNotes string
	SetupCmd  string
	StartCmd  string
	HealthURL string
	AppURL    string
}

type Input struct {
	RunID        string
	Issue        linear.Issue
	RepoURL      string
	Branch       string
	Repo         RepoInfo
	Approved     bool
	ApprovalNote string
	// 回归测试：锁定的 commit，以及计划复用的来源测试（平台会预先写入计划阶段）。
	Commit     string
	PlanSource string
	// Scope 是父单测试本次选中的子单编号；为空表示普通测试，或父单这次没带子单。
	Scope []string
}

// 下一步动作。
const (
	ActionStage  = "stage"  // 执行 Stage
	ActionSkip   = "skip"   // 记录一个被跳过的阶段，然后再次调用 Next
	ActionAwait  = "await"  // 等待人工确认计划
	ActionFinish = "finish" // 收尾；Fatal 非空表示测试失败
)

type StageSpec struct {
	Role           string                `json:"role"`
	Round          int                   `json:"round"`
	Kind           string                `json:"kind"`
	Harness        string                `json:"harness"`
	Prompt         string                `json:"prompt"`
	Schema         json.RawMessage       `json:"schema,omitempty"`
	Browser        bool                  `json:"browser"`
	TimeoutMinutes int                   `json:"timeout_minutes"`
	Service        *protocol.ServiceSpec `json:"service,omitempty"`
}

type Step struct {
	Action string
	Stage  StageSpec // ActionStage / ActionSkip
	Note   string    // 跳过原因、重试原因
	Fatal  string    // ActionFinish 时的失败原因
}

// defaultTimeout 是没有办法按工作量估算时的回退时限（分钟）。
var defaultTimeout = map[string]int{RolePlan: 15, RoleService: 20, RoleVerify: 60, RoleUI: 30, RoleShots: 12, RoleRed: 20, RoleRecheck: 25, RoleExplore: 30, RoleJudge: 10}

func latest(recs []StageRecord, role string, round int) *StageRecord {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Role == role && recs[i].Round == round {
			return &recs[i]
		}
	}
	return nil
}

func attempts(recs []StageRecord, role string, round int) int {
	n := 0
	for _, r := range recs {
		if r.Role == role && r.Round == round && r.Status != StatusSkipped {
			n++
		}
	}
	return n
}

// Usage 汇总已完成阶段的耗时与费用。
func Usage(recs []StageRecord) (elapsedMS int64, cost float64) {
	for _, r := range recs {
		elapsedMS += r.DurationMS
		cost += r.CostUSD
	}
	return
}

func remainingMinutes(cfg Config, recs []StageRecord) int {
	el, _ := Usage(recs)
	return cfg.MaxMinutes - int(el/60000)
}

// costLack 返回因费用超限应跳过可选阶段的原因，费用够时返回空。
func costLack(cfg Config, recs []StageRecord) string {
	_, cost := Usage(recs)
	if cfg.MaxCostUSD > 0 && cost >= cfg.MaxCostUSD {
		return fmt.Sprintf("已用费用 $%.2f 达到上限 $%.2f，因预算跳过", cost, cfg.MaxCostUSD)
	}
	return ""
}

// ParsePlan 取出计划阶段的结果（失败或缺失时返回 nil）。
func ParsePlan(recs []StageRecord) *Plan {
	r := latest(recs, RolePlan, 0)
	if r == nil || r.Status != StatusOK {
		return nil
	}
	var p Plan
	if json.Unmarshal(r.Output, &p) != nil {
		return nil
	}
	return &p
}

func hasUIItems(p *Plan) bool {
	if p == nil {
		return true // 没有计划时无法判断，保守地执行
	}
	for _, it := range p.Items {
		if it.Method == "ui" {
			return true
		}
	}
	return false
}

// serviceSpec 合并仓库配置与计划建议，仓库配置优先。
func serviceSpec(repo RepoInfo, p *Plan) (protocol.ServiceSpec, bool) {
	s := protocol.ServiceSpec{SetupCmd: repo.SetupCmd, StartCmd: repo.StartCmd, HealthURL: repo.HealthURL, AppURL: repo.AppURL}
	fromRepo := s.StartCmd != ""
	if p != nil && !fromRepo {
		s.SetupCmd, s.StartCmd, s.TeardownCmd = p.Service.SetupCmd, p.Service.StartCmd, p.Service.TeardownCmd
		if s.HealthURL == "" {
			s.HealthURL = p.Service.HealthURL
		}
		if s.AppURL == "" {
			s.AppURL = p.Service.AppURL
		}
	}
	return s, fromRepo
}

// redChallenges 取出某一轮红队的质疑；红队超时但交了进度文件时同样可用。
func redChallenges(r *StageRecord) []challengeOut {
	if r == nil || (r.Status != StatusOK && !Partial(*r)) || len(r.Output) == 0 {
		return nil
	}
	var out redOut
	_ = json.Unmarshal(r.Output, &out)
	return out.Challenges
}

// Next 根据配置和已完成的阶段，决定下一步。它是纯函数：相同输入总是得到相同的下一步。
func Next(cfg Config, in Input, recs []StageRecord) Step {
	cfg = cfg.Normalize()
	if cfg.RedHarness == "" {
		cfg.RedHarness = cfg.Harness
	}
	// stage 构造一个待派发的 harness 阶段：时限按 3.2.1 的工作量估算，再受剩余预算约束。
	// 预算被压缩时把原因写进 Note，报告里能看到不是按常规时长跑的。
	stage := func(role string, round int, harness string, browser bool) Step {
		t, reason := Gate(cfg, in, recs, role, round)
		return Step{Action: ActionStage, Stage: StageSpec{
			Role: role, Round: round, Kind: protocol.StageKindHarness, Harness: harness, Browser: browser,
			Prompt: buildPrompt(role, round, cfg, in, recs, t), Schema: schemaFor(role),
			TimeoutMinutes: t,
		}, Note: reason}
	}
	skip := func(role string, round int, reason string) Step {
		return Step{Action: ActionSkip, Stage: StageSpec{Role: role, Round: round, Kind: protocol.StageKindHarness}, Note: reason}
	}
	// optional 用于可跳过的环节：预算不足就明确记录“因预算不足未执行”，而不是派出去等超时。
	optional := func(role string, round int, harness string, browser bool) Step {
		if r := costLack(cfg, recs); r != "" {
			return skip(role, round, r)
		}
		if _, reason := Gate(cfg, in, recs, role, round); reason != "" {
			return skip(role, round, reason)
		}
		return stage(role, round, harness, browser)
	}

	// 1. 计划
	planRec := latest(recs, RolePlan, 0)
	if planRec == nil {
		return stage(RolePlan, 0, cfg.Harness, false)
	}
	plan := ParsePlan(recs)
	if cfg.ApprovePlan && plan != nil && !in.Approved {
		return Step{Action: ActionAwait}
	}

	// 2. 托管服务
	if cfg.Service && latest(recs, RoleService, 0) == nil {
		spec, fromRepo := serviceSpec(in.Repo, plan)
		switch {
		case spec.StartCmd == "":
			return Step{Action: ActionSkip, Stage: StageSpec{Role: RoleService, Kind: protocol.StageKindService},
				Note: "没有可用的启动命令：仓库未配置，计划阶段也没有给出"}
		case !fromRepo && plan != nil && !plan.NeedsService:
			return Step{Action: ActionSkip, Stage: StageSpec{Role: RoleService, Kind: protocol.StageKindService},
				Note: "计划判断本次验证不需要运行服务"}
		}
		t, reason := Gate(cfg, in, recs, RoleService, 0)
		return Step{Action: ActionStage, Stage: StageSpec{Role: RoleService, Kind: protocol.StageKindService,
			Service: &spec, TimeoutMinutes: t}, Note: reason}
	}

	// 3. 执行验证（核心阶段）：失败但有进度文件时继续，结论按未完成降级。
	v := latest(recs, RoleVerify, 0)
	if v == nil {
		return stage(RoleVerify, 0, cfg.Harness, false)
	}
	if v.Status != StatusOK && !Partial(*v) {
		if v.Status == StatusError && RetryableFailure(ClassifyFailure(v.Status, v.Error)) && attempts(recs, RoleVerify, 0) < 2 {
			st := stage(RoleVerify, 0, cfg.Harness, false)
			st.Note = appendNote(st.Note, "上一次执行验证失败，重试一次："+clip(v.Error, 200))
			return st
		}
		return Step{Action: ActionFinish, Fatal: "执行验证阶段失败：" + firstNonEmpty(v.Error, v.Status)}
	}

	// 4. UI 走查
	if cfg.UI != UINone && latest(recs, RoleUI, 0) == nil {
		if cfg.UI == UITargeted && !hasUIItems(plan) {
			return skip(RoleUI, 0, "计划中没有界面类条目")
		}
		return optional(RoleUI, 0, cfg.Harness, true)
	}

	// 5. 截图质检：UI 走查产出了截图时，用新会话只读图判断能不能当证据。
	if cfg.ShotReview && latest(recs, RoleShots, 0) == nil {
		uiRec := latest(recs, RoleUI, 0)
		shots := Consolidate(cfg, recs).Shots
		switch {
		case uiRec == nil || (uiRec.Status != StatusOK && !Partial(*uiRec)):
			// UI 走查没有成功，没有可质检的截图；不记一条跳过，避免噪音。
		case len(shots) == 0:
			return skip(RoleShots, 0, "UI 走查没有登记截图")
		default:
			return optional(RoleShots, 0, cfg.Harness, false)
		}
	}

	// 6. 红蓝循环
	redHarness := cfg.RedHarness
	for round := 1; round <= cfg.RedRounds; round++ {
		red := latest(recs, RoleRed, round)
		if red == nil {
			st := optional(RoleRed, round, redHarness, false)
			if redHarness != cfg.RedHarness && st.Action == ActionStage {
				st.Note = fmt.Sprintf("交叉模型 %s 在之前的轮次失败过，本轮直接使用 %s", cfg.RedHarness, cfg.Harness)
			}
			return st
		}
		// 失败后的换模型重试：交叉模型失败 → 主模型；主模型被拦截 → 另一个模型。
		if (red.Status == StatusError || red.Status == StatusTimeout) && attempts(recs, RoleRed, round) == 1 && !Partial(*red) {
			if red.Harness != cfg.Harness {
				st := stage(RoleRed, round, cfg.Harness, false)
				st.Note = appendNote(st.Note, fmt.Sprintf("交叉模型 %s 执行失败，改用 %s 新开会话重试：%s", red.Harness, cfg.Harness, clip(red.Error, 200)))
				return st
			}
			if ClassifyFailure(red.Status, red.Error) == FailureModel && cfg.RedHarness != cfg.Harness {
				st := stage(RoleRed, round, cfg.RedHarness, false)
				st.Note = appendNote(st.Note, fmt.Sprintf("主模型被拦截，改用 %s 重试一次：%s", cfg.RedHarness, clip(red.Error, 200)))
				return st
			}
		}
		if attempts(recs, RoleRed, round) > 1 {
			redHarness = cfg.Harness // 本轮交叉模型失败过，后续轮次不再尝试
		}
		if red.Status != StatusOK && !Partial(*red) {
			break // 失败、跳过，且没有进度文件
		}
		if len(redChallenges(red)) == 0 {
			break // 红队没有质疑（收敛）
		}
		rc := latest(recs, RoleRecheck, round)
		if rc == nil {
			return optional(RoleRecheck, round, cfg.Harness, false)
		}
		if rc.Status != StatusOK && !Partial(*rc) {
			break
		}
	}

	// 7. 主动探索
	if cfg.Explore && latest(recs, RoleExplore, 0) == nil {
		return optional(RoleExplore, 0, cfg.Harness, false)
	}

	// 8. 裁决：预算永远优先留给它；确实一分钟都不剩时明确记录，不静默跳过。
	if cfg.Judge && latest(recs, RoleJudge, 0) == nil {
		if r := costLack(cfg, recs); r != "" {
			return skip(RoleJudge, 0, r)
		}
		if _, reason := Gate(cfg, in, recs, RoleJudge, 0); reason != "" {
			return skip(RoleJudge, 0, reason)
		}
		return stage(RoleJudge, 0, cfg.Harness, false)
	}

	return Step{Action: ActionFinish}
}

func schemaFor(role string) json.RawMessage {
	switch role {
	case RolePlan:
		return PlanSchema
	case RoleUI:
		return UISchema
	case RoleRed:
		return RedSchema
	case RoleExplore:
		return ExploreSchema
	case RoleJudge:
		return JudgeSchema
	default:
		return ReportSchema
	}
}

// appendNote 追加阶段说明，保留 budget 门槛写下的压缩时限原因。
func appendNote(note, more string) string {
	if note == "" {
		return more
	}
	return note + "；" + more
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
