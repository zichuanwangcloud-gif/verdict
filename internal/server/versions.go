package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// ---- 版本 ----

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	repoID, _ := parseInt64(r.URL.Query().Get("repo_id"))
	vs, err := s.st.Versions(r.Context(), repoID)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, vs)
}

func parseInt64(v string) (int64, error) {
	return strconv.ParseInt(v, 10, 64)
}

func (s *Server) createVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RepoID int64  `json:"repo_id"`
		Name   string `json:"name"`
		Note   string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		fail(w, http.StatusBadRequest, "请填写版本名")
		return
	}
	if _, err := s.st.Repo(r.Context(), in.RepoID); err != nil {
		fail(w, http.StatusBadRequest, "仓库不存在")
		return
	}
	v, err := s.st.CreateVersion(r.Context(), in.RepoID, in.Name, strings.TrimSpace(in.Note))
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, "这个仓库里已经有同名版本")
		return
	}
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, v)
}

func (s *Server) updateVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := s.st.Version(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = v.Name
	}
	if err := s.st.UpdateVersion(r.Context(), id, name, strings.TrimSpace(in.Note)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(w, http.StatusConflict, "这个仓库里已经有同名版本")
			return
		}
		failErr(w, err)
		return
	}
	v, _ = s.st.Version(r.Context(), id)
	ok(w, v)
}

func (s *Server) deleteVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	if err := s.st.DeleteVersion(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			failErr(w, err)
			return
		}
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) addVersionIssue(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		IssueKey   string   `json:"issue_key"`
		Move       bool     `json:"move"`
		ChildScope []string `json:"child_scope"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := s.st.Version(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	if v.Status == store.VersionReleased {
		fail(w, http.StatusBadRequest, "版本已发布，不能再添加 issue")
		return
	}
	in.IssueKey = strings.TrimSpace(in.IssueKey)
	if in.IssueKey == "" {
		fail(w, http.StatusBadRequest, "请填写 issue 编号")
		return
	}
	client := linearClient(s.linearKey(r.Context()))
	issue, err := client.GetIssue(r.Context(), in.IssueKey)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	// 父单成员：确认子单范围后，把选中的子单一起加入版本。
	scope := normalizeChildScope(in.ChildScope)
	var childIssues []linear.Issue
	if len(scope) > 0 {
		children := make(map[string]bool, len(issue.Children))
		for _, c := range issue.Children {
			children[c.Identifier] = true
		}
		for _, k := range scope {
			if !children[k] {
				fail(w, http.StatusBadRequest, fmt.Sprintf("%s 不是 %s 的子单", k, issue.Identifier))
				return
			}
		}
		for _, k := range scope {
			ci, err := client.GetIssue(r.Context(), k)
			if err != nil {
				fail(w, http.StatusBadGateway, err.Error())
				return
			}
			childIssues = append(childIssues, ci)
		}
	}
	var vScope []string
	if in.ChildScope != nil {
		vScope = scope
	}
	if err := s.st.AddVersionIssue(r.Context(), v, issue, vScope, in.Move); err != nil {
		if errors.Is(err, store.ErrIssueInOtherVersion) {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		failErr(w, err)
		return
	}
	for _, ci := range childIssues {
		if err := s.st.AddVersionIssue(r.Context(), v, ci, nil, in.Move); err != nil {
			if errors.Is(err, store.ErrIssueInOtherVersion) {
				fail(w, http.StatusConflict, err.Error())
				return
			}
			failErr(w, err)
			return
		}
	}
	s.detail(w, r, v.ID)
}

func (s *Server) removeVersionIssue(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	v, err := s.st.Version(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	if v.Status == store.VersionReleased {
		fail(w, http.StatusBadRequest, "版本已发布，不能再修改成员")
		return
	}
	if err := s.st.RemoveVersionIssue(r.Context(), id, r.PathValue("key")); err != nil {
		failErr(w, err)
		return
	}
	s.detail(w, r, id)
}

// ---- 版本详情（含矩阵） ----

type matrixCell struct {
	RunID    string  `json:"run_id"`
	Status   string  `json:"status"`
	Verdict  string  `json:"verdict"`
	Attempt  int     `json:"attempt"`
	CostUSD  float64 `json:"cost_usd"`
	Runtime  string  `json:"runtime_name"`
	Commit   string  `json:"commit_sha"`
	Finished bool    `json:"finished"`
	// Overall 是父单这一格的批次整体结论（通过 / 部分通过 / 部分未通过 / 未通过 / 未完成）；
	// 普通行与子单行为空，直接看 verdict。
	Overall string `json:"overall,omitempty"`
}

type matrixRow struct {
	Key         string        `json:"key"`  // issue 编号，或用例的 issue 编号
	Kind        string        `json:"kind"` // issue / case
	Title       string        `json:"title"`
	Level       string        `json:"level,omitempty"` // 基线用例的级别
	CaseID      int64         `json:"case_id,omitempty"`
	Branch      *matrixCell   `json:"branch,omitempty"` // 分支上的最近一次测试
	Cells       []*matrixCell `json:"cells"`            // 与 regressions 一一对应
	Integration bool          `json:"integration"`      // 分支通过、冻结节点未通过
	History     bool          `json:"history"`          // 基线用例：上一轮通过、这一轮未通过
	// 父单成员在版本里覆盖的子单；子单成员指向所属父单，供前端分组。
	Scope            []string `json:"scope,omitempty"`
	ParentIdentifier string   `json:"parent_identifier,omitempty"`
}

type regressionRow struct {
	store.Regression
	Total   int     `json:"total"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Partial int     `json:"partial"`
	Errored int     `json:"errored"`
	Running int     `json:"running"`
	CostUSD float64 `json:"cost_usd"`
}

