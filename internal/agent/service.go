package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
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
	// 安装 / 启动命令交给 sh 执行，占位符由 shell 从环境变量展开；
	// 但健康检查和前端地址是 agent 自己发 HTTP 请求用的，不会经过 shell，必须在这里替换。
	spec.SetupCmd, spec.StartCmd = s.expand(spec.SetupCmd), s.expand(spec.StartCmd)
	spec.HealthURL, spec.AppURL = s.expand(spec.HealthURL), s.expand(spec.AppURL)
	spec.TeardownCmd = s.expand(spec.TeardownCmd)
	st := protocol.ServiceStatus{StartCmd: spec.StartCmd, HealthURL: spec.HealthURL, URL: firstNonEmpty(spec.AppURL, spec.HealthURL),
		TeardownCmd: spec.TeardownCmd}
	env := s.env()

	// 重新启动服务（续跑、重试）时，先清理上一次建的资源，安装命令就能从干净状态重建。
	// 清理命令要在安装之前登记：安装失败时可能已经建了一半的库，收尾时同样要删。
	runTeardown(a.dir, s.runID, s.prep.Dir, logf)
	saveTeardown(a.dir, s.runID, spec.TeardownCmd, env)

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

	for _, u := range []string{spec.HealthURL, spec.AppURL} {
		if strings.Contains(u, "VERDICT_PORT") {
			return nil, st, fmt.Errorf("地址 %q 里的端口占位符没有取到值：这个仓库声明了 %d 个端口，"+
				"请检查命令里用的编号（VERDICT_PORT、VERDICT_PORT1…）和仓库设置里的端口数", u, s.portCount)
		}
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

// validHTTPURL 粗查健康检查 / 前端地址：必须是 http(s) 且带端口，或带默认端口的标准地址。
func validHTTPURL(raw string) error {
	u, err := urlpkg.Parse(raw)
	if err != nil {
		return fmt.Errorf("地址不合法 %q：%v（检查仓库配置里的端口占位符）", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("地址不合法 %q：必须以 http:// 或 https:// 开头", raw)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("地址不合法 %q：缺少主机名", raw)
	}
	// "http://127.0.0.1:" 这类写法会解析成 Host="127.0.0.1:"、端口为空，实际连不上。
	if strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("地址不合法 %q：端口是空的（检查仓库配置里的端口占位符有没有展开）", raw)
	}
	return nil
}

// expand 把配置里的端口占位符替换成实际分配到的端口。
// 支持 VERDICT_PORT（第一个）、VERDICT_PORT1..N（第 n 个）；未分配的编号替换为空串并由调用方报错。
func (s *session) expand(v string) string {
	if !strings.Contains(v, "VERDICT_PORT") {
		return v
	}
		// 与 env() 保持一致：VERDICT_PORT 和 VERDICT_PORT1 都指向第一个端口，PORTn 指向第 n 个。
	for i := 0; i < 9; i++ {
		key := "VERDICT_PORT"
		if i > 0 {
			key = fmt.Sprintf("VERDICT_PORT%d", i)
		}
		idx := max(i-1, 0) // VERDICT_PORT → 0，VERDICT_PORT1 → 0，VERDICT_PORT2 → 1
		val := ""
		if idx < len(s.ports) {
			val = strconv.Itoa(s.ports[idx])
		}
		v = strings.ReplaceAll(v, "${"+key+"}", val)
	}
	return v
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
	// 先把地址解析一次：端口缺失、占位符残留这类问题重试也没用，立刻报错。
	if err := validHTTPURL(url); err != nil {
		return err
	}
	logf("… 等待健康检查 " + url)
	deadline := time.Now().Add(healthTimeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		if !svc.alive() {
			return errors.New("服务进程在健康检查通过前退出")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			// 地址本身不合法（例如残留了没被替换的占位符），重试没有意义。
			return fmt.Errorf("健康检查地址不合法 %q：%v（检查仓库配置里的占位符是否都被平台替换了）", url, err)
		}
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
