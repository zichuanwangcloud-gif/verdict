package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"verdict/internal/protocol"
	"verdict/internal/server"
	"verdict/internal/server/store"
)

// fakeLinearAPI 模拟 Linear：GetIssue 返回固定内容，CreateComment 计数。
type fakeLinearAPI struct {
	mu       sync.Mutex
	comments int
	issues   map[string]string // key → title
}

func startLinear(t *testing.T) *fakeLinearAPI {
	f := &fakeLinearAPI{issues: map[string]string{"ENG-1": "登录功能", "ENG-2": "退款"}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		switch {
		case strings.Contains(body.Query, "commentCreate"):
			f.mu.Lock()
			f.comments++
			f.mu.Unlock()
			io.WriteString(w, `{"data":{"commentCreate":{"success":true,"comment":{"url":"https://linear.app/c/1"}}}}`)
		default:
			key, _ := body.Variables["id"].(string)
			title, ok := f.issues[key]
			if !ok {
				io.WriteString(w, `{"data":{"issue":null}}`)
				return
			}
			desc := "## 验收标准\n1. 首页可访问\n2. 存在 feature.txt"
			io.WriteString(w, `{"data":{"issue":{"id":"uuid-`+key+`","identifier":"`+key+`","title":"`+title+
				`","description":"`+strings.ReplaceAll(desc, "\n", "\\n")+`","url":"https://linear.app/`+key+
				`","comments":{"nodes":[]}}}}`)
		}
	}))
	t.Cleanup(ts.Close)
	server.LinearEndpoint = ts.URL
	t.Cleanup(func() { server.LinearEndpoint = "" })
	return f
}

func (e *env) mustCall(method, path string, body, out any) {
	e.t.Helper()
	if code := e.call(method, path, body, out); code != 200 && code != 201 {
		e.t.Fatalf("%s %s 返回 %d", method, path, code)
	}
}

func TestVersionFreezeRegression(t *testing.T) {
	e := setup(t)
	lin := startLinear(t)
	e.mustCall("PUT", "/api/settings", map[string]string{"linear_api_key": "lin_test"}, nil)

	// 版本与成员。
	var v store.Version
	e.mustCall("POST", "/api/versions", map[string]any{"repo_id": e.repoID, "name": "1.4.0"}, &v)
	e.mustCall("POST", "/api/versions/"+itoa(v.ID)+"/issues", map[string]any{"issue_key": "ENG-1"}, nil)
	e.mustCall("POST", "/api/versions/"+itoa(v.ID)+"/issues", map[string]any{"issue_key": "ENG-2"}, nil)

	// 冻结到 feature 分支：先记下它当前的 commit。
	shas := strings.Fields(run(t, e.repo, "rev-list", "--max-count", "1", "feature"))
	if len(shas) != 1 {
		t.Fatal("取不到 feature 的 commit")
	}
	var g store.Regression
	e.mustCall("POST", "/api/versions/"+itoa(v.ID)+"/regressions", map[string]any{
		"ref": "feature", "baseline": "none", "issue_tier": "C", "case_tier": "C", "runtime_ids": []int64{e.rtID},
	}, &g)
	if g.FreezeRef != "feature" || g.FreezeFullRef != "refs/heads/feature" || g.FreezeSHA != shas[0] || g.Status != store.RegressionRunning {
		t.Fatalf("冻结结果不对: %+v", g)
	}

	// 等两轮子测试全部结束、回归收尾。
	var detail versionDetailResp
	e.waitFor("回归结束", func() bool {
		e.mustCall("GET", "/api/versions/"+itoa(v.ID), nil, &detail)
		return len(detail.Regressions) == 1 && detail.Regressions[0].Status != store.RegressionRunning
	})
	if detail.Version.Status != store.VersionFrozen {
		t.Fatalf("冻结回归后版本应为 frozen: %s", detail.Version.Status)
	}
	if detail.Regressions[0].Verdict != protocol.VerdictPartial {
		t.Fatalf("C 档完成、含 unverifiable 的结论应为 partial: %+v", detail.Regressions[0])
	}
	// 矩阵：每个成员一行，子测试锁定在冻结 commit 上。
	if len(detail.Rows) != 2 {
		t.Fatalf("矩阵应有 2 行: %+v", detail.Rows)
	}
	var runIDs []string
	for _, row := range detail.Rows {
		if row.Branch != nil {
			t.Fatalf("没测过分支，分支列应为空: %+v", row)
		}
		if len(row.Cells) != 1 || row.Cells[0] == nil {
			t.Fatalf("应有第 1 轮的格子: %+v", row)
		}
		if row.Cells[0].Commit != shas[0] {
			t.Fatalf("子测试应锁定在 %s，实际 %s", shas[0], row.Cells[0].Commit)
		}
		runIDs = append(runIDs, row.Cells[0].RunID)
	}
	for _, id := range runIDs {
		r := e.get(id)
		if r.Status != store.StatusCompleted || r.CommitSHA != shas[0] {
			t.Fatalf("子测试应完成并锁定 commit: %+v", r)
		}
		// 计划复用：C 档正常会有 plan 阶段，但这里的计划来自……没有来源测试时仍有 plan 阶段。
		if r.VersionID != v.ID || r.RegressionID != g.ID {
			t.Fatalf("子测试归属不对: %+v", r)
		}
	}
	// 回归子测试默认不回写 Linear。
	lin.mu.Lock()
	comments := lin.comments
	lin.mu.Unlock()
	if comments != 0 {
		t.Fatalf("回归子测试不应默认回写 Linear，实际写了 %d 次", comments)
	}
	// 子测试不出现在普通测试列表里。
	var list []map[string]any
	e.mustCall("GET", "/api/runs", nil, &list)
	if len(list) != 0 {
		t.Fatalf("普通测试列表不应包含回归子测试: %+v", list)
	}

	// 结论不是「通过」时发布必须写理由。
	var msg map[string]string
	if code := e.call("POST", "/api/versions/"+itoa(v.ID)+"/release", map[string]any{"note": ""}, &msg); code != 409 {
		t.Fatalf("没有理由应拒绝发布，实际 %d %v", code, msg)
	}
	var rel struct {
		CasesAdded int `json:"cases_added"`
	}
	e.mustCall("POST", "/api/versions/"+itoa(v.ID)+"/release", map[string]any{"note": "ENG-2 下个版本再补"}, &rel)
	var after store.Version
	e.mustCall("GET", "/api/versions/"+itoa(v.ID), nil, &detail)
	after = detail.Version
	if after.Status != store.VersionReleased || after.ReleaseNote == "" {
		t.Fatalf("应已发布并记录理由: %+v", after)
	}
	// partial 通过的成员也会沉淀用例。
	var cases []map[string]any
	e.mustCall("GET", "/api/repos/"+itoa(e.repoID)+"/cases", nil, &cases)
	if len(cases) != 2 {
		t.Fatalf("应沉淀 2 条用例: %+v", cases)
	}
}

