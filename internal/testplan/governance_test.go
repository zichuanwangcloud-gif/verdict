package testplan

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"verdict/internal/protocol"
)

// planWith 构造一份带 n 条验收标准的计划输出；ui 指定其中界面类条目的数量。
func planWith(n, ui int) json.RawMessage {
	var items []PlanItem
	for i := 1; i <= n; i++ {
		method := "api"
		if i <= ui {
			method = "ui"
		}
		items = append(items, PlanItem{ID: fmt.Sprintf("AC-%d", i), Criterion: "c", Method: method})
	}
	return j(Plan{Summary: "s", NeedsService: true, Service: ServiceHint{StartCmd: "npm start"}, Items: items})
}

func TestEstimateMinutes(t *testing.T) {
	cfg := Preset(TierS)
	for _, tc := range []struct {
		name  string
		recs  []StageRecord
		role  string
		round int
		want  int
	}{
		{"计划·无条目按下限", nil, RolePlan, 0, 20},
		{"计划·40 条", []StageRecord{okStage(RolePlan, planWith(40, 0))}, RolePlan, 0, 32},
		{"计划·超上限", []StageRecord{okStage(RolePlan, planWith(200, 0))}, RolePlan, 0, 40},
		{"验证·无条目按下限", nil, RoleVerify, 0, 30},
		{"验证·21 条", []StageRecord{okStage(RolePlan, planWith(21, 0))}, RoleVerify, 0, 36},
		{"验证·超上限", []StageRecord{okStage(RolePlan, planWith(200, 0))}, RoleVerify, 0, 90},
		{"界面·7 条", []StageRecord{okStage(RolePlan, planWith(10, 7))}, RoleUI, 0, 24},
		{"界面·无条目按下限", []StageRecord{okStage(RolePlan, planWith(10, 0))}, RoleUI, 0, 15},
		{"复测·4 条质疑", []StageRecord{{Role: RoleRed, Round: 1, Status: StatusOK, Output: j(map[string]any{"challenges": []map[string]string{
			{"ac_id": "AC-1"}, {"ac_id": "AC-1"}, {"ac_id": "AC-1"}, {"ac_id": "AC-1"}}})}}, RoleRecheck, 1, 22},
		{"复测·无质疑按下限", nil, RoleRecheck, 1, 15},
		{"裁决固定", nil, RoleJudge, 0, 10},
		{"探索固定", nil, RoleExplore, 0, 25},
		{"启动服务固定", nil, RoleService, 0, 25},
	} {
		if got := EstimateMinutes(cfg, in, tc.recs, tc.role, tc.round); got != tc.want {
			t.Errorf("%s: EstimateMinutes(%s)=%d want %d", tc.name, tc.role, got, tc.want)
		}
	}
}

// TestGateSkipsStageWithoutBudget：CR-2104 的复盘场景——只剩 6 分钟，复测预计 22 分钟，裁决要保留 10 分钟。
func TestGateSkipsStageWithoutBudget(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	cfg.ShotReview = false
	red := okStage(RoleRed, map[string]any{"challenges": []map[string]string{
		{"ac_id": "AC-1", "claim": "c"}, {"ac_id": "AC-1", "claim": "c"},
		{"ac_id": "AC-1", "claim": "c"}, {"ac_id": "AC-1", "claim": "c"}}})
	red.Round = 1
	verify := okStage(RoleVerify, report("pass", "pass"))
	verify.DurationMS = 84 * 60000 // 已用 84 分钟，剩 6 分钟
	ui := okStage(RoleUI, map[string]any{"summary": "", "pages": []any{}, "shots": []any{}, "acs": []any{}, "findings": []any{}})
	recs := []StageRecord{okStage(RolePlan, planOut), {Role: RoleService, Status: StatusOK}, verify, ui, red}

	if timeout, reason := Gate(cfg, in, recs, RoleRecheck, 1); reason == "" || timeout != 1 {
		t.Fatalf("预算不足的复测不应派发: timeout=%d reason=%q", timeout, reason)
	} else if !strings.Contains(reason, "22 分钟") || !strings.Contains(reason, "预算不足") {
		t.Fatalf("跳过原因应写明预计耗时和预算不足: %q", reason)
	}
	st := Next(cfg, in, recs)
	if st.Action != ActionSkip || st.Stage.Role != RoleRecheck || !strings.Contains(st.Note, "预算不足") {
		t.Fatalf("流水线应跳过预算不足的复测: %+v", st)
	}
}

