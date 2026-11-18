package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchIssues(t *testing.T) {
	var gotVars map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotVars = body.Variables
		io.WriteString(w, `{"data":{"issues":{"nodes":[
{"id":"1","identifier":"ENG-101","title":"退款","url":"u1","updatedAt":"2026-11-10","state":{"name":"Todo"},"team":{"name":"工程"},"assignee":null},
{"id":"2","identifier":"ENG-12","title":"登录","url":"u2","updatedAt":"2026-11-12","state":{"name":"Done"},"team":{"name":"工程"},"assignee":{"displayName":"张三"}}
]}}}`)
	}))
	defer ts.Close()
	c := New("k")
	c.Endpoint = ts.URL

	list, err := c.SearchIssues(context.Background(), "ENG-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应返回 2 条: %+v", list)
	}
	// 两条都是 ENG-1 前缀匹配，保持接口返回顺序；同时验证字段解析
	if list[0].Identifier != "ENG-101" || list[1].Identifier != "ENG-12" || list[1].Assignee != "张三" || list[1].State != "Done" || list[0].State != "Todo" {
		t.Fatalf("排序或解析不对: %+v", list)
	}

	if gotVars["first"] != float64(10) || gotVars["filter"] == nil {
		t.Fatalf("查询变量不对: %v", gotVars)
	}
	f, _ := json.Marshal(gotVars["filter"])
	if string(f) != `{"or":[{"identifier":{"containsIgnoreCase":"ENG-1"}},{"title":{"containsIgnoreCase":"ENG-1"}}]}` {
		t.Fatalf("应同时按编号和标题匹配: %s", f)
	}

	// 空关键字：不传 filter，返回最近更新
	if _, err := c.SearchIssues(context.Background(), "  ", 0); err != nil {
		t.Fatal(err)
	}
	if _, has := gotVars["filter"]; has {
		t.Fatal("空关键字不应加 filter")
	}
	if gotVars["first"] != float64(20) {
		t.Fatalf("默认 limit 应为 20: %v", gotVars["first"])
	}
	// 编号完全相同的最前，即使接口把它排在后面
	list, _ = c.SearchIssues(context.Background(), "ENG-12", 10)
	if list[0].Identifier != "ENG-12" {
		t.Fatalf("完全匹配应排最前: %+v", list)
	}
}
