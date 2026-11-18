package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/testplan"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func issue(key string) linear.Issue {
	return linear.Issue{ID: "uuid-" + key, Identifier: key, Title: key + " 标题"}
}

func TestVersionsAndMembers(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	repo, _ := st.CreateRepo(ctx, Repo{URL: "u", Name: "n"})
	other, _ := st.CreateRepo(ctx, Repo{URL: "u2", Name: "n2"})

	v, err := st.CreateVersion(ctx, repo.ID, "1.4.0", "备注")
	if err != nil || v.Status != VersionOpen || v.IssueCount != 0 {
		t.Fatalf("%v %+v", err, v)
	}
	if _, err := st.CreateVersion(ctx, repo.ID, "1.4.0", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("同仓库重名应冲突: %v", err)
	}
	if _, err := st.CreateVersion(ctx, other.ID, "1.4.0", ""); err != nil {
		t.Fatalf("不同仓库可以重名: %v", err)
	}

	if err := st.AddVersionIssue(ctx, v, issue("ENG-1"), false); err != nil {
		t.Fatal(err)
	}
	if id, _ := st.IssueVersion(ctx, repo.ID, "ENG-1"); id != v.ID {
		t.Fatalf("归属不对: %d", id)
	}
	// 同一仓库另一个版本：不移动时冲突，移动后只剩一份。
	v2, _ := st.CreateVersion(ctx, repo.ID, "1.5.0", "")
	if err := st.AddVersionIssue(ctx, v2, issue("ENG-1"), false); !errors.Is(err, ErrIssueInOtherVersion) {
		t.Fatalf("应报已在其他版本: %v", err)
	}
	if err := st.AddVersionIssue(ctx, v2, issue("ENG-1"), true); err != nil {
		t.Fatal(err)
	}
	if id, _ := st.IssueVersion(ctx, repo.ID, "ENG-1"); id != v2.ID {
		t.Fatalf("移动后归属不对: %d", id)
	}
	issues, _ := st.VersionIssues(ctx, v.ID)
	if len(issues) != 0 {
		t.Fatalf("旧版本不应还有成员: %+v", issues)
	}
	// 已发布版本里的 issue 再开发，新版本可以直接加入。
	if ok, _ := st.ReleaseVersion(ctx, v2.ID, ""); !ok {
		t.Fatal("应能发布")
	}
	if err := st.AddVersionIssue(ctx, v, issue("ENG-1"), false); err != nil {
		t.Fatalf("已发布版本里的 issue 应能开始新一轮: %v", err)
	}
	if err := st.AddVersionIssue(ctx, v, issue("MANUAL"), false); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRepo(ctx, repo.ID); err == nil {
		t.Fatal("有版本的仓库不能删除")
	}
}

func TestRegressionLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rt, _, _ := st.CreateRuntime(ctx, "rt")
	repo, _ := st.CreateRepo(ctx, Repo{URL: "u", Name: "n"})
	v, _ := st.CreateVersion(ctx, repo.ID, "1.4.0", "")
	st.AddVersionIssue(ctx, v, issue("ENG-1"), false)

	cfg := testplan.Preset("B")
	cfg.Harness = "claude"
	mk := func(i int64, caseID int64) NewRun {
		return NewRun{RepoID: repo.ID, Branch: "release/1.4", Issue: issue("ENG-1"), RuntimeID: rt.ID, Config: cfg,
			VersionID: v.ID, CaseID: caseID, PinRef: "refs/heads/release/1.4", PinSHA: "0123456789abcdef0123456789abcdef01234567", Attempt: 1}
	}
	g, err := st.CreateRegression(ctx, Regression{VersionID: v.ID, FreezeRef: "release/1.4", FreezeFullRef: "refs/heads/release/1.4",
		FreezeSHA: "0123456789abcdef0123456789abcdef01234567", Scope: RegressionScope{Baseline: BaselineCore},
		Config: RegressionConfig{Harness: "claude", IssueTier: "B", CaseTier: "C"}, RuntimeIDs: []int64{rt.ID}},
		[]NewRun{mk(rt.ID, 0), mk(rt.ID, 7)})
	if err != nil {
		t.Fatal(err)
	}
	if g.Seq != 1 || g.Status != RegressionRunning || len(g.RuntimeIDs) != 1 || g.RuntimeIDs[0] != rt.ID {
		t.Fatalf("%+v", g)
	}
	if vv, _ := st.Version(ctx, v.ID); vv.Status != VersionFrozen {
		t.Fatalf("冻结后版本应为 frozen: %s", vv.Status)
	}
	// 同一版本不能同时有两轮。
	if _, err := st.CreateRegression(ctx, Regression{VersionID: v.ID}, nil); err == nil {
		t.Fatal("进行中的回归没结束时不应再冻结")
	}
	briefs, _ := st.RegressionRuns(ctx, g.ID)
	if len(briefs) != 2 || briefs[0].PinSHA != "0123456789abcdef0123456789abcdef01234567" || briefs[1].CaseID != 7 {
		t.Fatalf("子测试不对: %+v", briefs)
	}
	// 队列里的子测试可以被另一个运行时领走。
	rt2, _, _ := st.CreateRuntime(ctx, "rt2")
	ok, err := st.ClaimRun(ctx, briefs[0].ID, rt.ID, rt2.ID)
	if err != nil || !ok {
		t.Fatalf("应能领取: %v %v", err, ok)
	}
	if again, _ := st.ClaimRun(ctx, briefs[0].ID, rt.ID, rt2.ID); again {
		t.Fatal("已被领走的不能再领")
	}
	q, _ := st.QueuedRegressionRuns(ctx, g.ID)
	if len(q) != 2 {
		t.Fatalf("排队的子测试应还在: %+v", q)
	}
	for _, b := range q {
		if b.ID == briefs[0].ID && b.RuntimeID != rt2.ID {
			t.Fatalf("领取后应改派到新运行时: %+v", b)
		}
	}
	if ok, _ := st.FinishRegression(ctx, g.ID, RegressionCompleted, protocol.VerdictPass); !ok {
		t.Fatal("应能写结论")
	}
	if ok, _ := st.FinishRegression(ctx, g.ID, RegressionCompleted, protocol.VerdictFail); ok {
		t.Fatal("结论只写一次")
	}
	// 重跑：同一轮追加尝试，轮次恢复进行中。
	if err := st.AddRegressionRuns(ctx, g.ID, []NewRun{func() NewRun { n := mk(rt2.ID, 7); n.Attempt = 2; return n }()}); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.Regression(ctx, g.ID); r.Status != RegressionRunning || r.Verdict != "" || r.FinishedAt != nil {
		t.Fatalf("重跑后应恢复进行中: %+v", r)
	}
}