// TestJudgeBudgetPriority：预算再紧也先给裁决挤出时间；确实一分钟不剩时明确记录未执行。
func TestJudgeBudgetPriority(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	cfg.Explore = true
	spent := okStage(RoleVerify, report("pass"))
	spent.DurationMS = 89 * 60000 // 剩 1 分钟
	recs := []StageRecord{spent}

	if timeout, reason := Gate(cfg, in, recs, RoleExplore, 0); reason == "" || timeout != 1 {
		t.Fatalf("预算不足时探索应被跳过: timeout=%d reason=%q", timeout, reason)
	}
	if timeout, reason := Gate(cfg, in, recs, RoleJudge, 0); reason != "" || timeout != 1 {
		t.Fatalf("裁决应优先获得剩余预算: timeout=%d reason=%q", timeout, reason)
	}

	spent.DurationMS = 90 * 60000 // 一分不剩
	recs = []StageRecord{spent}
	if timeout, reason := Gate(cfg, in, recs, RoleJudge, 0); timeout != 0 || !strings.Contains(reason, "裁决未执行") {
		t.Fatalf("预算耗尽时应明确记录裁决未执行: timeout=%d reason=%q", timeout, reason)
	}
}

// TestDeadlineFailurePolicy：到点超时与网关抖动分开处理；服务失败按确定性 / 可恢复分类。
func TestDeadlineFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		status, msg, want string
	}{
		{StatusTimeout, "阶段超过时限 25m0s", FailureDeadline},
		{StatusTimeout, "超过时限仍未收到运行时的结果", FailureDeadline},
		{StatusError, "stream disconnected before completion", FailureTransient},
		{StatusError, "This content was flagged for possible cybersecurity risk", FailureModel},
		{StatusError, "安装依赖失败: exit status 1\n最后的输出：password=***", FailureServiceFatal},
		{StatusError, "健康检查 http://127.0.0.1:3000/health 在 3m0s 内没有通过（最后一次：connection refused）", FailureService},
		{StatusError, "服务进程在健康检查通过前退出", FailureService},
	} {
		if got := ClassifyFailure(tc.status, tc.msg); got != tc.want {
			t.Errorf("ClassifyFailure(%q)=%s want %s", tc.msg, got, tc.want)
		}
	}
	if MaxRetriesFor(FailureDeadline) != 1 || MaxRetriesFor(FailureModel) != 1 || MaxRetriesFor(FailureService) != 1 {
		t.Error("到点超时 / 模型拦截 / 服务可恢复失败都只应重试一次")
	}
	if MaxRetriesFor(FailureServiceFatal) != 0 || MaxRetriesFor(FailureAuth) != 0 {
		t.Error("确定性失败和凭据问题不应重试")
	}
	if MaxRetriesFor(FailureTransient) != -1 {
		t.Error("网关抖动应沿用设置里的重试上限")
	}
	if RetryableFailure(FailureServiceFatal) || !RetryableFailure(FailureDeadline) {
		t.Error("可重试判定不对")
	}
}

// TestProgressAdoption：超时但有进度文件时采用其中的条目，置信度降为低，覆盖强度仍为降级。
func TestProgressAdoption(t *testing.T) {
	// B 档没有裁决：进度文件里的「通过」不会被“未裁决”规则再降一档，便于单独验证采用逻辑。
	cfg := Preset(TierB)
	recs := []StageRecord{
		okStage(RolePlan, planOut),
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true}},
		{Role: RoleVerify, Status: StatusTimeout, Error: "阶段超过时限 30m0s", Output: report("pass", "pass")},
	}
	res := Consolidate(cfg, recs)
	if len(res.ACs) != 2 || res.ACs[0].Status != protocol.ACPass {
		t.Fatalf("进度文件里的结论应被采用: %+v", res.ACs)
	}
	for _, a := range res.ACs {
		if a.Confidence != "low" || !strings.Contains(a.Reasoning, "结果来自进度文件") {
			t.Fatalf("采用进度文件的结论应标为未完成、置信度低: %+v", a)
		}
	}
	if !res.Coverage.Degraded || res.Coverage.Unjudged {
		t.Fatalf("超时环节的覆盖强度必须仍是降级: %+v", res.Coverage)
	}
	if !containsString(res.Coverage.Missing, RoleVerify) || !strings.Contains(res.Coverage.Reason, "执行验证未完成") {
		t.Fatalf("降级原因应说明执行验证未完成: %+v", res.Coverage)
	}
	// 不采用进度文件时行为不变：没有输出就是没有结果。
	res = Consolidate(cfg, []StageRecord{{Role: RoleVerify, Status: StatusTimeout, Error: "阶段超过时限 30m0s"}})
	if len(res.ACs) != 0 {
		t.Fatalf("没有进度文件时不应凭空产生结论: %+v", res.ACs)
	}
}

