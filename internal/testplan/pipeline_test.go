package testplan

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"verdict/internal/linear"
	"verdict/internal/protocol"
)

var in = Input{RunID: "r1", Issue: linear.Issue{Identifier: "ENG-1", Title: "T", Description: "AC1 AC2"},
	RepoURL: "git@x:o/r.git", Branch: "feat", Repo: RepoInfo{TestNotes: "notes"}}

func j(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

var planOut = j(Plan{Summary: "s", NeedsService: true, Service: ServiceHint{StartCmd: "npm start", HealthURL: "http://127.0.0.1:3000/health", AppURL: "http://127.0.0.1:3000"},
	Items: []PlanItem{{ID: "AC-1", Criterion: "接口", Method: "api"}, {ID: "AC-2", Criterion: "页面", Method: "ui"}}})

func report(statuses ...string) json.RawMessage {
	var acs []map[string]string
	for i, s := range statuses {
		acs = append(acs, map[string]string{"id": fmt.Sprintf("AC-%d", i+1), "status": s, "evidence": "ev", "reasoning": "r", "confidence": "high", "response": "resp"})
	}
	return j(map[string]any{"summary": "verify", "acs": acs})
}

// sim 根据 Next 的指示不断「执行」阶段，outputs 按角色+轮次给出输出；返回角色序列与最终记录。
func sim(t *testing.T, cfg Config, input Input, outputs func(StageSpec) (string, json.RawMessage, int64)) ([]string, []StageRecord, Step) {
	t.Helper()
	var recs []StageRecord
	var roles []string
	for i := 0; i < 40; i++ {
		st := Next(cfg, input, recs)
		switch st.Action {
		case ActionFinish, ActionAwait:
			return roles, recs, st
		case ActionSkip:
			roles = append(roles, st.Stage.Role+"(skip)")
			recs = append(recs, StageRecord{Seq: i, Role: st.Stage.Role, Round: st.Stage.Round, Status: StatusSkipped, Note: st.Note})
		case ActionStage:
			if st.Stage.Kind == protocol.StageKindHarness && (st.Stage.Prompt == "" || len(st.Stage.Schema) == 0) {
				t.Fatalf("%s 阶段缺少提示词或 schema", st.Stage.Role)
			}
			status, out, ms := outputs(st.Stage)
			name := st.Stage.Role
			if st.Stage.Round > 0 {
				name = fmt.Sprintf("%s%d", name, st.Stage.Round)
			}
			if st.Stage.Harness != "" && st.Stage.Harness != cfg.Harness {
				name += "@" + st.Stage.Harness
			}
			roles = append(roles, name)
			rec := StageRecord{Seq: i, Role: st.Stage.Role, Round: st.Stage.Round, Harness: st.Stage.Harness, Status: status, Output: out, DurationMS: ms, CostUSD: 0.5, Note: st.Note}
			if st.Stage.Role == RoleService {
				rec.Service = &protocol.ServiceStatus{Running: status == StatusOK, URL: "http://127.0.0.1:3000", StartCmd: st.Stage.Service.StartCmd}
			}
			recs = append(recs, rec)
		}
	}
	t.Fatal("流水线没有结束")
	return nil, nil, Step{}
}

func happy(challengeRounds int) func(StageSpec) (string, json.RawMessage, int64) {
	return func(s StageSpec) (string, json.RawMessage, int64) {
		switch s.Role {
		case RolePlan:
			return StatusOK, planOut, 60000
		case RoleService:
			return StatusOK, nil, 10000
		case RoleVerify:
			return StatusOK, report("pass", "unverifiable"), 120000
		case RoleUI:
			return StatusOK, j(map[string]any{"summary": "ui", "pages": []UIPage{{URL: "/", Screenshots: []string{"home.png"}}},
				"shots":    []Shot{{File: "home.png", URL: "/", State: "首页", Purpose: "AC-2", Readable: true}},
				"acs":      []map[string]string{{"id": "AC-2", "status": "pass", "evidence": "截图 home.png", "confidence": "high"}},
				"findings": []map[string]string{{"severity": "low", "title": "按钮错位", "detail": "d", "evidence": "e"}}}), 60000
		case RoleShots:
			return StatusOK, j(map[string]any{"summary": "截图都可用",
				"shot_reviews": []map[string]any{{"file": "home.png", "verdict": "usable", "occluded": false, "readable": true, "problems": []string{}, "suggest": ""}}}), 30000
		case RoleRed:
			var ch []map[string]string
			if s.Round <= challengeRounds {
				ch = []map[string]string{{"ac_id": "AC-1", "kind": "insufficient_evidence", "claim": "只看了代码", "suggested_check": "curl", "proposed_status": "fail"}}
			}
			return StatusOK, j(map[string]any{"summary": "red", "challenges": ch, "findings": []any{}}), 60000
		case RoleRecheck:
			return StatusOK, j(map[string]any{"summary": "re", "acs": []map[string]string{{"id": "AC-1", "status": "fail", "evidence": "curl → 500", "reasoning": "r", "confidence": "high", "response": "认同"}}}), 60000
		case RoleExplore:
			return StatusOK, j(map[string]any{"summary": "ex", "findings": []map[string]string{{"severity": "high", "title": "SQL 注入", "detail": "d", "evidence": "e"}}}), 60000
		case RoleJudge:
			return StatusOK, j(map[string]any{"summary": "judge", "acs": []map[string]string{{"id": "AC-1", "status": "fail", "confidence": "high", "rationale": "复测 500"}, {"id": "AC-2", "status": "pass", "confidence": "medium", "rationale": "截图"}}}), 30000
		}
		return StatusError, nil, 0
	}
}

func withHarness(c Config) Config { c.Harness, c.RedHarness = "claude", "codex"; return c }

func TestTierPipelines(t *testing.T) {
	cases := []struct {
		tier   string
		rounds int
		want   string
	}{
		{TierC, 0, "plan verify"},
		{TierB, 0, "plan service verify"},
		{TierA, 1, "plan service verify ui red1@codex recheck1 judge"},
		{TierS, 1, "plan service verify ui red1@codex recheck1 red2@codex explore judge"},
		{TierS, 9, "plan service verify ui red1@codex recheck1 red2@codex recheck2 red3@codex recheck3 explore judge"},
	}
	for _, c := range cases {
		roles, _, st := sim(t, withHarness(Preset(c.tier)), in, happy(c.rounds))
		// A/S 档会多一个截图质检阶段：切掉它再比对，其余顺序保持一致。
		if got := strings.Join(withoutRole(roles, RoleShots), " "); got != c.want {
			t.Errorf("%s 档(质疑 %d 轮): %s\n  want %s", c.tier, c.rounds, got, c.want)
		}
		if c.tier == TierA || c.tier == TierS {
			if !containsRole(roles, RoleShots) {
				t.Errorf("%s 档应有截图质检阶段: %s", c.tier, strings.Join(roles, " "))
			}
		}
		if st.Action != ActionFinish || st.Fatal != "" {
			t.Errorf("%s 档应正常结束: %+v", c.tier, st)
		}
	}
}

// withoutRole 去掉指定的角色（含带轮次后缀的写法）。
func withoutRole(roles []string, role string) []string {
	var out []string
	for _, r := range roles {
		if r == role || strings.HasPrefix(r, role+"(") || strings.HasPrefix(r, role+"@") {
			continue
		}
		out = append(out, r)
	}
	return out
}

func containsRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role || strings.HasPrefix(r, role+"(") || strings.HasPrefix(r, role+"@") {
			return true
		}
	}
	return false
}

