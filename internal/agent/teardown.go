package agent

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const teardownTimeout = 2 * time.Minute

// teardown 是测试收尾时要执行的清理命令（删除本次在共享容器里建的独立库等）。
// 它会落盘，agent 进程意外退出后，下次启动时补执行，避免临时库残留。
type teardown struct {
	Cmd string   `json:"cmd"`
	Env []string `json:"env"`
}

func teardownFile(dir, runID string) string {
	return filepath.Join(dir, "pids", runID+".teardown.json")
}

// saveTeardown 记录本次测试的清理命令；cmd 为空时删除记录。
func saveTeardown(dir, runID, cmd string, env []string) {
	f := teardownFile(dir, runID)
	if strings.TrimSpace(cmd) == "" {
		_ = os.Remove(f)
		return
	}
	b, _ := json.Marshal(teardown{Cmd: cmd, Env: env})
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	_ = os.WriteFile(f, b, 0o600)
}

// runTeardown 执行并删除记录的清理命令。失败只记日志，不影响测试结论。
func runTeardown(dir, runID, workDir string, logf func(string)) {
	f := teardownFile(dir, runID)
	b, err := os.ReadFile(f)
	if err != nil {
		return
	}
	_ = os.Remove(f)
	var t teardown
	if json.Unmarshal(b, &t) != nil || strings.TrimSpace(t.Cmd) == "" {
		return
	}
	if workDir == "" {
		workDir = dir
	} else if _, err := os.Stat(workDir); err != nil {
		workDir = dir
	}
	logf("▶ 清理测试资源：" + t.Cmd)
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	out, err := runSetup(ctx, workDir, t.Cmd, t.Env, logf)
	if err != nil {
		logf("⚠ 清理命令失败：" + err.Error() + "\n" + out)
	}
}

// teardownLeftovers 补执行上次 agent 进程退出时没来得及执行的清理命令。
func teardownLeftovers(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "pids", "*.teardown.json"))
	for _, f := range files {
		runID := strings.TrimSuffix(filepath.Base(f), ".teardown.json")
		log.Printf("补执行测试 %s 的清理命令", runID)
		runTeardown(dir, runID, "", func(s string) { log.Print(s) })
	}
}