// TestCoverageServiceAndPlanDegraded：服务和计划失败要出现在覆盖强度降级里；被跳过不算失败。
func TestCoverageServiceAndPlanDegraded(t *testing.T) {
	cfg := Preset(TierB)
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusError, Error: "退出码 1"},
		{Role: RoleService, Status: StatusError, Error: "安装依赖失败: exit status 1"},
		okStage(RoleVerify, report("pass")),
	}
	c := coverage(cfg, recs)
	if !c.Degraded || !containsString(c.Missing, RolePlan) || !containsString(c.Missing, RoleService) {
		t.Fatalf("计划和服务的失败都应记为降级: %+v", c)
	}
	if !strings.Contains(c.Reason, "测试计划缺失") || !strings.Contains(c.Reason, "平台未能启动被测服务") {
		t.Fatalf("降级原因应写明计划和服务的缺失: %q", c.Reason)
	}

	// 仓库没有启动命令、服务被跳过：不算降级。
	c = coverage(cfg, []StageRecord{
		okStage(RolePlan, planOut),
		{Role: RoleService, Status: StatusSkipped, Note: "没有可用的启动命令"},
		okStage(RoleVerify, report("pass")),
	})
	if c.Degraded {
		t.Fatalf("服务被跳过不应降级: %+v", c)
	}
}

// TestRedModelSwitch：红队用主模型被拦截时，换另一个模型重试一次。
func TestRedModelSwitch(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	cfg.ShotReview = false
	recs := []StageRecord{
		okStage(RolePlan, planOut),
		{Role: RoleService, Status: StatusOK},
		okStage(RoleVerify, report("pass", "pass")),
		okStage(RoleUI, map[string]any{"summary": "", "pages": []any{}, "shots": []any{}, "acs": []any{}, "findings": []any{}}),
		{Role: RoleRed, Round: 1, Harness: "claude", Status: StatusError,
			Error: "This content was flagged for possible cybersecurity risk"},
	}
	st := Next(cfg, in, recs)
	if st.Action != ActionStage || st.Stage.Role != RoleRed || st.Stage.Harness != "codex" {
		t.Fatalf("主模型被拦截应换另一个模型重试: %+v", st)
	}
	if !strings.Contains(st.Note, "拦截") {
		t.Fatalf("重试应说明是模型拦截: %q", st.Note)
	}
	// 没有另一个模型可用时不换，交给网关类重试策略。
	cfg.RedHarness = cfg.Harness
	st = Next(cfg, in, recs)
	if st.Action != ActionStage || st.Stage.Harness != cfg.Harness {
		t.Fatalf("没有另一个模型时不应换模型: %+v", st)
	}
}

// TestProgressPromptAndServiceRules：多条目环节的提示词带进度交卷约定；计划提示词带启动脚本硬约束。
func TestProgressPromptAndServiceRules(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	cfg.ShotReview = false
	recs := []StageRecord{okStage(RolePlan, planOut), {Role: RoleService, Status: StatusOK}}
	st := Next(cfg, in, recs)
	if st.Stage.Role != RoleVerify {
		t.Fatalf("应派发执行验证: %+v", st)
	}
	for _, want := range []string{"stage-result.json", fmt.Sprintf("限时 %d 分钟", st.Stage.TimeoutMinutes), "允许只包含已完成的条目"} {
		if !strings.Contains(st.Stage.Prompt, want) {
			t.Errorf("执行验证提示词缺少 %q", want)
		}
	}

	planPrompt := Next(cfg, in, nil).Stage.Prompt
	for _, want := range []string{"第一行不要写 set -o pipefail", "至少 32 个字符", "不要假设「没有密码也能连」", "echo setup-ok"} {
		if !strings.Contains(planPrompt, want) {
			t.Errorf("计划提示词缺少启动脚本硬约束 %q", want)
		}
	}
	// C 档不起服务，不需要这些约束。
	if p := Next(withHarness(Preset(TierC)), in, nil).Stage.Prompt; strings.Contains(p, "set -o pipefail") {
		t.Error("C 档计划不应包含启动脚本约束")
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
