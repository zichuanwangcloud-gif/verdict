package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"verdict/internal/protocol"
)

// Claude 通过 `claude -p --output-format stream-json --json-schema` 运行 Claude Code。
type Claude struct{}

func (Claude) Name() string { return "claude" }

func (Claude) Detect(ctx context.Context) protocol.HarnessInfo {
	info := protocol.HarnessInfo{Name: "claude"}
	if _, err := exec.LookPath("claude"); err != nil {
		info.Detail = "PATH 中找不到 claude"
		return info
	}
	info.Installed = true
	if v, err := quick(ctx, 15*time.Second, "claude", "--version"); err == nil {
		info.Version = firstLine(v)
	}
	out, err := quick(ctx, 30*time.Second, "claude", "auth", "status")
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Provider   string `json:"apiProvider"`
	}
	if jerr := json.Unmarshal([]byte(out), &st); jerr == nil {
		info.LoggedIn = st.LoggedIn
		if st.LoggedIn {
			info.Detail = strings.Trim(st.AuthMethod+" / "+st.Provider, " /")
		} else {
			info.Detail = "未登录：请在该机器上执行 claude 完成登录"
		}
	} else if err != nil {
		info.Detail = "无法获取登录状态: " + clip(out, 200)
	}
	if isRoot() {
		info.Detail = strings.TrimSpace(info.Detail + "；注意：以 root 运行时 Claude Code 会拒绝跳过权限确认，请用普通用户运行 agent")
	}
	return info
}

func (Claude) Run(ctx context.Context, spec RunSpec) (Output, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose",
		"--json-schema", string(spec.Schema), "--dangerously-skip-permissions"}

	var result *claudeResult
	err := runStreaming(ctx, spec, "claude", args, func(line []byte) {
		if r := claudeLine(line, spec.Log); r != nil {
			result = r
		}
	})
	if err != nil {
		return Output{}, err
	}
	if result == nil {
		return Output{}, ErrNoResult
	}
	out := Output{CostUSD: result.TotalCostUSD}
	if result.IsError {
		return out, fmt.Errorf("Claude Code 执行失败（%s）: %s", result.Subtype, clip(result.Result, 500))
	}
	switch {
	case len(result.StructuredOutput) > 0 && string(result.StructuredOutput) != "null":
		out.Structured = result.StructuredOutput
	case json.Valid([]byte(result.Result)):
		out.Structured = json.RawMessage(result.Result)
	default:
		return out, ErrNoResult
	}
	return out, nil
}

type claudeResult struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
}

// claudeLine 把一行 stream-json 事件翻译成日志；遇到 result 事件时返回它。
func claudeLine(line []byte, log LogFunc) *claudeResult {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Model   string `json:"model"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		if s := strings.TrimSpace(string(line)); s != "" {
			log(clip(s, 2000))
		}
		return nil
	}
	sub := ""
	if ev.ParentToolUseID != nil && *ev.ParentToolUseID != "" {
		sub = "  [子任务] "
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			log("▶ Claude Code 会话开始，模型 " + ev.Model)
		}
	case "assistant":
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(ev.Message.Content, &blocks)
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					log(sub + "💬 " + clip(t, 4000))
				}
			case "tool_use":
				if b.Name == "StructuredOutput" {
					log(sub + "📝 提交结构化结果")
					continue
				}
				log(sub + "🔧 " + b.Name + " " + toolInput(b.Input))
			}
		}
	case "user":
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return nil
		}
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			txt := blockText(b.Content)
			if txt == "" || txt == "Structured output provided successfully" {
				continue
			}
			mark := "↳ "
			if b.IsError {
				mark = "↳ ⚠ "
			}
			log(sub + "   " + mark + clip(txt, 2000))
		}
	case "result":
		var r claudeResult
		if err := json.Unmarshal(line, &r); err != nil {
			log("⚠ 无法解析 result 事件: " + err.Error())
			return nil
		}
		log(fmt.Sprintf("■ Claude Code 结束（%s），费用 $%.4f", r.Subtype, r.TotalCostUSD))
		return &r
	}
	return nil
}

// toolInput 挑出最有信息量的参数，其余截断展示。
func toolInput(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return clip(string(raw), 500)
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if v, ok := m[k].(string); ok && v != "" {
			return clip(v, 1000)
		}
	}
	return clip(string(raw), 500)
}

func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}
