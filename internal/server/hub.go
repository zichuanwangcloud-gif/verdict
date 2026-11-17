package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

const (
	pingInterval = 30 * time.Second
	pingTimeout  = 15 * time.Second
	// watchdog 宽限：agent 自己会执行超时，这里只兜底「运行时一去不回」的情况。
	watchdogGrace = 15 * time.Minute
)

type agentConn struct {
	runtimeID int64
	ws        *websocket.Conn
	writeMu   sync.Mutex
	cancel    context.CancelFunc
}

func (c *agentConn) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// Hub 管理全部 agent 连接，并负责把排队的测试派给空闲的运行时。
type Hub struct {
	st *store.Store
	// OnFinish 在测试进入终态后调用（回写 Linear 等），在独立 goroutine 中执行。
	OnFinish func(runID string)

	mu    sync.Mutex
	conns map[int64]*agentConn
	// schedMu 串行化调度，避免同一运行时被重复派单。
	schedMu sync.Mutex
}

func NewHub(st *store.Store) *Hub {
	return &Hub{st: st, conns: map[int64]*agentConn{}}
}

func (h *Hub) Online(runtimeID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[runtimeID]
	return ok
}

func (h *Hub) conn(runtimeID int64) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[runtimeID]
}

// Disconnect 断开某运行时的连接（例如运行时被删除）。
func (h *Hub) Disconnect(runtimeID int64) {
	if c := h.conn(runtimeID); c != nil {
		c.ws.Close(websocket.StatusPolicyViolation, "runtime removed")
		c.cancel()
	}
}

func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(v, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// ServeWS 是 agent 的 WebSocket 入口。
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	rt, err := h.st.RuntimeByToken(r.Context(), bearer(r))
	if err != nil {
		http.Error(w, "运行时令牌无效", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	c := &agentConn{runtimeID: rt.ID, ws: ws, cancel: cancel}

	h.mu.Lock()
	if old := h.conns[rt.ID]; old != nil {
		old.ws.Close(websocket.StatusPolicyViolation, "replaced by new connection")
		old.cancel()
	}
	h.conns[rt.ID] = c
	h.mu.Unlock()
	log.Printf("运行时 #%d(%s) 已连接 %s", rt.ID, rt.Name, r.RemoteAddr)

	defer func() {
		cancel()
		ws.CloseNow()
		h.mu.Lock()
		if h.conns[rt.ID] == c {
			delete(h.conns, rt.ID)
		}
		h.mu.Unlock()
		log.Printf("运行时 #%d(%s) 已断开", rt.ID, rt.Name)
	}()

	go h.pingLoop(ctx, c)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if err := h.handle(ctx, c, data); err != nil {
			log.Printf("运行时 #%d 消息处理失败: %v", rt.ID, err)
		}
	}
}

func (h *Hub) pingLoop(ctx context.Context, c *agentConn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.ws.Close(websocket.StatusGoingAway, "ping timeout")
				c.cancel()
				return
			}
			_ = h.st.TouchRuntime(ctx, c.runtimeID)
		}
	}
}

func (h *Hub) handle(ctx context.Context, c *agentConn, data []byte) error {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeHello:
		var m protocol.Hello
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		return h.onHello(ctx, c, m)
	case protocol.TypeProbe:
		var m protocol.Probe
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		return h.st.UpdateRuntimeInfo(ctx, c.runtimeID, m.Host)
	case protocol.TypeEvent:
		var m protocol.Event
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		return h.onEvent(ctx, c, m)
	default:
		return errors.New("未知消息类型: " + env.Type)
	}
}

func (h *Hub) onHello(ctx context.Context, c *agentConn, m protocol.Hello) error {
	if err := h.st.UpdateRuntimeInfo(ctx, c.runtimeID, m.Host); err != nil {
		return err
	}
	active, err := h.st.RunsOnRuntime(ctx, c.runtimeID, store.StatusDispatched, store.StatusPreparing, store.StatusRunning)
	if err != nil {
		return err
	}
	sync := protocol.Sync{Type: protocol.TypeSync, Runs: []protocol.SyncRun{}}
	for _, r := range active {
		if slices.Contains(m.KnownRuns, r.ID) {
			last, err := h.st.LastSeq(ctx, r.ID)
			if err != nil {
				return err
			}
			sync.Runs = append(sync.Runs, protocol.SyncRun{RunID: r.ID, LastSeq: last})
			continue
		}
		if r.Status == store.StatusDispatched {
			// agent 从未收到这条任务，重新排队。
			_, err = h.st.SetStatus(ctx, r.ID, store.StatusQueued, "", store.StatusDispatched)
		} else {
			if ok, _ := h.st.SetStatus(ctx, r.ID, store.StatusError, "运行时丢失了这次测试（agent 数据目录被清理或更换了机器）", r.Status); ok {
				h.finished(r.ID)
			}
		}
		if err != nil {
			return err
		}
	}
	if err := c.send(ctx, sync); err != nil {
		return err
	}
	// agent 仍在执行、但平台上已结束（例如离线期间被取消）的测试，通知它停下。
	if m.Running != "" {
		if r, err := h.st.Run(ctx, m.Running); err == nil && store.Terminal(r.Status) {
			_ = c.send(ctx, protocol.Cancel{Type: protocol.TypeCancel, RunID: r.ID})
		}
	}
	go h.Schedule(c.runtimeID)
	return nil
}

