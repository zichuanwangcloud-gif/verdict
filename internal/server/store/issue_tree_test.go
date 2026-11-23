package store

import (
	"context"
	"path/filepath"
	"testing"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/testplan"
)

func newSetStore(t *testing.T) (*Store, Repo, Runtime) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rt, _, err := st.CreateRuntime(context.Background(), "rt")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.CreateRepo(context.Background(), Repo{URL: "u", Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	return st, repo, rt
}

func mkRun(t *testing.T, st *Store, repo Repo, rt Runtime, issue linear.Issue, setID, parentKey string, scope []string, verdict string) string {
	t.Helper()
	cfg := testplan.Preset("A")
	cfg.Harness = "claude"
	r, err := st.CreateRun(context.Background(), NewRun{
		RepoID: repo.ID, Branch: "b", Issue: issue, RuntimeID: rt.ID, Config: cfg,
		SetID: setID, IssueParentKey: parentKey, Scope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "" {
		if _, err := st.SetStatus(context.Background(), r.ID, StatusCompleted, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.SetVerdict(context.Background(), r.ID, verdict); err != nil {
			t.Fatal(err)
		}
	}
	return r.ID
}

func parentIssue() linear.Issue {
	return linear.Issue{Identifier: "ENG-1", Title: "父单", Children: []linear.ChildRef{
		{Identifier: "ENG-2", Title: "子一"},
		{Identifier: "ENG-3", Title: "子二"},
	}}
}

func TestSetSummaryFullPass(t *testing.T) {
	st, repo, rt := newSetStore(t)
	ctx := context.Background()
	pid := mkRun(t, st, repo, rt, parentIssue(), "s1", "", []string{"ENG-2", "ENG-3"}, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s1", "ENG-1", nil, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-3", Title: "子二"}, "s1", "ENG-1", nil, protocol.VerdictPass)

	sm, err := st.SetSummary(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetPass || len(sm.Children) != 2 || len(sm.Uncovered) != 0 {
		t.Fatalf("全选全过应为通过: %+v", sm)
	}
	if sm.Children[0].RunID == "" || sm.Children[0].Verdict != protocol.VerdictPass {
		t.Fatalf("子单信息不全: %+v", sm.Children[0])
	}
}

func TestSetSummaryPartialCoverage(t *testing.T) {
	st, repo, rt := newSetStore(t)
	pid := mkRun(t, st, repo, rt, parentIssue(), "s2", "", []string{"ENG-2"}, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s2", "ENG-1", nil, protocol.VerdictPass)

	sm, err := st.SetSummary(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetPartialPass || len(sm.Uncovered) != 1 || sm.Uncovered[0].Identifier != "ENG-3" {
		t.Fatalf("只覆盖一部分应为部分通过: %+v", sm)
	}
}

func TestSetSummaryChildFail(t *testing.T) {
	st, repo, rt := newSetStore(t)
	pid := mkRun(t, st, repo, rt, parentIssue(), "s3", "", []string{"ENG-2", "ENG-3"}, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s3", "ENG-1", nil, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-3", Title: "子二"}, "s3", "ENG-1", nil, protocol.VerdictFail)

	sm, err := st.SetSummary(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetPartialFail {
		t.Fatalf("子单未通过应为部分未通过: %+v", sm)
	}
}

func TestSetSummaryParentFail(t *testing.T) {
	st, repo, rt := newSetStore(t)
	pid := mkRun(t, st, repo, rt, parentIssue(), "s4", "", []string{"ENG-2"}, protocol.VerdictFail)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s4", "ENG-1", nil, protocol.VerdictPass)

	sm, err := st.SetSummary(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetFail {
		t.Fatalf("父单未通过应为未通过: %+v", sm)
	}
}

func TestSetSummaryIncomplete(t *testing.T) {
	st, repo, rt := newSetStore(t)
	// 父单还在跑。
	pid := mkRun(t, st, repo, rt, parentIssue(), "s5", "", []string{"ENG-2"}, "")
	sm, err := st.SetSummary(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetIncomplete {
		t.Fatalf("父单未结束应为未完成: %+v", sm)
	}

	// 子单被选中但没建出 run。
	pid2 := mkRun(t, st, repo, rt, parentIssue(), "s6", "", []string{"ENG-2"}, protocol.VerdictPass)
	sm2, err := st.SetSummary(context.Background(), pid2)
	if err != nil {
		t.Fatal(err)
	}
	if sm2.Overall != SetIncomplete || !sm2.Children[0].Missing {
		t.Fatalf("子单缺失应为未完成且标记 missing: %+v", sm2)
	}
}

func TestSetSummaryParentPartial(t *testing.T) {
	st, repo, rt := newSetStore(t)
	pid := mkRun(t, st, repo, rt, parentIssue(), "s7", "", []string{"ENG-2", "ENG-3"}, protocol.VerdictPartial)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s7", "ENG-1", nil, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-3", Title: "子二"}, "s7", "ENG-1", nil, protocol.VerdictPass)

	sm, err := st.SetSummary(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetPartial {
		t.Fatalf("父单存疑应为部分无法验证: %+v", sm)
	}
}

// 同一子单在批次里重试过，聚合应取最新一条。
func TestSetSummaryUsesLatestChildRun(t *testing.T) {
	st, repo, rt := newSetStore(t)
	pid := mkRun(t, st, repo, rt, parentIssue(), "s8", "", []string{"ENG-2", "ENG-3"}, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s8", "ENG-1", nil, protocol.VerdictFail)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-2", Title: "子一"}, "s8", "ENG-1", nil, protocol.VerdictPass)
	mkRun(t, st, repo, rt, linear.Issue{Identifier: "ENG-3", Title: "子二"}, "s8", "ENG-1", nil, protocol.VerdictPass)

	sm, err := st.SetSummary(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Overall != SetPass {
		t.Fatalf("应取最新一次子单结果: %+v", sm)
	}
	for _, c := range sm.Children {
		if c.IssueKey == "ENG-2" && c.Verdict != protocol.VerdictPass {
			t.Fatalf("ENG-2 应取最新通过记录: %+v", c)
		}
	}
}

func TestVersionIssueAndCaseScope(t *testing.T) {
	st, repo, _ := newSetStore(t)
	ctx := context.Background()
	v, err := st.CreateVersion(ctx, repo.ID, "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	parent := linear.Issue{Identifier: "ENG-1", Title: "父单"}
	if err := st.AddVersionIssue(ctx, v, parent, []string{"ENG-2", "ENG-3"}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.AddVersionIssue(ctx, v, linear.Issue{Identifier: "ENG-2", Title: "子"}, nil, false); err != nil {
		t.Fatal(err)
	}
	scopeOf := func() map[string][]string {
		vis, err := st.VersionIssues(ctx, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, vi := range vis {
			out[vi.IssueKey] = vi.Scope
		}
		return out
	}
	got := scopeOf()
	if len(got["ENG-1"]) != 2 || len(got["ENG-2"]) != 0 {
		t.Fatalf("成员范围不对: %+v", got)
	}
	// 再次加入父单时更新范围，不动子单。
	if err := st.AddVersionIssue(ctx, v, parent, []string{"ENG-2"}, false); err != nil {
		t.Fatal(err)
	}
	got = scopeOf()
	if len(got["ENG-1"]) != 1 || got["ENG-1"][0] != "ENG-2" {
		t.Fatalf("父单范围未更新: %+v", got)
	}

	c, err := st.UpsertCase(ctx, Case{RepoID: repo.ID, IssueKey: "ENG-1", Issue: parent, Scope: []string{"ENG-2"}}, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Scope) != 1 || c.Scope[0] != "ENG-2" {
		t.Fatalf("用例范围不对: %+v", c)
	}
}
