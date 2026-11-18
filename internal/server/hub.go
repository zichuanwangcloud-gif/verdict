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
)

const (
	pingInterval = 30 * time.Second
	pingTimeout  = 15 * time.Second
)

type agentConn struct {
	runtimeID int64
	ws        *websocket.Conn
	writeMu   sync.Mutex
	cancel    context.CancelFunc
	// compatible 在收到 hello 后确定：协议版本不一致的 agent 不派发任务。
	compatible bool
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

// Hub 管理全部 agent 连接；测试的推进逻辑在 orchestrator.go。
type Hub struct {
	st *store.Store
	// OnFinish 在测试进入终态后调用（回写 Linear 等），在独立 goroutine 中执行。
	OnFinish func(runID string)

	mu    sync.Mutex
	conns map[int64]*agentConn
	// schedMu 串行化调度，避免同一运行时被重复派单。
	schedMu sync.Mutex
	// advMu 串行化测试推进，避免同一个测试被重复创建阶段。
	advMu sync.Mutex

	// 等待 agent 解析冻结节点的请求。
	resolveMu sync.Mutex
	resolves  map[string]chan protocol.Resolved
}

func NewHub(st *store.Store) *Hub {
	return &Hub{st: st, conns: map[int64]*agentConn{}, resolves: map[string]chan protocol.Resolved{}}
}

func (h *Hub) Online(runtimeID int64) bool {
	return h.conn(runtimeID) != nil
}

func (h *Hub) conn(runtimeID int64) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[runtimeID]
}

