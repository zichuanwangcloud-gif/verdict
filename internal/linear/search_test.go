package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake 同时处理 issues(filter:) 和 searchIssues(term:)。
// handler 里不直接调 t.Errorf（那会在响应写到一半时中断连接），问题记在 errs 里，请求结束后再断言。
type fake struct {
	calls    []map[string]any
	errs     []string
	issuesFn func(filter map[string]any) []string
	searchFn func(term string) []string
}

func bad(f *fake, format string, args ...any) { f.errs = append(f.errs, fmt.Sprintf(format, args...)) }

// node 生成一条 issue；state / assignee 为空时返回 null。
func node(id, title, updated, state, assignee string) string {
	a := "null"
	if assignee != "" {
		a = `{"displayName":"` + assignee + `"}`
	}
	st := "null"
	if state != "" {
		st = `{"name":"` + state + `"}`
	}
	return `{"id":"` + id + `","identifier":"` + id + `","title":"` + title + `","url":"u/` + id + `","updatedAt":"` + updated +
		`","state":` + st + `,"team":{"name":"工程","key":"` + strings.Split(id, "-")[0] + `"},"assignee":` + a + `}`
}

func (f *fake) client(t *testing.T) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.calls = append(f.calls, body.Variables)
		var nodes []string
		if strings.Contains(body.Query, "searchIssues") {
			term, _ := body.Variables["term"].(string)
			if f.searchFn != nil {
				nodes = f.searchFn(term)
			}
		} else {
			flt, _ := body.Variables["filter"].(map[string]any)
			if f.issuesFn != nil {
				nodes = f.issuesFn(flt)
			}
		}
		io.WriteString(w, `{"data":{"issues":{"nodes":[`+strings.Join(nodes, ",")+`]},"searchIssues":{"nodes":[`+strings.Join(nodes, ",")+`]}}}`)
	}))
	t.Cleanup(ts.Close)
	c := New("k")
	c.Endpoint = ts.URL
	return c
}

func ids(list []IssueSummary) []string {
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.Identifier
	}
	return out
}

func TestSearchEmpty(t *testing.T) {
	f := &fake{}
	f.issuesFn = func(flt map[string]any) []string {
		if flt != nil {
			bad(f, "空关键字不应带 filter: %v", flt)
		}
		return []string{node("ENG-142", "购物车", "2026-11-15", "In Progress", "王小明"), node("ENG-120", "登录", "2026-11-10", "Done", "")}
	}
	list, err := f.client(t).SearchIssues(context.Background(), "  ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.errs) > 0 {
		t.Fatal(f.errs)
	}
	if len(list) != 2 || list[0].Identifier != "ENG-142" || list[0].Assignee != "王小明" || list[0].Team != "工程" || list[0].State != "In Progress" {
		t.Fatalf("解析或排序不对: %+v", list)
	}
}

func TestSearchByIdentifier(t *testing.T) {
	f := &fake{}
	f.issuesFn = func(filter map[string]any) []string {
		// filter = {and: [{team:…}, {or: [区间…]}]}
		and, ok := filter["and"].([]any)
		if !ok || len(and) != 2 {
			bad(f, "应同时带团队和编号区间: %v", filter)
			return nil
		}
		if team := and[0].(map[string]any)["team"].(map[string]any)["key"].(map[string]any)["eq"]; team != "ENG" {
			bad(f, "团队 key 应为大写 ENG，实际 %v", team)
		}
		ranges := and[1].(map[string]any)["or"].([]any)
		first := ranges[0].(map[string]any)["number"].(map[string]any)
		if fmt.Sprint(first["gte"], "-", first["lte"]) != "1-1" {
			bad(f, "首段区间应为 1..1，实际 %v", first)
		}
		if len(ranges) < 3 {
			bad(f, "前缀区间应覆盖 1、10-19、100-199…，实际 %d 段", len(ranges))
		}
		return []string{node("ENG-142", "购物车", "2026-11-15", "", ""), node("ENG-138", "乘法", "2026-11-14", "", ""), node("ENG-99", "别的", "2026-11-13", "", "")}
	}
	list, err := f.client(t).SearchIssues(context.Background(), "eng-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.errs) > 0 {
		t.Fatal(f.errs)
	}
	if got := ids(list); len(got) != 3 || got[2] != "ENG-99" {
		t.Fatalf("以关键字开头的编号应排前面: %v", got)
	}
}