func (h *Hub) onEvent(ctx context.Context, c *agentConn, ev protocol.Event) error {
	run, err := h.st.Run(ctx, ev.RunID)
	if err != nil {
		return err
	}
	if run.RuntimeID != c.runtimeID {
		return errors.New("事件所属测试不在该运行时上: " + ev.RunID)
	}
	last, err := h.st.LastSeq(ctx, ev.RunID)
	if err != nil {
		return err
	}
	if ev.Seq <= last {
		return nil // 重复事件
	}
	switch ev.Kind {
	case protocol.KindAccepted:
		_, err = h.st.SetStatus(ctx, run.ID, store.StatusPreparing, "", store.StatusDispatched, store.StatusQueued)
	case protocol.KindPhase:
		switch ev.Phase {
		case store.StatusPreparing:
			_, err = h.st.SetStatus(ctx, run.ID, store.StatusPreparing, "", store.StatusDispatched, store.StatusQueued)
		case store.StatusRunning:
			_, err = h.st.SetStatus(ctx, run.ID, store.StatusRunning, "", store.StatusDispatched, store.StatusQueued, store.StatusPreparing)
		}
	case protocol.KindCommit:
		err = h.st.SetCommit(ctx, run.ID, ev.Commit)
	case protocol.KindLog:
		err = h.st.AppendLog(ctx, run.ID, ev.Seq, ev.TS, ev.Text)
	case protocol.KindResult:
		if ev.Result == nil {
			return errors.New("result 事件缺少结果")
		}
		var ok bool
		ok, err = h.st.Finish(ctx, run.ID, *ev.Result)
		if ok {
			h.finished(run.ID)
		}
		go h.Schedule(c.runtimeID)
	}
	if err != nil {
		return err
	}
	_, err = h.st.AcceptSeq(ctx, run.ID, ev.Seq)
	return err
}

func (h *Hub) finished(runID string) {
	if h.OnFinish != nil {
		go h.OnFinish(runID)
	}
}

// Schedule 在运行时在线且空闲时，派发最早排队的测试。
func (h *Hub) Schedule(runtimeID int64) {
	h.schedMu.Lock()
	defer h.schedMu.Unlock()
	c := h.conn(runtimeID)
	if c == nil {
		return
	}
	ctx := context.Background()
	busy, err := h.st.RunsOnRuntime(ctx, runtimeID, store.StatusDispatched, store.StatusPreparing, store.StatusRunning)
	if err != nil || len(busy) > 0 {
		return
	}
	queued, err := h.st.RunsOnRuntime(ctx, runtimeID, store.StatusQueued)
	if err != nil || len(queued) == 0 {
		return
	}
	r := queued[0]
	ok, err := h.st.SetStatus(ctx, r.ID, store.StatusDispatched, "", store.StatusQueued)
	if err != nil || !ok {
		return
	}
	msg := protocol.Start{
		Type: protocol.TypeStart, RunID: r.ID, RepoURL: r.RepoURL, Branch: r.Branch, Harness: r.Harness,
		Prompt: r.Prompt, Schema: testplan.Schema, TimeoutMinutes: r.TimeoutMinutes,
	}
	if err := c.send(ctx, msg); err != nil {
		log.Printf("下发测试 %s 失败: %v", r.ID, err)
		_, _ = h.st.SetStatus(ctx, r.ID, store.StatusQueued, "", store.StatusDispatched)
		return
	}
	log.Printf("测试 %s 已下发到运行时 #%d", r.ID, runtimeID)
}

// Cancel 取消测试。排队中的直接取消；执行中的通知 agent，离线时直接标记。
func (h *Hub) Cancel(ctx context.Context, run store.Run) error {
	if store.Terminal(run.Status) {
		return errors.New("测试已结束")
	}
	if run.Status == store.StatusQueued {
		ok, err := h.st.SetStatus(ctx, run.ID, store.StatusCanceled, "测试在排队时被取消", store.StatusQueued)
		if err != nil {
			return err
		}
		if ok {
			h.finished(run.ID)
			return nil
		}
		// 恰好刚被派发，继续按执行中处理。
	}
	if c := h.conn(run.RuntimeID); c != nil {
		if err := c.send(ctx, protocol.Cancel{Type: protocol.TypeCancel, RunID: run.ID}); err == nil {
			return nil // agent 停止后会上报 canceled 结果
		}
	}
	ok, err := h.st.SetStatus(ctx, run.ID, store.StatusCanceled, "运行时离线，测试被直接标记为取消",
		store.StatusQueued, store.StatusDispatched, store.StatusPreparing, store.StatusRunning)
	if ok {
		h.finished(run.ID)
	}
	return err
}

// RequestProbe 让在线的运行时重新探测 harness。
func (h *Hub) RequestProbe(ctx context.Context, runtimeID int64) error {
	c := h.conn(runtimeID)
	if c == nil {
		return errors.New("运行时不在线")
	}
	return c.send(ctx, protocol.Probe{Type: protocol.TypeProbe})
}

// Watchdog 定期把「远超超时仍无结果」的测试标记为 error。
func (h *Hub) Watchdog(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rts, err := h.st.Runtimes(ctx)
		if err != nil {
			continue
		}
		for _, rt := range rts {
			runs, err := h.st.RunsOnRuntime(ctx, rt.ID, store.StatusDispatched, store.StatusPreparing, store.StatusRunning)
			if err != nil {
				continue
			}
			for _, r := range runs {
				start := r.CreatedAt
				if r.StartedAt != nil {
					start = r.StartedAt
				}
				if start == nil {
					continue
				}
				deadline := start.Add(time.Duration(r.TimeoutMinutes)*time.Minute + watchdogGrace)
				if time.Now().After(deadline) {
					if ok, _ := h.st.SetStatus(ctx, r.ID, store.StatusError, "超过时限仍未收到运行时的结果", r.Status); ok {
						h.finished(r.ID)
						go h.Schedule(rt.ID)
					}
				}
			}
			// 顺带：在线运行时如有排队任务则尝试派发（兜底）。
			if h.Online(rt.ID) {
				go h.Schedule(rt.ID)
			}
		}
	}
}
