package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// fakeIssueServer 按 variables.id 返回预置的 issue 节点。
func fakeIssueServer(t *testing.T, issues map[string]string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		key, _ := body.Variables["id"].(string)
		node, found := issues[key]
		if !found {
			io.WriteString(w, `{"data":{"issue":null}}`)
			return
		}
		io.WriteString(w, `{"data":{"issue":`+node+`}}`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func issueJSON(key, title, parent string, children []string) string {
	p := "null"
	if parent != "" {
		p = fmt.Sprintf(`{"id":"id-%s","identifier":%q,"title":"父单"}`, parent, parent)
	}
	ch := "[]"
	if len(children) > 0 {
		parts := make([]string, 0, len(children))
		for _, c := range children {
			parts = append(parts, fmt.Sprintf(`{"id":"id-%s","identifier":%q,"title":"子单 %s","state":{"name":"Todo","type":"unstarted"}}`, c, c, c))
		}
		ch = "[" + strings.Join(parts, ",") + "]"
	}
	return fmt.Sprintf(`{"id":"id-%s","identifier":%q,"title":%q,"description":"需求正文","url":"https://linear.app/%s",
"parent":%s,"children":{"nodes":%s},"comments":{"nodes":[]}}`, key, key, title, key, p, ch)
}

type treeFixture struct {
	st     *store.Store
	ts     *httptest.Server
	repoID int64
	rtID   int64
}

func newTreeFixture(t *testing.T) *treeFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "tree.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lin := fakeIssueServer(t, map[string]string{
		"ENG-1": issueJSON("ENG-1", "父单", "", []string{"ENG-2", "ENG-3"}),
		"ENG-2": issueJSON("ENG-2", "子一", "ENG-1", nil),
		"ENG-3": issueJSON("ENG-3", "子二", "ENG-1", nil),
		"ENG-9": issueJSON("ENG-9", "无关单", "", nil),
	})
	if err := st.SetSetting(ctx, settingLinearKey, "lin_test"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERDICT_LINEAR_ENDPOINT", lin.URL)
	rt, _, err := st.CreateRuntime(ctx, "rt")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.CreateRepo(ctx, store.Repo{URL: "git@x:o/r.git", Name: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{AdminToken: "t"}, st)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &treeFixture{st: st, ts: ts, repoID: repo.ID, rtID: rt.ID}
}

func (f *treeFixture) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(buf)
}

func (f *treeFixture) create(t *testing.T, issueKey string, scope []string) (int, string) {
	t.Helper()
	scopeJSON, _ := json.Marshal(scope)
	body := fmt.Sprintf(`{"issue_key":%q,"repo_id":%d,"branch":"main","runtime_id":%d,"harness":"claude","child_scope":%s}`,
		issueKey, f.repoID, f.rtID, scopeJSON)
	return f.do(t, "POST", "/api/runs", body)
}

