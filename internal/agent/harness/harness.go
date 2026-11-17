// Package harness 封装运行时上可用的编码 agent（Claude Code、Codex）。
//
// 每个适配器负责三件事：探测是否可用、以无交互方式启动、把流式事件翻译成可读日志
// 并取回结构化结果。新增 harness 只需实现 Harness 接口并在 All 中注册。
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"verdict/internal/protocol"
)

type Output struct {
	Structured json.RawMessage // 符合 schema 的结构化结果
	CostUSD    float64
}

// LogFunc 接收一行可读日志。
type LogFunc func(string)

// MCPServer 是挂载给 harness 的一个 stdio MCP 服务（例如无头浏览器）。
type MCPServer struct {
	Name    string
	Command string
	Args    []string
}

type RunSpec struct {
	Dir     string // 工作目录（被测代码）
	TempDir string // 适配器可用的临时目录（在工作目录之外）
	Prompt  string
	Schema  json.RawMessage
	Env     []string   // 追加的环境变量
	MCP     *MCPServer // 可选：本阶段挂载的 MCP 服务
	Log     LogFunc
}

type Harness interface {
	Name() string
	Detect(ctx context.Context) protocol.HarnessInfo
	Run(ctx context.Context, spec RunSpec) (Output, error)
}

func All() []Harness { return []Harness{Claude{}, Codex{}} }

// Override 供测试替换某个名字对应的实现。
var Override = map[string]Harness{}

func Get(name string) (Harness, bool) {
	if h, ok := Override[name]; ok {
		return h, true
	}
	for _, h := range All() {
		if h.Name() == name {
			return h, true
		}
	}
	return nil, false
}

// ---- 工具函数 ----

// quick 执行一条短命令，返回合并输出与错误。
func quick(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + fmt.Sprintf("…（共 %d 字）", len(r))
	}
	return s
}

// runStreaming 在独立进程组中运行命令：stdin 写入 prompt，stdout 每行交给 onLine，
// stderr 转成日志。主进程退出后立即结束整个进程组，避免后台残留进程拖住管道。
func runStreaming(ctx context.Context, spec RunSpec, name string, args []string, onLine func([]byte)) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	setProcessGroup(cmd)

	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return fmt.Errorf("启动 %s 失败: %w", name, err)
	}
	// 子进程已继承写端，父进程关闭自己的副本，进程组全部退出后读端才能读到 EOF。
	outW.Close()
	errW.Close()

	readers := make(chan struct{}, 2)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			onLine(sc.Bytes())
		}
		readers <- struct{}{}
	}()
	go func() {
		sc := bufio.NewScanner(errR)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				spec.Log("[stderr] " + clip(t, 2000))
			}
		}
		readers <- struct{}{}
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		terminateGroup(cmd, 10*time.Second, done)
		waitErr = context.Cause(ctx)
	}
	// 主进程已退出：清理同组的残留进程（例如模型拉起的开发服务器）。
	killGroup(cmd)
	for i := 0; i < 2; i++ {
		select {
		case <-readers:
		case <-time.After(5 * time.Second):
			// 有进程脱离了进程组仍持有管道，直接放弃读取。
			outR.Close()
			errR.Close()
		}
	}
	outR.Close()
	errR.Close()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return waitErr
}

var ErrNoResult = errors.New("harness 结束但没有给出结构化结果")
