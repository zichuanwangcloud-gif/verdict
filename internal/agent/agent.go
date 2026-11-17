package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"verdict/internal/agent/harness"
	"verdict/internal/protocol"
)

const (
	keepFailed  = 24 * time.Hour
	keepJournal = 7 * 24 * time.Hour
)

type Agent struct {
	dir     string
	cfg     Config
	journal *Journal
	ws      Workspace

	mu     sync.Mutex
	conn   *websocket.Conn
	synced bool // 收到 sync 并补发完成后，事件才实时发送

	execMu  sync.Mutex
	running string
	cancel  context.CancelCauseFunc
}

var errCanceled = errors.New("测试被取消")

func New(dir string) (*Agent, error) {
	cfg, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	if cfg.Path != "" {
		os.Setenv("PATH", cfg.Path)
	}
	j, err := OpenJournal(filepath.Join(dir, "journal"))
	if err != nil {
		return nil, err
	}
	return &Agent{dir: dir, cfg: cfg, journal: j, ws: Workspace{Root: dir}}, nil
}

// Probe 探测本机信息与 harness。
func Probe(ctx context.Context) protocol.HostInfo {
	host, _ := os.Hostname()
	info := protocol.HostInfo{AgentVersion: protocol.Version, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if out, err := exec.CommandContext(ctx, "git", "--version").Output(); err == nil {
		info.Git = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "git version"))
	}
	if out, err := exec.CommandContext(ctx, "docker", "--version").Output(); err == nil {
		info.Docker = strings.TrimSpace(string(out))
	}
	var wg sync.WaitGroup
	hs := harness.All()
	info.Harnesses = make([]protocol.HarnessInfo, len(hs))
	for i, h := range hs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info.Harnesses[i] = h.Detect(ctx)
		}()
	}
	wg.Wait()
	return info
}

// Run 常驻运行：断线自动重连，直到 ctx 结束。
func (a *Agent) Run(ctx context.Context) error {
	a.recoverInterrupted()
	go a.housekeeping(ctx)

	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			break
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2+1)))
		log.Printf("与后端的连接断开: %v；%s 后重连", err, wait.Round(time.Second))
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
	a.execMu.Lock()
	if a.cancel != nil {
		a.cancel(errors.New("agent 正在退出"))
	}
	a.execMu.Unlock()
	return ctx.Err()
}

// recoverInterrupted 给上次进程退出时没有结果的测试补写 error 结果。
func (a *Agent) recoverInterrupted() {
	for _, id := range a.journal.Runs() {
		if !a.journal.Finished(id) {
			log.Printf("测试 %s 在 agent 重启前未完成，标记为中断", id)
			_, _ = a.journal.Append(protocol.Event{RunID: id, Kind: protocol.KindResult,
				Result: &protocol.RunResult{Status: "error", Error: "agent 进程重启，测试中断"}})
		}
	}
}

func (a *Agent) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.execMu.Lock()
		active := a.running
		a.execMu.Unlock()
		a.ws.GC(ctx, keepFailed, active)
		a.journal.Prune(keepJournal)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) session(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, resp, err := websocket.Dial(dctx, wsURL(a.cfg.Server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.cfg.Token}},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("令牌被拒绝（运行时可能已被删除）: %w", err)
		}
		return err
	}
	conn.SetReadLimit(64 << 20)
	defer conn.CloseNow()
	log.Printf("已连接 %s", a.cfg.Server)

	sctx, scancel := context.WithCancel(ctx)
	defer scancel()

	a.mu.Lock()
	a.conn, a.synced = conn, false
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.conn == conn {
			a.conn, a.synced = nil, false
		}
		a.mu.Unlock()
	}()

	a.execMu.Lock()
	running := a.running
	a.execMu.Unlock()
	hello := protocol.Hello{Type: protocol.TypeHello, Host: Probe(sctx), KnownRuns: a.journal.Runs(), Running: running}
	if err := a.write(sctx, conn, hello); err != nil {
		return err
	}

	for {
		_, data, err := conn.Read(sctx)
		if err != nil {
			return err
		}
		if err := a.handle(sctx, conn, data); err != nil {
			log.Printf("处理后端消息失败: %v", err)
		}
	}
}

func (a *Agent) write(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (a *Agent) handle(ctx context.Context, conn *websocket.Conn, data []byte) error {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeSync:
		var m protocol.Sync
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		return a.onSync(ctx, conn, m)
	case protocol.TypeStart:
		var m protocol.Start
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		a.onStart(m)
	case protocol.TypeCancel:
		var m protocol.Cancel
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		a.execMu.Lock()
		if a.running == m.RunID && a.cancel != nil {
			log.Printf("收到取消：%s", m.RunID)
			a.cancel(errCanceled)
		}
		a.execMu.Unlock()
	case protocol.TypeProbe:
		go func() {
			info := Probe(ctx)
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.conn == conn {
				if err := a.write(ctx, conn, protocol.Probe{Type: protocol.TypeProbe, Host: info}); err != nil {
					conn.CloseNow()
				}
			}
		}()
	default:
		return errors.New("未知消息类型: " + env.Type)
	}
	return nil
}

