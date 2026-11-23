package testplan

import (
	"strings"
	"testing"

	"verdict/internal/protocol"
)

// uiWith 构造一个 UI 阶段输出。
func uiWith(evidence string, shots ...Shot) []StageRecord {
	return []StageRecord{{Role: RoleUI, Status: StatusOK, Output: j(map[string]any{
		"summary": "ui", "pages": []any{}, "shots": shots,
		"acs":      []map[string]string{{"id": "AC-2", "status": "pass", "evidence": evidence, "confidence": "high"}},
		"findings": []any{},
	})}}
}

func shotsReview(file, verdict string) StageRecord {
	return StageRecord{Role: RoleShots, Status: StatusOK, Output: j(map[string]any{
		"summary": "质检",
		"shot_reviews": []map[string]any{{
			"file": file, "verdict": verdict, "occluded": verdict == "unusable", "readable": verdict == "usable",
			"problems": []string{"画面被弹窗挡住"}, "suggest": "关掉弹窗后重截",
		}},
	})}
}

func TestShotGateDropsPassOnUnusableShot(t *testing.T) {
	cfg := withHarness(Preset(TierB))
	recs := uiWith("操作步骤见 ac-2-dialog.png", Shot{File: "ac-2-dialog.png", State: "弹窗打开"})
	recs = append(recs, shotsReview("ac-2-dialog.png", "unusable"))
	res := Consolidate(cfg, recs)
	if res.ACs[0].Status != protocol.ACUnverifiable {
		t.Fatalf("截图不可用时通过应降级: %+v", res.ACs[0])
	}
	if !strings.Contains(res.ACs[0].Reasoning, "截图不可用") {
		t.Fatalf("应写明降级原因: %s", res.ACs[0].Reasoning)
	}
}

func TestShotGateKeepsPassWithUsableShot(t *testing.T) {
	cfg := withHarness(Preset(TierB))
	recs := uiWith("见 ac-2-dialog.png 和 ac-2-after.png",
		Shot{File: "ac-2-dialog.png", State: "弹窗打开"},
		Shot{File: "ac-2-after.png", State: "关闭后", Readable: true})
	recs = append(recs, shotsReview("ac-2-dialog.png", "unusable"))
	if got := Consolidate(cfg, recs).ACs[0].Status; got != protocol.ACPass {
		t.Fatalf("还有一张可用截图时不应降级: %s", got)
	}
}

func TestShotGateIgnoresUncitedShots(t *testing.T) {
	cfg := withHarness(Preset(TierB))
	// evidence 没有引用任何截图（纯接口验证），即使别的截图不可用也不受影响。
	recs := uiWith("curl 返回 200", Shot{File: "other.png", State: "别的页面"})
	recs = append(recs, shotsReview("other.png", "unusable"))
	if got := Consolidate(cfg, recs).ACs[0].Status; got != protocol.ACPass {
		t.Fatalf("未引用截图的结论不应降级: %s", got)
	}
}

func TestShotSelfCheckUnreadableCountsAsUnusable(t *testing.T) {
	cfg := withHarness(Preset(TierB))
	// 没有独立质检阶段时，UI 阶段自检说 readable=false 也算不可用。
	recs := uiWith("见 ac-2-blank.png", Shot{File: "ac-2-blank.png", State: "首页", Readable: false})
	if got := Consolidate(cfg, recs).ACs[0].Status; got != protocol.ACUnverifiable {
		t.Fatalf("自检判不可读时应降级: %s", got)
	}
}

func TestShotVerdictReviewsOverrideSelfCheck(t *testing.T) {
	// 自检说不可读，但独立质检说可用：以质检为准。
	recs := uiWith("见 a.png", Shot{File: "a.png", State: "首页", Readable: false})
	recs = append(recs, shotsReview("a.png", "usable"))
	if bad := shotVerdict(recs); len(bad) != 0 {
		t.Fatalf("独立质检应压过自检: %v", bad)
	}
}

func TestShotMachineIssueOnlyWhenUnjudged(t *testing.T) {
	recs := []StageRecord{{
		Role: RoleUI, Status: StatusOK,
		Output: j(map[string]any{"summary": "", "pages": []any{}, "shots": []Shot{
			{File: "a.png", State: "首页", Readable: true}, {File: "b.png", State: "首页", Readable: true},
		}, "acs": []any{}, "findings": []any{}}),
		ArtifactIssues: []protocol.ArtifactIssue{{File: "a.png", Reason: "画面几乎是纯色，疑似空白页"}},
	}}
	bad := shotVerdict(recs)
	if len(bad) != 1 || bad["a.png"] == "" {
		t.Fatalf("模型没说问题的疑似空白图应被判不可用: %v", bad)
	}
	if _, ok := bad["b.png"]; ok {
		t.Fatal("正常图不应被判不可用")
	}
}