// usable 返回可以派发任务的连接。
func (h *Hub) usable(runtimeID int64) *agentConn {
	c := h.conn(runtimeID)
	if c == nil || !c.compatible {
		return nil
	}
	return c
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
	ws.SetReadLimit(16 << 20)
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
	case protocol.TypeResolved:
		var m protocol.Resolved
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		h.resolveMu.Lock()
		ch := h.resolves[m.ReqID]
		delete(h.resolves, m.ReqID)
		h.resolveMu.Unlock()
		if ch != nil {
			ch <- m
		}
		return nil
	case protocol.TypeProbe:
		var m protocol.Probe
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if err := h.st.UpdateRuntimeInfo(ctx, c.runtimeID, m.Host); err != nil {
			return err
		}
		// agent 上报的 busy 是判断有没有空槽位的依据之一，变化后要立刻重新调度，
		// 否则它空闲下来后只能等看门狗。
		go h.Schedule(c.runtimeID)
		return nil
	case protocol.TypeEvent:
		if !c.compatible {
			return nil
		}
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
	if m.Host.Protocol != protocol.ProtocolVersion {
		log.Printf("运行时 #%d 的 agent 协议版本 %d 与后端 %d 不一致，不派发任务", c.runtimeID, m.Host.Protocol, protocol.ProtocolVersion)
		return nil
	}
	c.compatible = true

	active, err := h.st.RunsOnRuntime(ctx, c.runtimeID, store.ActiveStatuses...)
	if err != nil {
		return err
	}
	sync := protocol.Sync{Type: protocol.TypeSync, Runs: []protocol.SyncRun{}}
	var resume []string
	for _, r := range active {
		if slices.Contains(m.KnownRuns, r.ID) {
			last, err := h.st.LastSeq(ctx, r.ID)
			if err != nil {
				return err
			}
			sync.Runs = append(sync.Runs, protocol.SyncRun{RunID: r.ID, LastSeq: last})
			resume = append(resume, r.ID)
			continue
		}
		if r.Status == store.StatusDispatched {
			// agent 从未收到这条任务，重新排队。
			if _, err := h.st.SetStatus(ctx, r.ID, store.StatusQueued, "", store.StatusDispatched); err != nil {
				return err
			}
			continue
		}
		h.closeRun(ctx, r.ID, store.StatusError, "运行时丢失了这次测试（agent 数据目录被清理或更换了机器）", protocol.ClosedResult{})
	}
	if err := c.send(ctx, sync); err != nil {
		return err
	}
	// agent 仍持有工作区、但平台上已结束（例如离线期间被取消）的测试，通知它收尾。
	for _, id := range m.Active {
		if r, err := h.st.Run(ctx, id); err == nil && store.Terminal(r.Status) {
			_ = c.send(ctx, protocol.Cancel{Type: protocol.TypeCancel, RunID: r.ID})
		}
	}
	// sync 之后推进：需要重发的阶段会被 agent 按 stage_id 去重。
	for _, id := range resume {
		go h.Advance(id)
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
	advance := false
	switch ev.Kind {
	case protocol.KindAccepted:
		_, err = h.st.SetStatus(ctx, run.ID, store.StatusPreparing, "", store.StatusDispatched, store.StatusQueued)
	case protocol.KindReady:
		if len(ev.Ports) > 0 {
			_ = h.st.SetPorts(ctx, run.ID, ev.Ports)
		}
		if err = h.st.SetCommit(ctx, run.ID, ev.Commit); err == nil {
			_, err = h.st.SetStatus(ctx, run.ID, store.StatusRunning, "", store.StatusDispatched, store.StatusQueued, store.StatusPreparing)
			advance = true
		}
	case protocol.KindStageStarted:
		if err = h.st.MarkStage(ctx, ev.StageID, "running"); err == nil {
			err = h.st.SetCurrentStage(ctx, run.ID, ev.StageID)
		}
	case protocol.KindLog:
		err = h.st.AppendLog(ctx, run.ID, ev.StageID, ev.Seq, ev.TS, ev.Text)
	case protocol.KindStageResult:
		if ev.Stage == nil {
			return errors.New("stage_result 事件缺少结果")
		}
		if _, err = h.st.FinishStage(ctx, ev.StageID, *ev.Stage); err == nil {
			err = h.refreshResult(ctx, run)
			advance = true
		}
	case protocol.KindClosed:
		if ev.Closed == nil {
			return errors.New("closed 事件缺少结果")
		}
		h.onClosed(ctx, run, *ev.Closed)
	}
	if err != nil {
		return err
	}
	if _, err = h.st.AcceptSeq(ctx, run.ID, ev.Seq); err != nil {
		return err
	}
	if advance {
		go h.Advance(run.ID)
	}
	return nil
}

func (h *Hub) onClosed(ctx context.Context, run store.Run, c protocol.ClosedResult) {
	if c.Busy && !c.Canceled {
		// 运行时并发已满，测试根本没开始：放回队列，不判失败。
		if _, err := h.st.SetStatus(ctx, run.ID, store.StatusQueued, "", run.Status, store.StatusDispatched, store.StatusPreparing); err == nil {
			log.Printf("测试 %s 因运行时并发已满重新排队", run.ID)
		}
		return
	}
	status, msg := store.StatusCompleted, ""
	switch {
	case c.Canceled:
		status, msg = store.StatusCanceled, "测试被取消"
	case c.Error != "":
		status, msg = store.StatusError, c.Error
	case run.Status != store.StatusFinalizing:
		status, msg = store.StatusError, "运行时在测试完成前结束了工作区"
	case run.Error != "":
		status = store.StatusError // 流水线致命错误，原因已在进入收尾时写入
	}
	h.closeRun(ctx, run.ID, status, msg, c)
	go h.Schedule(run.RuntimeID)
}

// closeRun 写入终态并触发回写。
func (h *Hub) closeRun(ctx context.Context, runID, status, msg string, c protocol.ClosedResult) {
	_ = h.st.CancelOpenStages(ctx, runID)
	ok, err := h.st.CloseRun(ctx, runID, status, msg, c)
	if err != nil {
		log.Printf("测试 %s 写入终态失败: %v", runID, err)
		return
	}
	if ok {
		log.Printf("测试 %s 结束：%s %s", runID, status, msg)
		if h.OnFinish != nil {
			go h.OnFinish(runID)
		}
	}
}

// Resolve 让一个在线的运行时把分支 / tag / SHA 解析成 commit。
// runtimeID 指定优先使用的运行时，0 表示任选一个在线的；prefer 是用它解析失败后继续尝试的候选。
func (h *Hub) Resolve(ctx context.Context, repoURL, ref string, candidates []int64) (protocol.Resolved, int64, error) {
	reqID := store.RandomHex(8)
	ch := make(chan protocol.Resolved, 1)
	h.resolveMu.Lock()
	h.resolves[reqID] = ch
	h.resolveMu.Unlock()
	defer func() {
		h.resolveMu.Lock()
		delete(h.resolves, reqID)
		h.resolveMu.Unlock()
	}()
	msg := protocol.Resolve{Type: protocol.TypeResolve, ReqID: reqID, RepoURL: repoURL, Ref: ref}
	var lastErr error
	for _, id := range candidates {
		c := h.usable(id)
		if c == nil {
			continue
		}
		h.schedMu.Lock() // 与派单串行，避免同时往同一连接写大消息互相等待
		err := c.send(ctx, msg)
		h.schedMu.Unlock()
		if err != nil {
			lastErr = err
			continue
		}
		select {
		case res := <-ch:
			if res.Error != "" {
				lastErr = errors.New(res.Error)
				continue
			}
			return res, id, nil
		case <-ctx.Done():
			return protocol.Resolved{}, 0, errors.New("等待运行时解析超时")
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有在线的运行时可以解析冻结节点")
	}
	return protocol.Resolved{}, 0, lastErr
}

// OnlineIDs 返回在线的运行时。
func (h *Hub) OnlineIDs() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]int64, 0, len(h.conns))
	for id, c := range h.conns {
		if c.compatible {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Schedule 在运行时在线、版本兼容且还有空闲槽位时，派发排队的测试。
// 并发度取用户设置（0 时按机器规模算），并与 agent 上报的实际忙碌数取小。
func (h *Hub) Schedule(runtimeID int64) {
	h.schedMu.Lock()
	defer h.schedMu.Unlock()
	c := h.usable(runtimeID)
	if c == nil {
		return
	}
	ctx := context.Background()
	rt, err := h.st.Runtime(ctx, runtimeID)
	if err != nil {
		return
	}
	limit := effectiveConcurrency(rt)
	if info := rt.Info; info != nil && info.Busy > 0 && info.Busy >= limit {
		return // agent 自己报告已经满了，等它空闲下来再派
	}
	busy, err := h.st.RunsOnRuntime(ctx, runtimeID, store.ActiveStatuses...)
	if err != nil {
		return
	}
	for len(busy) < limit {
		r, ok := h.nextRun(ctx, runtimeID)
		if !ok {
			return
		}
		if ok, err := h.st.SetStatus(ctx, r.ID, store.StatusDispatched, "", store.StatusQueued); err != nil || !ok {
			return
		}
		msg := protocol.Start{Type: protocol.TypeStart, RunID: r.ID, RepoURL: r.RepoURL, Branch: r.Branch,
			Ref: r.PinRef, Commit: r.PinSHA, Ports: r.RepoPorts}
		if err := c.send(ctx, msg); err != nil {
			log.Printf("下发测试 %s 失败: %v", r.ID, err)
			_, _ = h.st.SetStatus(ctx, r.ID, store.StatusQueued, "", store.StatusDispatched)
			return
		}
		log.Printf("测试 %s 已下发到运行时 #%d（占用 %d/%d 个槽位）", r.ID, runtimeID, len(busy)+1, limit)
		if r, err := h.st.Run(ctx, r.ID); err == nil {
			busy = append(busy, r)
		} else {
			return
		}
	}
}

// nextRun 取下一条要派发的测试：先自己队列里的，再领别的运行时名下的回归子测试。
// 普通测试优先于回归子测试，避免回归把日常测试堵住。
func (h *Hub) nextRun(ctx context.Context, runtimeID int64) (store.Run, bool) {
	queued, err := h.st.RunsOnRuntime(ctx, runtimeID, store.StatusQueued)
	if err != nil {
		return store.Run{}, false
	}
	for _, r := range queued {
		if r.RegressionID == "" {
			return r, true
		}
	}
	if len(queued) > 0 {
		return queued[0], true
	}
	claimed := h.claimable(ctx, runtimeID)
	for _, r := range claimed {
		if r.RegressionID == "" {
			return r, true
		}
	}
	if len(claimed) > 0 {
		return claimed[0], true
	}
	return store.Run{}, false
}

// RequestProbe 让在线的运行时重新探测 harness。
func (h *Hub) RequestProbe(ctx context.Context, runtimeID int64) error {
	c := h.conn(runtimeID)
	if c == nil {
		return errors.New("运行时不在线")
	}
	return c.send(ctx, protocol.Probe{Type: protocol.TypeProbe})
}

// claimable 把这个运行时能接手的回归子测试改派到自己名下，返回可派发的测试。
// 这样回归能分摊到多个运行时，先空闲的机器多跑。
func (h *Hub) claimable(ctx context.Context, runtimeID int64) []store.Run {
	gs, err := h.st.RunningRegressionsFor(ctx, runtimeID)
	if err != nil {
		return nil
	}
	var out []store.Run
	for _, g := range gs {
		briefs, err := h.st.QueuedRegressionRuns(ctx, g.ID)
		if err != nil {
			continue
		}
		for _, b := range briefs {
			if b.RuntimeID == runtimeID {
				continue // 已经在自己名下，前面就能取到
			}
			ok, err := h.st.ClaimRun(ctx, b.ID, b.RuntimeID, runtimeID)
			if err != nil || !ok {
				continue
			}
			r, err := h.st.Run(ctx, b.ID)
			if err == nil {
				out = append(out, r)
			}
		}
	}
	return out
}
