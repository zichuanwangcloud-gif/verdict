package testplan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"verdict/internal/protocol"
)

// Result 是把全部阶段叠加后的当前结论。
type Result struct {
	Summary  string              `json:"summary"`
	ACs      []protocol.ACResult `json:"acs"`
	Findings []protocol.Finding  `json:"findings"`
	Pages    []UIPage            `json:"pages"`
	Shots    []Shot              `json:"shots"`
	ShotRevs []ShotReview        `json:"shot_reviews"`
	Coverage Coverage            `json:"coverage"`
}

// Coverage 描述这次测试实际达到的流程强度。
type Coverage struct {
	// Missing 是承诺了但没有成功的阶段角色。
	Missing []string `json:"missing"`
	// Degraded 表示强度不足，结论需要打折扣。
	Degraded bool `json:"degraded"`
	// Unjudged 表示缺少裁决步骤（最严格的一种降级）。
	Unjudged bool   `json:"unjudged"`
	Reason   string `json:"reason"`
}

var roleNames = map[string]string{
	RolePlan: "测试计划", RoleService: "启动托管服务", RoleVerify: "执行验证",
	RoleUI: "UI 走查", RoleRed: "红队质疑", RoleExplore: "主动探索", RoleJudge: "裁决",
}

// coverage 判断承诺的阶段是否都成功。任意一轮红队成功即算达成。
// 计划、服务、执行验证只要「发生过但没成功」就降级：这也代表结论的可信度打了折扣。
func coverage(cfg Config, recs []StageRecord) Coverage {
	var c Coverage
	ok := func(role string) bool {
		for _, r := range recs {
			if r.Role == role && r.Status == StatusOK {
				return true
			}
		}
		return false
	}
	failed := func(role string) bool {
		for _, r := range recs {
			if r.Role == role && (r.Status == StatusError || r.Status == StatusTimeout) {
				return true
			}
		}
		return false
	}
	committed := func(role string) bool {
		switch role {
		case RoleUI:
			return cfg.UI != UINone
		case RoleRed:
			return cfg.RedRounds > 0
		case RoleExplore:
			return cfg.Explore
		case RoleJudge:
			return cfg.Judge
		}
		return false
	}
	for _, role := range []string{RoleUI, RoleRed, RoleExplore, RoleJudge} {
		if committed(role) && !ok(role) {
			c.Missing = append(c.Missing, role)
		}
	}
	var notes []string
	if failed(RolePlan) && ParsePlan(recs) == nil {
		c.Missing = append(c.Missing, RolePlan)
		notes = append(notes, "测试计划缺失，验证按通用方法进行")
	}
	if failed(RoleService) && !ok(RoleService) {
		c.Missing = append(c.Missing, RoleService)
		notes = append(notes, "平台未能启动被测服务，后续环境由模型自建，结论一致性下降")
	}
	if failed(RoleVerify) && !ok(RoleVerify) {
		c.Missing = append(c.Missing, RoleVerify)
		notes = append(notes, "执行验证未完成，结论部分来自进度文件")
	}
	if len(c.Missing) == 0 {
		return c
	}
	c.Degraded = true
	var names []string
	for _, role := range c.Missing {
		names = append(names, roleNames[role])
		if role == RoleJudge {
			c.Unjudged = true
		}
	}
	var parts []string
	if len(names) > 0 {
		parts = append(parts, "未达成："+strings.Join(names, "、"))
	}
	parts = append(parts, notes...)
	c.Reason = strings.Join(parts, "；")
	return c
}

func (r Result) Verdict() string { return protocol.Report{ACs: r.ACs}.Verdict() }

func normStatus(s string, allowDisputed bool) string {
	switch s {
	case protocol.ACPass, protocol.ACFail, protocol.ACUnverifiable:
		return s
	case protocol.ACDisputed:
		if allowDisputed {
			return s
		}
	}
	return protocol.ACUnverifiable
}

func normConfidence(s string) string {
	switch s {
	case "high", "medium", "low":
		return s
	}
	return "medium"
}

var severityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}

