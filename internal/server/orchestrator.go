package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
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
		Repo:     testplan.RepoInfo{TestNotes: repo.TestNotes, SetupCmd: repo.SetupCmd, StartCmd: repo.StartCmd, HealthURL: repo.HealthURL, AppURL: repo.AppURL},
		Approved: run.Approved, ApprovalNote: run.ApprovalNote,
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
	if waiting := h.advanceRetries(ctx, run, &stages); waiting {
		return nil // 有阶段正在退避等待，或刚刚创建了重试：先等它
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
		if st.Status == testplan.StatusRetryWait {
			return nil // 等待重试，到点后由看门狗重新推进
		}
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
			st, err := h.st.AddStage(ctx, run.ID, step.Stage, testplan.StatusSkipped, step.Note, 1)
			if err != nil {
				return err
			}
			stages = append(stages, st)
			continue
		case testplan.ActionStage:
			st, err := h.st.AddStage(ctx, run.ID, step.Stage, testplan.StatusPending, step.Note, h.nextAttempt(stages, step.Stage))
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
	h.promoteRetries(ctx, now)
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

// nextAttempt 返回这个角色/轮次这一次的尝试序号（从 1 开始）。
func (h *Hub) nextAttempt(stages []store.Stage, spec testplan.StageSpec) int {
	n := 1
	for _, st := range stages {
		if st.Role == spec.Role && st.Round == spec.Round {
			n++
		}
	}
	return n
}

// retrySettings 读取用户设置（没配时用默认值）。
func (h *Hub) retrySettings(ctx context.Context) (retries int, delay int) {
	retries, delay = testplan.DefaultStageRetries, testplan.DefaultRetryDelaySecs
	if v, err := h.st.Setting(ctx, SettingStageRetries); err == nil && v != "" {
		if n, perr := strconv.Atoi(v); perr == nil {
			retries = max(0, min(n, testplan.MaxStageRetries))
		}
	}
	if v, err := h.st.Setting(ctx, SettingRetryDelay); err == nil && v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			delay = min(n, 3600)
		}
	}
	return
}

// advanceRetries 处理可重试的失败：安排退避等待，到点后创建新的尝试。
// 返回 true 表示调用了流程应该先停在这里（等待中，或刚派发了重试）。
func (h *Hub) advanceRetries(ctx context.Context, run store.Run, stages *[]store.Stage) bool {
	limit, delay := h.retrySettings(ctx)
	now := time.Now()
	var waiting bool
	for _, st := range *stages {
		if st.Status != testplan.StatusRetryWait || st.NextAttempt == nil {
			continue
		}
		if now.Before(*st.NextAttempt) {
			return true // 还没到点
		}
		// 到点：把这个阶段恢复成失败记录，再创建一个新的尝试。
		if err := h.st.ClearStageRetry(ctx, st.ID); err != nil {
			continue
		}
		attempt := h.nextAttempt(*stages, st.Spec)
		spec := st.Spec
		spec.Prompt = retryPrompt(spec.Prompt, st.Error, attempt)
		created, err := h.st.AddStage(ctx, run.ID, spec, testplan.StatusPending, fmt.Sprintf("第 %d 次尝试（上次失败：%s）", attempt, clipText(st.Error, 120)), attempt)
		if err != nil {
			return true
		}
		c := h.usable(run.RuntimeID)
		_ = h.sendStage(ctx, c, run, created)
		log.Printf("测试 %s 阶段 %s/%d 第 %d 次尝试", run.ID, st.Role, st.Round, attempt)
		return true
	}
	// 没有等待中的重试：看看最新的失败阶段要不要安排重试。
	for _, st := range *stages {
		if st.Status != testplan.StatusError && st.Status != testplan.StatusTimeout {
			continue
		}
		if !h.isLatest(*stages, st) {
			continue
		}
		if st.Kind != protocol.StageKindHarness || st.Harness != run.Config.Harness {
			continue // 交叉模型失败交给流水线的降级逻辑
		}
		if !testplan.RetryableFailure(st.FailureKind) {
			continue
		}
		attempts := 0
		for _, x := range *stages {
			if x.Role == st.Role && x.Round == st.Round && x.Kind == protocol.StageKindHarness {
				attempts++
			}
		}
		if attempts >= limit {
			continue // 重试用完，交给流水线降级
		}
		if elapsed, _ := testplan.Usage(store.Records(*stages)); int(elapsed/60000) >= run.Config.MaxMinutes {
			continue // 预算不够
		}
		next := now.Add(time.Duration(testplan.RetryDelaySeconds(delay, attempts+1)) * time.Second)
		if err := h.st.SetStageRetry(ctx, st.ID, next); err != nil {
			log.Printf("设置阶段 %s 重试失败: %v", st.ID, err)
			continue
		}
		log.Printf("测试 %s 阶段 %s/%d 第 %d 次失败（%s），%s 后重试", run.ID, st.Role, st.Round, attempts, st.FailureKind,
			next.Sub(now).Round(time.Second))
		// 事件驱动：到点直接推进，不必等每分钟一次的看门狗。
		runID := run.ID
		time.AfterFunc(next.Sub(now)+time.Second, func() { h.Advance(runID) })
		waiting = true
	}
	return waiting
}

// isLatest 判断这个阶段是不是同一角色/轮次里最后的一条。
func (h *Hub) isLatest(stages []store.Stage, st store.Stage) bool {
	for _, x := range stages {
		if x.Role == st.Role && x.Round == st.Round && x.Seq > st.Seq {
			return false
		}
	}
	return true
}

// retryPrompt 在重试时追加一段说明，不复用上次失败的中间结果。
func retryPrompt(prompt, lastErr string, attempt int) string {
	return prompt + fmt.Sprintf("\n## 重试说明\n这是第 %d 次尝试，上一次执行这个阶段时失败：%s\n"+
		"请从头完成本阶段的任务，不需要参考上次的任何中间结果。\n", attempt, clipText(lastErr, 300))
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// promoteRetries 把到点的重试阶段恢复成失败状态，让流水线重新创建一次尝试。
func (h *Hub) promoteRetries(ctx context.Context, now time.Time) {
	stages, err := h.st.DueRetries(ctx, now)
	if err != nil {
		return
	}
	for _, st := range stages {
		if err := h.st.ClearStageRetry(ctx, st.ID); err != nil {
			continue
		}
		log.Printf("阶段 %s 重试时间已到，重新推进测试 %s", st.ID, st.RunID)
		go h.Advance(st.RunID)
	}
}
