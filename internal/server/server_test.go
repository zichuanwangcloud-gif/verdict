package server

import (
	"strings"
	"testing"

	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

func TestCommentBody(t *testing.T) {
	r := store.Run{ID: "abc", Status: store.StatusCompleted, Verdict: protocol.VerdictFail, RepoName: "org/repo", Tier: "S",
		Branch: "feat", CommitSHA: "0123456789abcdef", Harness: "claude", RuntimeName: "rt", SourceModified: true,
		Config: testplan.Config{RedRounds: 2, RedHarness: "codex"},
		ACs: []protocol.ACResult{{ID: "AC-1", Criterion: "a|b", Status: "fail", Confidence: "high", Evidence: "line1\nline2",
			Challenges: []protocol.Challenge{{Round: 1}}}},
		Findings: []protocol.Finding{{Severity: "high", Title: "SQL 注入", Detail: "d"}}}
	stages := []store.Stage{
		{StageRecord: testplan.StageRecord{Role: "plan", Status: "ok"}},
		{StageRecord: testplan.StageRecord{Role: "ui", Status: "skipped"}},
		{StageRecord: testplan.StageRecord{Role: "red", Round: 1, Status: "ok"}},
		{StageRecord: testplan.StageRecord{Role: "red", Round: 2, Status: "ok"}},
	}
	b := CommentBody(r, stages, "https://v")
	for _, want := range []string{"❌ 未通过", "S 对抗", "`0123456789ab`", `a\|b`, "line1<br>line2", "修改了被测源码",
		"https://v/runs/abc", "红队 codex", "测试计划 → ~~UI 走查~~ → 红队质疑 ×2", "经 1 次质疑", "| 高 |", "🚨 另外发现 1 个", "🟠 高 SQL 注入"} {
		if !strings.Contains(b, want) {
			t.Errorf("评论缺少 %q:\n%s", want, b)
		}
	}
	r.Status, r.Error = store.StatusError, "拉取失败"
	if !strings.Contains(CommentBody(r, nil, ""), "测试未完成：拉取失败") {
		t.Error("出错时应说明原因")
	}
}

func TestRepoNameAndBranch(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:org/repo.git":      "org/repo",
		"https://github.com/org/repo.git/": "org/repo",
		"https://gitlab.com/a/b/c":         "b/c",
	} {
		if got := repoName(in); got != want {
			t.Errorf("repoName(%q)=%q want %q", in, got, want)
		}
	}
	for b, valid := range map[string]bool{"feature/x": true, "-rf": false, "a..b": false, "a b": false, "": false, "v1.2": true} {
		if validBranch(b) != valid {
			t.Errorf("validBranch(%q) 应为 %v", b, valid)
		}
	}
}

func TestResolveConfig(t *testing.T) {
	both := &protocol.HostInfo{Harnesses: []protocol.HarnessInfo{
		{Name: "claude", Installed: true, LoggedIn: true}, {Name: "codex", Installed: true, LoggedIn: true}}}
	onlyClaude := &protocol.HostInfo{Harnesses: []protocol.HarnessInfo{
		{Name: "claude", Installed: true, LoggedIn: true}, {Name: "codex", Installed: true, LoggedIn: false}}}

	c := resolveConfig(runInput{Tier: "S", Harness: "claude"}, both)
	if c.Tier != "S" || c.RedHarness != "codex" || c.RedRounds != 3 || c.MaxMinutes != 180 {
		t.Fatalf("S 档应交叉模型: %+v", c)
	}
	if c := resolveConfig(runInput{Tier: "A", Harness: "claude"}, onlyClaude); c.RedHarness != "claude" {
		t.Fatalf("另一种 harness 未登录时应降级为同一 harness: %+v", c)
	}
	yes, cost := true, 3.5
	c = resolveConfig(runInput{Tier: "B", Harness: "codex", ApprovePlan: &yes, MaxMinutes: 30, MaxCostUSD: &cost}, both)
	if !c.ApprovePlan || c.MaxMinutes != 30 || c.MaxCostUSD != 3.5 || c.RedHarness != "codex" || c.Tier != "B" {
		t.Fatalf("覆盖项不对: %+v", c)
	}
	custom := testplan.Config{Service: true, UI: "full", RedRounds: 2, CrossModel: true}
	c = resolveConfig(runInput{Tier: "custom", Config: &custom, Harness: "codex"}, both)
	if c.Tier != "custom" || c.UI != "full" || c.RedHarness != "claude" || c.MaxMinutes == 0 {
		t.Fatalf("自定义不对: %+v", c)
	}
	if c := resolveConfig(runInput{Harness: "claude"}, nil); c.Tier != "B" {
		t.Fatal("默认 B 档")
	}
}
