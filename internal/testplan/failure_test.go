package testplan

import (
	"encoding/json"
	"testing"

	"verdict/internal/protocol"
)

func TestClassifyFailure(t *testing.T) {
	for msg, want := range map[string]string{
		"stream disconnected before completion":         FailureTransient,
		"API Error: 400 The upstream provider rejected": FailureTransient,
		"429 Too Many Requests":                         FailureTransient,
		"dial tcp: connection reset by peer":            FailureTransient,
		"401 Unauthorized: invalid api key":             FailureAuth,
		"not logged in":                                 FailureAuth,
		"harness 输出不是合法 JSON":                           FailureOther,
		"exit status 1":                                 FailureOther,
	} {
		if got := ClassifyFailure(StatusError, msg); got != want {
			t.Errorf("ClassifyFailure(%q)=%s want %s", msg, got, want)
		}
	}
	if got := ClassifyFailure(StatusTimeout, "没写错误信息"); got != FailureTransient {
		t.Errorf("超时本身可重试: %s", got)
	}
	if !RetryableFailure(FailureTransient) || !RetryableFailure(FailureOther) || RetryableFailure(FailureAuth) {
		t.Error("可重试判定不对")
	}
}

func TestRetryDelay(t *testing.T) {
	for _, tc := range []struct{ base, attempt, want int }{
		{60, 2, 60}, {60, 3, 120}, {60, 4, 240}, {60, 6, 480}, {60, 10, 480}, {120, 2, 120}, {0, 2, 60},
	} {
		if got := RetryDelaySeconds(tc.base, tc.attempt); got != tc.want {
			t.Errorf("RetryDelaySeconds(%d,%d)=%d want %d", tc.base, tc.attempt, got, tc.want)
		}
	}
}

func okStage(role string, out any) StageRecord {
	b, _ := json.Marshal(out)
	return StageRecord{Role: role, Status: StatusOK, Output: b}
}

func passReport() map[string]any {
	return map[string]any{"summary": "全过", "acs": []map[string]string{
		{"id": "AC-1", "status": "pass", "evidence": "实测通过", "confidence": "high", "response": ""},
		{"id": "AC-2", "status": "pass", "evidence": "实测通过", "confidence": "high", "response": ""},
	}}
}

// TestCoverageUnjudgedDowngrades：缺少裁决时，所有「通过」降为存疑，结论不能是通过。
func TestCoverageUnjudgedDowngrades(t *testing.T) {
	cfg := Preset(TierS)
	recs := []StageRecord{
		okStage(RoleVerify, passReport()),
		okStage(RoleUI, map[string]any{"summary": "", "pages": []any{}, "findings": []any{}, "acs": []any{}}),
		{Role: RoleRed, Round: 1, Status: StatusError, Error: "stream disconnected"},
		okStage(RoleExplore, map[string]any{"summary": "", "findings": []any{}}),
		{Role: RoleJudge, Status: StatusError, Error: "API Error: 400 upstream provider rejected"},
	}
	res := Consolidate(cfg, recs)
	if !res.Coverage.Degraded || !res.Coverage.Unjudged {
		t.Fatalf("应判定为降级且未经裁决: %+v", res.Coverage)
	}
	if len(res.Coverage.Missing) != 2 { // red 全部失败 + judge 失败
		t.Fatalf("缺失阶段应为红队与裁决: %+v", res.Coverage.Missing)
	}
	for _, a := range res.ACs {
		if a.Status != protocol.ACDisputed {
			t.Fatalf("缺少裁决时通过应降为存疑: %+v", a)
		}
	}
	if v := res.Verdict(); v != protocol.VerdictPartial {
		t.Fatalf("结论应为部分无法验证，实际 %s", v)
	}
}

// TestCoverageCompleteUnaffected：阶段齐全时不降级。
func TestCoverageCompleteUnaffected(t *testing.T) {
	cfg := Preset(TierA)
	recs := []StageRecord{
		okStage(RoleVerify, passReport()),
		okStage(RoleUI, map[string]any{"summary": "", "pages": []any{}, "findings": []any{}, "acs": []any{}}),
		okStage(RoleRed, map[string]any{"summary": "", "challenges": []any{}, "findings": []any{}}),
		okStage(RoleJudge, map[string]any{"summary": "裁决通过", "findings": []any{},
			"acs": []map[string]string{{"id": "AC-1", "status": "pass", "confidence": "high", "rationale": "r"},
				{"id": "AC-2", "status": "pass", "confidence": "high", "rationale": "r"}}}),
	}
	res := Consolidate(cfg, recs)
	if res.Coverage.Degraded || len(res.Coverage.Missing) != 0 {
		t.Fatalf("齐全时不应降级: %+v", res.Coverage)
	}
	if v := res.Verdict(); v != protocol.VerdictPass {
		t.Fatalf("应通过，实际 %s", v)
	}
	// A 档不承诺探索，缺探索不算降级。
	if len(res.Coverage.Missing) != 0 {
		t.Fatal("A 档不承诺探索")
	}
}

// TestCoverageRedOnlyMissingKeepsVerdict：只缺红队时结论不变，但标注降级。
func TestCoverageRedOnlyMissingKeepsVerdict(t *testing.T) {
	cfg := Preset(TierA)
	recs := []StageRecord{
		okStage(RoleVerify, passReport()),
		okStage(RoleUI, map[string]any{"summary": "", "pages": []any{}, "findings": []any{}, "acs": []any{}}),
		{Role: RoleRed, Round: 1, Status: StatusError, Error: "exit status 1"},
		okStage(RoleJudge, map[string]any{"summary": "", "findings": []any{},
			"acs": []map[string]string{{"id": "AC-1", "status": "pass", "confidence": "high", "rationale": "r"},
				{"id": "AC-2", "status": "pass", "confidence": "high", "rationale": "r"}}}),
	}
	res := Consolidate(cfg, recs)
	if !res.Coverage.Degraded || res.Coverage.Unjudged {
		t.Fatalf("应降级但不含未裁决: %+v", res.Coverage)
	}
	if v := res.Verdict(); v != protocol.VerdictPass {
		t.Fatalf("有裁决时结论不受红队缺失影响，实际 %s", v)
	}
}
