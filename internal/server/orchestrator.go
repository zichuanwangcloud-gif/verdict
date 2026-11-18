package server

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

const (
	stageGrace      = 10 * time.Minute // agent 自己执行阶段超时，这里只兜底
	offlineLimit    = 30 * time.Minute // 运行时离线超过这个时间，执行中的测试判为出错
	approvalLimit   = 24 * time.Hour   // 计划等待确认的上限
	finalizeLimit   = 15 * time.Minute // 收尾等待上限
	maxStagesPerRun = 40               // 防御：流水线异常时不至于无限创建阶段
)

func (h *Hub) input(ctx context.Context, run store.Run) (testplan.Input, error) {
	repo, err := h.st.Repo(ctx, run.RepoID)
	if err != nil {
		return testplan.Input{}, err
	}
	return testplan.Input{
		RunID: run.ID, Issue: run.Issue, RepoURL: repo.URL, Branch: run.Branch,
		Repo:       testplan.RepoInfo{TestNotes: repo.TestNotes, SetupCmd: repo.SetupCmd, StartCmd: repo.StartCmd, HealthURL: repo.HealthURL, AppURL: repo.AppURL},
		Approved:   run.Approved, ApprovalNote: run.ApprovalNote,
		Commit: run.PinSHA, PlanSource: run.PlanSource,
	}, nil
}

// refreshResult 按已完成阶段重新汇总结论，执行过程中报告页可以看到逐步更新的结果。
func (h *Hub) refreshResult(ctx context.Context, run store.Run) error {
	stages, err := h.st.Stages(ctx, run.ID)
	if err != nil {
		return err
	}
	recs := store.Records(stages)
	res := testplan.Consolidate(run.Config, recs)
	_, cost := testplan.Usage(recs)
	return h.st.SetResult(ctx, run.ID, res, cost)
}

// Advance 推进一个测试：重发尚未得到结果的阶段，或者计算并派发下一步。
// 可以在任何时候重复调用（收到阶段结果、agent 重连、看门狗），agent 按 stage_id 去重。
func (h *Hub) Advance(runID string) {
	h.advMu.Lock()
	defer h.advMu.Unlock()
	ctx := context.Background()
	if err := h.advance(ctx, runID); err != nil {
		log.Printf("推进测试 %s 失败: %v", runID, err)
	}
}

func (h *Hub) advance(ctx context.Context, runID string) error {
	run, err := h.st.Run(ctx, runID)
	if err != nil {
		return err
	}
	c := h.usable(run.RuntimeID)
	switch run.Status {
	case store.StatusFinalizing:
		if c != nil {
			return c.send(ctx, protocol.Finish{Type: protocol.TypeFinish, RunID: run.ID})
		}
		return nil
	case store.StatusRunning:
	default:
		return nil // 排队、准备中、等待确认、已结束：没有可推进的
	}

	stages, err := h.st.Stages(ctx, run.ID)
	if err != nil {
		return err
	}
	// 回归测试复用旧计划：第一次推进时把计划阶段直接写成已完成，跳过重新规划。
	if run.PresetPlan != "" && testplan.ParsePlan(store.Records(stages)) == nil &&
		!slices.ContainsFunc(stages, func(st store.Stage) bool { return st.Role == testplan.RolePlan }) {
		if err := h.st.AddPresetPlan(ctx, run.ID, firstNonEmpty(run.PlanSource, "（手动加入的基线用例）"), run.PresetPlan); err != nil {
			return err
		}
		if stages, err = h.st.Stages(ctx, run.ID); err != nil {
			return err
		}
	}
	for _, st := range stages {
		if !testplan.Done(st.Status) {
			return h.sendStage(ctx, c, run, st) // 还在执行或尚未送达：重发
		}
	}

	in, err := h.input(ctx, run)
	if err != nil {
		return err
	}
	for range maxStagesPerRun {
		if len(stages) >= maxStagesPerRun {
			return h.finalize(ctx, c, run, stages, "阶段数量超过上限，流水线异常终止")
		}
		step := testplan.Next(run.Config, in, store.Records(stages))
		switch step.Action {
		case testplan.ActionSkip:
			st, err := h.st.AddStage(ctx, run.ID, step.Stage, testplan.StatusSkipped, step.Note)
			if err != nil {
				return err
			}
			stages = append(stages, st)
			continue
		case testplan.ActionStage:
			st, err := h.st.AddStage(ctx, run.ID, step.Stage, testplan.StatusPending, step.Note)
			if err != nil {
				return err
			}
			return h.sendStage(ctx, c, run, st)
		case testplan.ActionAwait:
			_, err := h.st.SetStatus(ctx, run.ID, store.StatusAwaiting, "", store.StatusRunning)
			return err
		case testplan.ActionFinish:
			return h.finalize(ctx, c, run, stages, step.Fatal)
		}
	}
	return errors.New("流水线没有收敛")
}

