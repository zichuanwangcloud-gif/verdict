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
	"strconv"
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
	http    *http.Client

	mu     sync.Mutex
	conn   *websocket.Conn
	synced bool // 收到 sync 并补发完成后，事件才实时发送

	sesMu    sync.Mutex
	sessions map[string]*session // 当前持有工作区的测试，按 runID 索引
	// maxSessions 是 agent 侧的并发上限；0 表示完全信任平台调度。
	maxSessions int
}

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
	return &Agent{dir: dir, cfg: cfg, journal: j, ws: Workspace{Root: dir},
		http: &http.Client{Timeout: 2 * time.Minute}, sessions: map[string]*session{}}, nil
}

// Probe 探测本机信息、harness 与浏览器。
func Probe(ctx context.Context) protocol.HostInfo {
	host, _ := os.Hostname()
	info := protocol.HostInfo{AgentVersion: protocol.Version, Protocol: protocol.ProtocolVersion, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH}
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
	info.Browser = probeBrowser()
	info.CPU = runtime.NumCPU()
	info.MemTotalMB = memTotalMB()
	wg.Wait()
	return info
}

// memTotalMB 返回物理内存（MB），读不到时返回 0。
func memTotalMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				if kb, err := strconv.Atoi(f[0]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// Run 常驻运行：断线自动重连，直到 ctx 结束。
// 退出时先在连接仍然可用的情况下收尾当前测试，让后端立即知道测试中断，再断开连接。
func (a *Agent) Run(ctx context.Context) error {
	killLeftovers(a.dir)
	teardownLeftovers(a.dir)
	a.recoverInterrupted()
	go a.housekeeping(ctx)

	connCtx, connCancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		var wg sync.WaitGroup
		for _, s := range a.allSessions() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.closeSession(s, protocol.ClosedResult{Error: "agent 正在退出，测试中断"}, false)
			}()
		}
		wg.Wait()
		connCancel()
	}()

	backoff := time.Second
	for connCtx.Err() == nil {
		start := time.Now()
		err := a.session(connCtx)
		if connCtx.Err() != nil {
			break
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2+1)))
		log.Printf("与后端的连接断开: %v；%s 后重连", err, wait.Round(time.Second))
		select {
		case <-connCtx.Done():
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
	<-stopped
	return ctx.Err()
}

// recoverInterrupted 给上次进程退出时没有收尾的测试补写 closed 事件。
func (a *Agent) recoverInterrupted() {
	for _, id := range a.journal.Runs() {
		if !a.journal.Closed(id) {
			log.Printf("测试 %s 在 agent 重启前未完成，标记为中断", id)
			_, _ = a.journal.Append(protocol.Event{RunID: id, Kind: protocol.KindClosed,
				Closed: &protocol.ClosedResult{Error: "agent 进程重启，测试中断"}})
		}
	}
}

func (a *Agent) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.ws.GC(ctx, keepFailed, a.activeRunIDs())
		a.journal.Prune(keepJournal)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// allSessions 返回当前全部会话的快照。
func (a *Agent) allSessions() []*session {
	a.sesMu.Lock()
	defer a.sesMu.Unlock()
	out := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		out = append(out, s)
	}
	return out
}

func (a *Agent) activeRunIDs() []string {
	ss := a.allSessions()
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.runID)
	}
	return out
}

func (a *Agent) sessionFor(runID string) *session {
	a.sesMu.Lock()
	defer a.sesMu.Unlock()
	return a.sessions[runID]
}

// busy 返回正在执行的测试数。
func (a *Agent) busy() int {
	a.sesMu.Lock()
	defer a.sesMu.Unlock()
	return len(a.sessions)
}

// session 维持一次 WebSocket 连接。
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

	host := Probe(sctx)
	host.Busy = a.busy()
	hello := protocol.Hello{Type: protocol.TypeHello, Host: host, KnownRuns: a.journal.Runs(), Active: a.activeRunIDs()}
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

func decodeAs[T any](data []byte) (T, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

func (a *Agent) handle(ctx context.Context, conn *websocket.Conn, data []byte) error {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeSync:
		m, err := decodeAs[protocol.Sync](data)
		if err != nil {
			return err
		}
		return a.onSync(ctx, conn, m)
	case protocol.TypeStart:
		m, err := decodeAs[protocol.Start](data)
		if err != nil {
			return err
		}
		a.onStart(m)
	case protocol.TypeStage:
		m, err := decodeAs[protocol.Stage](data)
		if err != nil {
			return err
		}
		a.onStage(m)
	case protocol.TypeFinish:
		m, err := decodeAs[protocol.Finish](data)
		if err != nil {
			return err
		}
		a.onEnd(m.RunID, protocol.ClosedResult{})
	case protocol.TypeCancel:
		m, err := decodeAs[protocol.Cancel](data)
		if err != nil {
			return err
		}
		log.Printf("收到取消：%s", m.RunID)
		a.onEnd(m.RunID, protocol.ClosedResult{Canceled: true})
	case protocol.TypeResolve:
		m, err := decodeAs[protocol.Resolve](data)
		if err != nil {
			return err
		}
		go func() {
			res := Resolve(ctx, m)
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.conn == conn {
				if err := a.write(ctx, conn, res); err != nil {
					conn.CloseNow()
				}
			}
		}()
	case protocol.TypeProbe:
		go a.probeNow()
		return nil
	default:
		return errors.New("未知消息类型: " + env.Type)
	}
	return nil
}

// probeNow 立即探测并上报一次（并发度变化后也会调用）。
func (a *Agent) probeNow() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info := Probe(ctx)
	info.Busy, info.Concurrency = a.busy(), a.concurrency()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn != nil {
		if err := a.write(ctx, a.conn, protocol.Probe{Type: protocol.TypeProbe, Host: info}); err != nil {
			a.conn.CloseNow()
		}
	}
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

func (a *Agent) logger(runID, stageID string) func(string) {
	return func(s string) {
		a.emit(protocol.Event{RunID: runID, Kind: protocol.KindLog, StageID: stageID, Text: s})
	}
}
