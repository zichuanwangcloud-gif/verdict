package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"verdict/internal/protocol"
)

// TestExpandPorts 验证端口占位符替换。
func TestExpandPorts(t *testing.T) {
	s := &session{ports: []int{31001, 31002}}
	for in, want := range map[string]string{
		"http://127.0.0.1:${VERDICT_PORT}/health": "http://127.0.0.1:31001/health",
		"http://127.0.0.1:${VERDICT_PORT1}":       "http://127.0.0.1:31001",
		"http://127.0.0.1:${VERDICT_PORT2}":       "http://127.0.0.1:31002",
		"pnpm dev --port ${VERDICT_PORT}":         "pnpm dev --port 31001",
		"没有占位符的地址":                                "没有占位符的地址",
	} {
		if got := s.expand(in); got != want {
			t.Errorf("expand(%q)=%q want %q", in, got, want)
		}
	}
	// 没有分配到的编号替换为空串，交给上层报错。
	if got := s.expand("http://127.0.0.1:${VERDICT_PORT3}"); got != "http://127.0.0.1:" {
		t.Errorf("未分配的端口应替换为空: %q", got)
	}
}

// TestWaitHealthyInvalidURL 健康检查地址非法时返回错误，不能 panic。
// 修复前这里是 client.Do(nil)，会直接让 agent 段错误退出。
func TestWaitHealthyInvalidURL(t *testing.T) {
	svc := &service{done: make(chan struct{})}
	err := waitHealthy(context.Background(), svc, "http://127.0.0.1:", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "端口是空的") {
		t.Fatalf("非法地址应返回明确错误: %v", err)
	}
}

// TestServiceUrlPlaceholderNotExpanded：占位符取不到值时，在启动服务前就报错。
func TestServiceUrlPlaceholderNotExpanded(t *testing.T) {
	a := &Agent{}
	s := &session{runID: "r", ports: nil}
	dir := t.TempDir()
	s.prep = &Prepared{Dir: dir}
	s.tmp = t.TempDir()
	_, _, err := a.startService(context.Background(), s, protocol.ServiceSpec{
		StartCmd:  "sleep 30",
		HealthURL: "http://127.0.0.1:${VERDICT_PORT}/health",
	}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "端口") {
		t.Fatalf("应提示端口问题: %v", err)
	}
}

// TestServiceHealthy 正常路径：服务起来后健康检查通过。
func TestServiceHealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer ts.Close()
	svc := &service{done: make(chan struct{})}
	if err := waitHealthy(context.Background(), svc, ts.URL, func(string) {}); err != nil {
		t.Fatalf("应通过健康检查: %v", err)
	}
}