func TestConsolidateFull(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	_, recs, _ := sim(t, cfg, in, happy(1))
	res := Consolidate(cfg, recs)
	if len(res.ACs) != 2 || res.ACs[0].Status != "fail" || res.ACs[1].Status != "pass" || res.Summary != "judge" {
		t.Fatalf("汇总不对: %+v", res)
	}
	a1 := res.ACs[0]
	if len(a1.Challenges) != 1 || a1.Challenges[0].Resolution != "fail" || a1.Challenges[0].Response != "认同" || !strings.HasPrefix(a1.Reasoning, "【裁决】") {
		t.Fatalf("质疑记录不对: %+v", a1)
	}
	if res.ACs[1].Method != "ui" || !strings.Contains(res.ACs[1].Evidence, "home.png") {
		t.Fatalf("UI 结论应覆盖界面条目: %+v", res.ACs[1])
	}
	if len(res.Findings) != 2 || res.Findings[0].Severity != "high" || res.Severe() != 1 || len(res.Pages) != 1 {
		t.Fatalf("额外发现应按严重程度排序: %+v", res.Findings)
	}
	if res.Verdict() != protocol.VerdictFail {
		t.Fatal("有未通过条目应判未通过")
	}
}

func TestBudgetSkipsOptionalStages(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	cfg.MaxMinutes = 6 // plan 1 + service + verify 2 → 剩 3 分钟，不够 UI（5）
	roles, _, st := sim(t, cfg, in, happy(1))
	got := strings.Join(roles, " ")
	if got != "plan service verify ui(skip) red(skip) explore(skip) judge" {
		t.Fatalf("预算不足应跳过可选阶段: %s", got)
	}
	if st.Fatal != "" {
		t.Fatal("预算不足不应导致失败")
	}
	cfg = withHarness(Preset(TierA))
	cfg.MaxCostUSD = 1 // 每阶段 0.5，三个阶段后超支
	roles, recs, _ := sim(t, cfg, in, happy(1))
	if !strings.Contains(strings.Join(roles, " "), "ui(skip)") || !strings.Contains(recs[3].Note, "费用") {
		t.Fatalf("费用超限应跳过: %v %q", roles, recs[3].Note)
	}
}