func TestCasesUpsertAndRetire(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	repo, _ := st.CreateRepo(ctx, Repo{URL: "u", Name: "n"})
	plan := &testplan.Plan{Summary: "计划", Items: []testplan.PlanItem{{ID: "AC-1", Criterion: "c"}}}
	c, err := st.UpsertCase(ctx, Case{RepoID: repo.ID, IssueKey: "ENG-1", Issue: issue("ENG-1"), Plan: plan,
		SourceRunID: "r1", SourceVersionID: 1, Level: CaseCore}, "沉淀", "v1")
	if err != nil || c.Level != CaseCore || c.Plan == nil || c.Status != CaseActive {
		t.Fatalf("%v %+v", err, c)
	}
	if err := st.UpdateCase(ctx, c.ID, "", CaseRetired, "需求变更", nil, "", "停用", "需求变更"); err != nil {
		t.Fatal(err)
	}
	// 再次沉淀：保留级别，重新启用，计划被覆盖。
	newPlan := &testplan.Plan{Summary: "新计划"}
	c2, err := st.UpsertCase(ctx, Case{RepoID: repo.ID, IssueKey: "ENG-1", Issue: issue("ENG-1"), Plan: newPlan, SourceRunID: "r2"},
		"更新", "v2")
	if err != nil || c2.Level != CaseCore || c2.Status != CaseActive || c2.Plan.Summary != "新计划" || c2.RetiredReason != "" {
		t.Fatalf("%v %+v", err, c2)
	}
	events, _ := st.CaseHistory(ctx, c.ID)
	if len(events) != 3 {
		t.Fatalf("历史应有 3 条: %+v", events)
	}
	if got, _ := st.CaseByIssue(ctx, repo.ID, "ENG-1"); got.ID != c.ID {
		t.Fatal("同仓库同 issue 只应有一条用例")
	}
	if err := st.DeleteCase(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Case(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("删除后应不存在")
	}
}

func TestPresetPlanStage(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rt, _, _ := st.CreateRuntime(ctx, "rt")
	repo, _ := st.CreateRepo(ctx, Repo{URL: "u", Name: "n"})
	cfg := testplan.Preset("C")
	cfg.Harness = "claude"
	run, _ := st.CreateRun(ctx, NewRun{RepoID: repo.ID, Branch: "b", RuntimeID: rt.ID, Config: cfg,
		PlanSource: "src1", PresetPlan: `{"summary":"旧计划"}`})
	if run.PresetPlan != `{"summary":"旧计划"}` || run.PlanSource != "src1" {
		t.Fatalf("%+v", run)
	}
	if err := st.AddPresetPlan(ctx, run.ID, "src1", run.PresetPlan); err != nil {
		t.Fatal(err)
	}
	stages, _ := st.Stages(ctx, run.ID)
	p := testplan.ParsePlan(Records(stages))
	if p == nil || p.Summary != "旧计划" || stages[0].Status != testplan.StatusOK {
		t.Fatalf("预置计划没生效: %+v", stages)
	}
}
