// Package testplan 负责把「需求 + 仓库说明」变成交给 harness 的提示词和结果 Schema。
//
// 这是「怎么让模型测」的唯一入口，后续优化测试流程主要改这里。
package testplan

import (
	"encoding/json"
	"fmt"
	"strings"

	"verdict/internal/linear"
)

type Input struct {
	RunID     string
	Issue     linear.Issue
	RepoURL   string
	Branch    string
	TestNotes string
}

// Schema 是两种 harness 共用的严格结构（Codex 要求 additionalProperties=false 且全部 required）。
var Schema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "acs"],
  "properties": {
    "summary": {"type": "string"},
    "acs": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "criterion", "status", "evidence", "reasoning"],
        "properties": {
          "id": {"type": "string"},
          "criterion": {"type": "string"},
          "status": {"type": "string", "enum": ["pass", "fail", "unverifiable"]},
          "evidence": {"type": "string"},
          "reasoning": {"type": "string"}
        }
      }
    }
  }
}`)

func Prompt(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, `你是一名严格的功能验收测试工程师。当前工作目录是仓库 %s 在分支 %s 上的一份干净副本。
你的任务：逐条验证下面这个需求的验收标准在这份代码上是否达成，并给出证据。

## 测试纪律
1. 不要修改被测源码去「让测试通过」。可以新建临时脚本、临时测试文件，但不要改动已有的源码文件；平台会检查源码改动并在报告里标出。
2. 优先做真实的功能验证：安装依赖、构建、启动服务、发请求/执行命令/运行测试，观察实际行为。只读代码得出的结论要在 reasoning 里说明。
3. 每条结论都必须附证据：执行了什么命令或请求、关键输出是什么。没有证据的不能判 pass。
4. 因为环境、依赖、凭据缺失等原因无法验证的，判 unverifiable，并在 reasoning 里写清缺什么；不要猜测，不要硬判 pass。
5. 如果需要用 docker compose 启动依赖，项目名必须用 -p verdict-%s，平台会在结束后清理。启动的后台进程在结束前尽量关闭。
6. 验收标准以需求描述里明确列出的为准；如果需求没有明确列出验收标准，就从需求描述中提炼出可验证的条目，并在 criterion 里写清楚。
7. 按条目编号 id（AC-1、AC-2…）；summary 用中文简述整体结论和主要问题。

`, in.RepoURL, in.Branch, in.RunID)

	b.WriteString("## 需求\n")
	fmt.Fprintf(&b, "编号：%s\n标题：%s\n", in.Issue.Identifier, in.Issue.Title)
	if in.Issue.URL != "" {
		fmt.Fprintf(&b, "链接：%s\n", in.Issue.URL)
	}
	b.WriteString("\n<issue_description>\n")
	b.WriteString(strings.TrimSpace(in.Issue.Description))
	b.WriteString("\n</issue_description>\n")

	if len(in.Issue.Comments) > 0 {
		b.WriteString("\n<issue_comments>\n")
		for _, c := range in.Issue.Comments {
			fmt.Fprintf(&b, "--- %s（%s）\n%s\n", c.Author, c.CreatedAt, strings.TrimSpace(c.Body))
		}
		b.WriteString("</issue_comments>\n")
	}

	if s := strings.TrimSpace(in.TestNotes); s != "" {
		b.WriteString("\n## 本仓库的测试说明（由维护者提供）\n<test_notes>\n")
		b.WriteString(s)
		b.WriteString("\n</test_notes>\n")
	}

	b.WriteString("\n完成全部验证后，按要求的结构输出结果。\n")
	return b.String()
}