func TestSearchByIdentifierExactFirst(t *testing.T) {
	f := &fake{issuesFn: func(map[string]any) []string {
		return []string{node("ENG-1420", "更新", "2026-11-16", "", ""), node("ENG-142", "目标", "2026-11-01", "", "")}
	}}
	list, err := f.client(t).SearchIssues(context.Background(), "ENG-142", 10)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Identifier != "ENG-142" {
		t.Fatalf("编号完全一致应排最前: %v", ids(list))
	}
}

func TestSearchText(t *testing.T) {
	f := &fake{}
	f.issuesFn = func(filter map[string]any) []string {
		tq := filter["title"].(map[string]any)["containsIgnoreCase"]
		if tq != "登录" {
			bad(f, "标题过滤参数不对: %v", tq)
		}
		return []string{node("ENG-120", "登录跳转修复", "2026-11-10", "", "")}
	}
	f.searchFn = func(string) []string {
		return []string{node("ENG-88", "登录优化", "2026-11-12", "", ""), node("ENG-120", "登录跳转修复", "2026-11-10", "", "")}
	}
	list, err := f.client(t).SearchIssues(context.Background(), "登录", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.errs) > 0 {
		t.Fatal(f.errs)
	}
	// 两种查询的结果按标识符合并，再按最近更新排序
	if got := ids(list); len(got) != 2 || got[0] != "ENG-88" {
		t.Fatalf("文本搜索结果应按最近更新排序: %v", got)
	}
}

func TestSearchFallsBackToFullText(t *testing.T) {
	f := &fake{}
	f.issuesFn = func(map[string]any) []string { return nil }
	f.searchFn = func(string) []string {
		return []string{node("OPS-7", "描述里提到关键词", "2026-11-01", "", "")}
	}
	list, err := f.client(t).SearchIssues(context.Background(), "关键词", 10)
	if err != nil || len(list) != 1 || list[0].Identifier != "OPS-7" {
		t.Fatalf("标题没命中时应退回全文搜索: %v %v", err, ids(list))
	}
}

func TestSearchUnknownTeamFallsBack(t *testing.T) {
	f := &fake{}
	f.issuesFn = func(map[string]any) []string { return nil }
	f.searchFn = func(term string) []string {
		if term != "XYZ-1" {
			bad(f, "全文搜索关键字应为原始输入，实际 %q", term)
		}
		return []string{node("XYZ-1", "标题里提到编号", "2026-11-01", "", "")}
	}
	list, err := f.client(t).SearchIssues(context.Background(), "XYZ-1", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("团队查不到时应退回全文搜索: %v %v", err, ids(list))
	}
	if len(f.errs) > 0 {
		t.Fatal(f.errs)
	}
}

func TestSearchLimit(t *testing.T) {
	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, node(fmt.Sprintf("ENG-%02d", i), "x", "2026-11-01", "", ""))
	}
	f := &fake{issuesFn: func(map[string]any) []string { return many }}
	if list, err := f.client(t).SearchIssues(context.Background(), "", 5); err != nil || len(list) != 5 {
		t.Fatalf("应按 limit 截断: %v %d", err, len(list))
	}
	if list, err := f.client(t).SearchIssues(context.Background(), "", 0); err != nil || len(list) != 30 {
		t.Fatalf("limit 为 0 时应返回全部（不超过上限）: %v %d", err, len(list))
	}
}