type versionDetail struct {
	Version     store.Version   `json:"version"`
	Regressions []regressionRow `json:"regressions"`
	Rows        []matrixRow     `json:"rows"`
	Cases       []store.Case    `json:"cases"` // 基线里已有的用例（用于提示 issue 是否有回归覆盖）
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	s.detail(w, r, id)
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := r.Context()
	v, err := s.st.Version(ctx, id)
	if err != nil {
		failErr(w, err)
		return
	}
	issues, err := s.st.VersionIssues(ctx, id)
	if err != nil {
		failErr(w, err)
		return
	}
	regs, err := s.st.Regressions(ctx, id)
	if err != nil {
		failErr(w, err)
		return
	}
	runs, err := s.st.VersionRuns(ctx, id)
	if err != nil {
		failErr(w, err)
		return
	}
	cases, _ := s.st.Cases(ctx, v.RepoID)

	// 每轮回归取每项的最新一次尝试。
	latest := make([]map[string]*store.RunBrief, len(regs))
	for i := range regs {
		latest[i] = map[string]*store.RunBrief{}
	}
	branch := map[string]*store.RunBrief{}
	order := []string{}
	seen := map[string]bool{}
	cell := func(b *store.RunBrief, names map[int64]string) *matrixCell {
		return &matrixCell{RunID: b.ID, Status: b.Status, Verdict: b.Verdict, Attempt: b.Attempt, CostUSD: b.CostUSD,
			Runtime: names[b.RuntimeID], Commit: b.CommitSHA, Finished: store.Terminal(b.Status)}
	}
	runtimeNames := map[int64]string{}
	if rts, err := s.st.Runtimes(ctx); err == nil {
		for _, rt := range rts {
			runtimeNames[rt.ID] = rt.Name
		}
	}
	rows := map[string]*matrixRow{}
	rowFor := func(key string) *matrixRow {
		if _, ok := rows[key]; !ok {
			rows[key] = &matrixRow{Key: key, Kind: "issue", Cells: make([]*matrixCell, len(regs))}
			order = append(order, key)
			seen[key] = true
		}
		return rows[key]
	}
	// 成员优先，保证顺序稳定。父单记录覆盖范围，子单记录所属父单，前端据此分组。
	parentOf := map[string]string{}
	for _, vi := range issues {
		for _, k := range vi.Scope {
			parentOf[k] = vi.IssueKey
		}
	}
	for _, vi := range issues {
		row := rowFor(vi.IssueKey)
		row.Title = vi.Issue.Title
		row.Scope = vi.Scope
		row.ParentIdentifier = parentOf[vi.IssueKey]
	}
	for _, b := range runs {
		if b.RegressionID == "" {
			if cur := branch[b.IssueKey]; cur == nil || b.Attempt >= cur.Attempt {
				branch[b.IssueKey] = &b
			}
			if row, ok := rows[b.IssueKey]; ok {
				row.Title = titleOf(row.Title, b.IssueKey)
			}
			continue
		}
		if i := slices.IndexFunc(regs, func(g store.Regression) bool { return g.ID == b.RegressionID }); i >= 0 {
			if cur := latest[i][b.IssueKey]; cur == nil || b.Attempt >= cur.Attempt || cur.CreatedAt.Before(*b.CreatedAt) {
				latest[i][b.IssueKey] = &b
			}
		}
	}
	caseLevel := map[string]store.Case{}
	for _, c := range cases {
		caseLevel[c.IssueKey] = c
	}
	for _, vi := range issues {
		row := rowFor(vi.IssueKey)
		if c, ok := caseLevel[vi.IssueKey]; ok {
			row.Kind, row.Level, row.CaseID = "case", c.Level, c.ID
		}
	}
	for key := range branch {
		if !seen[key] {
			rowFor(key)
		}
	}
	for i := range regs {
		for key := range latest[i] {
			if !seen[key] {
				row := rowFor(key)
				if c, ok := caseLevel[key]; ok {
					row.Kind, row.Level, row.CaseID, row.Title = "case", c.Level, c.ID, c.Issue.Title
				}
			}
		}
	}

	regRows := make([]regressionRow, len(regs))
	for i, g := range regs {
		rr := regressionRow{Regression: g, Total: len(latest[i])}
		for _, b := range latest[i] {
			rr.CostUSD += b.CostUSD
			switch {
			case !store.Terminal(b.Status):
				rr.Running++
			case b.Verdict == protocol.VerdictPass:
				rr.Passed++
			case b.Verdict == protocol.VerdictFail:
				rr.Failed++
			case b.Verdict == protocol.VerdictPartial:
				rr.Partial++
			default:
				rr.Errored++
			}
		}
		regRows[i] = rr
	}

	out := make([]matrixRow, 0, len(order))
	for _, key := range order {
		row := rows[key]
		if b := branch[key]; b != nil {
			row.Branch = cell(b, runtimeNames)
			if row.Title == "" {
				row.Title = b.IssueKey
			}
		}
		prevVerdict := ""
		for i := range regs {
			if b := latest[i][key]; b != nil {
				row.Cells[i] = cell(b, runtimeNames)
			}
			// 历史回归：基线用例在同一项上「曾经通过、现在未通过」。
			if row.Kind == "case" && row.Cells[i] != nil && row.Cells[i].Finished {
				if prevVerdict == protocol.VerdictPass && row.Cells[i].Verdict == protocol.VerdictFail {
					row.History = true
				}
				if row.Cells[i].Verdict != "" {
					prevVerdict = row.Cells[i].Verdict
				}
			}
		}
		// 集成回归：分支最近一次通过，最新一轮冻结回归未通过。
		if n := len(regs); n > 0 && row.Branch != nil && row.Branch.Verdict == protocol.VerdictPass {
			if c := row.Cells[n-1]; c != nil && c.Finished && c.Verdict == protocol.VerdictFail {
				row.Integration = true
			}
		}
		out = append(out, *row)
	}
	// 父单行的每一格显示整批结论（按设计 §6.2），子单行仍看自己的 verdict。
	for i := range out {
		if len(out[i].Scope) == 0 {
			continue
		}
		if b := out[i].Branch; b != nil && b.RunID != "" {
			if sm, err := s.st.SetSummary(ctx, b.RunID); err == nil {
				b.Overall = sm.Overall
			}
		}
		for _, c := range out[i].Cells {
			if c == nil || c.RunID == "" {
				continue
			}
			if sm, err := s.st.SetSummary(ctx, c.RunID); err == nil {
				c.Overall = sm.Overall
			}
		}
	}
	ok(w, versionDetail{Version: v, Regressions: regRows, Rows: out, Cases: cases})
}

