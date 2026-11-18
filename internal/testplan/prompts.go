package testplan

import (
	"fmt"
	"strings"

	"verdict/internal/protocol"
)

const evidenceClip = 3000

func roleIntro(role string, round int, cfg Config) string {
	switch role {
	case RolePlan:
		return "你是这次验收测试的测试设计负责人。这一阶段只做测试设计：阅读需求和代码，**不要执行验证，不要修改任何文件**。"
	case RoleVerify:
		return "你是蓝队测试工程师，负责按测试计划逐条实测这个分支是否满足验收标准。"
	case RoleUI:
		return "你是 UI 走查工程师，使用无头浏览器在真实页面上验证界面类的验收标准。"
	case RoleRed:
		return fmt.Sprintf("你是红队审查员（第 %d 轮）。蓝队已经给出了结论和证据，你的任务是**找出其中的错误**。你和蓝队是相互独立的会话，不要默认相信蓝队。", round)
	case RoleRecheck:
		return fmt.Sprintf("你是蓝队测试工程师。红队在第 %d 轮对部分结论提出了质疑，你需要针对质疑重新实测。", round)
	case RoleExplore:
		return "你是探索性测试工程师。在验收标准之外，主动寻找这次改动可能引入的问题。"
	case RoleJudge:
		return "你是这次验收测试的裁决者。你要根据全部材料，逐条给出最终结论。"
	}
	return ""
}

func buildPrompt(role string, round int, cfg Config, in Input, recs []StageRecord) string {
	var b strings.Builder
	b.WriteString(roleIntro(role, round, cfg))
	b.WriteString("\n\n")

	writeRequirement(&b, in)
	writeRepo(&b, in)
	if role != RolePlan {
		b.WriteString("## 运行环境\n")
		b.WriteString(serviceContext(cfg, in, recs))
		b.WriteString("\n\n")
	}
	writeRules(&b, role, in)

	plan := ParsePlan(recs)
	cur := Consolidate(cfg, recs)
	b.WriteString("## 本阶段任务\n")
	switch role {
	case RolePlan:
		writePlanTask(&b, cfg)
	case RoleVerify:
		writeVerifyTask(&b, cfg, in, plan, recs)
	case RoleUI:
		writeUITask(&b, cfg, in, recs, plan, cur)
	case RoleRed:
		writeRedTask(&b, plan, cur)
	case RoleRecheck:
		writeRecheckTask(&b, cfg, round, cur)
	case RoleExplore:
		writeExploreTask(&b, cur)
	case RoleJudge:
		writeJudgeTask(&b, plan, cur, recs)
	}
	b.WriteString("\n完成后，按要求的结构输出结果。\n")
	return b.String()
}

func writeRequirement(b *strings.Builder, in Input) {
	b.WriteString("## 需求\n")
	fmt.Fprintf(b, "编号：%s\n标题：%s\n", in.Issue.Identifier, in.Issue.Title)
	if in.Issue.URL != "" {
		fmt.Fprintf(b, "链接：%s\n", in.Issue.URL)
	}
	b.WriteString("\n<issue_description>\n")
	b.WriteString(strings.TrimSpace(in.Issue.Description))
	b.WriteString("\n</issue_description>\n")
	if len(in.Issue.Comments) > 0 {
		b.WriteString("\n<issue_comments>\n")
		for _, c := range in.Issue.Comments {
			fmt.Fprintf(b, "--- %s（%s）\n%s\n", c.Author, c.CreatedAt, strings.TrimSpace(c.Body))
		}
		b.WriteString("</issue_comments>\n")
	}
	b.WriteString("\n")
}

func writeRepo(b *strings.Builder, in Input) {
	r := in.Repo
	var lines []string
	for _, kv := range [][2]string{{"安装依赖", r.SetupCmd}, {"启动命令", r.StartCmd}, {"健康检查地址", r.HealthURL}, {"前端入口地址", r.AppURL}} {
		if strings.TrimSpace(kv[1]) != "" {
			lines = append(lines, fmt.Sprintf("- %s：`%s`", kv[0], kv[1]))
		}
	}
	notes := strings.TrimSpace(r.TestNotes)
	if len(lines) == 0 && notes == "" {
		return
	}
	b.WriteString("## 仓库维护者提供的测试信息\n")
	if len(lines) > 0 {
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
	}
	if notes != "" {
		b.WriteString("<test_notes>\n")
		b.WriteString(notes)
		b.WriteString("\n</test_notes>\n")
	}
	b.WriteString("\n")
}

