// Package e2e 用真实的后端 + 真实的 agent + 本地 git 仓库 + 假 harness 跑通整条链路。
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verdict/internal/agent"
	"verdict/internal/agent/harness"
	"verdict/internal/protocol"
	"verdict/internal/server"
	"verdict/internal/server/store"
)

const admin = "test-admin"

// fake 根据提示词里的标记决定行为。
type fake struct{}

func (fake) Name() string { return "claude" }
func (fake) Detect(context.Context) protocol.HarnessInfo {
	return protocol.HarnessInfo{Name: "claude", Installed: true, LoggedIn: true, Version: "fake"}
}
func (fake) Run(ctx context.Context, spec harness.RunSpec) (harness.Output, error) {
	spec.Log("fake 开始")
	if _, err := os.Stat(filepath.Join(spec.Dir, "feature.txt")); err != nil {
		return harness.Output{}, fmt.Errorf("工作区里没有分支文件: %v", err)
	}
	if strings.Contains(spec.Prompt, "MODIFY") {
		os.WriteFile(filepath.Join(spec.Dir, "feature.txt"), []byte("被改了\n"), 0o644)
		os.WriteFile(filepath.Join(spec.Dir, "scratch.sh"), []byte("echo 临时脚本\n"), 0o644) // 未跟踪文件不算改源码
	}
	if strings.Contains(spec.Prompt, "SLOW") {
		for i := 0; i < 40; i++ {
			select {
			case <-ctx.Done():
				return harness.Output{}, context.Cause(ctx)
			case <-time.After(100 * time.Millisecond):
				spec.Log(fmt.Sprintf("tick %d", i))
			}
		}
	}
	rep := protocol.Report{Summary: "ok", ACs: []protocol.ACResult{
		{ID: "AC-1", Criterion: "存在 feature.txt", Status: "pass", Evidence: "ls feature.txt"},
		{ID: "AC-2", Criterion: "性能", Status: "unverifiable", Reasoning: "无压测环境"},
	}}
	b, _ := json.Marshal(rep)
	return harness.Output{Structured: b, CostUSD: 0.01}, nil
}

