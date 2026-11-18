package server

import (
	"context"
	"net/http"
	"strings"

	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// ---- 回归基线用例 ----

func (s *Server) listCases(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	cases, err := s.st.Cases(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	// 附上每个用例最近的回归结果（含不稳定标记）。
	history, err := s.st.RepoRegressionRuns(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	type caseView struct {
		store.Case
		Results []store.RunBrief `json:"results"`
		Flaky   bool             `json:"flaky"`
	}
	out := make([]caseView, 0, len(cases))
	for _, c := range cases {
		v := caseView{Case: c, Results: []store.RunBrief{}}
		seen := map[string]bool{}
		for _, b := range history {
			if b.CaseID != c.ID || !store.Terminal(b.Status) || b.Verdict == "" {
				continue
			}
			v.Results = append(v.Results, b)
			if sha := b.PinSHA; sha != "" {
				key := sha + "=" + b.Verdict
				if seen[key] {
					continue
				}
				seen[key] = true
			}
		}
		// 同一个 commit 上出现过不同的通过/未通过结论，标为不稳定。
		bySHA := map[string]map[string]bool{}
		for _, b := range v.Results {
			if b.PinSHA == "" {
				continue
			}
			if bySHA[b.PinSHA] == nil {
				bySHA[b.PinSHA] = map[string]bool{}
			}
			bySHA[b.PinSHA][b.Verdict] = true
		}
		for _, set := range bySHA {
			if len(set) > 1 {
				v.Flaky = true
			}
		}
		if len(v.Results) > 10 {
			v.Results = v.Results[len(v.Results)-10:]
		}
		out = append(out, v)
	}
	ok(w, out)
}

// createCase 从一次通过的测试手动加入基线。
func (s *Server) createCase(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		RunID string `json:"run_id"`
		Level string `json:"level"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	run, err := s.st.Run(r.Context(), in.RunID)
	if err != nil {
		failErr(w, err)
		return
	}
	if run.RepoID != repoID {
		fail(w, http.StatusBadRequest, "测试不属于这个仓库")
		return
	}
	if run.Status != store.StatusCompleted || (run.Verdict != "pass" && run.Verdict != "partial") {
		fail(w, http.StatusConflict, "只能把通过（或部分无法验证）的测试加入回归基线")
		return
	}
	if run.IssueKey == "MANUAL" || run.Issue.ID == "" {
		fail(w, http.StatusConflict, "手动输入的需求没有稳定编号，不能加入回归基线")
		return
	}
	if in.Level != store.CaseCore {
		in.Level = store.CaseNormal
	}
	plan, _ := s.st.RunPlan(r.Context(), run.ID)
	if plan == nil {
		fail(w, http.StatusConflict, "这次测试没有可复用的计划，回归时无法按计划重测")
		return
	}
	c, err := s.st.UpsertCase(r.Context(), store.Case{RepoID: repoID, IssueKey: run.IssueKey, Issue: run.Issue, Plan: plan,
		SourceRunID: run.ID, SourceVersionID: run.VersionID, Level: in.Level}, "手动加入基线", "来自测试 "+run.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, c)
}

func (s *Server) getCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	c, err := s.st.Case(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	events, _ := s.st.CaseHistory(r.Context(), id)
	results, _ := s.st.RepoRegressionRuns(r.Context(), c.RepoID)
	runs := []store.RunBrief{}
	for _, b := range results {
		if b.CaseID == c.ID {
			runs = append(runs, b)
		}
	}
	ok(w, map[string]any{"case": c, "history": events, "runs": runs})
}

// updateCase 处理三种回归未通过后的处理：改级别、停用、更新计划；也支持重新启用。
func (s *Server) updateCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		Level  string `json:"level"`
		Status string `json:"status"`
		Reason string `json:"reason"`
		// 计划过时：用某次回归子测试的计划更新用例。
		PlanRunID string `json:"plan_run_id"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.st.Case(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	level, status, reason := strings.TrimSpace(in.Level), strings.TrimSpace(in.Status), strings.TrimSpace(in.Reason)
	if level != "" && level != store.CaseCore && level != store.CaseNormal {
		fail(w, http.StatusBadRequest, "级别只能是 core / normal")
		return
	}
	if status != "" && status != store.CaseActive && status != store.CaseRetired {
		fail(w, http.StatusBadRequest, "状态只能是 active / retired")
		return
	}
	if status == store.CaseRetired && reason == "" {
		fail(w, http.StatusBadRequest, "停用要写原因（例如需求已变更）")
		return
	}
	var plan *testplan.Plan
	planSource := ""
	if in.PlanRunID != "" {
		run, err := s.st.Run(r.Context(), in.PlanRunID)
		if err != nil {
			failErr(w, err)
			return
		}
		if run.RepoID != c.RepoID || run.IssueKey != c.IssueKey {
			fail(w, http.StatusBadRequest, "这次测试不是同一个 issue")
			return
		}
		if plan, _ = s.st.RunPlan(r.Context(), run.ID); plan == nil {
			fail(w, http.StatusConflict, "这次测试没有可用的计划")
			return
		}
		planSource = run.ID
	}
	action, detail := "修改", ""
	var parts []string
	if level != "" {
		parts = append(parts, "级别改为 "+level)
	}
	if status != "" {
		parts = append(parts, "状态改为 "+status)
		if reason != "" {
			parts = append(parts, "原因："+reason)
		}
	}
	if plan != nil {
		parts = append(parts, "计划更新自测试 "+planSource)
	}
	detail = strings.Join(parts, "；")
	if err := s.st.UpdateCase(r.Context(), id, level, status, reason, plan, planSource, action, detail); err != nil {
		failErr(w, err)
		return
	}
	updated, _ := s.st.Case(r.Context(), id)
	ok(w, updated)
}

func (s *Server) deleteCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	if err := s.st.DeleteCase(r.Context(), id); err != nil {
		failErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) caseHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	events, err := s.st.CaseHistory(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, events)
}

// ---- 回归轮次收尾 ----

// regressionDone 在回归子测试结束时检查整轮是否已经结束。
func (s *Server) regressionDone(ctx context.Context, regressionID string) {
	if regressionID == "" {
		return
	}
	briefs, err := s.st.RegressionRuns(ctx, regressionID)
	if err != nil {
		return
	}
	latest := map[string]store.RunBrief{}
	for _, b := range briefs {
		if cur, ok := latest[b.IssueKey]; !ok || b.Attempt >= cur.Attempt {
			latest[b.IssueKey] = b
		}
	}
	fail, partial, errored := false, false, false
	for _, b := range latest {
		if !store.Terminal(b.Status) {
			return // 还有没跑完的
		}
		switch {
		case b.Status != store.StatusCompleted:
			errored = true
		case b.Verdict == "fail":
			fail = true
		case b.Verdict == "partial":
			partial = true
		}
	}
	verdict := "pass"
	switch {
	case fail:
		verdict = "fail"
	case errored:
		verdict = store.RegressionIncomplete
	case partial:
		verdict = "partial"
	}
	if _, err := s.st.FinishRegression(ctx, regressionID, store.RegressionCompleted, verdict); err != nil {
		return
	}
	g, err := s.st.Regression(ctx, regressionID)
	if err != nil {
		return
	}
	gv, err := s.st.Version(ctx, g.VersionID)
	if err != nil {
		return
	}
	if gv.Status == store.VersionOpen {
		_ = s.st.SetVersionStatus(ctx, gv.ID, store.VersionFrozen)
	}
}