func titleOf(title, fallback string) string {
	if title != "" {
		return title
	}
	return fallback
}

// ---- 冻结与回归 ----

type freezeInput struct {
	Ref        string  `json:"ref"`
	Baseline   string  `json:"baseline"` // core / all / none，默认 core
	Harness    string  `json:"harness"`
	IssueTier  string  `json:"issue_tier"`
	CaseTier   string  `json:"case_tier"`
	RuntimeIDs []int64 `json:"runtime_ids"`
}

func (s *Server) createRegression(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in freezeInput
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	g, code, err := s.freeze(r.Context(), id, in)
	if err != nil {
		fail(w, code, err.Error())
		return
	}
	ok(w, g)
}

func (s *Server) freeze(ctx context.Context, versionID int64, in freezeInput) (store.Regression, int, error) {
	v, err := s.st.Version(ctx, versionID)
	if err != nil {
		return store.Regression{}, http.StatusNotFound, err
	}
	if v.Status == store.VersionReleased {
		return store.Regression{}, http.StatusBadRequest, errors.New("版本已发布，不能再冻结回归")
	}
	repo, err := s.st.Repo(ctx, v.RepoID)
	if err != nil {
		return store.Regression{}, http.StatusInternalServerError, err
	}
	in.Ref = strings.TrimSpace(in.Ref)
	if in.Ref == "" {
		return store.Regression{}, http.StatusBadRequest, errors.New("请填写冻结的分支、tag 或 commit")
	}
	if in.Baseline == "" {
		in.Baseline = store.BaselineCore
	}
	if !slices.Contains([]string{store.BaselineCore, store.BaselineAll, store.BaselineNone}, in.Baseline) {
		return store.Regression{}, http.StatusBadRequest, errors.New("回归基线范围只能是 core / all / none")
	}
	if in.Harness == "" {
		in.Harness = "claude"
	}
	if !slices.Contains(harnesses, in.Harness) {
		return store.Regression{}, http.StatusBadRequest, errors.New("harness 只支持 claude / codex")
	}
	in.IssueTier = tierOr(in.IssueTier, testplan.TierB)
	in.CaseTier = tierOr(in.CaseTier, testplan.TierC)

	issues, err := s.st.VersionIssues(ctx, versionID)
	if err != nil {
		return store.Regression{}, http.StatusInternalServerError, err
	}
	cases, err := s.st.Cases(ctx, v.RepoID)
	if err != nil {
		return store.Regression{}, http.StatusInternalServerError, err
	}
	if in.Baseline != store.BaselineNone {
		cases = slices.DeleteFunc(cases, func(c store.Case) bool {
			return c.Status != store.CaseActive || (in.Baseline == store.BaselineCore && c.Level != store.CaseCore)
		})
	} else {
		cases = nil
	}
	// 本版本 issue 已作为成员；基线用例里和成员重复的不重复跑。
	member := map[string]bool{}
	for _, vi := range issues {
		member[vi.IssueKey] = true
	}
	cases = slices.DeleteFunc(cases, func(c store.Case) bool { return member[c.IssueKey] })

	if len(issues) == 0 && len(cases) == 0 {
		return store.Regression{}, http.StatusBadRequest, errors.New("这个版本还没有成员，也没有可用的回归用例")
	}

	// 参与运行时：优先用用户选中的，其次全部在线的；必须装好所选 harness。
	ids := in.RuntimeIDs
	if len(ids) == 0 {
		ids = s.hub.OnlineIDs()
	}
	var ready []int64
	for _, rid := range ids {
		rt, err := s.st.Runtime(ctx, rid)
		if err != nil || rt.Info == nil || rt.Info.Protocol != protocol.ProtocolVersion {
			continue
		}
		if h, found := rt.Info.Harness(in.Harness); found && h.Ready() {
			ready = append(ready, rid)
		}
	}
	if len(ready) == 0 {
		return store.Regression{}, http.StatusBadRequest, fmt.Errorf("没有可用的运行时：需要至少一个在线、已装好并登录 %s 的运行时", in.Harness)
	}

	// 解析冻结节点。
	rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	res, _, err := s.hub.Resolve(rctx, repo.URL, in.Ref, ready)
	cancel()
	if err != nil {
		return store.Regression{}, http.StatusBadGateway, fmt.Errorf("解析冻结节点失败：%w", err)
	}
	// 直接填 SHA 时 res.Ref 为空，agent 会先按分支拉取，再按 SHA 补齐。
	fullRef := res.Ref

	lk := s.linearKey(ctx)
	// 父单成员把父子 run 绑成同一批次：父 run 带 scope，选中的子单 run 带 set_id 与父单编号。
	setIDs := map[string]string{}
	childParent := map[string]string{}
	for _, vi := range issues {
		if len(vi.Scope) > 0 {
			setIDs[vi.IssueKey] = store.RandomHex(6)
		}
	}
	for _, vi := range issues {
		for _, k := range vi.Scope {
			childParent[k] = vi.IssueKey
		}
	}
	runs := make([]store.NewRun, 0, len(issues)+len(cases))
	next := 0
	pick := func() int64 {
		id := ready[next%len(ready)]
		next++
		return id
	}
	for _, vi := range issues {
		issue := vi.Issue
		if vi.IssueKey != "MANUAL" && lk != "" {
			if fresh, err := linearClient(lk).GetIssue(ctx, vi.IssueKey); err == nil {
				issue = fresh
			}
		}
		src, plan, _ := s.st.LatestBranchRunWithPlan(ctx, versionID, vi.IssueKey)
		cfg := testplan.Preset(in.IssueTier)
		cfg.Harness, cfg.RedHarness = in.Harness, in.Harness
		cfg.ApprovePlan = false
		cfg = cfg.Normalize()
		n := store.NewRun{RepoID: v.RepoID, Branch: in.Ref, Issue: issue, RuntimeID: pick(), Config: cfg,
			VersionID: versionID, PinRef: fullRef, PinSHA: res.SHA, PlanSource: src, Attempt: 1}
		if id, ok := setIDs[vi.IssueKey]; ok {
			n.SetID = id
			n.Scope = vi.Scope
		} else if p, ok := childParent[vi.IssueKey]; ok {
			n.SetID = setIDs[p]
			n.IssueParentKey = p
		}
		if plan != nil {
			n.PresetPlan = planJSON(plan)
		}
		runs = append(runs, n)
	}
	for _, c := range cases {
		cfg := testplan.Preset(in.CaseTier)
		cfg.Harness, cfg.RedHarness = in.Harness, in.Harness
		cfg.ApprovePlan = false
		cfg = cfg.Normalize()
		n := store.NewRun{RepoID: v.RepoID, Branch: in.Ref, Issue: c.Issue, RuntimeID: pick(), Config: cfg,
			VersionID: versionID, CaseID: c.ID, PinRef: fullRef, PinSHA: res.SHA, PlanSource: c.SourceRunID, Attempt: 1}
		if c.Plan != nil {
			n.PresetPlan = planJSON(c.Plan)
		}
		runs = append(runs, n)
	}

	g, err := s.st.CreateRegression(ctx, store.Regression{VersionID: versionID, FreezeRef: in.Ref, FreezeFullRef: fullRef, FreezeSHA: res.SHA,
		Scope: store.RegressionScope{Baseline: in.Baseline}, Config: store.RegressionConfig{Harness: in.Harness, IssueTier: in.IssueTier, CaseTier: in.CaseTier},
		RuntimeIDs: ready}, runs)
	if err != nil {
		return store.Regression{}, http.StatusConflict, err
	}
	for _, rid := range ready {
		go s.hub.Schedule(rid)
	}
	return g, 0, nil
}

