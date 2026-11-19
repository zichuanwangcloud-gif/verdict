package testplan

import (
	"encoding/json"
	"strings"

	"verdict/internal/protocol"
)

// 截图判定来源。后写入的判定优先级更高（人工之外，独立质检最可信）。
const (
	ShotFromSelf    = "self"    // UI 阶段自检
	ShotFromShots   = "shots"   // 独立质检阶段
	ShotFromMachine = "machine" // agent 的 PNG 体检
)

// shotVerdict 汇总一批截图最终的可用性判定。
//
// 优先级从高到低：
//  1. 独立质检（shots）：换了一个会话专门看图，最可信。判 usable 时其他判定都不能推翻。
//  2. 机器体检：客观测量（纯色、过小、重复），比自检可靠。
//  3. UI 阶段自检：截图的人自己填的，最容易自我说服，只在上面两者沉默时生效。
//
// 返回的 map 只包含「不可用」的文件，键为文件名，值为原因。
func shotVerdict(recs []StageRecord) map[string]string {
	bad := map[string]string{}   // file → 原因
	settled := map[string]bool{} // 已经有更高优先级的判定，低优先级不再改写

	// 1. 独立质检。
	for _, r := range recs {
		if r.Role != RoleShots || r.Status != StatusOK || len(r.Output) == 0 {
			continue
		}
		var out shotsOut
		if err := json.Unmarshal(r.Output, &out); err != nil {
			continue
		}
		for _, rv := range out.Reviews {
			f := strings.TrimSpace(rv.File)
			if f == "" {
				continue
			}
			settled[f] = true
			if rv.Verdict == "unusable" {
				bad[f] = shotReason(rv)
			} else {
				delete(bad, f)
			}
		}
	}

	// 2. 机器体检：独立质检没给结论的，按机器的客观测量判。
	for _, n := range suspiciousShots(recs) {
		f := strings.TrimSpace(n.File)
		if f == "" || settled[f] {
			continue
		}
		settled[f] = true
		bad[f] = n.Reason
	}

	// 3. UI 阶段自检：兜底。
	for _, r := range recs {
		if r.Role != RoleUI || r.Status != StatusOK || len(r.Output) == 0 {
			continue
		}
		var out uiOut
		if err := json.Unmarshal(r.Output, &out); err != nil {
			continue
		}
		for _, s := range out.Shots {
			f := strings.TrimSpace(s.File)
			if f == "" || settled[f] {
				continue
			}
			if s.Covered {
				bad[f] = "画面被遮挡"
			} else if !s.Readable {
				bad[f] = "内容不清晰"
			}
		}
	}
	return bad
}

func shotReason(rv shotReviewOut) string {
	var parts []string
	if rv.Occluded {
		parts = append(parts, "被遮挡")
	}
	if !rv.Readable {
		parts = append(parts, "不清晰")
	}
	parts = append(parts, rv.Problems...)
	if len(parts) == 0 {
		return "质检判定不可用"
	}
	return strings.Join(parts, "；")
}

// ShotReviewOf 返回独立质检阶段对各截图的结论，供报告展示。
// bad 是最终判定为「不可用」的文件集合，用来把机器体检和自检的结论也折叠进来，
// 这样报告页展示的每一条都与硬约束的判定口径一致。
func ShotReviewOf(recs []StageRecord) []ShotReview {
	var out []ShotReview
	seen := map[string]bool{}
	for _, r := range recs {
		if r.Role != RoleShots || r.Status != StatusOK || len(r.Output) == 0 {
			continue
		}
		var o shotsOut
		if err := json.Unmarshal(r.Output, &o); err != nil {
			continue
		}
		for _, rv := range o.Reviews {
			seen[rv.File] = true
			out = append(out, ShotReview{
				File: rv.File, Verdict: rv.Verdict, Occluded: rv.Occluded, Readable: rv.Readable,
				Problems: rv.Problems, Suggest: rv.Suggest, Source: ShotFromShots,
			})
		}
	}
	// 质检阶段没覆盖到、但被机器体检或自检判为不可用的截图，补一条记录。
	for _, n := range suspiciousShots(recs) {
		if n.File == "" || seen[n.File] {
			continue
		}
		seen[n.File] = true
		out = append(out, ShotReview{File: n.File, Verdict: "unusable", Problems: []string{n.Reason}, Source: ShotFromMachine})
	}
	return out
}

// suspiciousShots 收集 agent 报上来的产物基础检查问题。
func suspiciousShots(recs []StageRecord) []protocol.ArtifactIssue {
	var out []protocol.ArtifactIssue
	for _, r := range recs {
		out = append(out, r.ArtifactIssues...)
	}
	return out
}

// applyShotGate 是合成结论时的硬约束：一条验收结论的 evidence 引用的截图，
// 如果全部被判不可用、又没有其他证据支撑，就把结论从 pass 降为 unverifiable。
//
// 只处理 pass：fail 和 unverifiable 本来就不会因为截图变好而升级。
func applyShotGate(res *Result, bad map[string]string) {
	if len(bad) == 0 || len(res.ACs) == 0 {
		return
	}
	for i := range res.ACs {
		a := &res.ACs[i]
		if a.Status != protocol.ACPass {
			continue
		}
		cited, total := citeCounts(a.Evidence, bad)
		if total == 0 || len(cited) < total {
			continue // evidence 没引用截图，或引用的截图里还有可用的
		}
		var reasons []string
		for _, f := range cited {
			reasons = append(reasons, f+"："+bad[f])
		}
		a.Status = protocol.ACUnverifiable
		a.Confidence = "low"
		a.Reasoning = strings.TrimSpace(a.Reasoning + "\n（平台：证据引用的截图不可用（" +
			strings.Join(reasons, "；") + "），结论降为无法验证）")
	}
}

// citeCounts 返回 evidence 引用的截图里被判不可用的那些，以及引用的截图总数。
func citeCounts(evidence string, bad map[string]string) (badNames []string, total int) {
	for _, f := range shotCitationNames(evidence) {
		total++
		if _, ok := bad[f]; ok {
			badNames = append(badNames, f)
		}
	}
	return badNames, total
}

// shotCitationNames 从 evidence 里提取 .png / .jpg 形式的文件名。
// 只做保守的文本匹配：出现文件名即算引用，不做语义判断。
func shotCitationNames(evidence string) []string {
	var out []string
	for _, field := range strings.FieldsFunc(evidence, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '，' || r == '、' || r == '；' ||
			r == '（' || r == '）' || r == '(' || r == ')' || r == '`' || r == '「' || r == '」' ||
			r == '"' || r == '\'' || r == '*' || r == '|' || r == ':' || r == '：'
	}) {
		f := strings.Trim(field, "`'\"()[]<>.,;:")
		lower := strings.ToLower(f)
		if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
			out = append(out, f)
		}
	}
	return out
}

// citedShots 返回 evidence 里被引用、且已在 bad 表中的文件名。
func citedShots(evidence string, bad map[string]string) []string {
	var out []string
	for _, f := range shotCitationNames(evidence) {
		if _, ok := bad[f]; ok {
			out = append(out, f)
		}
	}
	return out
}
