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
		if r.Status != StatusOK || len(r.Output) == 0 {
			continue
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
					continue
				}
				// 非界面条目：UI 结果作为补充证据；与原结论冲突时降低置信度。
				a.Evidence = strings.TrimSpace(a.Evidence + "\n\n【UI 走查】" + st + "：" + x.Evidence)
				if st != a.Status {
					a.Confidence = "low"
					a.Reasoning = strings.TrimSpace(a.Reasoning + "\n（UI 走查结论为 " + st + "，与此不一致）")
				}
			}
			res.Pages = append(res.Pages, out.Pages...)
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
			}
			if out.Summary != "" {
				res.Summary = out.Summary
			}
			judged = true
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
	if res.ACs == nil {
		res.ACs = []protocol.ACResult{}
	}
	if res.Findings == nil {
		res.Findings = []protocol.Finding{}
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
