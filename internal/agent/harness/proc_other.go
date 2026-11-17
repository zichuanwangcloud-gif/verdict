//go:build !unix

package harness

import (
	"os/exec"
	"time"
)

// 非 unix 平台只做尽力而为的清理（MVP 的运行时只支持 Linux / macOS）。
func setProcessGroup(cmd *exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd, grace time.Duration, done <-chan error) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-done
}

func killGroup(cmd *exec.Cmd) {}

func isRoot() bool { return false }
