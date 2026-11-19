package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"verdict/internal/linear"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

func linearIssue(key string) linear.Issue {
	return linear.Issue{Identifier: key, Title: "需求 " + key, Description: "## 验收标准\n1. 一条"}
}

func testCfg() testplan.Config {
	return testplan.Preset(testplan.TierB).Normalize()
}

// truthEnv 准备一个带一条已完成测试的环境，方便测真值接口。
func truthEnv(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Config{AdminToken: "t"}, st)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	ctx := t.Context()
	repo, err := st.CreateRepo(ctx, store.Repo{URL: "git@x:o/r.git", Name: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	rt, _, err := st.CreateRuntime(ctx, "机器")
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(ctx, store.NewRun{RepoID: repo.ID, Branch: "feat", RuntimeID: rt.ID,
		Issue: linearIssue("ENG-1"), Config: testCfg()})
	if err != nil {
		t.Fatal(err)
	}
	return ts, st, run.ID
}

func do(t *testing.T, ts *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

func TestTruthLifecycle(t *testing.T) {
	ts, _, runID := truthEnv(t)

	if code, _ := do(t, ts, "GET", "/api/runs/"+runID+"/truth", ""); code != 404 {
		t.Fatalf("没标记过应 404，实际 %d", code)
	}
	// 判未通过必须给说明或单子。
	if code, body := do(t, ts, "PUT", "/api/runs/"+runID+"/truth", `{"verdict":"fail"}`); code != 400 {
		t.Fatalf("未通过但没有说明应 400，实际 %d %s", code, body)
	}
	if code, body := do(t, ts, "PUT", "/api/runs/"+runID+"/truth", `{"verdict":"maybe"}`); code != 400 {
		t.Fatalf("非法结论应 400，实际 %d %s", code, body)
	}

	code, body := do(t, ts, "PUT", "/api/runs/"+runID+"/truth",
		`{"verdict":"fail","source":"ticket","note":"人工发现合计算错","ticket":"步骤 1 …","ticket_ref":"BUG-1"}`)
	if code != 200 {
		t.Fatalf("标记失败: %d %s", code, body)
	}
	var got store.Truth
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fail" || got.Source != "ticket" || got.TicketRef != "BUG-1" {
		t.Fatalf("返回不对: %+v", got)
	}

	// 重复标记就地覆盖。
	code, body = do(t, ts, "PUT", "/api/runs/"+runID+"/truth", `{"verdict":"pass"}`)
	if code != 200 {
		t.Fatalf("改为通过失败: %d %s", code, body)
	}
	var again store.Truth
	json.Unmarshal([]byte(body), &again)
	if again.Verdict != "pass" || again.Ticket != "" {
		t.Fatalf("覆盖后残留旧值: %+v", again)
	}

	// 列表与详情都带上真值。
	_, body = do(t, ts, "GET", "/api/runs", "")
	if !strings.Contains(body, `"truth":{"run_id":"`+runID+`"`) {
		t.Fatalf("列表没带上真值: %s", body)
	}
	_, body = do(t, ts, "GET", "/api/runs/"+runID, "")
	if !strings.Contains(body, `"truth":{"run_id":"`+runID+`"`) {
		t.Fatalf("详情没带上真值: %s", body)
	}

	if code, _ := do(t, ts, "DELETE", "/api/runs/"+runID+"/truth", ""); code != 200 {
		t.Fatalf("撤销失败: %d", code)
	}
	if code, _ := do(t, ts, "GET", "/api/runs/"+runID+"/truth", ""); code != 404 {
		t.Fatalf("撤销后应 404，实际 %d", code)
	}
}

// 分析型 run 不出现在测试列表里，也不能标真值。
func TestPostmortemRunExcludedFromList(t *testing.T) {
	ts, st, _ := truthEnv(t)
	ctx := t.Context()
	pm, err := st.CreateRun(ctx, store.NewRun{RepoID: 1, Branch: "feat", RuntimeID: 1,
		Issue: linearIssue("ENG-1"), Config: testCfg(), Kind: store.KindPostmortem})
	if err != nil {
		t.Fatal(err)
	}
	_, body := do(t, ts, "GET", "/api/runs", "")
	if strings.Contains(body, pm.ID) {
		t.Fatalf("分析型 run 不应出现在测试列表: %s", body)
	}
	if code, _ := do(t, ts, "PUT", "/api/runs/"+pm.ID+"/truth", `{"verdict":"pass"}`); code != 400 {
		t.Fatalf("分析型 run 不能标真值，实际 %d", code)
	}
}