func writeRules(b *strings.Builder, role string, in Input) {
	b.WriteString("## 工作区规则\n")
	fmt.Fprintf(b, "- 当前目录是仓库 %s 在分支 %s 上的一份干净副本。\n", in.RepoURL, in.Branch)
	b.WriteString("- 不要修改已有的源码文件；可以新建临时脚本、临时测试文件。平台会检查源码改动并在报告里标出。\n")
	if in.Commit != "" {
		fmt.Fprintf(b, "- 这是一次回归测试：工作区锁定在 commit %s（版本的冻结节点）。%s\n", in.Commit,
			map[bool]string{true: "测试计划来自测试 " + in.PlanSource + "；如果计划里的步骤与当前代码对不上（例如页面路径、接口地址变了），以验收标准本身为准判断，并在 reasoning 里说明计划哪里过时了。", false: ""}[in.PlanSource != ""])
	}
	if role != RolePlan {
		b.WriteString("- 截图等产物保存到环境变量 VERDICT_ARTIFACTS 指向的目录。\n")
		fmt.Fprintf(b, "- 需要用 docker compose 启动依赖时，项目名必须用 `-p verdict-%s`，平台会在测试结束后清理。\n", in.RunID)
		b.WriteString("- 你自己启动的后台进程，在本阶段结束前关闭；不要停止或重启平台托管的服务。\n")
	}
	b.WriteString("- 输出内容一律用中文。\n\n")
}

// AppURL 返回 UI 走查的入口地址。
func AppURL(in Input, recs []StageRecord) string {
	if in.Repo.AppURL != "" {
		return in.Repo.AppURL
	}
	if p := ParsePlan(recs); p != nil && p.Service.AppURL != "" {
		return p.Service.AppURL
	}
	if s := latest(recs, RoleService, 0); s != nil && s.Service != nil && s.Service.URL != "" {
		return s.Service.URL
	}
	return ""
}

func serviceContext(cfg Config, in Input, recs []StageRecord) string {
	if cfg.StaticOnly {
		return "本档位只做静态与命令行验证：可以运行单元测试、命令行工具、阅读代码；不要启动长期运行的服务，也不要做浏览器测试。"
	}
	compose := fmt.Sprintf("docker compose 项目名用 `verdict-%s`", in.RunID)
	rec := latest(recs, RoleService, 0)
	switch {
	case rec == nil:
		return "平台没有启动被测服务。如需服务请自行启动（" + compose + "），并在本阶段结束前关闭。"
	case rec.Status == StatusSkipped:
		return "平台没有启动被测服务（" + rec.Note + "）。如需服务请自行启动（" + compose + "），并在本阶段结束前关闭。"
	case rec.Status == StatusOK && rec.Service != nil && rec.Service.Running:
		s := rec.Service
		var b strings.Builder
		b.WriteString("平台已在后台启动被测服务，并会一直运行到测试结束：\n")
		fmt.Fprintf(&b, "- 启动命令：`%s`\n", s.StartCmd)
		if s.URL != "" {
			fmt.Fprintf(&b, "- 地址：%s\n", s.URL)
		}
		if s.LogFile != "" {
			fmt.Fprintf(&b, "- 服务日志：%s（排查问题时可以读）\n", s.LogFile)
		}
		b.WriteString("不要停止或重启它；如果你确实需要以不同配置运行，另起一个端口。")
		return b.String()
	default:
		var b strings.Builder
		b.WriteString("平台尝试启动被测服务失败：")
		b.WriteString(firstNonEmpty(rec.Error, rec.Status))
		if rec.Service != nil && rec.Service.LogTail != "" {
			fmt.Fprintf(&b, "\n最后的输出：\n```\n%s\n```", clip(rec.Service.LogTail, 3000))
		}
		b.WriteString("\n你可以尝试自行启动（" + compose + "）；仍然无法启动时，依赖服务的条目判 unverifiable 并写明原因。")
		return b.String()
	}
}

