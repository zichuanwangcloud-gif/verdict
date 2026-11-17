package harness

import (
	"os"
	"strings"
	"testing"
)

func TestClaudeLine(t *testing.T) {
	var logs []string
	log := func(s string) { logs = append(logs, s) }
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"先看代码"},{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"a.txt","is_error":false}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"boom"}],"is_error":true}]}}`,
		`非 JSON 行`,
	}
	for _, l := range lines {
		if claudeLine([]byte(l), log) != nil {
			t.Fatal("不应返回结果")
		}
	}
	r := claudeLine([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"{\"ok\":true}","structured_output":{"ok":true},"total_cost_usd":0.5}`), log)
	if r == nil || string(r.StructuredOutput) != `{"ok":true}` || r.TotalCostUSD != 0.5 {
		t.Fatalf("result 解析不对: %+v", r)
	}
	all := strings.Join(logs, "\n")
	for _, want := range []string{"claude-opus-5-5", "💬 先看代码", "🔧 Bash ls -la", "↳ a.txt", "↳ ⚠ boom", "非 JSON 行"} {
		if !strings.Contains(all, want) {
			t.Errorf("日志缺少 %q:\n%s", want, all)
		}
	}
}

func TestCodexLine(t *testing.T) {
	var logs []string
	log := func(s string) { logs = append(logs, s) }
	codexLine([]byte(`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`), log)
	codexLine([]byte(`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./...","aggregated_output":"ok","exit_code":0}}`), log)
	codexLine([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"完成"}}`), log)
	msg := codexLine([]byte(`{"type":"turn.failed","error":{"message":"stream disconnected"}}`), log)
	if msg != "stream disconnected" {
		t.Fatalf("失败信息不对: %q", msg)
	}
	all := strings.Join(logs, "\n")
	for _, want := range []string{"$ go test ./...", "退出码 0 ok", "💬 完成", "Codex 回合失败"} {
		if !strings.Contains(all, want) {
			t.Errorf("日志缺少 %q:\n%s", want, all)
		}
	}
}

func TestCodexMCPArgs(t *testing.T) {
	args := codexMCPArgs(&MCPServer{Name: "playwright", Command: "npx", Args: []string{"-y", "@playwright/mcp@0.0.84", "--output-dir", `/tmp/a "b"`}})
	want := []string{"-c", `mcp_servers.playwright.command="npx"`, "-c", `mcp_servers.playwright.args=["-y","@playwright/mcp@0.0.84","--output-dir","/tmp/a \"b\""]`, "-c", "mcp_servers.playwright.startup_timeout_sec=120"}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q", args)
	}
}

func TestClaudeMCPConfig(t *testing.T) {
	dir := t.TempDir()
	p, err := claudeMCPConfig(RunSpec{TempDir: dir, MCP: &MCPServer{Name: "playwright", Command: "npx", Args: []string{"-y", "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"mcpServers":{"playwright":{"args":["-y","x"],"command":"npx","type":"stdio"}}`) {
		t.Fatalf("配置不对: %s", b)
	}
}