func TestCrossModelFallback(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	out := happy(0)
	roles, recs, _ := sim(t, cfg, in, func(s StageSpec) (string, json.RawMessage, int64) {
		if s.Role == RoleRed && s.Harness == "codex" {
			return StatusError, nil, 1000
		}
		return out(s)
	})
	if got := strings.Join(roles, " "); got != "plan service verify ui shots red1@codex red1 judge" {
		t.Fatalf("交叉模型失败应改用主 harness 重试: %s", got)
	}
	// 重试那条记录紧跟在失败的交叉模型尝试之后。
	retry := len(recs) - 2
	if !strings.Contains(recs[retry].Note, "交叉模型") {
		t.Fatalf("重试应说明原因: %q", recs[retry].Note)
	}
}

func TestApprovalGate(t *testing.T) {
	cfg := withHarness(Preset(TierB))
	cfg.ApprovePlan = true
	roles, _, st := sim(t, cfg, in, happy(0))
	if st.Action != ActionAwait || strings.Join(roles, " ") != "plan" {
		t.Fatalf("计划后应等待确认: %v %+v", roles, st)
	}
	approved := in
	approved.Approved, approved.ApprovalNote = true, "重点测退款"
	roles, recs, _ := sim(t, cfg, approved, happy(0))
	if strings.Join(roles, " ") != "plan service verify" {
		t.Fatalf("确认后应继续: %v", roles)
	}
	_ = recs
	st = Next(cfg, approved, recs[:2])
	if !strings.Contains(st.Stage.Prompt, "重点测退款") {
		t.Fatal("确认时的补充说明应带进执行验证的提示词")
	}
}

func TestFailurePolicies(t *testing.T) {
	cfg := withHarness(Preset(TierB))
	out := happy(0)
	// 计划失败：继续执行验证；服务失败：继续；验证连续失败两次：致命。
	roles, _, st := sim(t, cfg, in, func(s StageSpec) (string, json.RawMessage, int64) {
		switch s.Role {
		case RolePlan, RoleVerify:
			return StatusError, nil, 1000
		}
		return out(s)
	})
	if strings.Join(roles, " ") != "plan service(skip) verify verify" || st.Fatal == "" {
		t.Fatalf("失败策略不对: %v %+v", roles, st)
	}
	// 仓库配置了启动命令：即使计划失败也启动服务，并以仓库配置为准。
	in2 := in
	in2.Repo.StartCmd = "make run"
	st = Next(cfg, in2, []StageRecord{{Role: RolePlan, Status: StatusError}})
	if st.Action != ActionStage || st.Stage.Service == nil || st.Stage.Service.StartCmd != "make run" {
		t.Fatalf("应使用仓库的启动命令: %+v", st)
	}
	// 计划判断不需要服务，且仓库没有配置：跳过。
	noSvc := j(Plan{NeedsService: false, Service: ServiceHint{StartCmd: "x"}, Items: []PlanItem{{ID: "AC-1", Method: "cli"}}})
	st = Next(cfg, in, []StageRecord{{Role: RolePlan, Status: StatusOK, Output: noSvc}})
	if st.Action != ActionSkip || !strings.Contains(st.Note, "不需要") {
		t.Fatalf("应跳过服务: %+v", st)
	}
}

func TestConsolidateGuards(t *testing.T) {
	cfg := withHarness(Preset(TierA))
	cfg.Judge = false
	noEvidence := j(map[string]any{"summary": "", "acs": []map[string]string{{"id": "AC-1", "status": "pass", "evidence": " "}, {"id": "AC-2", "status": "pass", "evidence": "ok"}}})
	red := j(map[string]any{"challenges": []map[string]string{{"ac_id": "AC-2", "claim": "反例", "proposed_status": "fail"}}})
	res := Consolidate(cfg, []StageRecord{
		{Role: RoleVerify, Status: StatusOK, Output: noEvidence},
		{Role: RoleRed, Round: 1, Status: StatusOK, Output: red},
		{Role: RoleRecheck, Round: 1, Status: StatusSkipped},
	})
	if res.ACs[0].Status != "unverifiable" {
		t.Error("没有证据的通过应降级")
	}
	if res.ACs[1].Status != "disputed" {
		t.Error("没有得到回应的质疑应标为存疑")
	}
	if res.Verdict() != protocol.VerdictPartial || !strings.Contains(res.Summary, "存疑 1") {
		t.Fatalf("结论不对: %s %s", res.Verdict(), res.Summary)
	}
}

