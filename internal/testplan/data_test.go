package testplan

import (
	"strings"
	"testing"

	"verdict/internal/protocol"
)

func TestDataName(t *testing.T) {
	for in, want := range map[string]string{
		"7bd308b1aa69": "verdict_7bd308b1aa69",
		"Run-1.x":      "verdict_run_1_x",
	} {
		if got := DataName(in); got != want {
			t.Errorf("DataName(%q)=%q want %q", in, got, want)
		}
	}
}

// 计划阶段要求独立库、写造数步骤，并给出 teardown_cmd；不能把数据不足写成无法验证的条件。
func TestPlanPromptRequiresIsolatedData(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	p := Next(cfg, in, nil).Stage.Prompt
	for _, want := range []string{"verdict_r1", "teardown_cmd", "SET verdict:owner r1 NX", "造数方法", "不要把「数据不足」写成判无法验证的条件"} {
		if !strings.Contains(p, want) {
			t.Errorf("计划提示词缺少 %q", want)
		}
	}
	if !strings.Contains(string(PlanSchema), `"teardown_cmd"`) {
		t.Error("计划 schema 缺少 teardown_cmd")
	}
	// C 档不起服务，不需要隔离规则。
	if strings.Contains(Next(withHarness(Preset(TierC)), in, nil).Stage.Prompt, "teardown_cmd") {
		t.Error("C 档计划不应包含数据隔离规则")
	}
}

// 实测、复测阶段：数据不足要自己造，不算环境缺失；红队要追问因数据不足判的无法验证。
func TestVerifyPromptsPrepareData(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	recs := []StageRecord{{Role: RolePlan, Status: StatusOK, Output: planOut}}
	var b strings.Builder
	writeVerifyTask(&b, cfg, in, ParsePlan(recs), recs)
	if !strings.Contains(b.String(), "数据不足**不属于**环境缺失") {
		t.Error("实测提示词缺少造数规则")
	}
	b.Reset()
	writeRecheckTask(&b, cfg, 1, Result{})
	if !strings.Contains(b.String(), "数据不足**不属于**环境缺失") {
		t.Error("复测提示词缺少造数规则")
	}
	b.Reset()
	writeRedTask(&b, nil, Result{})
	if !strings.Contains(b.String(), "能不能在独立库里造数据后实测") {
		t.Error("红队提示词缺少造数追问")
	}
}

func TestServiceSpecTeardown(t *testing.T) {
	p := &Plan{Service: ServiceHint{StartCmd: "run", TeardownCmd: "dropdb verdict_r1"}}
	if s, _ := serviceSpec(RepoInfo{}, p); s.TeardownCmd != "dropdb verdict_r1" {
		t.Fatalf("计划里的 teardown_cmd 没有带到服务配置: %+v", s)
	}
	// 仓库配置了启动命令时整套以仓库为准，计划的清理命令不能单独生效。
	if s, _ := serviceSpec(RepoInfo{StartCmd: "repo start"}, p); s.TeardownCmd != "" {
		t.Fatalf("仓库配置优先时不应使用计划的清理命令: %+v", s)
	}
	ctx := serviceContext(Preset(TierS), in, []StageRecord{{Role: RoleService, Status: StatusOK,
		Service: &protocol.ServiceStatus{Running: true, StartCmd: "run", TeardownCmd: "dropdb verdict_r1"}}})
	if !strings.Contains(ctx, "dropdb verdict_r1") {
		t.Fatalf("运行环境说明里应列出收尾清理命令: %s", ctx)
	}
}