func writePlanTask(b *strings.Builder, cfg Config) {
	b.WriteString(`输出测试计划：
1. 把需求中的验收标准拆成可以独立验证的条目，编号 AC-1、AC-2……。需求没有明确列出验收标准时，从需求描述中提炼可验证的条目。
2. 为每条选择验证方式 method：cli（命令行、单元测试）、api（对运行中的服务发请求）、ui（需要在浏览器里操作页面）、code（只能通过阅读代码判断）。能实测的不要选 code。
3. 每条写清前置条件 preconditions、具体步骤 steps（具体到命令、接口、页面）和预期现象 expected。
4. 判断验证是否需要把服务跑起来（needs_service），并给出 service 建议：安装依赖命令 setup_cmd、**前台运行**的启动命令 start_cmd（不要加 &、nohup，平台会托管它在后台运行）、健康检查地址 health_url、前端入口地址 app_url。仓库维护者已经提供的，以维护者的为准。用不上的字段填空字符串。
   端口一律用占位符 ${VERDICT_PORT}（第 2、3 个端口写成 ${VERDICT_PORT2}、${VERDICT_PORT3}），**不要写死 3000 这样的端口号**：平台会同时跑多个测试，写死会互相冲突。
5. summary 用一两句话说明测试思路。
`)
	if cfg.BriefPlan {
		b.WriteString("\n本档位只做静态与命令行验证：条目保持精简，method 只能用 cli 或 code，needs_service 填 false。\n")
	}
}

func writeVerifyTask(b *strings.Builder, cfg Config, in Input, plan *Plan, recs []StageRecord) {
	if plan != nil {
		b.WriteString("按下面的测试计划逐条实测：\n\n")
		b.WriteString(renderPlan(plan))
	} else {
		b.WriteString("测试计划阶段没有产出可用的计划，请直接从需求中提炼验收条目（编号 AC-1、AC-2……）并逐条实测。\n\n")
	}
	b.WriteString(`
要求：
1. 优先真实验证：运行命令、发请求、执行测试，观察实际行为。只读代码得出的结论，confidence 不能是 high，并在 reasoning 里说明。
2. evidence 写清执行了什么、关键输出是什么（原样摘录）。没有证据不能判 pass。
3. 因环境、依赖、凭据缺失等原因无法验证的，判 unverifiable，并写清缺什么。不要猜测，不要硬判 pass。
4. 条目编号与计划保持一致；response 填空字符串。summary 用中文总结整体情况。
`)
	if cfg.StaticOnly {
		b.WriteString("5. 本档位只用命令行、单元测试和阅读代码验证；需要运行服务或浏览器的条目判 unverifiable。\n")
	} else if cfg.UI != UINone {
		b.WriteString("5. method 为 ui 的条目会由后续 UI 走查阶段用浏览器专门验证：能通过接口间接验证的先验证；否则判 unverifiable，reasoning 写「留待 UI 走查」。\n")
	} else {
		b.WriteString("5. 界面类条目可以用 curl 检查页面内容，或在环境允许时自行编写 playwright 脚本验证。\n")
	}
	if cfg.SelfReview {
		b.WriteString("6. 输出前逐条自查：证据是否真的来自本次执行、是否直接支撑结论；拿不准的降低 confidence 或改判 unverifiable。\n")
	}
	if cfg.Stability {
		b.WriteString("7. 每条的关键检查至少独立执行两次；两次结果不一致的判 unverifiable，并在 evidence 里写出两次的结果。\n")
	}
	if n := strings.TrimSpace(in.ApprovalNote); n != "" {
		fmt.Fprintf(b, "\n测试负责人在确认计划时补充：\n<approval_note>\n%s\n</approval_note>\n", n)
	}
}

func writeUITask(b *strings.Builder, cfg Config, in Input, recs []StageRecord, plan *Plan, cur Result) {
	b.WriteString("你可以使用名为 playwright 的浏览器工具（browser_navigate、browser_snapshot、browser_click、browser_type、browser_take_screenshot、browser_console_messages、browser_resize 等）。\n\n")
	if u := AppURL(in, recs); u != "" {
		fmt.Fprintf(b, "入口地址：%s\n\n", u)
	} else {
		b.WriteString("入口地址未知：请根据服务信息、测试说明和代码推断前端地址。\n\n")
	}
	if cfg.UI == UIFull {
		b.WriteString("范围：验证计划中 method 为 ui 的条目；再走查与本次需求相关的页面和主要交互，检查控制台报错、表单校验、空状态和错误状态，以及桌面（1280 宽）和手机（390 宽）两种宽度下的显示。\n")
	} else {
		b.WriteString("范围：只验证计划中 method 为 ui 的条目，以及完成这些条目需要经过的页面。\n")
	}
	b.WriteString(`
要求：
1. 关键状态都要截图，保存到环境变量 VERDICT_ARTIFACTS 指向的目录，文件名只用英文、数字和短横线，以 .png 结尾；在 pages[].screenshots 里填写文件名。
2. acs 只输出你用浏览器实际验证过的条目，编号与计划一致；evidence 写清操作步骤和看到的结果，并引用截图文件名；response 填空字符串。
3. 与验收标准无关、但确实存在的界面问题写进 findings，附复现步骤或截图文件名；不要写主观的审美意见。
4. 浏览器工具不可用时，可以改用 playwright 脚本；都不可用时，ui 条目判 unverifiable 并说明原因。
`)
	if plan != nil {
		var items []PlanItem
		for _, it := range plan.Items {
			if it.Method == "ui" {
				items = append(items, it)
			}
		}
		if len(items) > 0 {
			b.WriteString("\n计划中的界面类条目：\n\n")
			b.WriteString(renderPlan(&Plan{Items: items}))
		}
	}
	if len(cur.ACs) > 0 {
		b.WriteString("\n蓝队目前的结论（供参考）：\n\n")
		b.WriteString(renderACs(cur.ACs, false))
	}
}