type env struct {
	t      *testing.T
	ts     *httptest.Server
	srv    *server.Server
	repo   string
	agDir  string
	rtID   int64
	repoID int64
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func setup(t *testing.T) *env {
	harness.Override["claude"] = fake{}
	t.Cleanup(func() { delete(harness.Override, "claude") })
	root := t.TempDir()

	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "init")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-m", "init")
	run(t, repo, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("功能\n"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-m", "feature")

	st, err := store.Open(filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := server.New(server.Config{AdminToken: admin}, st)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &env{t: t, ts: ts, srv: srv, repo: repo, agDir: filepath.Join(root, "agent")}

	var rt struct {
		Runtime store.Runtime `json:"runtime"`
		Token   string        `json:"token"`
		Install string        `json:"install_command"`
	}
	e.call("POST", "/api/runtimes", map[string]string{"name": "本机"}, &rt)
	if !strings.Contains(rt.Install, rt.Token) || !strings.Contains(rt.Install, ts.URL+"/install.sh") {
		t.Fatalf("安装命令不对: %s", rt.Install)
	}
	e.rtID = rt.Runtime.ID
	var rp store.Repo
	e.call("POST", "/api/repos", map[string]string{"url": repo, "test_notes": "无"}, &rp)
	e.repoID = rp.ID

	if err := agent.SaveConfig(e.agDir, agent.Config{Server: ts.URL, Token: rt.Token}); err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(e.agDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	e.waitFor("运行时上线", func() bool {
		var rts []store.Runtime
		e.call("GET", "/api/runtimes", nil, &rts)
		return len(rts) == 1 && rts[0].Online && rts[0].Info != nil
	})
	return e
}

func (e *env) call(method, path string, body, out any) int {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("等待超时: %s", what)
}

func (e *env) newRun(desc string) store.Run {
	e.t.Helper()
	var r store.Run
	code := e.call("POST", "/api/runs", map[string]any{
		"manual_issue": map[string]string{"title": "测试需求", "description": desc},
		"repo_id":      e.repoID, "branch": "feature", "runtime_id": e.rtID, "harness": "claude",
	}, &r)
	if code != 200 {
		e.t.Fatalf("创建测试失败: %d", code)
	}
	return r
}

func (e *env) waitDone(id string) store.Run {
	e.t.Helper()
	var r store.Run
	e.waitFor("测试结束", func() bool {
		e.call("GET", "/api/runs/"+id, nil, &r)
		return store.Terminal(r.Status)
	})
	return r
}

func (e *env) logs(id string) []store.LogLine {
	var ls []store.LogLine
	e.call("GET", "/api/runs/"+id+"/logs", nil, &ls)
	return ls
}

func TestHappyPath(t *testing.T) {
	e := setup(t)
	r := e.waitDone(e.newRun("需求：有 feature.txt").ID)
	if r.Status != store.StatusCompleted || r.Verdict != protocol.VerdictPartial || len(r.ACs) != 2 {
		t.Fatalf("结果不对: %+v", r)
	}
	head := run(t, e.repo, "rev-parse", "feature")
	if r.CommitSHA != head {
		t.Fatalf("commit 应为 %s，实际 %s", head, r.CommitSHA)
	}
	if r.SourceModified || r.CostUSD != 0.01 || r.StartedAt == nil || r.FinishedAt == nil {
		t.Fatalf("字段不对: %+v", r)
	}
	if !strings.Contains(r.Prompt, "需求：有 feature.txt") || !strings.Contains(r.Prompt, "verdict-"+r.ID) {
		t.Fatal("提示词缺少需求或 compose 项目名")
	}
	ls := e.logs(r.ID)
	if len(ls) == 0 || !strings.Contains(joinLogs(ls), "fake 开始") {
		t.Fatalf("日志缺失: %v", ls)
	}
	// 成功的测试应清理 worktree
	if _, err := os.Stat(filepath.Join(e.agDir, "runs", r.ID)); !os.IsNotExist(err) {
		t.Fatal("worktree 未清理")
	}
	// 第二次测试复用 bare 缓存
	r2 := e.waitDone(e.newRun("再来一次").ID)
	if r2.Status != store.StatusCompleted {
		t.Fatalf("第二次失败: %+v", r2)
	}
	if !strings.Contains(server.CommentBody(r, "https://v.example"), "https://v.example/runs/"+r.ID) {
		t.Fatal("评论缺少报告链接")
	}
}

func TestSourceModified(t *testing.T) {
	e := setup(t)
	r := e.waitDone(e.newRun("MODIFY").ID)
	if !r.SourceModified || !strings.Contains(r.Diff, "被改了") || strings.Contains(r.Diff, "scratch.sh") {
		t.Fatalf("应只标记已跟踪文件的改动: modified=%v diff=%q", r.SourceModified, r.Diff)
	}
}

func TestCancel(t *testing.T) {
	e := setup(t)
	r := e.newRun("SLOW")
	e.waitFor("开始执行", func() bool {
		var x store.Run
		e.call("GET", "/api/runs/"+r.ID, nil, &x)
		return x.Status == store.StatusRunning
	})
	if code := e.call("POST", "/api/runs/"+r.ID+"/cancel", nil, nil); code != 200 {
		t.Fatalf("取消失败 %d", code)
	}
	if got := e.waitDone(r.ID); got.Status != store.StatusCanceled {
		t.Fatalf("应为 canceled: %+v", got)
	}
}

func TestQueueAndBranchError(t *testing.T) {
	e := setup(t)
	a := e.newRun("SLOW")
	var b store.Run
	e.call("POST", "/api/runs", map[string]any{
		"manual_issue": map[string]string{"description": "x"},
		"repo_id":      e.repoID, "branch": "no-such-branch", "runtime_id": e.rtID, "harness": "claude",
	}, &b)
	if b.Status != store.StatusQueued {
		t.Fatalf("第二个测试应排队: %s", b.Status)
	}
	if got := e.waitDone(a.ID); got.Status != store.StatusCompleted {
		t.Fatalf("a: %+v", got)
	}
	got := e.waitDone(b.ID)
	if got.Status != store.StatusError || !strings.Contains(got.Error, "no-such-branch") {
		t.Fatalf("b 应因分支不存在失败: %+v", got)
	}
}

func TestReconnectReplaysEvents(t *testing.T) {
	e := setup(t)
	r := e.newRun("SLOW")
	e.waitFor("开始执行", func() bool { return len(e.logs(r.ID)) > 3 })
	e.srv.Hub().Disconnect(e.rtID) // 模拟断线，测试在 agent 侧继续
	got := e.waitDone(r.ID)
	if got.Status != store.StatusCompleted {
		t.Fatalf("断线后应仍能完成: %+v", got)
	}
	ls := e.logs(r.ID)
	ticks := strings.Count(joinLogs(ls), "tick ")
	if ticks != 40 {
		t.Fatalf("断线期间的日志应全部补发，实际 tick 数 %d", ticks)
	}
	for i := 1; i < len(ls); i++ {
		if ls[i].Seq <= ls[i-1].Seq {
			t.Fatal("日志 seq 非递增")
		}
	}
}

func TestAuth(t *testing.T) {
	e := setup(t)
	resp, _ := http.Get(e.ts.URL + "/api/runs")
	if resp.StatusCode != 401 {
		t.Fatalf("未登录应 401，实际 %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/agent/ws", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("错误的运行时令牌应 401，实际 %d", resp.StatusCode)
	}
}

func joinLogs(ls []store.LogLine) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(l.Text)
		b.WriteString("\n")
	}
	return b.String()
}
