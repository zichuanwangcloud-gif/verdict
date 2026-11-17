package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"verdict/internal/protocol"
	"verdict/internal/testplan"
)

// 旧版（v1）数据库：没有新列、没有 stages 表。升级后旧数据可读、新功能可用。
const v1Schema = `
CREATE TABLE runtimes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
  info TEXT NOT NULL DEFAULT 'null', last_seen TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE repos (id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
  test_notes TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE runs (id TEXT PRIMARY KEY, repo_id INTEGER NOT NULL, branch TEXT NOT NULL, commit_sha TEXT NOT NULL DEFAULT '',
  issue_key TEXT NOT NULL, issue TEXT NOT NULL, runtime_id INTEGER NOT NULL, harness TEXT NOT NULL, timeout_minutes INTEGER NOT NULL,
  status TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', acs TEXT NOT NULL DEFAULT '[]',
  raw TEXT NOT NULL DEFAULT '', prompt TEXT NOT NULL, source_modified INTEGER NOT NULL DEFAULT 0, diff TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', linear_comment_url TEXT NOT NULL DEFAULT '',
  linear_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, started_at TEXT NOT NULL DEFAULT '', finished_at TEXT NOT NULL DEFAULT '');
CREATE TABLE run_logs (run_id TEXT NOT NULL, seq INTEGER NOT NULL, ts INTEGER NOT NULL, text TEXT NOT NULL, PRIMARY KEY (run_id, seq));
INSERT INTO runtimes VALUES (1,'旧机器','h','null','','2026-11-17T00:00:00Z');
INSERT INTO repos VALUES (1,'git@x:o/r.git','o/r','老说明','2026-11-17T00:00:00Z');
INSERT INTO runs (id,repo_id,branch,issue_key,issue,runtime_id,harness,timeout_minutes,status,verdict,acs,prompt,created_at)
  VALUES ('old1',1,'main','ENG-1','{"identifier":"ENG-1","title":"旧"}',1,'claude',60,'completed','pass','[{"id":"AC-1","status":"pass"}]','旧提示词','2026-11-17T00:00:00Z');
INSERT INTO run_logs VALUES ('old1',1,1,'旧日志');`

func TestMigrateFromV1(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(v1Schema); err != nil {
		t.Fatal(err)
	}
	db.Close()

	ctx := context.Background()
	for i := 0; i < 2; i++ { // 第二次打开验证迁移幂等
		st, err := Open(p)
		if err != nil {
			t.Fatalf("第 %d 次打开失败: %v", i+1, err)
		}
		old, err := st.Run(ctx, "old1")
		if err != nil || !old.Legacy() || old.Verdict != "pass" || old.Prompt != "旧提示词" || len(old.ACs) != 1 {
			t.Fatalf("旧记录读取不对: %v %+v", err, old)
		}
		logs, _ := st.Logs(ctx, "old1", 0, 10)
		if len(logs) != 1 || logs[0].Stage != "" {
			t.Fatalf("旧日志不对: %+v", logs)
		}
		repo, _ := st.Repo(ctx, 1)
		repo.StartCmd = "make run"
		if err := st.UpdateRepo(ctx, repo); err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
}

func TestStagesLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, _, _ := st.CreateRuntime(ctx, "rt")
	repo, _ := st.CreateRepo(ctx, Repo{URL: "u", Name: "n", AppURL: "http://x"})
	cfg := testplan.Preset("A")
	cfg.Harness = "claude"
	run, err := st.CreateRun(ctx, NewRun{RepoID: repo.ID, Branch: "b", RuntimeID: rt.ID, Config: cfg})
	if err != nil || run.Tier != "A" || run.Config.RedRounds != 1 || run.TimeoutMinutes != 90 || run.Legacy() {
		t.Fatalf("%v %+v", err, run)
	}
	s1, _ := st.AddStage(ctx, run.ID, testplan.StageSpec{Role: "plan", Kind: "harness", Harness: "claude", Prompt: "p"}, testplan.StatusPending, "")
	s2, _ := st.AddStage(ctx, run.ID, testplan.StageSpec{Role: "ui"}, testplan.StatusSkipped, "预算")
	if s1.Seq != 1 || s2.Seq != 2 || s2.FinishedAt == nil || s1.Spec.Prompt != "p" {
		t.Fatalf("阶段不对: %+v %+v", s1, s2)
	}
	ok, _ := st.FinishStage(ctx, s1.ID, protocol.StageResult{Status: "ok", Structured: []byte(`{"a":1}`), DurationMS: 5, Artifacts: []string{"x.png"}})
	again, _ := st.FinishStage(ctx, s1.ID, protocol.StageResult{Status: "error"})
	if !ok || again {
		t.Fatal("阶段结果只能写一次")
	}
	stages, _ := st.Stages(ctx, run.ID)
	if len(stages) != 2 || string(stages[0].Output) != `{"a":1}` || stages[0].Artifacts[0] != "x.png" || stages[0].StartedAt == nil {
		t.Fatalf("%+v", stages)
	}
	if ok, _ := st.SetStatus(ctx, run.ID, StatusAwaiting, ""); !ok {
		t.Fatal()
	}
	if ok, _ := st.Approve(ctx, run.ID, "注意退款"); !ok {
		t.Fatal("应能确认")
	}
	run, _ = st.Run(ctx, run.ID)
	if !run.Approved || run.Status != StatusRunning || run.ApprovalNote != "注意退款" {
		t.Fatalf("%+v", run)
	}
	if ok, _ := st.CloseRun(ctx, run.ID, StatusCompleted, "", protocol.ClosedResult{SourceModified: true, Diff: "d"}); !ok {
		t.Fatal()
	}
	if ok, _ := st.CloseRun(ctx, run.ID, StatusError, "x", protocol.ClosedResult{}); ok {
		t.Fatal("终态不应被覆盖")
	}
}
