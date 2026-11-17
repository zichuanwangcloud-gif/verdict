package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	return []string{"VERDICT_RUN_ID=" + s.runID, "COMPOSE_PROJECT_NAME=verdict-" + s.runID, "VERDICT_ARTIFACTS=" + s.artifacts}
}

func (a *Agent) onStart(m protocol.Start) {
	a.sesMu.Lock()
	if a.ses != nil {
		busy := a.ses.runID
		a.sesMu.Unlock()
		if busy != m.RunID {
			a.emit(protocol.Event{RunID: m.RunID, Kind: protocol.KindClosed, Closed: &protocol.ClosedResult{Error: "运行时正忙（正在执行 " + busy + "）"}})
		}
		return
	}
	if a.journal.Closed(m.RunID) {
		a.sesMu.Unlock()
		return // 已经结束过，结果会通过 sync 补发
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	tmp := a.ws.TempDir(m.RunID)
	s := &session{runID: m.RunID, repoURL: m.RepoURL, branch: m.Branch, ctx: ctx, cancel: cancel,
		ready: make(chan struct{}), tmp: tmp, artifacts: filepath.Join(tmp, "artifacts")}
	s.lifetime = time.AfterFunc(sessionLifetime, func() {
		a.closeSession(s, protocol.ClosedResult{Error: "测试超过 48 小时没有结束，agent 自动收尾"}, false)
	})
	a.ses = s
	a.sesMu.Unlock()
	go a.prepare(s)
}

func (a *Agent) prepare(s *session) {
	logf := a.logger(s.runID, "")
	log.Printf("开始测试 %s：%s@%s", s.runID, s.repoURL, s.branch)
	a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindAccepted})
	prep, err := a.ws.Prepare(s.ctx, s.runID, s.repoURL, s.branch, logf)
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
	a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindReady, Commit: prep.Commit})
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
	timeout := time.Duration(max(m.TimeoutMinutes, 1)) * time.Minute
	ctx, cancel := context.WithCancelCause(s.ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, timeout, stageTimeoutError{timeout})
	done := make(chan struct{})
	s.stageID, s.stageCancel, s.stageDone = m.StageID, cancel, done
	s.mu.Unlock()

	go func() {
		defer func() {
			cancelTimeout()
			cancel(nil)
			s.mu.Lock()
			s.stageID, s.stageCancel, s.stageDone = "", nil, nil
			s.mu.Unlock()
			close(done)
		}()
		<-s.ready
		res := a.runStage(ctx, s, m)
		a.emit(protocol.Event{RunID: s.runID, Kind: protocol.KindStageResult, StageID: m.StageID, Stage: &res})
		log.Printf("测试 %s 阶段 %s（%s）结束：%s %s", s.runID, m.StageID, m.Role, res.Status, res.Error)
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
	if a.ses == s {
		a.ses = nil
	}
	a.sesMu.Unlock()
}