func writeRedTask(b *strings.Builder, plan *Plan, cur Result) {
	b.WriteString(`你可以在工作区里自行执行命令、发请求来复核（不要修改被测源码，不要停止平台托管的服务）。逐条检查：
- 证据是否真的来自实际执行、是否直接支撑结论？是不是只读了代码就判了通过？
- 是否存在反例：边界输入、异常路径、权限、空数据、并发？
- 判 fail 的，会不会其实是测试方法或环境的问题？判 unverifiable 的，是不是其实可以验证？
- 结果是否可能不稳定？

只在有具体理由时提出质疑。每条质疑写清 claim（问题是什么）、suggested_check（建议怎么复核，尽量给出可以直接执行的命令或步骤）、proposed_status（你认为应该是什么结论）。站得住的结论不要提；没有任何质疑时 challenges 为空数组。
与验收标准无关、但你确实发现的问题写进 findings，附证据。summary 用中文总结你的审查结论。
`)
	if plan != nil {
		b.WriteString("\n测试计划：\n\n")
		b.WriteString(renderPlan(plan))
	}
	b.WriteString("\n蓝队当前的结论与证据（含此前各轮的质疑和回应）：\n\n")
	b.WriteString(renderACs(cur.ACs, true))
	b.WriteString(renderKnownFindings(cur.Findings))
}

func writeRecheckTask(b *strings.Builder, cfg Config, round int, cur Result) {
	b.WriteString(`针对下面每条质疑重新实测：
- 按质疑内容和建议的复核方式实际执行，给出更新后的 status、evidence、reasoning、confidence；
- 在 response 里直接回应质疑：认同就修正结论，不认同就用新的证据说明原结论成立。
acs 只输出被质疑的条目，编号保持不变。summary 用中文总结复测结果。
`)
	if cfg.Stability {
		b.WriteString("关键检查至少独立执行两次；结果不一致的判 unverifiable。\n")
	}
	var challenged []protocol.ACResult
	for _, a := range cur.ACs {
		for _, c := range a.Challenges {
			if c.Round == round {
				challenged = append(challenged, a)
				break
			}
		}
	}
	b.WriteString("\n被质疑的条目：\n\n")
	b.WriteString(renderACs(challenged, true))
}

func writeExploreTask(b *strings.Builder, cur Result) {
	b.WriteString(`- 先用 git log、git show 了解本分支最近的改动范围；
- 重点关注：回归（原有功能被破坏）、边界输入、错误处理、基础安全问题（注入、越权、敏感信息泄露、明文凭据）；
- 每个问题都要有证据：复现步骤和实际输出。没有证据的猜测不要报告。
severity 取值：critical（数据丢失、安全漏洞、核心功能不可用）、high（主要功能出错）、medium（次要功能或体验问题）、low（细节）。
已经在验收标准里覆盖的问题不要重复报告。summary 用中文总结探索结论。
`)
	b.WriteString("\n验收标准当前的结论（供参考，避免重复）：\n\n")
	b.WriteString(renderACs(cur.ACs, false))
	b.WriteString(renderKnownFindings(cur.Findings))
}

