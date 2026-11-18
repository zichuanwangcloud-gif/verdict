package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"verdict/internal/protocol"
)

// 记录的清理命令执行一次后删除记录，带上记录时的环境变量。
func TestRunTeardown(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	mark := filepath.Join(work, "dropped")
	saveTeardown(dir, "r1", "echo $VERDICT_RUN_ID > "+mark, []string{"VERDICT_RUN_ID=r1"})
	runTeardown(dir, "r1", work, func(string) {})
	if b, err := os.ReadFile(mark); err != nil || strings.TrimSpace(string(b)) != "r1" {
		t.Fatalf("清理命令没有按记录执行: %q %v", b, err)
	}
	if _, err := os.Stat(teardownFile(dir, "r1")); !os.IsNotExist(err) {
		t.Fatal("执行后应删除记录")
	}
	os.Remove(mark)
	runTeardown(dir, "r1", work, func(string) {}) // 不能重复执行
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("清理命令被执行了两次")
	}
}

// agent 重启后补执行上次遗留的清理命令。
func TestTeardownLeftovers(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "dropped")
	saveTeardown(dir, "r9", "touch "+mark, nil)
	teardownLeftovers(dir)
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("遗留的清理命令没有执行")
	}
}

// 重新启动服务前先执行上一次的清理命令，并登记新的清理命令（端口占位符已展开）。
func TestStartServiceRegistersTeardown(t *testing.T) {
	a := &Agent{dir: t.TempDir()}
	work := t.TempDir()
	s := &session{runID: "r2", ports: []int{31005}, prep: &Prepared{Dir: work}, tmp: t.TempDir()}
	old := filepath.Join(work, "old-dropped")
	saveTeardown(a.dir, "r2", "touch "+old, nil)

	svc, _, err := a.startService(context.Background(), s, protocol.ServiceSpec{
		StartCmd: "sleep 30", TeardownCmd: "echo ${VERDICT_PORT} > new-dropped",
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	if _, err := os.Stat(old); err != nil {
		t.Fatal("重新启动前应先执行上一次的清理命令")
	}
	svc.Stop()
	runTeardown(a.dir, "r2", work, func(string) {})
	if b, _ := os.ReadFile(filepath.Join(work, "new-dropped")); strings.TrimSpace(string(b)) != "31005" {
		t.Fatalf("新的清理命令没有登记或占位符没展开: %q", b)
	}
}
