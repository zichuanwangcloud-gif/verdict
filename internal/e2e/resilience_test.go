package e2e

import (
	"strings"
	"testing"

	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// setRetries 把重试设置改成测试用的值（间隔 1 秒，避免测试太慢）。
func (e *env) setRetries(n, delaySeconds int) {
	e.t.Helper()
	if code := e.call("PUT", "/api/settings", map[string]any{"stage_retries": n, "stage_retry_delay": delaySeconds}, nil); code != 200 {
		e.t.Fatalf("设置重试参数失败: %d", code)
	}
}

func roles(r detail) []string {
	var out []string
	for _, s := range r.Stages {
		out = append(out, s.Role+"#"+string(rune('0'+s.Attempt)))
	}
	return out
}

// TestStageRetryRecovers：执行验证阶段前两次因网关抖动失败，重试后成功，结论正常。
func TestStageRetryRecovers(t *testing.T) {
	e := setup(t)
	e.setRetries(5, 1) // 首次重试 1 秒，之后 2、4 秒
	r := e.waitDone(e.newRun("C", "FLAKY_2", nil).ID)
	if r.Status != store.StatusCompleted {
		t.Fatalf("重试后应完成: %s %s", r.Status, r.Error)
	}
	var verify []store.Stage
	for _, s := range r.Stages {
		if s.Role == testplan.RoleVerify {
			verify = append(verify, s)
		}
	}
	if len(verify) != 3 {
		t.Fatalf("执行验证应尝试 3 次（2 次失败 + 1 次成功）: %v", roles(r))
	}
	for _, s := range verify[:2] {
		if s.Status != testplan.StatusError || s.FailureKind != testplan.FailureTransient {
			t.Fatalf("前两次应为可重试的网关失败: %+v", s)
		}
	}
	if verify[2].Status != testplan.StatusOK || verify[2].Attempt != 3 {
		t.Fatalf("第 3 次应成功: %+v", verify[2])
	}
	if r.Coverage.Degraded {
		t.Fatalf("重试成功后不应降级: %+v", r.Coverage)
	}
}

// TestCoverageDegradedWhenRetriesExhausted：重试次数用尽后降级，缺裁决则结论不能是通过。
func TestCoverageDegradedWhenRetriesExhausted(t *testing.T) {
	e := setup(t)
	e.setRetries(1, 1) // 不重试
	r := e.waitDone(e.newRun("A", "FLAKY_9", nil).ID)
	// 主 harness 全阶段失败，verify 阶段直接判出错。
	if r.Status != store.StatusError {
		t.Fatalf("执行验证失败应判出错: %s", r.Status)
	}
	if !strings.Contains(r.Error, "执行验证阶段失败") {
		t.Fatalf("错误信息应说明是执行验证失败: %s", r.Error)
	}
}

// TestResumeFromFailedRun：出错的测试可以从失败处继续，已完成的阶段不重跑。
func TestResumeFromFailedRun(t *testing.T) {
	e := setup(t)
	// 只让执行验证失败（重试上限 1 = 不重试），计划阶段正常成功。
	e.setRetries(1, 1)
	r := e.waitDone(e.newRun("C", "FLAKY:verify:9", nil).ID)
	if r.Status != store.StatusError {
		t.Fatalf("应因执行验证失败而出错: %s", r.Status)
	}
	if r.ParentRunID != "" {
		t.Fatal("原测试不应有来源")
	}
	// 续跑：原需求里带着失败标记，所以续跑这一步的执行验证仍然会失败；
	// 这里验证的是「已完成的阶段被带过去、不重新规划」这条机制。
	e.setRetries(1, 1)
	var resumed detail
	code := e.call("POST", "/api/runs/"+r.ID+"/resume", nil, &resumed)
	if code != 200 {
		var msg map[string]string
		e.call("POST", "/api/runs/"+r.ID+"/resume", nil, &msg)
		t.Fatalf("续跑失败: %d %v", code, msg)
	}
	if code := e.call("GET", "/api/runs/"+resumed.ID, nil, &resumed); code != 200 {
		t.Fatalf("读取续跑测试失败: %d", code)
	}
	if len(resumed.Stages) == 0 || resumed.Stages[0].Role != testplan.RolePlan {
		t.Fatalf("续跑应带上已完成的计划阶段: %+v", resumed.Stages)
	}
	if resumed.ParentRunID != r.ID {
		t.Fatalf("续跑应指向原测试: %+v", resumed.ParentRunID)
	}
	got := e.waitDone(resumed.ID)
	plans := 0
	for _, s := range got.Stages {
		if s.Role == testplan.RolePlan {
			plans++
		}
	}
	if plans != 1 {
		t.Fatalf("计划阶段应只有一条（复制过来的），实际 %d: %v", plans, roles(got))
	}
	// 原测试的结果保留不变。
	if again := e.get(r.ID); again.Status != store.StatusError {
		t.Fatalf("原测试不应被修改: %s", again.Status)
	}
}

// TestResumeRejectsNonFailed：进行中或被取消的测试不能续跑。
func TestResumeRejectsNonFailed(t *testing.T) {
	e := setup(t)
	r := e.newRun("C", "SLOW", nil)
	var msg map[string]string
	if code := e.call("POST", "/api/runs/"+r.ID+"/resume", nil, &msg); code != 409 && code != 400 {
		t.Fatalf("进行中的测试应拒绝续跑: %d %v", code, msg)
	}
	e.waitDone(r.ID)
}
