package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"verdict/internal/agent/harness"
	"verdict/internal/protocol"
)

const sessionLifetime = 48 * time.Hour // 后端一去不回时，工作区和服务最多保留这么久

var errCanceled = errors.New("测试被取消")

type stageTimeoutError struct{ d time.Duration }

func (e stageTimeoutError) Error() string { return fmt.Sprintf("阶段超过时限 %s", e.d) }

// session 是一个测试在运行时上的全部状态：工作区、托管服务、正在执行的阶段。
type session struct {
	runID, repoURL, branch string
	ref, commit            string // 回归测试：完整引用名与锁定的 commit
	portCount              int    // 需要分配的端口数
	ports                  []int  // agent 实际分配到的端口
	ctx                    context.Context
	cancel                 context.CancelCauseFunc
	ready                  chan struct{} // 工作区准备结束（成功或失败）后关闭
	tmp                    string        // 本测试的临时目录（工作区之外）
	artifacts              string        // 截图等产物目录
	lifetime               *time.Timer

	mu          sync.Mutex
	prep        *Prepared
	svc         *service
	stageID     string
	stageCancel context.CancelCauseFunc
	stageDone   chan struct{}
	closing     bool
	uploaded    int
}

func (s *session) env() []string {
	out := []string{"VERDICT_RUN_ID=" + s.runID, "COMPOSE_PROJECT_NAME=verdict-" + s.runID, "VERDICT_ARTIFACTS=" + s.artifacts}
	if len(s.ports) > 0 {
		out = append(out, fmt.Sprintf("VERDICT_PORT=%d", s.ports[0]), fmt.Sprintf("VERDICT_PORT_COUNT=%d", len(s.ports)))
		for i, p := range s.ports {
			out = append(out, fmt.Sprintf("VERDICT_PORT%d=%d", i+1, p))
		}
	}
	return out
}

func (a *Agent) onStart(m protocol.Start) {
	a.sesMu.Lock()
	if _, running := a.sessions[m.RunID]; running {
		a.sesMu.Unlock()
		return // 重复派发
	}
	if a.journal.Closed(m.RunID) {
		a.sesMu.Unlock()
		return // 已经结束过，结果会通过 sync 补发
	}
	// 并发上限由平台决定（平台不会派超过并发的任务）；这里只做一层保护，
	// 超过时告诉平台重新排队，而不是判失败。
	if limit := a.concurrency(); limit > 0 && len(a.sessions) >= limit {
		a.sesMu.Unlock()
		a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindClosed,
			Closed: &protocol.ClosedResult{Busy: true, Error: "运行时并发已满"}})
		return
	}
	ports, err := freePorts(m.Ports)
	if err != nil {
		a.sesMu.Lock()
		delete(a.sessions, m.RunID)
		a.sesMu.Unlock()
		a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindClosed, Closed: &protocol.ClosedResult{Error: "分配端口失败: " + err.Error()}})
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	tmp := a.ws.TempDir(m.RunID)
	s := &session{ports: ports, runID: m.RunID, repoURL: m.RepoURL, branch: m.Branch, ref: m.Ref, commit: m.Commit, portCount: m.Ports,
		ctx: ctx, cancel: cancel, ready: make(chan struct{}), tmp: tmp, artifacts: filepath.Join(tmp, "artifacts")}
	s.lifetime = time.AfterFunc(sessionLifetime, func() {
		a.closeSession(s, protocol.ClosedResult{Error: "测试超过 48 小时没有结束，agent 自动收尾"}, false)
	})
	a.sessions[m.RunID] = s
	a.sesMu.Unlock()
	a.probeLater()
	go a.prepare(s)
}

// concurrency 是 agent 自己的并发上限；0 表示完全信任平台调度。
func (a *Agent) concurrency() int {
	return a.maxSessions
}

// probeLater 在会话数变化后补报一次探测，让平台知道实际忙碌程度。
func (a *Agent) probeLater() {
	go func() {
		time.Sleep(200 * time.Millisecond)
		a.probeNow()
	}()
}

