package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"verdict/internal/server/store"
)

func TestBoardEndpoints(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(Config{AdminToken: "t"}, st)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	do := func(method, path, body string) (*http.Response, string) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		return resp, string(buf[:n])
	}

	// 未配 Key 时应给出明确提示，而不是 500。
	if resp, body := do("GET", "/api/board/teams", ""); resp.StatusCode != 400 || !strings.Contains(body, "Linear API Key") {
		t.Fatalf("未配置 Key 应 400 并提示: %d %s", resp.StatusCode, body)
	}

	// settings 往返：记住列表页配置。
	_, body := do("PUT", "/api/settings", `{"board_defaults":{"repo_id":3,"branch":"feature/x","tier":"A","team":"ENG","states":["started"],"assignee":"all"}}`)
	if !strings.Contains(body, `"branch":"feature/x"`) {
		t.Fatalf("设置未回显: %s", body)
	}
	var got struct {
		BoardDefaults boardDefaults `json:"board_defaults"`
	}
	_, body = do("GET", "/api/settings", "")
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.BoardDefaults.Branch != "feature/x" || got.BoardDefaults.Tier != "A" || len(got.BoardDefaults.States) != 1 {
		t.Fatalf("配置未持久化: %s", body)
	}
}

func TestBatchRunValidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(Config{AdminToken: "t"}, st)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	post := func(body string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/board/runs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		return resp, string(buf[:n])
	}
	if resp, body := post(`{"issue_keys":[]}`); resp.StatusCode != 400 || !strings.Contains(body, "请选择") {
		t.Fatalf("空选择应 400: %d %s", resp.StatusCode, body)
	}
	many := strings.Repeat(`"ENG-1",`, 21)
	if resp, _ := post(`{"issue_keys":[` + strings.TrimSuffix(many, ",") + `]}`); resp.StatusCode != 400 {
		t.Fatalf("超过上限应 400: %d", resp.StatusCode)
	}
}