func writeJudgeTask(b *strings.Builder, plan *Plan, cur Result, recs []StageRecord) {
	b.WriteString(`不要重新执行测试；可以查看工作区里的文件，核对证据中引用的内容。逐条给出最终结论：
- pass：证据来自实际执行、直接支撑结论，且红队的质疑已被有效回应；
- fail：有确凿证据表明不满足；
- unverifiable：缺少验证条件，或证据不足；
- disputed：双方都有合理证据，无法判定。
confidence：high（证据充分、多次一致）、medium（证据可信但不够全面）、low（证据薄弱或只读了代码）。
rationale 简述理由并引用关键证据。summary 用中文总结整体结论、主要问题和仍存在的分歧。
acs 必须覆盖下面列出的每一条，编号保持不变。

此外整理各阶段报告的「额外发现」写进 findings：同一个根因的合并为一条（取这些条目里**最高**的 severity，合并证据，不允许降低），
没有证据支撑的纯推测删去，已经体现在验收结论里的不再重复。没有额外发现时 findings 为空数组。
`)
	if plan != nil {
		b.WriteString("\n测试计划：\n\n")
		b.WriteString(renderPlan(plan))
	}
	b.WriteString("\n当前结论、证据、质疑与回应：\n\n")
	b.WriteString(renderACs(cur.ACs, true))
	if len(cur.Findings) > 0 {
		b.WriteString("\n各阶段报告的额外发现（待合并去重）：\n\n")
		for i, f := range cur.Findings {
			fmt.Fprintf(b, "%d. [%s] %s（来自%s）\n   %s\n   证据：%s\n", i+1, f.Severity, f.Title, RoleName(f.Source), clip(f.Detail, 600), clip(f.Evidence, 800))
		}
	}
	for _, r := range recs {
		if r.Status == StatusSkipped || r.Note != "" {
			fmt.Fprintf(b, "\n说明：%s 阶段（第 %d 轮）%s %s", roleName[r.Role], r.Round, statusName(r.Status), r.Note)
		}
	}
	b.WriteString("\n")
}

var roleName = map[string]string{
	RolePlan: "测试计划", RoleService: "启动服务", RoleVerify: "执行验证", RoleUI: "UI 走查",
	RoleRed: "红队质疑", RoleRecheck: "蓝队复测", RoleExplore: "主动探索", RoleJudge: "裁决",
}

// RoleName 返回阶段的中文名。
func RoleName(role string) string { return firstNonEmpty(roleName[role], role) }

func statusName(s string) string {
	switch s {
	case StatusSkipped:
		return "被跳过"
	case StatusError:
		return "失败"
	case StatusTimeout:
		return "超时"
	}
	return ""
}

func renderPlan(p *Plan) string {
	var b strings.Builder
	for _, it := range p.Items {
		fmt.Fprintf(&b, "### %s（%s）%s\n", it.ID, it.Method, it.Criterion)
		if it.Preconditions != "" {
			fmt.Fprintf(&b, "- 前置条件：%s\n", it.Preconditions)
		}
		for i, s := range it.Steps {
			fmt.Fprintf(&b, "- 步骤 %d：%s\n", i+1, s)
		}
		if it.Expected != "" {
			fmt.Fprintf(&b, "- 预期：%s\n", it.Expected)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderACs(acs []protocol.ACResult, withChallenges bool) string {
	if len(acs) == 0 {
		return "（暂无）\n"
	}
	var b strings.Builder
	for _, a := range acs {
		fmt.Fprintf(&b, "### %s %s\n", a.ID, a.Criterion)
		fmt.Fprintf(&b, "- 结论：%s（置信度 %s）\n", a.Status, firstNonEmpty(a.Confidence, "—"))
		if a.Evidence != "" {
			fmt.Fprintf(&b, "- 证据：\n```\n%s\n```\n", clip(a.Evidence, evidenceClip))
		}
		if a.Reasoning != "" {
			fmt.Fprintf(&b, "- 理由：%s\n", clip(a.Reasoning, 1500))
		}
		if withChallenges {
			for _, c := range a.Challenges {
				fmt.Fprintf(&b, "- 第 %d 轮质疑（%s，建议结论 %s）：%s\n", c.Round, c.Kind, c.ProposedStatus, c.Claim)
				if c.SuggestedCheck != "" {
					fmt.Fprintf(&b, "  - 建议复核：%s\n", c.SuggestedCheck)
				}
				if c.Response != "" {
					fmt.Fprintf(&b, "  - 蓝队回应（复测后 %s）：%s\n", c.Resolution, clip(c.Response, 1500))
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderKnownFindings(fs []protocol.Finding) string {
	if len(fs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n已经记录的额外发现（不要重复报告，除非有新的证据或更严重的影响）：\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "- [%s] %s\n", f.Severity, f.Title)
	}
	return b.String()
}