func (a *Agent) prepare(s *session) {
	logf := a.logger(s.runID, "")
	log.Printf("开始测试 %s：%s@%s", s.runID, s.repoURL, s.branch)
	a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindAccepted})
	prep, err := a.ws.Prepare(s.ctx, s.runID, s.repoURL, Target{Branch: s.branch, Ref: s.ref, Commit: s.commit}, logf)
	if err == nil {
		err = os.MkdirAll(s.artifacts, 0o755)
	}
	s.mu.Lock()
	s.prep = prep
	s.mu.Unlock()
	close(s.ready)
	if err != nil {
		if errors.Is(context.Cause(s.ctx), errCanceled) {
			return // 取消流程负责收尾
		}
		logf("❌ 准备工作区失败: " + err.Error())
		go a.closeSession(s, protocol.ClosedResult{Error: "准备工作区失败: " + err.Error()}, true)
		return
	}
	a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindReady, Commit: prep.Commit, Ports: s.ports})
}

func (a *Agent) onStage(m protocol.Stage) {
	if a.journal.StageDone(m.RunID, m.StageID) {
		return // 已执行过，结果会通过 sync 补发
	}
	s := a.sessionFor(m.RunID)
	if s == nil {
		if !a.journal.Closed(m.RunID) {
			a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindStageResult, StageID: m.StageID,
				Stage: &protocol.StageResult{Status: protocol.StageError, Error: "运行时上没有这个测试的工作区"}})
		}
		return
	}
	s.mu.Lock()
	if s.closing || s.stageID != "" {
		s.mu.Unlock()
		return // 收尾中，或正在执行（同一阶段的重发，或后端异常的并发派发）
	}
	t0 := time.Now()
	timeout := time.Duration(max(m.TimeoutMinutes, 1)) * time.Minute
	ctx, cancel := context.WithCancelCause(s.ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, timeout, stageTimeoutError{timeout})
	done := make(chan struct{})
	s.stageID, s.stageCancel, s.stageDone = m.StageID, cancel, done
	s.mu.Unlock()

	go func() {
		var res protocol.StageResult
		defer func() {
			// 阶段里的任何 panic 都只让这个阶段失败，不能把整个 agent 带走——
			// agent 一崩，这台机器上其他正在跑的测试会被一起中断。
			if r := recover(); r != nil {
				res = protocol.StageResult{Status: protocol.StageError,
					Error: fmt.Sprintf("阶段执行时 agent 内部错误：%v", r), DurationMS: time.Since(t0).Milliseconds()}
				log.Printf("测试 %s 阶段 %s（%s）panic: %v\n%s", s.runID, m.StageID, m.Role, r, debug.Stack())
			}
			cancelTimeout()
			cancel(nil)
			s.mu.Lock()
			s.stageID, s.stageCancel, s.stageDone = "", nil, nil
			s.mu.Unlock()
			a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindStageResult, StageID: m.StageID, Stage: &res})
			log.Printf("测试 %s 阶段 %s（%s）结束：%s %s", s.runID, m.StageID, m.Role, res.Status, res.Error)
			close(done)
		}()
		<-s.ready
		res = a.runStage(ctx, s, m)
	}()
}

// classify 根据 context 的取消原因区分超时、取消与普通错误。
func classify(ctx context.Context, err error) (string, string) {
	cause := context.Cause(ctx)
	var te stageTimeoutError
	switch {
	case errors.Is(cause, errCanceled):
		return protocol.StageCanceled, "测试被取消"
	case errors.As(cause, &te):
		return protocol.StageTimeout, te.Error()
	case cause != nil:
		return protocol.StageError, cause.Error()
	}
	return protocol.StageError, err.Error()
}

