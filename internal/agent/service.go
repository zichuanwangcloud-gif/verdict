package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"verdict/internal/protocol"
)

const (
	serviceLogLines = 2000             // 回传到执行流的服务日志行数上限
	healthTimeout   = 3 * time.Minute  // 健康检查最长等待
	noHealthWait    = 5 * time.Second  // 没有健康检查地址时，等待后确认进程仍存活
	setupMaxOutput  = 4000             // 失败时回传的输出尾部字数
	stopGrace       = 10 * time.Second // 结束服务时 SIGTERM 到 SIGKILL 的宽限
)

// service 是 agent 托管的被测服务：在独立进程组中运行，直到测试收尾。
type service struct {
	cmd     *exec.Cmd
	pidFile string
	logFile string
	done    chan struct{}
	mu      sync.Mutex
	tail    []string
}

func (s *service) addTail(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = append(s.tail, line)
	if len(s.tail) > 60 {
		s.tail = s.tail[len(s.tail)-60:]
	}
}

func (s *service) Tail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clipTail(strings.Join(s.tail, "\n"), setupMaxOutput)
}

func (s *service) alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Stop 结束整个进程组。
func (s *service) Stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	pgid := s.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(stopGrace):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	_ = os.Remove(s.pidFile)
}

func clipTail(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

func shellCmd(ctx context.Context, dir, script string, env []string) *exec.Cmd {
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	} else {
		cmd = exec.Command("sh", "-c", script)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// runSetup 同步执行安装命令，输出逐行回传；超时或取消时结束整个进程组。
func runSetup(ctx context.Context, dir, script string, env []string, logf func(string)) (string, error) {
	cmd := shellCmd(nil, dir, script, env)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var out strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		n := 0
		for sc.Scan() {
			line := sc.Text()
			out.WriteString(line + "\n")
			if n++; n <= serviceLogLines {
				logf("🖥 " + clip(line, 500))
			}
		}
	}()
	if err := cmd.Start(); err != nil {
		pw.Close()
		wg.Wait()
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		err = context.Cause(ctx)
	}
	pw.Close()
	wg.Wait()
	return clipTail(out.String(), setupMaxOutput), err
}

// startService 执行安装命令、后台启动服务，并等待健康检查通过。
func (a *Agent) startService(ctx context.Context, s *session, spec protocol.ServiceSpec, logf func(string)) (*service, protocol.ServiceStatus, error) {
	st := protocol.ServiceStatus{StartCmd: spec.StartCmd, HealthURL: spec.HealthURL, URL: firstNonEmpty(spec.AppURL, spec.HealthURL)}
	env := s.env()

	if strings.TrimSpace(spec.SetupCmd) != "" {
		logf("▶ 安装依赖：" + spec.SetupCmd)
		out, err := runSetup(ctx, s.prep.Dir, spec.SetupCmd, env, logf)
		if err != nil {
			st.LogTail = out
			return nil, st, fmt.Errorf("安装依赖失败: %v", err)
		}
	}

	logDir := filepath.Join(s.tmp, "service")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, st, err
	}
	st.LogFile = filepath.Join(logDir, "service.log")
	lf, err := os.Create(st.LogFile)
	if err != nil {
		return nil, st, err
	}

	logf("▶ 启动服务：" + spec.StartCmd)
	cmd := shellCmd(nil, s.prep.Dir, spec.StartCmd, env)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		lf.Close()
		pw.Close()
		return nil, st, fmt.Errorf("启动服务失败: %w", err)
	}
	svc := &service{cmd: cmd, logFile: st.LogFile, done: make(chan struct{}), pidFile: filepath.Join(a.dir, "pids", s.runID+".pgid")}
	_ = os.MkdirAll(filepath.Dir(svc.pidFile), 0o700)
	_ = os.WriteFile(svc.pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		n := 0
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(lf, line)
			svc.addTail(line)
			if n++; n <= serviceLogLines {
				logf("🖥 " + clip(line, 500))
			} else if n == serviceLogLines+1 {
				logf(fmt.Sprintf("🖥 （服务日志超过 %d 行，后续只写入 %s）", serviceLogLines, st.LogFile))
			}
		}
		lf.Close()
	}()
	go func() {
		_ = cmd.Wait()
		pw.Close()
		close(svc.done)
	}()

	if err := waitHealthy(ctx, svc, spec.HealthURL, logf); err != nil {
		st.LogTail = svc.Tail()
		svc.Stop()
		return nil, st, err
	}
	st.Running = true
	logf("✔ 服务已就绪" + map[bool]string{true: "：" + st.URL, false: ""}[st.URL != ""])
	return svc, st, nil
}

func waitHealthy(ctx context.Context, svc *service, url string, logf func(string)) error {
	if strings.TrimSpace(url) == "" {
		select {
		case <-svc.done:
			return errors.New("服务进程启动后立即退出")
		case <-time.After(noHealthWait):
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	logf("… 等待健康检查 " + url)
	deadline := time.Now().Add(healthTimeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		if !svc.alive() {
			return errors.New("服务进程在健康检查通过前退出")
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			last = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("健康检查 %s 在 %s 内没有通过（最后一次：%s）", url, healthTimeout, last)
}

// killLeftovers 结束上次 agent 进程退出时残留的服务进程组。
func killLeftovers(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "pids", "*.pgid"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if pgid, perr := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && perr == nil && pgid > 1 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = os.Remove(f)
	}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