func TestCreateParentRunExpandsChildren(t *testing.T) {
	f := newTreeFixture(t)
	code, body := f.create(t, "ENG-1", []string{"ENG-2", "ENG-3"})
	if code != 200 {
		t.Fatalf("建父单测试失败: %d %s", code, body)
	}
	var run store.Run
	if err := json.Unmarshal([]byte(body), &run); err != nil {
		t.Fatal(err)
	}
	if run.SetID == "" || len(run.Scope) != 2 || run.IssueParentKey != "" {
		t.Fatalf("父 run 批次信息不对: %+v", run)
	}

	code, body = f.do(t, "GET", "/api/runs/"+run.ID, "")
	if code != 200 {
		t.Fatalf("读报告失败: %d %s", code, body)
	}
	var detail struct {
		store.Run
		SetSummary *store.IssueSetSummary `json:"set_summary"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	sm := detail.SetSummary
	if sm == nil {
		t.Fatal("父 run 应带 set_summary")
	}
	if sm.Overall != store.SetIncomplete || len(sm.Children) != 2 || len(sm.Uncovered) != 0 {
		t.Fatalf("聚合不对: %+v", sm)
	}
	for _, c := range detail.Issue.Children {
		if c.Description != "需求正文" {
			t.Fatalf("父单快照应带子单描述，父单计划才能理解子单改了什么: %+v", c)
		}
	}
	for _, c := range sm.Children {
		if c.RunID == "" {
			t.Fatalf("子单应已建出测试: %+v", c)
		}
		child, err := f.st.Run(context.Background(), c.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if child.IssueParentKey != "ENG-1" || child.SetID != run.SetID || child.Issue.Identifier != c.IssueKey {
			t.Fatalf("子 run 关联不对: %+v", child)
		}
		_, childBody := f.do(t, "GET", "/api/runs/"+child.ID, "")
		var childDetail struct {
			SetParentRunID string `json:"set_parent_run_id"`
		}
		if err := json.Unmarshal([]byte(childBody), &childDetail); err != nil {
			t.Fatal(err)
		}
		if childDetail.SetParentRunID != run.ID {
			t.Fatalf("子 run 应能跳回父 run: %+v", childDetail)
		}
	}
}

func TestCreateParentRunSubset(t *testing.T) {
	f := newTreeFixture(t)
	code, body := f.create(t, "ENG-1", []string{"ENG-2"})
	if code != 200 {
		t.Fatalf("建父单测试失败: %d %s", code, body)
	}
	var run store.Run
	json.Unmarshal([]byte(body), &run)
	_, body = f.do(t, "GET", "/api/runs/"+run.ID, "")
	var detail struct {
		SetSummary *store.IssueSetSummary `json:"set_summary"`
	}
	json.Unmarshal([]byte(body), &detail)
	if detail.SetSummary == nil || len(detail.SetSummary.Uncovered) != 1 ||
		detail.SetSummary.Uncovered[0].Identifier != "ENG-3" {
		t.Fatalf("未覆盖子单不对: %+v", detail.SetSummary)
	}
}

func TestCreateParentRunRejectsForeignChild(t *testing.T) {
	f := newTreeFixture(t)
	code, body := f.create(t, "ENG-1", []string{"ENG-9"})
	if code != 400 || !strings.Contains(body, "不是 ENG-1 的子单") {
		t.Fatalf("非子单应被拒绝: %d %s", code, body)
	}
}

func newServerWithLinear(t *testing.T, h http.HandlerFunc) (*store.Store, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetSetting(ctx, settingLinearKey, "lin_test"); err != nil {
		t.Fatal(err)
	}
	lin := httptest.NewServer(h)
	t.Cleanup(lin.Close)
	t.Setenv("VERDICT_LINEAR_ENDPOINT", lin.URL)
	s := New(Config{AdminToken: "t"}, st)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return st, ts
}

func TestBoardReturnsParentFields(t *testing.T) {
	st, ts := newServerWithLinear(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Query, "viewer") {
			io.WriteString(w, `{"data":{"viewer":{"id":"u-me"}}}`)
			return
		}
		io.WriteString(w, `{"data":{"issues":{"nodes":[
{"id":"id-ENG-3","identifier":"ENG-3","title":"子单","url":"u3","updatedAt":"2026-01-02","priority":1,
 "state":{"name":"Todo","type":"unstarted","color":"#999"},"team":{"name":"工程","key":"ENG"},"assignee":null,
 "labels":{"nodes":[]},"parent":{"id":"id-ENG-2","identifier":"ENG-2","title":"父单"},"children":{"nodes":[]}},
{"id":"id-ENG-2","identifier":"ENG-2","title":"父单","url":"u2","updatedAt":"2026-01-01","priority":2,
 "state":{"name":"Todo","type":"unstarted","color":"#999"},"team":{"name":"工程","key":"ENG"},"assignee":null,
 "labels":{"nodes":[]},"parent":null,
 "children":{"nodes":[{"id":"id-ENG-3","identifier":"ENG-3","title":"子单","state":{"name":"Todo","type":"unstarted"}}]}}
]}}}`)
	})

	// 给子单造一条已完成的测试，看板应在 children_runs 里带回来。
	ctx := context.Background()
	rt, _, err := st.CreateRuntime(ctx, "rt")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.CreateRepo(ctx, store.Repo{URL: "git@x:o/r.git", Name: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testplan.Preset("C")
	cfg.Harness = "claude"
	run, err := st.CreateRun(ctx, store.NewRun{RepoID: repo.ID, Branch: "main",
		Issue: linear.Issue{Identifier: "ENG-3"}, RuntimeID: rt.ID, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetStatus(ctx, run.ID, store.StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVerdict(ctx, run.ID, protocol.VerdictPass); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/board/issues?team=ENG", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("看板请求失败: %d %s", resp.StatusCode, buf)
	}
	var out struct {
		Issues []struct {
			Identifier       string `json:"identifier"`
			ParentIdentifier string `json:"parent_identifier"`
			ParentTitle      string `json:"parent_title"`
			ChildCount       int    `json:"child_count"`
			Children         []struct {
				Identifier string `json:"identifier"`
			} `json:"children"`
		} `json:"issues"`
		ChildrenRuns map[string]struct {
			RunID   string `json:"run_id"`
			Verdict string `json:"verdict"`
		} `json:"children_runs"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatal(err)
	}
	byKey := map[string]int{}
	for i, it := range out.Issues {
		byKey[it.Identifier] = i
	}
	pi, ok := byKey["ENG-2"]
	if !ok || out.Issues[pi].ChildCount != 1 || len(out.Issues[pi].Children) != 1 ||
		out.Issues[pi].Children[0].Identifier != "ENG-3" {
		t.Fatalf("父单行的子单信息不对: %+v", out.Issues)
	}
	ci, ok := byKey["ENG-3"]
	if !ok || out.Issues[ci].ParentIdentifier != "ENG-2" || out.Issues[ci].ParentTitle != "父单" {
		t.Fatalf("子单行的父单信息不对: %+v", out.Issues)
	}
	cr, ok := out.ChildrenRuns["ENG-3"]
	if !ok || cr.RunID != run.ID || cr.Verdict != protocol.VerdictPass {
		t.Fatalf("子单最近一次测试没带回来: %+v", out.ChildrenRuns)
	}
}

func TestAddVersionIssueWithChildren(t *testing.T) {
	f := newTreeFixture(t)
	ctx := context.Background()
	v, err := f.st.CreateVersion(ctx, f.repoID, "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/versions/%d/issues", v.ID)
	code, body := f.do(t, "POST", path, `{"issue_key":"ENG-1","child_scope":["ENG-2"]}`)
	if code != 200 {
		t.Fatalf("加入父单失败: %d %s", code, body)
	}
	vis, err := f.st.VersionIssues(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string][]string{}
	for _, vi := range vis {
		scope[vi.IssueKey] = vi.Scope
	}
	if len(vis) != 2 || len(scope["ENG-1"]) != 1 || scope["ENG-1"][0] != "ENG-2" {
		t.Fatalf("父单和选中子单都应成为成员: %+v", scope)
	}

	// 非该父单的子单应被拒绝。
	code, body = f.do(t, "POST", path, `{"issue_key":"ENG-1","child_scope":["ENG-9"]}`)
	if code != 400 || !strings.Contains(body, "不是 ENG-1 的子单") {
		t.Fatalf("非子单应被拒绝: %d %s", code, body)
	}
}