type versionDetailResp struct {
	Version     store.Version       `json:"version"`
	Regressions []regressionRowResp `json:"regressions"`
	Rows        []matrixRowResp     `json:"rows"`
}

type regressionRowResp struct {
	store.Regression
	Passed int `json:"passed"`
}

type matrixRowResp struct {
	Key    string            `json:"key"`
	Kind   string            `json:"kind"`
	Branch *matrixCellResp   `json:"branch"`
	Cells  []*matrixCellResp `json:"cells"`
}

type matrixCellResp struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	Verdict string `json:"verdict"`
	Commit  string `json:"commit_sha"`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestRegressionUsesBaselinePlan 验证基线用例回归时复用沉淀的计划、不重新规划。
func TestRegressionUsesBaselinePlan(t *testing.T) {
	e := setup(t)
	startLinear(t)
	e.mustCall("PUT", "/api/settings", map[string]string{"linear_api_key": "lin_test"}, nil)

	// 先测一次分支上的 issue（手动输入不行，基线要求稳定编号）。
	var r detail
	e.mustCall("POST", "/api/runs", map[string]any{"issue_key": "ENG-2", "repo_id": e.repoID, "branch": "feature",
		"runtime_id": e.rtID, "harness": "claude", "tier": "C"}, &r)
	r = e.waitDone(r.ID)
	if r.Status != store.StatusCompleted || r.Verdict != protocol.VerdictPartial {
		t.Fatalf("分支测试应完成: %s %s", r.Status, r.Error)
	}
	var c store.Case
	e.mustCall("POST", "/api/repos/"+itoa(e.repoID)+"/cases", map[string]any{"run_id": r.ID, "level": "core"}, &c)
	if c.Level != store.CaseCore || c.Plan == nil {
		t.Fatalf("用例应带计划: %+v", c)
	}
	srcPlanRounds := 0
	for _, st := range r.Stages {
		if st.Role == "plan" {
			srcPlanRounds++
		}
	}

	var v store.Version
	e.mustCall("POST", "/api/versions", map[string]any{"repo_id": e.repoID, "name": "2.0.0"}, &v)
	// 手动输入的需求不是成员，只靠基线。
	var g store.Regression
	e.mustCall("POST", "/api/versions/"+itoa(v.ID)+"/regressions", map[string]any{
		"ref": "feature", "baseline": "core", "issue_tier": "C", "case_tier": "C", "runtime_ids": []int64{e.rtID},
	}, &g)

	var detail versionDetailResp
	e.waitFor("回归结束", func() bool {
		e.mustCall("GET", "/api/versions/"+itoa(v.ID), nil, &detail)
		return len(detail.Regressions) == 1 && detail.Regressions[0].Status != store.RegressionRunning
	})
	if len(detail.Rows) != 1 || detail.Rows[0].Kind != "case" || detail.Rows[0].Key != "ENG-2" {
		t.Fatalf("矩阵应只有一条基线用例: %+v", detail.Rows)
	}
	cell := detail.Rows[0].Cells[0]
	sub := e.get(cell.RunID)
	if sub.PlanSource != r.ID || sub.CaseID != c.ID {
		t.Fatalf("子测试应复用来源测试的计划: %+v", sub)
	}
	plans := 0
	for _, st := range sub.Stages {
		if st.Role == "plan" {
			plans++
			if st.Note == "" || !strings.Contains(st.Note, "复用") {
				t.Fatalf("计划阶段应标记为复用: %+v", st)
			}
		}
	}
	if plans != 1 {
		t.Fatalf("应恰好有一个（复用的）计划阶段: %d", plans)
	}
	_ = srcPlanRounds
	// 基线用例在冻结节点上通过 → 版本结论为 partial（C 档 AC-2 无法验证）。
	if detail.Regressions[0].Verdict != protocol.VerdictPartial {
		t.Fatalf("结论不对: %+v", detail.Regressions[0])
	}
}
