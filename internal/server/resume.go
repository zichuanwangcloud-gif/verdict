package server

import (
	"context"
	"errors"
	"net/http"

	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// resumeRun 从一次失败的测试继续：复用计划与配置，把已成功的阶段带过去，生成一条新测试。
func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	old, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	run, code, err := s.resume(r.Context(), old)
	if err != nil {
		fail(w, code, err.Error())
		return
	}
	ok(w, run)
}

func (s *Server) resume(ctx context.Context, old store.Run) (store.Run, int, error) {
	if old.Status == store.StatusCanceled {
		return store.Run{}, http.StatusBadRequest, errors.New("被取消的测试不能续跑，请重新测试")
	}
	if store.Active(old.Status) || old.Status == store.StatusQueued {
		return store.Run{}, http.StatusConflict, errors.New("测试还在进行中，先等它结束或取消")
	}
	// 只有出错、或因为覆盖强度降级而完成的测试可以续跑。
	if old.Status != store.StatusError && !(old.Status == store.StatusCompleted && old.Coverage.Degraded) {
		return store.Run{}, http.StatusBadRequest, errors.New("只有出错或覆盖强度降级的测试可以续跑；其余情况请直接重新测试")
	}
	done, err := s.st.Stages(ctx, old.ID)
	if err != nil {
		return store.Run{}, http.StatusInternalServerError, err
	}
	okCount := 0
	for _, st := range done {
		if st.Status == testplan.StatusOK || st.Status == testplan.StatusSkipped {
			okCount++
		}
	}
	if okCount == 0 {
		return store.Run{}, http.StatusBadRequest, errors.New("这条测试还没有任何完成的阶段，直接重新测试即可")
	}
	n := store.NewRun{RepoID: old.RepoID, Branch: old.Branch, Issue: old.Issue, RuntimeID: old.RuntimeID, Config: old.Config,
		VersionID: old.VersionID, CaseID: old.CaseID, PinRef: old.PinRef, PinSHA: old.PinSHA,
		PlanSource: old.PlanSource, PresetPlan: old.PresetPlan, ParentRunID: old.ID, Attempt: 1}
	run, err := s.st.CreateRun(ctx, n)
	if err != nil {
		return store.Run{}, http.StatusInternalServerError, err
	}
	if _, err := s.st.CopyStages(ctx, old.ID, run.ID); err != nil {
		return store.Run{}, http.StatusInternalServerError, err
	}
	run, _ = s.st.Run(ctx, run.ID)
	go s.hub.Schedule(run.RuntimeID)
	return run, 0, nil
}
