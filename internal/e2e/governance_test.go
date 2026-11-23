package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// waitDoneLong 等待测试结束，给会真实超时的场景留够时间。
func (e *env) waitDoneLong(id string, d time.Duration) detail {
	e.t.Helper()
	var r detail
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		r = e.get(id)
		if store.Terminal(r.Status) {
			return r
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("等待测试结束超时: %s", id)
	return r
}

// TestProgressFileRescuesTimeout：场景 A（超时交卷）。
// 执行验证睡过时限，但已把进度写进 stage-result.json：测试不判通过、报告降级、
// 进度结果被采用，并且到点超时不会再有白费的重跑。
func TestProgressFileRescuesTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("需要等待一个 1 分钟的阶段时限")
	}
	e := setup(t)
	r := e.waitDoneLong(e.newRun("custom", "需求 PROGRESS", map[string]any{
		"config": map[string]any{"service": true, "judge": true, "max_minutes": 3},
	}).ID, 4*time.Minute)

	if r.Status != store.StatusCompleted {
		t.Fatalf("进度交卷后测试应正常收尾: %s %s", r.Status, r.Error)
	}
	var verify []store.Stage
	for _, s := range r.Stages {
		if s.Role == testplan.RoleVerify {
			verify = append(verify, s)
		}
	}
	if len(verify) != 1 {
		t.Fatalf("到点超时不应白跑第二次: %v", flow(r))
	}
	v := verify[0]
	if v.Status != testplan.StatusTimeout || len(v.Output) == 0 {
		t.Fatalf("执行验证应为超时且带进度结果: %+v", v)
	}
	if v.FailureKind != testplan.FailureDeadline {
		t.Fatalf("阶段到点超时应单独分类: %q", v.FailureKind)
	}
	if !r.Coverage.Degraded || !strings.Contains(r.Coverage.Reason, "执行验证未完成") {
		t.Fatalf("超时交卷后报告必须降级: %+v", r.Coverage)
	}
	if r.Verdict == "pass" {
		t.Fatalf("未完成的环节不能给出通过结论: %s", r.Verdict)
	}
	// 进度文件里的结论被采用（裁决覆盖之后仍保留“来自进度文件”的说明）。
	adopted := false
	for _, a := range r.ACs {
		if a.ID == "AC-1" && strings.Contains(a.Reasoning, "结果来自进度文件") {
			adopted = true
		}
	}
	if !adopted {
		t.Fatalf("进度文件里的结论应被采用: %+v", r.ACs)
	}
	if !strings.Contains(joinLogs(e.logs(r.ID)), "采用进度文件") {
		t.Fatal("日志里应说明采用了进度文件")
	}
}

// TestServiceFailureDegrades：场景 B（服务失败降级）。
// 安装命令失败时报告里能看到最后输出（已脱敏），测试继续执行，覆盖强度明确降级。
func TestServiceFailureDegrades(t *testing.T) {
	e := setup(t)
	// 测试环境里已经登记了这个仓库，直接改成会失败的安装命令。
	var rp store.Repo
	if code := e.call("PUT", fmt.Sprintf("/api/repos/%d", e.repoID), map[string]string{
		"url":       e.repo,
		"setup_cmd": "echo 'db password=s3cret' >&2; exit 1",
		"start_cmd": "sleep 30",
	}, &rp); code != 200 {
		t.Fatalf("更新仓库失败: %d", code)
	}
	r := e.waitDone(e.newRun("B", "需求", nil).ID)
	if r.Status != store.StatusCompleted {
		t.Fatalf("服务失败不应让整单失败: %s %s", r.Status, r.Error)
	}
	var svc []store.Stage
	for _, s := range r.Stages {
		if s.Role == testplan.RoleService {
			svc = append(svc, s)
		}
	}
	if len(svc) != 1 || svc[0].Status != testplan.StatusError {
		t.Fatalf("确定性脚本失败不应重试: %v", flow(r))
	}
	msg := svc[0].Error
	for _, want := range []string{"安装依赖失败", "最后的输出", "password=***"} {
		if !strings.Contains(msg, want) {
			t.Errorf("服务失败的错误应包含 %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "s3cret") {
		t.Errorf("错误里泄露了密码明文: %s", msg)
	}
	if !r.Coverage.Degraded || !strings.Contains(r.Coverage.Reason, "平台未能启动被测服务") {
		t.Fatalf("服务没起来时报告必须写明环境来源: %+v", r.Coverage)
	}
	// 后续环节仍然执行，报告里能看到执行验证的结论。
	if !strings.Contains(flow(r), "verify") {
		t.Fatalf("服务失败后应继续执行验证: %v", flow(r))
	}
}