func TestStaticOnlyPrompt(t *testing.T) {
	cfg := withHarness(Preset(TierC))
	st := Next(cfg, in, []StageRecord{{Role: RolePlan, Status: StatusOK, Output: planOut}})
	if st.Stage.Role != RoleVerify || !strings.Contains(st.Stage.Prompt, "静态与命令行验证") || !strings.Contains(st.Stage.Prompt, "AC-2") {
		t.Fatal("C 档执行验证提示词不对")
	}
	if !strings.Contains(Next(cfg, in, nil).Stage.Prompt, "method 只能用 cli 或 code") {
		t.Fatal("C 档计划应为简版")
	}
}

func TestPresetsRoundTrip(t *testing.T) {
	for _, p := range Presets() {
		if p.Normalize().MatchTier() != p.Tier {
			t.Errorf("%s 预设应能识别", p.Tier)
		}
	}
	c := Preset(TierB)
	c.UI = UITargeted
	if c.MatchTier() != TierCustom {
		t.Error("改了开关应为自定义")
	}
	if n := (Config{Tier: "x", StaticOnly: true, Service: true, UI: "full", RedRounds: 9}).Normalize(); n.Service || n.UI != "" || n.RedRounds != 5 || n.Tier != TierCustom || n.MaxMinutes < 5 {
		t.Errorf("Normalize 不对: %+v", n)
	}
}

func TestCrossModelStickyFallback(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	out := happy(2)
	roles, recs, _ := sim(t, cfg, in, func(s StageSpec) (string, json.RawMessage, int64) {
		if s.Role == RoleRed && s.Harness == "codex" {
			return StatusError, nil, 1000
		}
		return out(s)
	})
	got := strings.Join(roles, " ")
	if got != "plan service verify ui shots red1@codex red1 recheck1 red2 recheck2 red3 explore judge" {
		t.Fatalf("交叉模型失败后，后续轮次应直接用主 harness: %s", got)
	}
	_ = recs
}

func TestFindingsDedupe(t *testing.T) {
	cfg := withHarness(Preset(TierS))
	cfg.ShotReview = false // 本测试只关心发现去重，不关心截图质检
	ui := j(map[string]any{"acs": []any{}, "findings": []map[string]string{{"severity": "high", "title": "XSS", "detail": "d", "evidence": "e"}}})
	recs := []StageRecord{
		{Role: RolePlan, Status: StatusOK, Output: planOut},
		{Role: RoleService, Status: StatusOK, Service: &protocol.ServiceStatus{Running: true}},
		{Role: RoleVerify, Status: StatusOK, Output: report("pass", "pass")},
		{Role: RoleUI, Status: StatusOK, Output: ui},
	}
	if st := Next(cfg, in, recs); st.Stage.Role != RoleRed || !strings.Contains(st.Stage.Prompt, "不要重复报告") || !strings.Contains(st.Stage.Prompt, "[high] XSS") {
		t.Fatal("红队提示词应列出已记录的发现")
	}
	recs = append(recs,
		StageRecord{Role: RoleRed, Round: 1, Status: StatusOK, Output: j(map[string]any{"challenges": []any{}, "findings": []map[string]string{{"severity": "medium", "title": "XSS again", "detail": "d", "evidence": "e"}}})},
		StageRecord{Role: RoleExplore, Status: StatusOK, Output: j(map[string]any{"findings": []map[string]string{{"severity": "critical", "title": "存储型 XSS", "detail": "d", "evidence": "e"}}})},
	)
	if got := len(Consolidate(cfg, recs).Findings); got != 3 {
		t.Fatalf("裁决前应保留原始发现: %d", got)
	}
	st := Next(cfg, in, recs)
	if st.Stage.Role != RoleJudge || !strings.Contains(st.Stage.Prompt, "待合并去重") {
		t.Fatal("裁决提示词应包含待合并的发现")
	}
	recs = append(recs, StageRecord{Role: RoleJudge, Status: StatusOK, Output: j(map[string]any{"summary": "s", "acs": []any{},
		"findings": []map[string]string{{"severity": "critical", "title": "存储型 XSS（合并）", "detail": "d", "evidence": "e"}}})})
	res := Consolidate(cfg, recs)
	if len(res.Findings) != 1 || res.Findings[0].Source != RoleJudge || res.Findings[0].Severity != "critical" {
		t.Fatalf("裁决合并后的发现应替换原始列表: %+v", res.Findings)
	}
}
