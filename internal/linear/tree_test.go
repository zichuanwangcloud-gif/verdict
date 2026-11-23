package linear

import (
	"context"
	"strings"
	"testing"
)

func TestGetIssueTree(t *testing.T) {
	c := fakeLinear(t, func(q string, v map[string]any) string {
		if !strings.Contains(q, "parent { id identifier title }") || !strings.Contains(q, "children(first: 100)") {
			t.Errorf("GetIssue 查询应包含父子字段:\n%s", q)
		}
		return `{"data":{"issue":{
"id":"uuid-2","identifier":"ENG-2","title":"父单","description":"D","url":"https://linear.app/x",
"parent":null,
"children":{"nodes":[
  {"id":"uuid-3","identifier":"ENG-3","title":"子单一","state":{"name":"In Progress","type":"started"}},
  {"id":"uuid-4","identifier":"ENG-4","title":"子单二","state":null}
]},
"comments":{"nodes":[]}}}}`
	})
	is, err := c.GetIssue(context.Background(), "ENG-2")
	if err != nil {
		t.Fatal(err)
	}
	if is.Parent != nil {
		t.Fatalf("父单为空时不应解析出 parent: %+v", is.Parent)
	}
	if len(is.Children) != 2 || is.Children[0].Identifier != "ENG-3" || is.Children[0].StateType != "started" ||
		is.Children[0].State != "In Progress" || is.Children[1].State != "" {
		t.Fatalf("子单解析不对: %+v", is.Children)
	}
}

func TestGetIssueParent(t *testing.T) {
	c := fakeLinear(t, func(string, map[string]any) string {
		return `{"data":{"issue":{"id":"uuid-3","identifier":"ENG-3","title":"子单","description":"D","url":"u",
"parent":{"id":"uuid-2","identifier":"ENG-2","title":"父单"},
"children":{"nodes":[]},"comments":{"nodes":[]}}}}`
	})
	is, err := c.GetIssue(context.Background(), "ENG-3")
	if err != nil {
		t.Fatal(err)
	}
	if is.Parent == nil || is.Parent.ID != "uuid-2" || is.Parent.Identifier != "ENG-2" || is.Parent.Title != "父单" {
		t.Fatalf("父单解析不对: %+v", is.Parent)
	}
	if len(is.Children) != 0 {
		t.Fatalf("不应有子单: %+v", is.Children)
	}
}

// 列表结果也要带上父子关系，看板才能分层展示。
func TestSearchIssuesCarriesTree(t *testing.T) {
	f := &fake{}
	f.issuesFn = func(map[string]any) []string {
		return []string{`{"id":"uuid-2","identifier":"ENG-2","title":"父单","url":"u/ENG-2","updatedAt":"2026-01-01T00:00:00Z",
"state":{"name":"Todo"},"team":{"name":"工程","key":"ENG"},"assignee":null,"parent":null,
"children":{"nodes":[{"id":"uuid-3","identifier":"ENG-3","title":"子单","state":{"name":"Done","type":"completed"}}]}}`}
	}
	c := f.client(t)
	list, err := c.SearchIssues(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.errs) > 0 {
		t.Fatal(f.errs)
	}
	if len(list) != 1 || list[0].Parent != nil || len(list[0].Children) != 1 ||
		list[0].Children[0].StateType != "completed" {
		t.Fatalf("列表未带上父子关系: %+v", list)
	}
}

func TestBoardIssuesCarriesTree(t *testing.T) {
	c := fakeLinear(t, func(q string, v map[string]any) string {
		if strings.Contains(q, "issues(") && !strings.Contains(q, "parent { id identifier title }") {
			t.Errorf("BoardIssues 查询应包含父单字段:\n%s", q)
		}
		return `{"data":{"issues":{"nodes":[
{"id":"uuid-3","identifier":"ENG-3","title":"子单","url":"u","updatedAt":"2026-01-01","priority":2,
 "state":{"name":"In Progress","type":"started","color":"#f00"},"team":{"name":"工程","key":"ENG"},
 "assignee":null,"labels":{"nodes":[]},
 "parent":{"id":"uuid-2","identifier":"ENG-2","title":"父单"},
 "children":{"nodes":[]}}
]}}}`
	})
	list, err := c.BoardIssues(context.Background(), BoardQuery{TeamKey: "ENG"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Parent == nil || list[0].Parent.Identifier != "ENG-2" || list[0].Parent.Title != "父单" {
		t.Fatalf("看板未带上父单: %+v", list)
	}
}