// Consolidate 按阶段顺序叠加结论：计划 → 执行验证 → UI 走查 → 红队质疑 / 复测 → 主动探索 → 裁决。
// 超时 / 失败但交了进度文件的阶段同样参与叠加，结论置信度统一降为低，覆盖强度仍记为降级。
func Consolidate(cfg Config, recs []StageRecord) Result {
	var res Result
	idx := map[string]int{}
	get := func(id, criterion string) *protocol.ACResult {
		id = strings.TrimSpace(id)
		if i, ok := idx[id]; ok {
			if res.ACs[i].Criterion == "" {
				res.ACs[i].Criterion = criterion
			}
			return &res.ACs[i]
		}
		idx[id] = len(res.ACs)
		res.ACs = append(res.ACs, protocol.ACResult{ID: id, Criterion: criterion, Status: protocol.ACUnverifiable})
		return &res.ACs[len(res.ACs)-1]
	}
	addFindings := func(fs []findingOut, source string) {
		for _, f := range fs {
			if strings.TrimSpace(f.Title) == "" {
				continue
			}
			sev := f.Severity
			if _, ok := severityRank[sev]; !ok {
				sev = "medium"
			}
			res.Findings = append(res.Findings, protocol.Finding{Severity: sev, Title: f.Title, Detail: f.Detail, Evidence: f.Evidence, Source: source})
		}
	}

	if p := ParsePlan(recs); p != nil {
		for _, it := range p.Items {
			a := get(it.ID, it.Criterion)
			a.Method = it.Method
			a.Reasoning = "未得到验证结果"
			a.Confidence = "low"
		}
	}

	judged := false
	for _, r := range recs {
		if len(r.Output) == 0 {
			continue
		}
		adopted := Partial(r)
		if r.Status != StatusOK && !adopted {
			continue
		}
		// 进度文件里的结果只是数据：置信度按低处理，并注明这一环节没有正常结束。
		mark := func(a *protocol.ACResult) {
			if !adopted {
				return
			}
			a.Confidence = "low"
			a.Reasoning = strings.TrimSpace(a.Reasoning + "\n（本环节未完成，结果来自进度文件）")
		}
		switch r.Role {
		case RoleVerify:
			var out reportOut
			if json.Unmarshal(r.Output, &out) != nil {
				continue
			}
			for _, x := range out.ACs {
				a := get(x.ID, x.Criterion)
				a.Status, a.Evidence, a.Reasoning, a.Confidence = normStatus(x.Status, false), x.Evidence, x.Reasoning, normConfidence(x.Confidence)
				mark(a)
			}
			res.Summary = out.Summary

		case RoleUI:
			var out uiOut
			if json.Unmarshal(r.Output, &out) != nil {
				continue
			}
			for _, x := range out.ACs {
				a := get(x.ID, x.Criterion)
				st := normStatus(x.Status, false)
				if a.Method == "ui" || a.Status == protocol.ACUnverifiable || a.Evidence == "" {
					a.Status, a.Evidence, a.Reasoning, a.Confidence = st, x.Evidence, x.Reasoning, normConfidence(x.Confidence)
					mark(a)
					continue
				}
				// 非界面条目：UI 结果作为补充证据；与原结论冲突时降低置信度。
				a.Evidence = strings.TrimSpace(a.Evidence + "\n\n【UI 走查】" + st + "：" + x.Evidence)
				if st != a.Status {
					a.Confidence = "low"
					a.Reasoning = strings.TrimSpace(a.Reasoning + "\n（UI 走查结论为 " + st + "，与此不一致）")
				}
				mark(a)
			}
			res.Pages = append(res.Pages, out.Pages...)
			res.Shots = append(res.Shots, out.Shots...)
			addFindings(out.Findings, RoleUI)

		case RoleRed:
			var out redOut
			if json.Unmarshal(r.Output, &out) != nil {
				continue
			}
			for _, c := range out.Challenges {
				i, ok := idx[strings.TrimSpace(c.ACID)]
				if !ok {
					continue
				}
				res.ACs[i].Challenges = append(res.ACs[i].Challenges, protocol.Challenge{
					Round: r.Round, Kind: c.Kind, Claim: c.Claim, SuggestedCheck: c.SuggestedCheck,
					ProposedStatus: normStatus(c.ProposedStatus, false),
				})
			}
			addFindings(out.Findings, RoleRed)

		case RoleRecheck:
			var out reportOut
			if json.Unmarshal(r.Output, &out) != nil {
				continue
			}
			for _, x := range out.ACs {
				i, ok := idx[strings.TrimSpace(x.ID)]
				if !ok {
					continue
				}
				a := &res.ACs[i]
				a.Status, a.Evidence, a.Reasoning, a.Confidence = normStatus(x.Status, false), x.Evidence, x.Reasoning, normConfidence(x.Confidence)
				for j := range a.Challenges {
					if a.Challenges[j].Round == r.Round {
						a.Challenges[j].Response, a.Challenges[j].Resolution = x.Response, a.Status
					}
				}
				mark(a)
			}

		case RoleExplore:
			var out exploreOut
			if json.Unmarshal(r.Output, &out) != nil {
				continue
			}
			addFindings(out.Findings, RoleExplore)

		case RoleJudge:
			var out judgeOut
			if json.Unmarshal(r.Output, &out) != nil {
				continue
			}
			for _, x := range out.ACs {
				i, ok := idx[strings.TrimSpace(x.ID)]
				if !ok {
					continue
				}
				a := &res.ACs[i]
				a.Status, a.Confidence = normStatus(x.Status, true), normConfidence(x.Confidence)
				if x.Rationale != "" {
					a.Reasoning = strings.TrimSpace("【裁决】" + x.Rationale + "\n\n" + a.Reasoning)
				}
				mark(a)
			}
			if out.Summary != "" {
				res.Summary = out.Summary
			}
			// 裁决合并去重后的额外发现替换各阶段的原始列表；裁决没有给出时保留原始列表。
			// 裁决是超时交卷的（结果不完整）时只追加，避免把其它阶段的发现弄丢。
			if len(out.Findings) > 0 {
				if r.Status == StatusOK {
					res.Findings = nil
				}
				addFindings(out.Findings, RoleJudge)
			}
			if r.Status == StatusOK {
				judged = true
			}
		}
	}

	for i := range res.ACs {
		a := &res.ACs[i]
		// 没有裁决时：最后一轮质疑没有得到复测回应、且建议结论不同 → 存疑。
		if !judged && len(a.Challenges) > 0 {
			last := a.Challenges[len(a.Challenges)-1]
			if last.Resolution == "" && last.ProposedStatus != a.Status {
				a.Status, a.Confidence = protocol.ACDisputed, "low"
			}
		}
		// 硬约束：没有证据的「通过」降为无法验证。
		if a.Status == protocol.ACPass && strings.TrimSpace(a.Evidence) == "" {
			a.Status, a.Confidence = protocol.ACUnverifiable, "low"
			a.Reasoning = strings.TrimSpace(a.Reasoning + "\n（平台：判为通过但缺少证据，降为无法验证）")
		}
	}
	// 硬约束：证据引用的截图全部不可用时，通过结论同样不成立。
	applyShotGate(&res, shotVerdict(recs))

	sort.SliceStable(res.Findings, func(i, j int) bool {
		return severityRank[res.Findings[i].Severity] < severityRank[res.Findings[j].Severity]
	})
	if res.Summary == "" && len(res.ACs) > 0 {
		c := map[string]int{}
		for _, a := range res.ACs {
			c[a.Status]++
		}
		res.Summary = fmt.Sprintf("共 %d 条验收标准：通过 %d，未通过 %d，无法验证 %d，存疑 %d。",
			len(res.ACs), c[protocol.ACPass], c[protocol.ACFail], c[protocol.ACUnverifiable], c[protocol.ACDisputed])
	}
	res.Coverage = coverage(cfg, recs)
	// 缺少裁决时结论不能签字：所有通过降为存疑。裁决是 A/S 档结论的最终来源。
	if res.Coverage.Unjudged {
		for i := range res.ACs {
			if res.ACs[i].Status == protocol.ACPass {
				res.ACs[i].Status = protocol.ACDisputed
				res.ACs[i].Confidence = "low"
				res.ACs[i].Reasoning = strings.TrimSpace(res.ACs[i].Reasoning + "\n（平台：裁决阶段没有完成，通过结论降为存疑）")
			}
		}
	}
	if res.ACs == nil {
		res.ACs = []protocol.ACResult{}
	}
	if res.Findings == nil {
		res.Findings = []protocol.Finding{}
	}
	if res.Shots == nil {
		res.Shots = []Shot{}
	}
	res.ShotRevs = ShotReviewOf(recs)
	if res.ShotRevs == nil {
		res.ShotRevs = []ShotReview{}
	}
	return res
}

// Severe 返回 critical / high 级别的额外发现数量。
func (r Result) Severe() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == "critical" || f.Severity == "high" {
			n++
		}
	}
	return n
}
