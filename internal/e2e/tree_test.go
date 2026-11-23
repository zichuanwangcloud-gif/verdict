package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"verdict/internal/server"
	"verdict/internal/server/store"
)

// treeLinear 模拟 Linear 的父子 issue：GetIssue 返回固定树，评论正文记下来供断言。
type treeLinear struct {
	mu       sync.Mutex
	comments []string
}

var treeNodes = map[string]string{
	"ENG-1": `{"id":"uuid-ENG-1","identifier":"ENG-1","title":"父单","description":"## 验收标准\n1. 首页可访问\n2. 存在 feature.txt",
"url":"https://linear.app/ENG-1","parent":null,
"children":{"nodes":[
  {"id":"uuid-ENG-2","identifier":"ENG-2","title":"子一","state":{"name":"Todo","type":"unstarted"}},
  {"id":"uuid-ENG-3","identifier":"ENG-3","title":"子二","state":{"name":"Todo","type":"unstarted"}}
]},"comments":{"nodes":[]}}`,
	"ENG-2": `{"id":"uuid-ENG-2","identifier":"ENG-2","title":"子一","description":"子一的细节需求","url":"https://linear.app/ENG-2",
"parent":{"id":"uuid-ENG-1","identifier":"ENG-1","title":"父单"},"children":{"nodes":[]},"comments":{"nodes":[]}}`,
	"ENG-3": `{"id":"uuid-ENG-3","identifier":"ENG-3","title":"子二","description":"子二的细节需求","url":"https://linear.app/ENG-3",
"parent":{"id":"uuid-ENG-1","identifier":"ENG-1","title":"父单"},"children":{"nodes":[]},"comments":{"nodes":[]}}`,
}

func startTreeLinear(t *testing.T) *treeLinear {
	t.Helper()
	f := &treeLinear{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		switch {
		case strings.Contains(body.Query, "commentCreate"):
			in, _ := body.Variables["input"].(map[string]any)
			text, _ := in["body"].(string)
			f.mu.Lock()
			f.comments = append(f.comments, text)
			f.mu.Unlock()
			io.WriteString(w, `{"data":{"commentCreate":{"success":true,"comment":{"url":"https://linear.app/c/1"}}}}`)
		default:
			key, _ := body.Variables["id"].(string)
			node, ok := treeNodes[key]
			if !ok {
				io.WriteString(w, `{"data":{"issue":null}}`)
				return
			}
			io.WriteString(w, `{"data":{"issue":`+node+`}}`)
		}
	}))
	t.Cleanup(ts.Close)
	server.LinearEndpoint = ts.URL
	t.Cleanup(func() { server.LinearEndpoint = "" })
	return f
}

// 父单选子集 → 父子各自建测试 → 聚合结论 → 父单回写汇总评论。
func TestParentChildSet(t *testing.T) {
	e := setup(t)
	lin := startTreeLinear(t)
	e.mustCall("PUT", "/api/settings", map[string]string{"linear_api_key": "lin_test"}, nil)

	var parent store.Run
	e.mustCall("POST", "/api/runs", map[string]any{
		"issue_key": "ENG-1", "repo_id": e.repoID, "branch": "feature",
		"runtime_id": e.rtID, "harness": "claude", "tier": "C",
		"child_scope": []string{"ENG-2"},
	}, &parent)
	if parent.SetID == "" || len(parent.Scope) != 1 || parent.IssueParentKey != "" {
		t.Fatalf("父 run 批次信息不对: %+v", parent)
	}

	var detail struct {
		store.Run
		SetSummary *store.IssueSetSummary `json:"set_summary"`
	}
	e.waitFor("父子测试结束", func() bool {
		e.mustCall("GET", "/api/runs/"+parent.ID, nil, &detail)
		return detail.SetSummary != nil && detail.SetSummary.Overall != store.SetIncomplete
	})
	sm := detail.SetSummary
	if sm.Overall != store.SetPartial {
		// C 档的 AC-2 会留在无法验证，父单结论是部分无法验证；不能出现未通过。
		t.Fatalf("父单整体结论不对: %+v", sm)
	}
	if len(sm.Children) != 1 || sm.Children[0].IssueKey != "ENG-2" || sm.Children[0].RunID == "" {
		t.Fatalf("覆盖子单不对: %+v", sm.Children)
	}
	if len(sm.Uncovered) != 1 || sm.Uncovered[0].Identifier != "ENG-3" {
		t.Fatalf("未覆盖子单不对: %+v", sm.Uncovered)
	}

	var child struct {
		store.Run
		SetParentRunID string `json:"set_parent_run_id"`
	}
	e.mustCall("GET", "/api/runs/"+sm.Children[0].RunID, nil, &child)
	if child.SetParentRunID != parent.ID || child.IssueParentKey != "ENG-1" || child.SetID != parent.SetID {
		t.Fatalf("子 run 关联不对: %+v", child)
	}

	// 父单的汇总评论：包含整体结论、选中的子单和未覆盖列表。
	e.waitFor("父单回写汇总评论", func() bool {
		lin.mu.Lock()
		defer lin.mu.Unlock()
		for _, c := range lin.comments {
			if strings.Contains(c, "子单汇总") && strings.Contains(c, "ENG-2") && strings.Contains(c, "未覆盖：ENG-3") {
				return true
			}
		}
		return false
	})
}
