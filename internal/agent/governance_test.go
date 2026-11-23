package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verdict/internal/protocol"
)

// resetShell 临时替换解释器探测结果，测试结束后恢复。
func resetShell(t *testing.T, bin string) {
	t.Helper()
	shellMu.Lock()
	oldFind, oldPath, oldReady := shellFind, shellPath, shellReady
	shellFind, shellPath, shellReady = func() string { return bin }, "", false
	shellMu.Unlock()
	t.Cleanup(func() {
		shellMu.Lock()
		shellFind, shellPath, shellReady = oldFind, oldPath, oldReady
		shellMu.Unlock()
	})
}

// TestShellPrefersBash：有 bash 时安装 / 启动脚本用 bash 执行。
func TestShellPrefersBash(t *testing.T) {
	resetShell(t, "/bin/bash")
	if got := shellBinary(); got != "/bin/bash" {
		t.Fatalf("shellBinary()=%q，应优先使用 bash", got)
	}
	resetShell(t, "") // 模拟机器上没有 bash
	if got := shellBinary(); got != "sh" {
		t.Fatalf("没有 bash 时应回退到 sh，实际 %q", got)
	}
}

// TestRunSetupBashOnlySyntax：带严格模式的脚本在 bash 下能跑过第一行，不再 0 秒失败。
func TestRunSetupBashOnlySyntax(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("机器上没有 bash")
	}
	out, err := runSetup(context.Background(), t.TempDir(), "set -o pipefail; echo bash-ok", nil, func(string) {})
	if err != nil || !strings.Contains(out, "bash-ok") {
		t.Fatalf("bash 下严格模式脚本应正常执行: %q %v", out, err)
	}
}

// TestRunSetupFallsBackToSh：没有 bash 时回退到 sh，普通脚本的行为不变。
func TestRunSetupFallsBackToSh(t *testing.T) {
	resetShell(t, "sh")
	out, err := runSetup(context.Background(), t.TempDir(), "echo fallback-ok", nil, func(string) {})
	if err != nil || !strings.Contains(out, "fallback-ok") {
		t.Fatalf("回退到 sh 后普通脚本应正常执行: %q %v", out, err)
	}
}

// TestWithTailRedacts：安装失败时错误里带最后输出，密码 / 密钥被脱敏。
func TestWithTailRedacts(t *testing.T) {
	tail := "line-1\ndb password=hunter2\nAPI_KEY: abc123\njwt.secret = s3cr3t"
	err := withTail(errors.New("安装依赖失败: exit status 1"), tail)
	msg := err.Error()
	for _, want := range []string{"安装依赖失败", "最后的输出", "line-1", "password=***", "API_KEY: ***", "jwt.secret = ***"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q:\n%s", want, msg)
		}
	}
	for _, secret := range []string{"hunter2", "abc123", "s3cr3t"} {
		if strings.Contains(msg, secret) {
			t.Errorf("错误信息泄露了敏感值 %q:\n%s", secret, msg)
		}
	}
}

// TestLastLines：只保留最后 10 行，避免把整段日志塞进错误。
func TestLastLines(t *testing.T) {
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	got := lastLines(strings.Join(lines, "\n"), 10)
	if strings.Contains(got, "line-01") || strings.Contains(got, "line-02") {
		t.Fatalf("应丢掉最早的两行: %q", got)
	}
	if !strings.Contains(got, "line-12") || strings.Count(got, "\n") != 9 {
		t.Fatalf("应保留最后 10 行: %q", got)
	}
}

// TestStartServiceFailureIncludesTail：启动服务的错误带最后输出（已脱敏），并保留原始日志供排查。
func TestStartServiceFailureIncludesTail(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	work := t.TempDir()
	s := &session{runID: "r3", prep: &Prepared{Dir: work}, tmp: t.TempDir()}
	_, st, err := a.startService(context.Background(), s, protocol.ServiceSpec{
		SetupCmd: "echo 'db password=s3cret' >&2; exit 3",
	}, func(string) {})
	if err == nil {
		t.Fatal("安装命令失败时应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "安装依赖失败") || !strings.Contains(msg, "最后的输出") || !strings.Contains(msg, "password=***") {
		t.Fatalf("阶段错误应带脱敏后的尾部输出: %s", msg)
	}
	if strings.Contains(msg, "s3cret") || strings.Contains(st.LogTail, "s3cret") {
		t.Fatalf("回传的输出不应包含明文密码: err=%q tail=%q", msg, st.LogTail)
	}
}

// TestProgressResult：只采用本阶段开始后写入的合法 JSON 进度文件。
func TestProgressResult(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	if progressResult(dir, start) != nil {
		t.Fatal("没有进度文件时不应采用")
	}
	path := filepath.Join(dir, progressFile)
	valid, _ := json.Marshal(map[string]any{"summary": "部分完成", "acs": []any{map[string]string{"id": "AC-1"}}})
	if err := os.WriteFile(path, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	old := start.Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if progressResult(dir, start) != nil {
		t.Fatal("上一轮留下的旧进度文件不能当成本阶段的结果")
	}
	if err := os.WriteFile(path, valid, 0o644); err != nil { // 重新写入，mtime 变成现在
		t.Fatal(err)
	}
	if got := progressResult(dir, start); string(got) != string(valid) {
		t.Fatalf("本阶段写入的合法 JSON 应被采用: %q", got)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if progressResult(dir, start) != nil {
		t.Fatal("非法 JSON 应被忽略")
	}
}