func tierOr(t, def string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return def
	}
	return t
}

func planJSON(p *testplan.Plan) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// cancelRegression 取消整轮回归。
func (s *Server) cancelRegression(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.Regression(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	if g.Status != store.RegressionRunning {
		fail(w, http.StatusConflict, "这一轮回归已经结束")
		return
	}
	briefs, err := s.st.RegressionRuns(r.Context(), g.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	for _, b := range briefs {
		if !store.Terminal(b.Status) {
			if run, err := s.st.Run(r.Context(), b.ID); err == nil {
				_ = s.hub.Cancel(r.Context(), run)
			}
		}
	}
	if _, err := s.st.FinishRegression(r.Context(), g.ID, store.RegressionCanceled, ""); err != nil {
		failErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// retryRegression 重跑未通过的子测试：在同一轮里生成新的尝试，保留旧结果。
func (s *Server) retryRegression(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := s.st.Regression(ctx, r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	briefs, err := s.st.RegressionRuns(ctx, g.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	// 每项取最新一次尝试，挑出未通过的。
	latest := map[string]store.RunBrief{}
	for _, b := range briefs {
		key := b.IssueKey
		if cur, ok := latest[key]; !ok || b.Attempt >= cur.Attempt {
			latest[key] = b
		}
	}
	var runs []store.NewRun
	for _, b := range latest {
		if !store.Terminal(b.Status) || b.Verdict == protocol.VerdictPass {
			continue
		}
		old, err := s.st.Run(ctx, b.ID)
		if err != nil {
			continue
		}
		n := store.NewRun{RepoID: old.RepoID, Branch: old.Branch, Issue: old.Issue, RuntimeID: old.RuntimeID, Config: old.Config,
			VersionID: old.VersionID, CaseID: old.CaseID, PinRef: old.PinRef, PinSHA: old.PinSHA, PlanSource: old.PlanSource,
			PresetPlan: old.PresetPlan, Attempt: old.Attempt + 1,
			SetID: old.SetID, IssueParentKey: old.IssueParentKey, Scope: old.Scope}
		runs = append(runs, n)
	}
	if len(runs) == 0 {
		fail(w, http.StatusBadRequest, "这一轮没有需要重跑的子测试")
		return
	}
	if err := s.st.AddRegressionRuns(ctx, g.ID, runs); err != nil {
		failErr(w, err)
		return
	}
	for _, rid := range g.RuntimeIDs {
		go s.hub.Schedule(rid)
	}
	ok(w, map[string]int{"created": len(runs)})
}

// releaseVersion 标记版本已发布，并把通过的 issue 沉淀为回归用例。
func (s *Server) releaseVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	v, err := s.st.Version(ctx, id)
	if err != nil {
		failErr(w, err)
		return
	}
	regs, err := s.st.Regressions(ctx, id)
	if err != nil {
		failErr(w, err)
		return
	}
	if len(regs) == 0 {
		fail(w, http.StatusConflict, "这个版本还没有做过回归，先冻结回归一次")
		return
	}
	last := regs[len(regs)-1]
	if last.Status == store.RegressionRunning {
		fail(w, http.StatusConflict, "最近一轮回归还没结束")
		return
	}
	if last.Verdict != protocol.VerdictPass && strings.TrimSpace(in.Note) == "" {
		fail(w, http.StatusConflict, "最近一轮回归的结论不是「通过」，放行请填写理由")
		return
	}
	okRelease, err := s.st.ReleaseVersion(ctx, id, strings.TrimSpace(in.Note))
	if err != nil {
		failErr(w, err)
		return
	}
	if !okRelease {
		fail(w, http.StatusConflict, "版本已发布")
		return
	}
	deposited := s.depositCases(ctx, v, last)
	_ = s.st.SetVersionStatus(ctx, id, store.VersionReleased)
	v, _ = s.st.Version(ctx, id)
	ok(w, map[string]any{"version": v, "cases_added": deposited})
}

// depositCases 把一轮回归里通过的成员 issue 沉淀（或更新）为用例。
func (s *Server) depositCases(ctx context.Context, v store.Version, g store.Regression) int {
	briefs, err := s.st.RegressionRuns(ctx, g.ID)
	if err != nil {
		return 0
	}
	latest := map[string]store.RunBrief{}
	for _, b := range briefs {
		if b.CaseID != 0 {
			continue // 基线用例本身不重新沉淀
		}
		if cur, ok := latest[b.IssueKey]; !ok || b.Attempt >= cur.Attempt {
			latest[b.IssueKey] = b
		}
	}
	n := 0
	cases, _ := s.st.Cases(ctx, v.RepoID)
	byIssue := map[string]store.Case{}
	for _, c := range cases {
		byIssue[c.IssueKey] = c
	}
	// 父单用例保留它在本版本里覆盖的子单范围。
	vis, _ := s.st.VersionIssues(ctx, v.ID)
	scopeByIssue := map[string][]string{}
	for _, vi := range vis {
		if len(vi.Scope) > 0 {
			scopeByIssue[vi.IssueKey] = vi.Scope
		}
	}
	for key, b := range latest {
		// 没有未通过即可沉淀：部分无法验证的条目通常是环境所致，仍然值得长期回归。
		if b.Status != store.StatusCompleted || (b.Verdict != protocol.VerdictPass && b.Verdict != protocol.VerdictPartial) {
			continue
		}
		run, err := s.st.Run(ctx, b.ID)
		if err != nil {
			continue
		}
		plan, _ := s.st.RunPlan(ctx, run.ID)
		c := store.Case{RepoID: v.RepoID, IssueKey: key, Issue: run.Issue, Plan: plan, SourceRunID: run.ID, SourceVersionID: v.ID,
			Scope: scopeByIssue[key]}
		if prev, ok := byIssue[key]; ok {
			c.Level = prev.Level // 已有用例保留级别
		}
		action := "沉淀自版本 " + v.Name
		if _, ok := byIssue[key]; ok {
			action = "更新（版本 " + v.Name + "）"
		}
		if _, err := s.st.UpsertCase(ctx, c, action, "回归通过，快照与计划取自测试 "+run.ID); err == nil {
			n++
		}
	}
	return n
}