func (a *Agent) runStage(ctx context.Context, s *session, m protocol.Stage) protocol.StageResult {
	logf := a.logger(s.runID, m.StageID)
	t0 := time.Now()
	a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindStageStarted, StageID: m.StageID})
	s.mu.Lock()
	prep := s.prep
	s.mu.Unlock()
	if prep == nil {
		return protocol.StageResult{Status: protocol.StageError, Error: "工作区没有准备好"}
	}

	var res protocol.StageResult
	switch m.Kind {
	case protocol.StageKindService:
		if m.Service == nil {
			res = protocol.StageResult{Status: protocol.StageError, Error: "缺少服务配置"}
			break
		}
		s.mu.Lock()
		old := s.svc
		s.svc = nil
		s.mu.Unlock()
		old.Stop()
		svc, st, err := a.startService(ctx, s, *m.Service, logf)
		res.Service = &st
		if err != nil {
			res.Status, res.Error = classify(ctx, err)
			if res.Status == protocol.StageError {
				res.Error = err.Error()
			}
			logf("❌ " + res.Error)
			break
		}
		s.mu.Lock()
		s.svc = svc
		s.mu.Unlock()
		res.Status = protocol.StageOK

	case protocol.StageKindHarness:
		h, ok := harness.Get(m.Harness)
		if !ok {
			res = protocol.StageResult{Status: protocol.StageError, Error: "agent 不支持 harness: " + m.Harness}
			break
		}
		spec := harness.RunSpec{Dir: prep.Dir, TempDir: filepath.Join(s.tmp, "stage-"+m.StageID), Prompt: m.Prompt,
			Schema: m.Schema, Env: s.env(), Log: logf}
		if m.Browser {
			spec.MCP = browserMCP(s.artifacts)
			logf("▶ 已挂载浏览器工具（" + PlaywrightMCP + "）")
		}
		logf(fmt.Sprintf("▶ %s 阶段开始，使用 %s", m.Role, h.Name()))
		before := snapshot(s.artifacts)
		out, err := h.Run(ctx, spec)
		upCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		res.Artifacts = a.uploadNew(upCtx, s, m.StageID, before, logf)
		cancel()
		_ = os.RemoveAll(spec.TempDir)
		res.CostUSD = out.CostUSD
		switch {
		case err != nil:
			res.Status, res.Error = classify(ctx, err)
			if res.Status == protocol.StageError {
				res.Error = err.Error()
			}
			logf("❌ " + res.Error)
		case !json.Valid(out.Structured):
			res.Status, res.Error = protocol.StageError, "harness 的结构化输出不是合法 JSON"
		default:
			res.Status, res.Structured = protocol.StageOK, out.Structured
		}

	default:
		res = protocol.StageResult{Status: protocol.StageError, Error: "未知阶段类型: " + m.Kind}
	}
	res.DurationMS = time.Since(t0).Milliseconds()
	return res
}

// onEnd 处理 finish / cancel：收尾工作区并上报 closed。
func (a *Agent) onEnd(runID string, res protocol.ClosedResult) {
	s := a.sessionFor(runID)
	if s == nil {
		if !a.journal.Closed(runID) && len(mustEvents(a.journal, runID)) > 0 {
			if !res.Canceled {
				res.Error = "运行时上已经没有这个测试的工作区"
			}
			a.emit(protocol.Event{RunID: runID, Kind: protocol.KindClosed, Closed: &res})
		}
		return
	}
	go a.closeSession(s, res, false)
}

func mustEvents(j *Journal, runID string) []protocol.Event {
	evs, _ := j.Since(runID, 0)
	return evs
}

// closeSession 停止当前阶段与托管服务、清理 compose、检查源码改动、删除工作区，并上报 closed。
func (a *Agent) closeSession(s *session, res protocol.ClosedResult, keepWorktree bool) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.closing = true
	done := s.stageDone
	s.mu.Unlock()
	s.lifetime.Stop()

	cause := errCanceled
	if !res.Canceled {
		cause = errors.New("测试收尾")
	}
	s.cancel(cause)
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Minute):
			log.Printf("测试 %s 的阶段在一分钟内没有停止，继续收尾", s.runID)
		}
	}
	<-s.ready

	logf := a.logger(s.runID, "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s.mu.Lock()
	svc, prep := s.svc, s.prep
	s.svc = nil
	s.mu.Unlock()
	if svc != nil {
		logf("停止托管服务")
		svc.Stop()
	}
	workDir := ""
	if prep != nil {
		workDir = prep.Dir
	}
	runTeardown(a.dir, s.runID, workDir, logf)
	composeDown(ctx, s.runID, logf)
	if prep != nil {
		res.SourceModified, res.Diff = prep.Changes(ctx)
		if res.SourceModified {
			logf("⚠ 测试过程中被测源码发生了改动，已记录 diff")
		}
		if keepWorktree || res.Error != "" {
			logf(fmt.Sprintf("工作目录保留 %s 供排查：%s", keepFailed, prep.Dir))
		} else {
			prep.Remove(ctx)
		}
	}
	_ = os.RemoveAll(s.tmp)
	a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindClosed, Closed: &res})
	log.Printf("测试 %s 已收尾（取消=%v 错误=%s）", s.runID, res.Canceled, res.Error)

	a.sesMu.Lock()
	if a.sessions[s.runID] == s {
		delete(a.sessions, s.runID)
	}
	a.sesMu.Unlock()
	a.probeLater()
}
