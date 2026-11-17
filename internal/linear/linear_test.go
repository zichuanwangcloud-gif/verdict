package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeLinear(t *testing.T, handle func(query string, vars map[string]any) string) *Client {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_test" {
			w.WriteHeader(401)
			io.WriteString(w, `{"errors":[{"message":"Authentication required"}]}`)
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, handle(body.Query, body.Variables))
	}))
	t.Cleanup(ts.Close)
	c := New("lin_test")
	c.Endpoint = ts.URL
	return c
}

func TestGetIssue(t *testing.T) {
	c := fakeLinear(t, func(q string, v map[string]any) string {
		if v["id"] != "ENG-1" {
			t.Errorf("变量不对: %v", v)
		}
		return `{"data":{"issue":{"id":"uuid-1","identifier":"ENG-1","title":"T","description":"D","url":"https://linear.app/x",
"comments":{"nodes":[{"body":"后","createdAt":"2026-01-02T00:00:00Z","user":{"name":"B"}},{"body":"先","createdAt":"2026-01-01T00:00:00Z","user":null}]}}}}`
	})
	is, err := c.GetIssue(context.Background(), "ENG-1")
	if err != nil {
		t.Fatal(err)
	}
	if is.ID != "uuid-1" || len(is.Comments) != 2 || is.Comments[0].Body != "先" || is.Comments[0].Author != "（未知）" {
		t.Fatalf("解析不对: %+v", is)
	}
}

func TestErrors(t *testing.T) {
	c := fakeLinear(t, func(string, map[string]any) string { return `{"data":{"issue":null}}` })
	if _, err := c.GetIssue(context.Background(), "ENG-9"); err == nil || !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("应报找不到: %v", err)
	}
	c.APIKey = "wrong"
	if _, err := c.GetIssue(context.Background(), "ENG-9"); err == nil || !strings.Contains(err.Error(), "Authentication") {
		t.Fatalf("应透出认证错误: %v", err)
	}
	if _, err := New("").GetIssue(context.Background(), "X"); err == nil {
		t.Fatal("未配置 key 应报错")
	}
}

func TestCreateComment(t *testing.T) {
	c := fakeLinear(t, func(q string, v map[string]any) string {
		in := v["input"].(map[string]any)
		if in["issueId"] != "uuid-1" || in["body"] != "hello" {
			t.Errorf("变量不对: %v", v)
		}
		return `{"data":{"commentCreate":{"success":true,"comment":{"url":"https://linear.app/c/1"}}}}`
	})
	url, err := c.CreateComment(context.Background(), "uuid-1", "hello")
	if err != nil || url != "https://linear.app/c/1" {
		t.Fatalf("%v %s", err, url)
	}
}