// onSync 补发后端尚未收到的事件，之后切换到实时发送。
func (a *Agent) onSync(ctx context.Context, conn *websocket.Conn, m protocol.Sync) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn != conn {
		return nil
	}
	for _, r := range m.Runs {
		evs, err := a.journal.Since(r.RunID, r.LastSeq)
		if err != nil {
			return err
		}
		for _, ev := range evs {
			if err := a.write(ctx, conn, ev); err != nil {
				conn.CloseNow()
				return err
			}
		}
		if len(evs) > 0 {
			log.Printf("已补发测试 %s 的 %d 条事件", r.RunID, len(evs))
		}
	}
	a.synced = true
	return nil
}

// emit 先写本地日志，在线且已同步时再实时发送。
func (a *Agent) emit(ev protocol.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ev, err := a.journal.Append(ev)
	if err != nil {
		log.Printf("写入事件日志失败: %v", err)
		return
	}
	if a.conn != nil && a.synced {
		if err := a.write(context.Background(), a.conn, ev); err != nil {
			// 连接坏了：关闭后由重连 + sync 补发。
			a.conn.CloseNow()
			a.synced = false
		}
	}
}

func (a *Agent) onStart(m protocol.Start) {
	a.execMu.Lock()
	defer a.execMu.Unlock()
	if a.running != "" {
		if a.running == m.RunID {
			return
		}
		a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindResult,
			Result: &protocol.RunResult{Status: "error", Error: "运行时正忙（正在执行 " + a.running + "）"}})
		return
	}
	if a.journal.Finished(m.RunID) {
		return // 已执行过，结果会通过 sync 补发
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	a.running, a.cancel = m.RunID, cancel
	go func() {
		defer func() {
			a.execMu.Lock()
			a.running, a.cancel = "", nil
			a.execMu.Unlock()
			cancel(nil)
		}()
		a.execute(ctx, m)
	}()
}

func (a *Agent) execute(ctx context.Context, m protocol.Start) {
	logf := func(s string) {
		a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindLog, Text: s})
	}
	finish := func(res protocol.RunResult) {
		a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindResult, Result: &res})
		log.Printf("测试 %s 结束：%s %s", m.RunID, res.Status, res.Error)
	}
	log.Printf("开始测试 %s：%s@%s via %s", m.RunID, m.RepoURL, m.Branch, m.Harness)
	a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindAccepted})

	h, ok := harness.Get(m.Harness)
	if !ok {
		finish(protocol.RunResult{Status: "error", Error: "agent 不支持 harness: " + m.Harness})
		return
	}
	timeout := time.Duration(m.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = time.Hour
	}
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("超过时限 %s", timeout))
	defer cancelTimeout()

	statusFor := func(err error) protocol.RunResult {
		cause := context.Cause(ctx)
		if errors.Is(cause, errCanceled) {
			return protocol.RunResult{Status: "canceled", Error: "测试被取消"}
		}
		if cause != nil {
			return protocol.RunResult{Status: "error", Error: cause.Error()}
		}
		return protocol.RunResult{Status: "error", Error: err.Error()}
	}

	a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindPhase, Phase: "preparing"})
	prep, err := a.ws.Prepare(ctx, m.RunID, m.RepoURL, m.Branch, logf)
	if err != nil {
		logf("❌ 准备工作区失败: " + err.Error())
		res := statusFor(err)
		if prep != nil {
			prep.Remove(context.Background())
		}
		finish(res)
		return
	}
	a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindCommit, Commit: prep.Commit})
	a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindPhase, Phase: "running"})
	logf(fmt.Sprintf("工作目录 %s，启动 %s", prep.Dir, h.Name()))

	tmp := a.ws.TempDir(m.RunID)
	out, runErr := h.Run(ctx, harness.RunSpec{
		Dir: prep.Dir, TempDir: tmp, Prompt: m.Prompt, Schema: m.Schema, Log: logf,
		Env: []string{"VERDICT_RUN_ID=" + m.RunID, "COMPOSE_PROJECT_NAME=verdict-" + m.RunID},
	})

	cleanCtx, cancelClean := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelClean()
	composeDown(cleanCtx, m.RunID, logf)
	modified, diff := prep.Changes(cleanCtx)
	if modified {
		logf("⚠ 测试过程中被测源码发生了改动，已记录 diff")
	}

	var res protocol.RunResult
	if runErr != nil {
		res = statusFor(runErr)
		logf("❌ " + res.Error)
	} else {
		res = protocol.RunResult{Status: "completed", Raw: out.Structured}
		var rep protocol.Report
		if err := json.Unmarshal(out.Structured, &rep); err != nil {
			res = protocol.RunResult{Status: "error", Error: "harness 输出不符合结果结构: " + err.Error(), Raw: out.Structured}
		} else {
			res.Report = &rep
		}
	}
	res.CostUSD, res.SourceModified, res.Diff = out.CostUSD, modified, diff

	_ = os.RemoveAll(tmp)
	if res.Status == "completed" {
		prep.Remove(cleanCtx)
	} else {
		logf(fmt.Sprintf("工作目录保留 %s 供排查：%s", keepFailed, prep.Dir))
	}
	finish(res)
}
