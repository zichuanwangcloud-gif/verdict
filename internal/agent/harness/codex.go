package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"verdict/internal/protocol"
)

// Codex 通过 `codex exec --json --output-schema` 运行 Codex CLI。
type Codex struct{}

func (Codex) Name() string { return "codex" }

func (Codex) Detect(ctx context.Context) protocol.HarnessInfo {
	info := protocol.HarnessInfo{Name: "codex"}
	if _, err := exec.LookPath("codex"); err != nil {
		info.Detail = "PATH 中找不到 codex"
		return info
	}
	info.Installed = true
	if v, err := quick(ctx, 15*time.Second, "codex", "--version"); err == nil {
		info.Version = firstLine(v)
	}
	out, err := quick(ctx, 30*time.Second, "codex", "login", "status")
	if err == nil {
		info.LoggedIn = true
		info.Detail = clip(firstLine(out), 120)
	} else {
		info.Detail = "未登录：请在该机器上执行 codex login（" + clip(firstLine(out), 120) + "）"
	}
	return info
}

func (Codex) Run(ctx context.Context, spec RunSpec) (Output, error) {
	if err := os.MkdirAll(spec.TempDir, 0o700); err != nil {
		return Output{}, err
	}
	schemaFile := filepath.Join(spec.TempDir, "codex-schema.json")
	lastFile := filepath.Join(spec.TempDir, "codex-last-message.json")
	if err := os.WriteFile(schemaFile, spec.Schema, 0o600); err != nil {
		return Output{}, err
	}
	_ = os.Remove(lastFile)

	args := []string{"exec", "--json", "--output-schema", schemaFile, "-o", lastFile,
		"--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-C", spec.Dir, "-"}

	var failMsg string
	err := runStreaming(ctx, spec, "codex", args, func(line []byte) {
		if m := codexLine(line, spec.Log); m != "" {
			failMsg = m
		}
	})
	if err != nil {
		return Output{}, err
	}
	b, rerr := os.ReadFile(lastFile)
	if rerr != nil || len(strings.TrimSpace(string(b))) == 0 {
		if failMsg != "" {
			return Output{}, fmt.Errorf("Codex 执行失败: %s", clip(failMsg, 500))
		}
		return Output{}, ErrNoResult
	}
	s := strings.TrimSpace(string(b))
	if !json.Valid([]byte(s)) {
		return Output{}, fmt.Errorf("Codex 最终输出不是 JSON: %s", clip(s, 300))
	}
	return Output{Structured: json.RawMessage(s)}, nil
}

// codexLine 把一行 codex exec --json 事件翻译成日志；遇到失败事件时返回错误信息。
func codexLine(line []byte, log LogFunc) string {
	var ev struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
		Item *struct {
			Type             string `json:"type"`
			Text             string `json:"text"`
			Command          string `json:"command"`
			AggregatedOutput string `json:"aggregated_output"`
			ExitCode         *int   `json:"exit_code"`
			Status           string `json:"status"`
			Changes          []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"changes"`
			Server string `json:"server"`
			Tool   string `json:"tool"`
			Query  string `json:"query"`
		} `json:"item"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		if s := strings.TrimSpace(string(line)); s != "" {
			log(clip(s, 2000))
		}
		return ""
	}
	switch ev.Type {
	case "thread.started":
		log("▶ Codex 会话开始")
	case "turn.failed":
		msg := "未知错误"
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		log("⚠ Codex 回合失败: " + clip(msg, 1000))
		return msg
	case "error":
		log("⚠ " + clip(ev.Message, 1000))
		return ev.Message
	case "turn.completed":
		log("■ Codex 回合结束")
	case "item.started":
		if ev.Item != nil && ev.Item.Type == "command_execution" {
			log("🔧 $ " + clip(ev.Item.Command, 1000))
		}
	case "item.completed":
		it := ev.Item
		if it == nil {
			return ""
		}
		switch it.Type {
		case "agent_message":
			log("💬 " + clip(it.Text, 4000))
		case "reasoning":
			if t := strings.TrimSpace(it.Text); t != "" {
				log("💭 " + clip(t, 1000))
			}
		case "command_execution":
			code := "?"
			if it.ExitCode != nil {
				code = fmt.Sprint(*it.ExitCode)
			}
			log(fmt.Sprintf("   ↳ 退出码 %s %s", code, clip(it.AggregatedOutput, 2000)))
		case "file_change":
			var ps []string
			for _, c := range it.Changes {
				ps = append(ps, c.Kind+" "+c.Path)
			}
			log("✏️ 修改文件: " + strings.Join(ps, ", "))
		case "mcp_tool_call":
			log("🔧 MCP " + it.Server + "/" + it.Tool)
		case "web_search":
			log("🔎 " + it.Query)
		}
	}
	return ""
}
