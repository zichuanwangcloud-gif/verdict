// Package verifier 定义验证器协议与内置的通用验证器。
//
// 验证器是确定性的执行单元：输入参数，输出通过/未通过与原始输出。
// 模型负责「选哪个验证器、填什么参数」，不负责判断结果。
package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Result struct {
	Passed bool
	Output string
}

type Verifier interface {
	Name() string
	// Describe 给智能体看：这个验证器做什么、参数长什么样。
	Describe() string
	Run(ctx context.Context, params json.RawMessage) Result
}

type Registry struct{ m map[string]Verifier }

func NewRegistry(vs ...Verifier) *Registry {
	r := &Registry{m: map[string]Verifier{}}
	for _, v := range vs {
		r.m[v.Name()] = v
	}
	return r
}

func (r *Registry) Get(name string) (Verifier, bool) { v, ok := r.m[name]; return v, ok }

func (r *Registry) Catalog() []map[string]string {
	var out []map[string]string
	for _, v := range r.m {
		out = append(out, map[string]string{"name": v.Name(), "description": v.Describe()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"] < out[j]["name"] })
	return out
}

func Default() *Registry { return NewRegistry(Command{}, HTTP{}) }

const maxOutput = 16 << 10

func clip(s string) string {
	if len(s) > maxOutput {
		return s[:maxOutput] + "\n…(输出已截断)"
	}
	return s
}

// Command 执行一条命令，退出码 0 为通过。被测项目的测试、脚本、
// 浏览器走查都经由它接入，平台不关心命令背后是什么技术。
type Command struct{}

func (Command) Name() string { return "command" }
func (Command) Describe() string {
	return `执行命令，退出码为 0 即通过。参数: {"argv": ["程序","参数"...], "dir": "工作目录(可选)", "timeout_seconds": 600}`
}

func (Command) Run(ctx context.Context, raw json.RawMessage) Result {
	var p struct {
		Argv    []string `json:"argv"`
		Dir     string   `json:"dir"`
		Timeout int      `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Argv) == 0 {
		return Result{Output: fmt.Sprintf("参数无效: 需要非空 argv (%v)", err)}
	}
	if p.Timeout <= 0 {
		p.Timeout = 600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	cmd.Dir = p.Dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		out += "\n[" + err.Error() + "]"
	}
	return Result{Passed: err == nil, Output: clip(out)}
}

// HTTP 发一个请求，断言状态码与响应体包含的文本。
type HTTP struct{}

func (HTTP) Name() string { return "http" }
func (HTTP) Describe() string {
	return `发送请求并断言响应。参数: {"method":"GET","url":"...","headers":{},"body":"","expect_status":200,"expect_contains":"可选文本"}`
}

func (HTTP) Run(ctx context.Context, raw json.RawMessage) Result {
	var p struct {
		Method         string            `json:"method"`
		URL            string            `json:"url"`
		Headers        map[string]string `json:"headers"`
		Body           string            `json:"body"`
		ExpectStatus   int               `json:"expect_status"`
		ExpectContains string            `json:"expect_contains"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.URL == "" {
		return Result{Output: fmt.Sprintf("参数无效: 需要 url (%v)", err)}
	}
	if p.Method == "" {
		p.Method = http.MethodGet
	}
	if p.ExpectStatus == 0 {
		p.ExpectStatus = http.StatusOK
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, strings.NewReader(p.Body))
	if err != nil {
		return Result{Output: err.Error()}
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{Output: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, body)
	ok := resp.StatusCode == p.ExpectStatus &&
		(p.ExpectContains == "" || strings.Contains(string(body), p.ExpectContains))
	return Result{Passed: ok, Output: clip(out)}
}