func TestShotCitationNames(t *testing.T) {
	got := shotCitationNames("截图 `ac-3-form.png`，以及 ac-4-empty.png（对照 bash-5.png 不算）")
	want := map[string]bool{"ac-3-form.png": true, "ac-4-empty.png": true, "bash-5.png": true}
	if len(got) != len(want) {
		t.Fatalf("应提到 3 个文件: %v", got)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("不应提取 %q", f)
		}
	}
}

func TestShotsStageSkippedWithoutShots(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	// UI 阶段没登记截图 → 质检阶段跳过，并写明原因。
	ui := j(map[string]any{"summary": "", "pages": []any{}, "shots": []any{}, "acs": []any{}, "findings": []any{}})
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true, URL: "http://127.0.0.1:3000"}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
		{Role: RoleUI, Status: StatusOK, Output: ui},
	}
	st := Next(cfg, in, recs)
	if st.Action != ActionSkip || st.Stage.Role != RoleShots || !strings.Contains(st.Note, "没有登记截图") {
		t.Fatalf("没有截图时应跳过质检: %+v", st)
	}
}

func TestUITaskHasShotDiscipline(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true, URL: "http://127.0.0.1:3000"}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
	}
	st := Next(cfg, in, recs)
	if st.Stage.Role != RoleUI {
		t.Fatalf("应进入 UI 阶段: %s", st.Stage.Role)
	}
	for _, want := range []string{"截图纪律", "遮挡", "dialog-open", "shots", "回看", "净场检查", "browser_evaluate", "covered 指"} {
		if !strings.Contains(st.Stage.Prompt, want) {
			t.Errorf("UI 提示词缺少 %q", want)
		}
	}
}

func TestUITaskWithoutSelfCheckForbidsClaiming(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	cfg.ShotSelfCheck = false
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true, URL: "http://127.0.0.1:3000"}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
	}
	p := Next(cfg, in, recs).Stage.Prompt
	if strings.Contains(p, "截图后自检（必做）") {
		t.Fatal("关闭自检时不应要求回看")
	}
	if !strings.Contains(p, "不要") || !strings.Contains(p, "已回看") {
		t.Fatal("关闭自检时应明确禁止声称回看过")
	}
}

func TestPromptWarnsAboutSuspiciousShots(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	ui := j(map[string]any{"summary": "", "pages": []any{}, "shots": []Shot{{File: "blank.png", State: "首页", Readable: true}}, "acs": []any{}, "findings": []any{}})
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true, URL: "http://127.0.0.1:3000"}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
		{Role: RoleUI, Status: StatusOK, Output: ui, ArtifactIssues: []protocol.ArtifactIssue{{File: "blank.png", Reason: "画面几乎是纯色，疑似空白页"}}},
		{Role: RoleShots, Status: StatusOK, Output: j(map[string]any{"summary": "", "shot_reviews": []any{}})},
	}
	p := Next(cfg, in, recs).Stage.Prompt
	if !strings.Contains(p, "截图提示") || !strings.Contains(p, "blank.png") {
		t.Fatalf("后续阶段应收到疑似空白提示: %s", p)
	}
}

func TestShotsStageGetsShotReviewsSchema(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true, URL: "http://127.0.0.1:3000"}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
		{Role: RoleUI, Status: StatusOK, Output: uiWith("见 a.png", Shot{File: "a.png", State: "首页", Readable: true})[0].Output},
	}
	st := Next(cfg, in, recs)
	if st.Stage.Role != RoleShots {
		t.Fatalf("应进入截图质检阶段: %+v", st)
	}
	if !strings.Contains(string(st.Stage.Schema), "shot_reviews") {
		t.Fatalf("质检阶段必须下发 shot_reviews schema，否则结论无法被平台采纳: %s", st.Stage.Schema)
	}
}

func TestShotSelfCheckCoveredNeedsIssue(t *testing.T) {
	// covered 只在写明遮挡物（或同时不可读）时才算数：模型偶尔会把「这条已被覆盖」误填成 covered。
	recs := uiWith("见 a.png", Shot{File: "a.png", State: "首页", Covered: true, Readable: true})
	if bad := shotVerdict(recs); len(bad) != 0 {
		t.Fatalf("covered 但没有说明具体遮挡时不应判不可用: %v", bad)
	}
	recs = uiWith("见 b.png", Shot{File: "b.png", State: "首页", Covered: true, Readable: true,
		Issues: []string{"右上角 toast 挡住了标题"}})
	if bad := shotVerdict(recs); !strings.Contains(bad["b.png"], "遮挡") {
		t.Fatalf("写明遮挡物时应判不可用: %v", bad)
	}
}

func TestShotsPromptExplainsOcclusion(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true, URL: "http://127.0.0.1:3000"}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
		{Role: RoleUI, Status: StatusOK, Output: uiWith("见 a.png", Shot{File: "a.png", State: "首页", Readable: true})[0].Output},
	}
	p := Next(cfg, in, recs).Stage.Prompt
	for _, want := range []string{"遮挡判定", "每一张", "被验证对象"} {
		if !strings.Contains(p, want) {
			t.Errorf("截图质检提示词缺少 %q", want)
		}
	}
}
