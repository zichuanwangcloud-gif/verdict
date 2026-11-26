package store

import (
	"context"
	"path/filepath"
	"testing"

	"verdict/internal/protocol"
	"verdict/internal/testplan"
)

func newTestRun(t *testing.T, st *Store, rt Runtime, branch string) Run {
	t.Helper()
	repo, err := st.CreateRepo(context.Background(), Repo{URL: "git@example.com:o/" + branch + ".git", Name: "o/" + branch})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testplan.Preset("B")
	cfg.Harness = "claude"
	run, err := st.CreateRun(context.Background(), NewRun{RepoID: repo.ID, Branch: branch, RuntimeID: rt.ID, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetStatus(context.Background(), run.ID, StatusRunning, "", StatusQueued); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestServiceSlotsSetting(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, _, err := st.CreateRuntime(ctx, "rt")
	if err != nil {
		t.Fatal(err)
	}
	if rt.ServiceSlots != 0 {
		t.Fatalf("新运行时的服务槽位应为 0（自动），得到 %d", rt.ServiceSlots)
	}
	if err := st.SetServiceSlots(ctx, rt.ID, 2); err != nil {
		t.Fatal(err)
	}
	rt, err = st.Runtime(ctx, rt.ID)
	if err != nil || rt.ServiceSlots != 2 {
		t.Fatalf("服务槽位没有保存: %v %+v", err, rt)
	}
	if err := st.SetServiceSlots(ctx, 9999, 1); err != ErrNotFound {
		t.Fatalf("不存在的运行时应返回 ErrNotFound，得到 %v", err)
	}
}

func TestCountServiceHolders(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, _, _ := st.CreateRuntime(ctx, "rt")
	run := newTestRun(t, st, rt, "b1")

	// 排队中的测试不占槽位。
	if n, err := st.CountServiceHolders(ctx, rt.ID, ""); err != nil || n != 0 {
		t.Fatalf("初始占用应为 0: %d %v", n, err)
	}

	// 服务阶段一开始就先占位，避免多个测试同时启动服务。
	stage, err := st.AddStage(ctx, run.ID, testplan.StageSpec{Role: "service", Kind: protocol.StageKindService}, testplan.StatusPending, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountServiceHolders(ctx, rt.ID, ""); n != 1 {
		t.Fatalf("pending 服务阶段应占 1 个槽位，得到 %d", n)
	}
	// 自己的槽位不算在「别人占用」里。
	if n, _ := st.CountServiceHolders(ctx, rt.ID, run.ID); n != 0 {
		t.Fatalf("排除自己后应为 0，得到 %d", n)
	}

	// 服务启动失败 -> 释放。
	if _, err := st.FinishStage(ctx, stage.ID, protocol.StageResult{Status: protocol.StageError, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountServiceHolders(ctx, rt.ID, ""); n != 0 {
		t.Fatalf("服务失败后应释放槽位，得到 %d", n)
	}

	// 计划判断不需要服务 -> 跳过，不占槽位。
	if _, err := st.AddStage(ctx, run.ID, testplan.StageSpec{Role: "service", Kind: protocol.StageKindService}, testplan.StatusSkipped, "不需要", 1); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountServiceHolders(ctx, rt.ID, ""); n != 0 {
		t.Fatalf("跳过的服务阶段不占槽位，得到 %d", n)
	}

	// 测试结束 -> 即使服务阶段是 ok，也不再占槽位。
	stage2, err := st.AddStage(ctx, run.ID, testplan.StageSpec{Role: "service", Kind: protocol.StageKindService}, testplan.StatusOK, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = stage2
	if n, _ := st.CountServiceHolders(ctx, rt.ID, ""); n != 1 {
		t.Fatalf("ok 服务阶段应占 1 个槽位，得到 %d", n)
	}
	if _, err := st.SetStatus(ctx, run.ID, StatusCompleted, "", StatusRunning); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountServiceHolders(ctx, rt.ID, ""); n != 0 {
		t.Fatalf("测试结束后不应再占槽位，得到 %d", n)
	}
}

// 续跑时服务阶段必须重新执行，不能被复制成「已完成」。
func TestCopyStagesSkipsService(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, _, _ := st.CreateRuntime(ctx, "rt")
	src := newTestRun(t, st, rt, "src")
	for _, spec := range []testplan.StageSpec{
		{Role: "plan", Kind: protocol.StageKindHarness},
		{Role: "service", Kind: protocol.StageKindService},
		{Role: "verify", Kind: protocol.StageKindHarness},
	} {
		if _, err := st.AddStage(ctx, src.ID, spec, testplan.StatusOK, "", 1); err != nil {
			t.Fatal(err)
		}
	}
	dst := newTestRun(t, st, rt, "dst")
	n, err := st.CopyStages(ctx, src.ID, dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应复制 2 个非服务阶段，得到 %d", n)
	}
	stages, _ := st.Stages(ctx, dst.ID)
	for _, s := range stages {
		if s.Kind == protocol.StageKindService {
			t.Fatal("续跑不应复制服务阶段：新会话里服务需要重新拉起")
		}
	}
}