func (h *Hub) sendStage(ctx context.Context, c *agentConn, run store.Run, st store.Stage) error {
	if c == nil {
		return nil // 运行时离线：重连后会重新推进
	}
	msg := protocol.Stage{
		Type: protocol.TypeStage, RunID: run.ID, StageID: st.ID, Kind: st.Spec.Kind, Role: st.Spec.Role,
		TimeoutMinutes: st.Spec.TimeoutMinutes, Harness: st.Spec.Harness, Prompt: st.Spec.Prompt,
		Schema: st.Spec.Schema, Browser: st.Spec.Browser, Service: st.Spec.Service,
	}
	if err := c.send(ctx, msg); err != nil {
		return err
	}
	if st.Status == testplan.StatusPending {
		_ = h.st.MarkStage(ctx, st.ID, testplan.StatusSent)
	}
	return h.st.SetCurrentStage(ctx, run.ID, st.ID)
}

// finalize 写入最终结论并通知 agent 收尾。fatal 非空表示测试失败。
func (h *Hub) finalize(ctx context.Context, c *agentConn, run store.Run, stages []store.Stage, fatal string) error {
	recs := store.Records(stages)
	res := testplan.Consolidate(run.Config, recs)
	_, cost := testplan.Usage(recs)
	if err := h.st.SetResult(ctx, run.ID, res, cost); err != nil {
		return err
	}
	if fatal == "" {
		if err := h.st.SetVerdict(ctx, run.ID, res.Verdict()); err != nil {
			return err
		}
	}
	if _, err := h.st.SetStatus(ctx, run.ID, store.StatusFinalizing, fatal, store.StatusRunning); err != nil {
		return err
	}
	_ = h.st.SetCurrentStage(ctx, run.ID, "")
	if c != nil {
		return c.send(ctx, protocol.Finish{Type: protocol.TypeFinish, RunID: run.ID})
	}
	return nil
}

// Approve 确认计划并继续执行。
func (h *Hub) Approve(ctx context.Context, runID, note string) error {
	ok, err := h.st.Approve(ctx, runID, note)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("测试不在等待确认的状态")
	}
	go h.Advance(runID)
	return nil
}

// Cancel 取消测试。排队中的直接取消；已下发的通知 agent 收尾，离线时直接标记。
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
			if h.OnFinish != nil {
				go h.OnFinish(run.ID)
			}
			return nil
		}
		// 恰好刚被派发，继续按执行中处理。
	}
	if c := h.usable(run.RuntimeID); c != nil {
		if err := c.send(ctx, protocol.Cancel{Type: protocol.TypeCancel, RunID: run.ID}); err == nil {
			return nil // agent 收尾后会上报 closed(canceled)
		}
	}
	h.closeRun(ctx, run.ID, store.StatusCanceled, "运行时离线，测试被直接标记为取消", protocol.ClosedResult{})
	return nil
}

// Watchdog 定期兜底：阶段超时、运行时长时间离线、计划长时间未确认、收尾无响应，以及派发排队任务。
func (h *Hub) Watchdog(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.sweep(ctx, time.Now())
	}
}

func (h *Hub) sweep(ctx context.Context, now time.Time) {
	runs, err := h.st.ActiveRuns(ctx)
	if err != nil {
		return
	}
	for _, r := range runs {
		online := h.Online(r.RuntimeID)
		if !online {
			rt, err := h.st.Runtime(ctx, r.RuntimeID)
			if err == nil && (rt.LastSeen == nil || now.Sub(*rt.LastSeen) > offlineLimit) && r.Status != store.StatusAwaiting {
				h.closeRun(ctx, r.ID, store.StatusError, "运行时离线超过 30 分钟，测试中断", protocol.ClosedResult{})
			}
			continue
		}
		switch r.Status {
		case store.StatusAwaiting:
			if r.AwaitingSince != nil && now.Sub(*r.AwaitingSince) > approvalLimit {
				_ = h.Cancel(ctx, r)
			}
		case store.StatusFinalizing:
			if r.FinishedAt == nil && r.StartedAt != nil {
				if st, _ := h.st.Stages(ctx, r.ID); len(st) > 0 {
					last := st[len(st)-1].FinishedAt
					if last != nil && now.Sub(*last) > finalizeLimit {
						status := store.StatusCompleted
						if r.Error != "" {
							status = store.StatusError
						}
						h.closeRun(ctx, r.ID, status, "", protocol.ClosedResult{Diff: "（运行时没有回报收尾结果，源码改动未检查）"})
					}
				}
			}
		case store.StatusRunning:
			stages, err := h.st.Stages(ctx, r.ID)
			if err != nil {
				continue
			}
			for _, st := range stages {
				if testplan.Done(st.Status) {
					continue
				}
				from := st.CreatedAt
				if st.StartedAt != nil {
					from = st.StartedAt
				}
				if from != nil && now.Sub(*from) > time.Duration(st.Spec.TimeoutMinutes)*time.Minute+stageGrace {
					_, _ = h.st.FinishStage(ctx, st.ID, protocol.StageResult{Status: protocol.StageTimeout, Error: "超过时限仍未收到运行时的结果"})
					_ = h.refreshResult(ctx, r)
					go h.Advance(r.ID)
				}
			}
		}
	}
	rts, err := h.st.Runtimes(ctx)
	if err != nil {
		return
	}
	for _, rt := range rts {
		if h.Online(rt.ID) {
			go h.Schedule(rt.ID)
		}
	}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
