// Package e2e 用真实的后端 + 真实的 agent + 本地 git 仓库 + 真实的托管服务 + 假 harness 跑通整条链路。
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"verdict/internal/agent"
	"verdict/internal/agent/harness"
	"verdict/internal/protocol"
	"verdict/internal/server"
	"verdict/internal/server/store"
)

const admin = "test-admin"

// png 是一个最小的合法 PNG（1x1）。
var png = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89, 0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1, 0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 'B', 0x60, 0x82}

func role(prompt string) string {
	for k, v := range map[string]string{"测试设计负责人": "plan", "负责按测试计划逐条实测": "verify", "UI 走查工程师": "ui", "截图质检员": "shots",
		"红队审查员": "red", "针对质疑重新实测": "recheck", "探索性测试工程师": "explore", "裁决者": "judge"} {
		if strings.Contains(prompt, k) {
			return v
		}
	}
	return "?"
}

func envOf(spec harness.RunSpec, key string) string {
	for _, e := range spec.Env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

// fake 按阶段角色给出输出；需求描述里的标记控制特殊行为。
type fake struct {
	name string
	mu   sync.Mutex
	log  []string // 执行过的角色
	// serviceSeen 记录执行验证阶段时托管服务是否可访问
	serviceSeen map[string]bool
	// active / maxActive 记录同时执行的 harness 调用数，用于验证并发度。
	active    int
	maxActive int
	// tries 记录每个角色被执行的次数，用于验证阶段重试。
	tries map[string]int
}

// flakyCount 从提示词里读 FLAKY_N（所有角色）或 FLAKY:角色:N（指定角色），返回该角色前 N 次应失败。
func flakyCount(prompt string) int {
	if i := strings.Index(prompt, "FLAKY:"); i >= 0 {
		role := role(prompt)
		rest := prompt[i+len("FLAKY:"):]
		name, count, ok := strings.Cut(rest, ":")
		if !ok || name != role {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(strings.Fields(count)[0]))
		return n
	}
	i := strings.Index(prompt, "FLAKY_")
	if i < 0 {
		return 0
	}
	j := i + len("FLAKY_")
	k := j
	for k < len(prompt) && prompt[k] >= '0' && prompt[k] <= '9' {
		k++
	}
	if k == j {
		return 0
	}
	n, _ := strconv.Atoi(prompt[j:k])
	return n
}

func (f *fake) Name() string { return f.name }
func (f *fake) Detect(context.Context) protocol.HarnessInfo {
	return protocol.HarnessInfo{Name: f.name, Installed: true, LoggedIn: true, Version: "fake"}
}

func (f *fake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fake) Run(ctx context.Context, spec harness.RunSpec) (harness.Output, error) {
	r := role(spec.Prompt)
	f.mu.Lock()
	if f.tries == nil {
		f.tries = map[string]int{}
	}
	f.log = append(f.log, r)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	spec.Log("💬 fake " + f.name + " " + r)
	if _, err := os.Stat(filepath.Join(spec.Dir, "feature.txt")); err != nil {
		return harness.Output{}, fmt.Errorf("工作区里没有分支文件: %v", err)
	}
	if f.name == "codex" && strings.Contains(spec.Prompt, "CODEX_DOWN") {
		return harness.Output{}, fmt.Errorf("stream disconnected")
	}
	// FLAKY_N：同一个角色前 N 次用网关抖动失败，之后成功，用来验证重试。
	if n := flakyCount(spec.Prompt); n > 0 {
		f.mu.Lock()
		f.tries[r]++
		tries := f.tries[r]
		f.mu.Unlock()
		if tries <= n {
			return harness.Output{}, fmt.Errorf("stream disconnected before completion")
		}
	}
	out := func(v any) (harness.Output, error) {
		b, _ := json.Marshal(v)
		return harness.Output{Structured: b, CostUSD: 0.01}, nil
	}
	switch r {
	case "plan":
		return out(map[string]any{"summary": "计划", "needs_service": true,
			"service": map[string]string{"setup_cmd": "", "start_cmd": "", "health_url": "", "app_url": ""},
			"items": []map[string]any{
				{"id": "AC-1", "criterion": "存在 feature.txt", "method": "cli", "preconditions": "", "steps": []string{"ls"}, "expected": "存在"},
				{"id": "AC-2", "criterion": "首页可访问", "method": "ui", "preconditions": "", "steps": []string{"打开首页"}, "expected": "200"},
			}})
	case "verify":
		if strings.Contains(spec.Prompt, "MODIFY") {
			os.WriteFile(filepath.Join(spec.Dir, "feature.txt"), []byte("被改了\n"), 0o644)
			os.WriteFile(filepath.Join(spec.Dir, "scratch.sh"), []byte("echo 临时脚本\n"), 0o644)
		}
		if u := extractURL(spec.Prompt); u != "" {
			resp, err := http.Get(u)
			f.mu.Lock()
			if f.serviceSeen == nil {
				f.serviceSeen = map[string]bool{}
			}
			f.serviceSeen[u] = err == nil && resp.StatusCode == 200
			f.mu.Unlock()
			if resp != nil {
				resp.Body.Close()
			}
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
		return out(map[string]any{"summary": "蓝队完成", "acs": []map[string]string{
			{"id": "AC-1", "criterion": "存在 feature.txt", "status": "pass", "evidence": "ls feature.txt", "reasoning": "r", "confidence": "high", "response": ""},
			{"id": "AC-2", "criterion": "首页可访问", "status": "unverifiable", "evidence": "", "reasoning": "留待 UI 走查", "confidence": "low", "response": ""},
		}})
	case "ui":
		if spec.MCP == nil || spec.MCP.Name != "playwright" {
			return harness.Output{}, fmt.Errorf("UI 阶段应挂载浏览器 MCP")
		}
		dir := envOf(spec, "VERDICT_ARTIFACTS")
		os.MkdirAll(filepath.Join(dir, "shots"), 0o755)
		os.WriteFile(filepath.Join(dir, "shots", "home page.png"), png, 0o644)
		return out(map[string]any{"summary": "ui", "pages": []map[string]any{{"url": "/", "title": "首页", "screenshots": []string{"shots-home_page.png"}, "observations": ""}},
			"shots":    []map[string]any{{"file": "shots-home_page.png", "url": "/", "state": "首页", "purpose": "AC-2", "covered": false, "readable": true, "issues": []string{}}},
			"acs":      []map[string]string{{"id": "AC-2", "criterion": "", "status": "pass", "evidence": "截图 shots-home_page.png", "reasoning": "", "confidence": "high", "response": ""}},
			"findings": []map[string]string{{"severity": "low", "title": "按钮错位", "detail": "d", "evidence": "e"}}})
	case "shots":
		return out(map[string]any{"summary": "截图都可用", "shot_reviews": []map[string]any{
			{"file": "shots-home_page.png", "verdict": "usable", "occluded": false, "readable": true, "problems": []string{}, "suggest": ""}}})
	case "red":
		var ch []map[string]string
		if strings.Contains(spec.Prompt, "CHALLENGE") && !strings.Contains(spec.Prompt, "第 1 轮质疑") {
			ch = []map[string]string{{"ac_id": "AC-1", "kind": "insufficient_evidence", "claim": "只 ls 不够", "suggested_check": "cat feature.txt", "proposed_status": "fail"}}
		}
		return out(map[string]any{"summary": "red", "challenges": ch, "findings": []any{}})
	case "recheck":
		return out(map[string]any{"summary": "复测", "acs": []map[string]string{
			{"id": "AC-1", "criterion": "", "status": "pass", "evidence": "cat feature.txt → 功能", "reasoning": "内容正确", "confidence": "high", "response": "补充了内容检查"}}})
	case "explore":
		return out(map[string]any{"summary": "ex", "findings": []map[string]string{{"severity": "high", "title": "缺少输入校验", "detail": "d", "evidence": "e"}}})
	case "judge":
		return out(map[string]any{"summary": "裁决：全部通过", "acs": []map[string]string{
			{"id": "AC-1", "status": "pass", "confidence": "high", "rationale": "复测确认"},
			{"id": "AC-2", "status": "pass", "confidence": "medium", "rationale": "截图"}}})
	}
	return harness.Output{}, fmt.Errorf("未知角色")
}

func extractURL(prompt string) string {
	_, rest, ok := strings.Cut(prompt, "- 地址：")
	if !ok {
		return ""
	}
	u, _, _ := strings.Cut(rest, "\n")
	return strings.TrimSpace(u)
}

type env struct {
	t            *testing.T
	ts           *httptest.Server
	srv          *server.Server
	repo         string
	agDir        string
	rtID, repoID int64
	claude       *fake
	codex        *fake
	port         int
	stopAgent    func()
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

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func setup(t *testing.T) *env {
	e := &env{t: t, claude: &fake{name: "claude"}, codex: &fake{name: "codex"}, port: freePort(t)}
	harness.Override["claude"], harness.Override["codex"] = e.claude, e.codex
	t.Cleanup(func() { delete(harness.Override, "claude"); delete(harness.Override, "codex") })
	root := t.TempDir()

	e.repo = filepath.Join(root, "repo")
	os.MkdirAll(e.repo, 0o755)
	run(t, e.repo, "init")
	os.WriteFile(filepath.Join(e.repo, "index.html"), []byte("<h1>hi</h1>\n"), 0o644)
	run(t, e.repo, "add", ".")
	run(t, e.repo, "commit", "-m", "init")
	run(t, e.repo, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(e.repo, "feature.txt"), []byte("功能\n"), 0o644)
	run(t, e.repo, "add", ".")
	run(t, e.repo, "commit", "-m", "feature")

	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0o755)
	st, err := store.Open(filepath.Join(dataDir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.srv = server.New(server.Config{AdminToken: admin, DataDir: dataDir}, st)
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	e.agDir = filepath.Join(root, "agent")

	var rt struct {
		Runtime store.Runtime `json:"runtime"`
		Token   string        `json:"token"`
	}
	e.call("POST", "/api/runtimes", map[string]string{"name": "本机"}, &rt)
	e.rtID = rt.Runtime.ID
	var rp store.Repo
	health := fmt.Sprintf("http://127.0.0.1:%d/index.html", e.port)
	e.call("POST", "/api/repos", map[string]string{"url": e.repo, "test_notes": "无",
		"start_cmd": fmt.Sprintf("exec python3 -m http.server %d --bind 127.0.0.1", e.port), "health_url": health}, &rp)
	e.repoID = rp.ID

	if err := agent.SaveConfig(e.agDir, agent.Config{Server: e.ts.URL, Token: rt.Token}); err != nil {
		t.Fatal(err)
	}
	e.startAgent()
	t.Cleanup(func() { e.stopAgent() })
	return e
}

func (e *env) startAgent() {
	a, err := agent.New(e.agDir)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	e.stopAgent = func() { cancel(); <-done }
	e.waitFor("运行时上线", func() bool {
		var rts []map[string]any
		e.call("GET", "/api/runtimes", nil, &rts)
		return len(rts) == 1 && rts[0]["online"] == true && rts[0]["info"] != nil && rts[0]["outdated"] == false
	})
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
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("等待超时: %s", what)
}

type detail struct {
	store.Run
	Stages    []store.Stage    `json:"stages"`
	Artifacts []store.Artifact `json:"artifacts"`
}

func (e *env) newRun(tier, desc string, extra map[string]any) detail {
	e.t.Helper()
	body := map[string]any{
		"manual_issue": map[string]string{"title": "测试需求", "description": desc},
		"repo_id":      e.repoID, "branch": "feature", "runtime_id": e.rtID, "harness": "claude", "tier": tier,
	}
	for k, v := range extra {
		body[k] = v
	}
	var r detail
	if code := e.call("POST", "/api/runs", body, &r); code != 200 {
		e.t.Fatalf("创建测试失败: %d", code)
	}
	return r
}

func (e *env) get(id string) detail {
	var r detail
	e.call("GET", "/api/runs/"+id, nil, &r)
	return r
}

func (e *env) waitStatus(id string, pred func(detail) bool, what string) detail {
	e.t.Helper()
	var r detail
	e.waitFor(what, func() bool { r = e.get(id); return pred(r) })
	return r
}

func (e *env) waitDone(id string) detail {
	e.t.Helper()
	return e.waitStatus(id, func(r detail) bool { return store.Terminal(r.Status) }, "测试结束")
}

func (e *env) logs(id string) []store.LogLine {
	var ls []store.LogLine
	e.call("GET", "/api/runs/"+id+"/logs", nil, &ls)
	return ls
}

func flow(r detail) string {
	var parts []string
	for _, s := range r.Stages {
		p := s.Role
		if s.Round > 0 {
			p += fmt.Sprint(s.Round)
		}
		if s.Harness == "codex" {
			p += "@codex"
		}
		if s.Status != "ok" {
			p += "(" + s.Status + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

func (e *env) portOpen() bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.port), 300*time.Millisecond)
	if err == nil {
		c.Close()
	}
	return err == nil
}

func TestTierC(t *testing.T) {
	e := setup(t)
	r := e.waitDone(e.newRun("C", "需求", nil).ID)
	if r.Status != "completed" || flow(r) != "plan verify" || r.Tier != "C" {
		t.Fatalf("C 档流程不对: %s %s %s", r.Status, flow(r), r.Error)
	}
	if r.Verdict != protocol.VerdictPartial || len(r.ACs) != 2 || r.ACs[0].Method != "cli" {
		t.Fatalf("结论不对: %+v", r.ACs)
	}
	if head := run(t, e.repo, "rev-parse", "feature"); r.CommitSHA != head {
		t.Fatalf("commit 应为 %s，实际 %s", head, r.CommitSHA)
	}
	if _, err := os.Stat(filepath.Join(e.agDir, "runs", r.ID)); !os.IsNotExist(err) {
		t.Fatal("worktree 未清理")
	}
	if r.CostUSD != 0.02 {
		t.Fatalf("费用应累加: %v", r.CostUSD)
	}
}

func TestTierSFullPipeline(t *testing.T) {
	e := setup(t)
	r := e.waitDone(e.newRun("S", "需求 CHALLENGE", nil).ID)
	if r.Status != "completed" {
		t.Fatalf("应完成: %s %s", r.Status, r.Error)
	}
	if got := flow(r); got != "plan service verify ui shots red1@codex recheck1 red2@codex explore judge" {
		t.Fatalf("S 档流程不对: %s", got)
	}
	if r.Config.RedHarness != "codex" || strings.Join(e.codex.calls(), ",") != "red,red" {
		t.Fatalf("红队应使用交叉模型: %+v %v", r.Config, e.codex.calls())
	}
	// 托管服务：执行验证时可访问，测试结束后被停止。
	if len(e.claude.serviceSeen) != 1 {
		t.Fatalf("执行验证阶段应看到托管服务: %v", e.claude.serviceSeen)
	}
	for _, ok := range e.claude.serviceSeen {
		if !ok {
			t.Fatal("托管服务在执行验证时不可访问")
		}
	}
	if e.portOpen() {
		t.Fatal("测试结束后托管服务应被停止")
	}
	if svc := r.Stages[1].Service; svc == nil || !svc.Running || !strings.HasSuffix(svc.LogFile, "service.log") {
		t.Fatalf("服务阶段结果不对: %+v", svc)
	}
	// 产物：UI 阶段的截图被上传，可以下载。
	if len(r.Artifacts) != 1 || r.Artifacts[0].Name != "shots-home_page.png" || r.Artifacts[0].ContentType != "image/png" {
		t.Fatalf("产物不对: %+v", r.Artifacts)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/runs/"+r.ID+"/artifacts/shots-home_page.png", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("下载产物失败: %v %v", err, resp)
	}
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(buf.Bytes(), png) {
		t.Fatal("下载的产物内容不对")
	}
	// 结论：裁决覆盖、质疑记录、额外发现。
	if r.Verdict != protocol.VerdictPass || r.Summary != "裁决：全部通过" {
		t.Fatalf("结论不对: %s %s", r.Verdict, r.Summary)
	}
	ac1 := r.ACs[0]
	if len(ac1.Challenges) != 1 || ac1.Challenges[0].Resolution != "pass" || ac1.Challenges[0].Response == "" || !strings.Contains(ac1.Evidence, "cat feature.txt") {
		t.Fatalf("质疑记录不对: %+v", ac1)
	}
	if len(r.Findings) != 2 || r.Findings[0].Severity != "high" || len(r.Pages) != 1 {
		t.Fatalf("额外发现/页面不对: %+v %+v", r.Findings, r.Pages)
	}
	// 日志按阶段归属。
	byStage := map[string]int{}
	for _, l := range e.logs(r.ID) {
		byStage[l.Stage]++
	}
	for _, s := range r.Stages {
		if byStage[s.ID] == 0 {
			t.Errorf("阶段 %s(%s) 没有日志", s.ID, s.Role)
		}
	}
}

func TestCrossModelFallbackAndService(t *testing.T) {
	e := setup(t)
	r := e.waitDone(e.newRun("A", "需求 CODEX_DOWN", nil).ID)
	if got := flow(r); got != "plan service verify ui shots red1@codex(error) red1 judge" {
		t.Fatalf("交叉模型失败应降级重试: %s", got)
	}
	retry := -1
	for i, st := range r.Stages {
		if st.Role == "red" && st.Harness == "codex" {
			retry = i + 1
		}
	}
	if retry < 0 || retry >= len(r.Stages) || !strings.Contains(r.Stages[retry].Note, "交叉模型 codex 执行失败") || r.Status != "completed" {
		t.Fatalf("重试说明不对: 第 %d 个阶段 %q，状态 %s", retry, stageNote(r.Stages, retry), r.Status)
	}
}

// stageNote 安全地取某个阶段的说明，越界时返回空。
func stageNote(stages []store.Stage, i int) string {
	if i < 0 || i >= len(stages) {
		return ""
	}
	return stages[i].Note
}

func TestApprovalGate(t *testing.T) {
	e := setup(t)
	r := e.newRun("B", "需求", map[string]any{"approve_plan": true})
	r = e.waitStatus(r.ID, func(d detail) bool { return d.Status == store.StatusAwaiting }, "等待确认")
	if flow(r) != "plan" || r.AwaitingSince == nil {
		t.Fatalf("应只执行了计划: %s", flow(r))
	}
	if code := e.call("POST", "/api/runs/"+r.ID+"/approve", map[string]string{"note": "重点看边界"}, nil); code != 200 {
		t.Fatalf("确认失败 %d", code)
	}
	r = e.waitDone(r.ID)
	if r.Status != "completed" || flow(r) != "plan service verify" || !strings.Contains(r.Stages[2].Spec.Prompt, "重点看边界") {
		t.Fatalf("确认后应继续，并带上补充说明: %s", flow(r))
	}
	if code := e.call("POST", "/api/runs/"+r.ID+"/approve", nil, nil); code != 409 {
		t.Fatal("已结束的测试不能再确认")
	}
}

func TestCancelDuringStage(t *testing.T) {
	e := setup(t)
	r := e.newRun("B", "SLOW", nil)
	e.waitFor("执行验证开始", func() bool { return strings.Contains(joinLogs(e.logs(r.ID)), "tick 2") })
	if !e.portOpen() {
		t.Fatal("执行中托管服务应在运行")
	}
	if code := e.call("POST", "/api/runs/"+r.ID+"/cancel", nil, nil); code != 200 {
		t.Fatalf("取消失败 %d", code)
	}
	got := e.waitDone(r.ID)
	if got.Status != "canceled" || got.Stages[2].Status != "canceled" {
		t.Fatalf("应为 canceled: %s %s", got.Status, flow(got))
	}
	if e.portOpen() {
		t.Fatal("取消后托管服务应被停止")
	}
}

func TestReconnectMidStage(t *testing.T) {
	e := setup(t)
	r := e.newRun("B", "SLOW", nil)
	e.waitFor("执行验证开始", func() bool { return strings.Contains(joinLogs(e.logs(r.ID)), "tick 3") })
	e.srv.Hub().Disconnect(e.rtID) // 模拟断线：阶段在 agent 侧继续，重连后后端重发阶段、agent 去重
	got := e.waitDone(r.ID)
	if got.Status != "completed" || flow(got) != "plan service verify" {
		t.Fatalf("断线后应仍能完成: %s %s", got.Status, flow(got))
	}
	if n := strings.Count(joinLogs(e.logs(r.ID)), "tick "); n != 40 {
		t.Fatalf("断线期间的日志应全部补发，实际 %d", n)
	}
	if calls := e.claude.calls(); strings.Join(calls, ",") != "plan,verify" {
		t.Fatalf("阶段不应被重复执行: %v", calls)
	}
}

func TestSourceModified(t *testing.T) {
	e := setup(t)
	r := e.waitDone(e.newRun("C", "MODIFY", nil).ID)
	if !r.SourceModified || !strings.Contains(r.Diff, "被改了") || strings.Contains(r.Diff, "scratch.sh") {
		t.Fatalf("应只标记已跟踪文件的改动: modified=%v diff=%q", r.SourceModified, r.Diff)
	}
}

func TestQueueAndBranchError(t *testing.T) {
	e := setup(t)
	a := e.newRun("B", "SLOW", nil)
	var b detail
	e.call("POST", "/api/runs", map[string]any{"manual_issue": map[string]string{"description": "x"}, "tier": "C",
		"repo_id": e.repoID, "branch": "no-such-branch", "runtime_id": e.rtID, "harness": "claude"}, &b)
	if b.Status != store.StatusQueued {
		t.Fatalf("第二个测试应排队: %s", b.Status)
	}
	if got := e.waitDone(a.ID); got.Status != "completed" {
		t.Fatalf("a: %s %s", got.Status, got.Error)
	}
	got := e.waitDone(b.ID)
	if got.Status != "error" || !strings.Contains(got.Error, "no-such-branch") {
		t.Fatalf("b 应因分支不存在失败: %s %q", got.Status, got.Error)
	}
}

func TestAgentRestartMidRun(t *testing.T) {
	e := setup(t)
	r := e.newRun("B", "SLOW", nil)
	e.waitFor("执行验证开始", func() bool { return strings.Contains(joinLogs(e.logs(r.ID)), "tick 2") })
	e.stopAgent() // agent 退出会主动收尾
	got := e.waitDone(r.ID)
	if got.Status != "error" || !strings.Contains(got.Error, "agent") {
		t.Fatalf("agent 退出应使测试出错: %s %q", got.Status, got.Error)
	}
	if e.portOpen() {
		t.Fatal("agent 退出后托管服务应被停止")
	}
	e.startAgent()
	if r2 := e.waitDone(e.newRun("C", "x", nil).ID); r2.Status != "completed" {
		t.Fatalf("重启后应能继续执行新测试: %s %s", r2.Status, r2.Error)
	}
}

func TestOutdatedAgentAndAuth(t *testing.T) {
	e := setup(t)
	resp, _ := http.Get(e.ts.URL + "/api/runs")
	if resp.StatusCode != 401 {
		t.Fatalf("未登录应 401，实际 %d", resp.StatusCode)
	}
	resp, _ = http.Post(e.ts.URL+"/api/agent/runs/x/artifacts?name=a.png", "image/png", strings.NewReader("x"))
	if resp.StatusCode != 401 {
		t.Fatalf("没有运行时令牌不能上传产物，实际 %d", resp.StatusCode)
	}

	// 模拟旧版 agent：协议版本为 0 的 hello。
	var rt struct {
		Runtime store.Runtime `json:"runtime"`
		Token   string        `json:"token"`
	}
	e.call("POST", "/api/runtimes", map[string]string{"name": "旧机器"}, &rt)
	ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/api/agent/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + rt.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	hello, _ := json.Marshal(map[string]any{"type": "hello", "host": map[string]any{"agent_version": "0.1.0", "harnesses": []any{}}})
	ws.Write(context.Background(), websocket.MessageText, hello)
	e.waitFor("旧版 agent 被识别", func() bool {
		var rts []map[string]any
		e.call("GET", "/api/runtimes", nil, &rts)
		for _, x := range rts {
			if x["name"] == "旧机器" && x["outdated"] == true {
				return true
			}
		}
		return false
	})
	var res map[string]string
	code := e.call("POST", "/api/runs", map[string]any{"manual_issue": map[string]string{"description": "x"},
		"repo_id": e.repoID, "branch": "feature", "runtime_id": rt.Runtime.ID, "harness": "claude"}, &res)
	if code != 400 || !strings.Contains(res["error"], "版本过旧") {
		t.Fatalf("旧版 agent 应拒绝创建测试: %d %v", code, res)
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
