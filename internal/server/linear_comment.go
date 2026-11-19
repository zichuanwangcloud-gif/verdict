package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

func (s *Server) writeBack(runID string) { s.writeBackForce(runID, false) }

// writeBackForce 由用户手动触发时 (force=true) 即使回归子测试也回写。
func (s *Server) writeBackForce(runID string, force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run, err := s.st.Run(ctx, runID)
	if err != nil || run.Issue.ID == "" || run.Status == store.StatusCanceled {
		return
	}
	if run.RegressionID != "" && !force {
		// 回归子测试默认不回写，避免同一批 issue 反复被评论刷屏。
		return
	}
	key := s.linearKey(ctx)
	if key == "" {
		_ = s.st.SetLinearResult(ctx, runID, "", "未配置 Linear API Key，未回写")
		return
	}
	stages, _ := s.st.Stages(ctx, runID)
	url, err := linearClient(key).CreateComment(ctx, run.Issue.ID, CommentBody(run, stages, s.publicURL(ctx, nil)))
	msg := ""
	if err != nil {
		msg = err.Error()
		log.Printf("测试 %s 回写 Linear 失败: %v", runID, err)
	}
	_ = s.st.SetLinearResult(ctx, runID, url, msg)
}

var verdictText = map[string]string{
	protocol.VerdictPass:    "✅ 通过",
	protocol.VerdictFail:    "❌ 未通过",
	protocol.VerdictPartial: "⚠️ 部分无法验证",
}

var acText = map[string]string{
	protocol.ACPass:         "✅ 通过",
	protocol.ACFail:         "❌ 未通过",
	protocol.ACUnverifiable: "⚠️ 无法验证",
	protocol.ACDisputed:     "⚖️ 存疑",
}

var confidenceText = map[string]string{"high": "高", "medium": "中", "low": "低"}

var severityText = map[string]string{"critical": "🔴 严重", "high": "🟠 高", "medium": "🟡 中", "low": "⚪ 低"}

var tierText = map[string]string{
	testplan.TierS: "S 对抗", testplan.TierA: "A 深度", testplan.TierB: "B 标准", testplan.TierC: "C 快速", testplan.TierCustom: "自定义",
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "<br>")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// flow 把阶段压缩成一行：计划 → 启动服务 → 执行验证 → 红队质疑 ×2 → …
func flow(stages []store.Stage) string {
	var parts []string
	for i := 0; i < len(stages); i++ {
		st := stages[i]
		name := testplan.RoleName(st.Role)
		n := 1
		for i+1 < len(stages) && stages[i+1].Role == st.Role && stages[i+1].Status == st.Status {
			i++
			n++
		}
		switch st.Status {
		case testplan.StatusSkipped:
			name = "~~" + name + "~~"
		case testplan.StatusError, testplan.StatusTimeout:
			name += "（失败）"
		}
		if n > 1 {
			name += fmt.Sprintf(" ×%d", n)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, " → ")
}

// unusableShots 返回最终被判为不可用的截图文件名。
// 判定口径与 testplan 的硬约束一致：独立质检优先，其次是机器体检。
func unusableShots(stages []store.Stage) []string {
	var files []string
	settled := map[string]bool{}
	for _, st := range stages {
		if st.Role != testplan.RoleShots || st.Status != testplan.StatusOK || len(st.Output) == 0 {
			continue
		}
		var out struct {
			Reviews []struct {
				File    string `json:"file"`
				Verdict string `json:"verdict"`
			} `json:"shot_reviews"`
		}
		if json.Unmarshal(st.Output, &out) != nil {
			continue
		}
		for _, rv := range out.Reviews {
			if rv.File == "" || settled[rv.File] {
				continue
			}
			settled[rv.File] = true
			if rv.Verdict == "unusable" {
				files = append(files, rv.File)
			}
		}
	}
	for _, st := range stages {
		for _, i := range st.ArtifactIssues {
			if i.File == "" || settled[i.File] {
				continue
			}
			settled[i.File] = true
			files = append(files, i.File)
		}
	}
	return files
}

// CommentBody 生成回写到 Linear 的评论（Markdown）。
func CommentBody(run store.Run, stages []store.Stage, base string) string {
	var b strings.Builder
	b.WriteString("### Verdict 验收测试结果\n\n")
	commit := run.CommitSHA
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if run.Status == store.StatusCompleted {
		fmt.Fprintf(&b, "**结论：%s**", verdictText[run.Verdict])
	} else {
		fmt.Fprintf(&b, "**测试未完成：%s**", mdCell(run.Error))
	}
	if t := tierText[run.Tier]; t != "" {
		fmt.Fprintf(&b, "　·　档位 %s", t)
	}
	b.WriteString("\n\n")

	severe := 0
	for _, f := range run.Findings {
		if f.Severity == "critical" || f.Severity == "high" {
			severe++
		}
	}
	if severe > 0 {
		fmt.Fprintf(&b, "> 🚨 另外发现 %d 个严重/高危问题（不影响验收结论），见下方「额外发现」。\n\n", severe)
	}
	if c := run.Coverage; c.Degraded {
		fmt.Fprintf(&b, "> ⚠️ **覆盖强度降级**：%s。%s\n\n", mdCell(c.Reason),
			map[bool]string{true: "结论未经裁决，不代表这个档位的验收强度。", false: ""}[c.Unjudged])
	}

	fmt.Fprintf(&b, "- 仓库：`%s` 分支 `%s`", run.RepoName, run.Branch)
	if commit != "" {
		fmt.Fprintf(&b, "（commit `%s`）", commit)
	}
	fmt.Fprintf(&b, "\n- 执行：%s @ %s", run.Harness, run.RuntimeName)
	if run.Config.RedRounds > 0 && run.Config.RedHarness != "" && run.Config.RedHarness != run.Harness {
		fmt.Fprintf(&b, "，红队 %s", run.Config.RedHarness)
	}
	b.WriteString("\n")
	if len(stages) > 0 {
		fmt.Fprintf(&b, "- 流程：%s\n", flow(stages))
	}
	if run.SourceModified {
		b.WriteString("- ⚠️ 测试过程中修改了被测源码，结论可信度降低\n")
	}
	if run.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", run.Summary)
	}
	if len(run.ACs) > 0 {
		b.WriteString("\n| # | 验收标准 | 结果 | 置信度 | 证据 |\n|---|---|---|---|---|\n")
		for _, a := range run.ACs {
			st := acText[a.Status]
			if st == "" {
				st = a.Status
			}
			if n := len(a.Challenges); n > 0 {
				st += fmt.Sprintf("（经 %d 次质疑）", n)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", mdCell(a.ID), mdCell(a.Criterion), st,
				confidenceText[a.Confidence], mdCell(a.Evidence))
		}
	}
	if len(run.Findings) > 0 {
		b.WriteString("\n**额外发现**\n\n")
		for i, f := range run.Findings {
			if i == 10 {
				fmt.Fprintf(&b, "- …共 %d 条，见完整报告\n", len(run.Findings))
				break
			}
			fmt.Fprintf(&b, "- %s %s：%s\n", severityText[f.Severity], mdCell(f.Title), mdCell(f.Detail))
		}
	}
	if bad := unusableShots(stages); len(bad) > 0 {
		fmt.Fprintf(&b, "\n> 📷 有 %d 张截图不可用（白屏、遮挡或画面不清），相关结论已降为无法验证（如果它只靠这些截图支撑）。详见报告页的截图区。\n", len(bad))
	}
	if base != "" {
		fmt.Fprintf(&b, "\n[查看完整报告](%s/runs/%s)\n", base, run.ID)
	}
	return b.String()
}
