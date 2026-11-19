package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"verdict/internal/server/store"
)

// ---- 复盘指标 ----

// reviewCell 是混淆矩阵的一格：agent 结论 × 人工真值。
type reviewCell struct {
	Agent string `json:"agent"`
	Truth string `json:"truth"`
	Count int    `json:"count"`
}

// reviewStats 是复盘页顶部的数字。
//
// 漏报：agent 说通过/存疑，人工测出问题。误报：agent 说未通过/存疑，人工确认没问题。
// 「明确」和「存疑」分开统计，因为 agent 拍胸脯说通过却被人测出来，比它自己说无法验证却漏了要严重得多。
type reviewStats struct {
	Samples int `json:"samples"` // 有效样本（真值不是 invalid）
	// 分母
	HumanFail int `json:"human_fail"`
	HumanPass int `json:"human_pass"`

	// 漏报：人工未通过里，agent 判 pass / partial 的
	MissedClear int `json:"missed_clear"` // agent pass
	MissedDoubt int `json:"missed_doubt"` // agent partial
	// 误报：人工通过里，agent 判 fail / partial 的
	AlarmClear int `json:"alarm_clear"` // agent fail
	AlarmDoubt int `json:"alarm_doubt"` // agent partial
	// 命中与真放行
	Hit  int `json:"hit"`  // agent fail, 人工 fail
	Real int `json:"real"` // agent pass, 人工 pass
	// agent 说无法验证、人工也确认没问题的
	DoubtOK int `json:"doubt_ok"` // agent partial, 人工 pass 计入 AlarmDoubt，这里单列混淆矩阵

	MissedRate float64 `json:"missed_rate"` // (MissedClear + MissedDoubt) / HumanFail
	AlarmRate  float64 `json:"alarm_rate"`  // (AlarmClear + AlarmDoubt) / HumanPass

	Invalid int `json:"invalid"` // 真值为 invalid，不进任何分子分母

	// 复核覆盖率：让人知道样本有多不完整
	Completed int `json:"completed"`
	Marked    int `json:"marked"`

	Matrix []reviewCell `json:"matrix"`
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// computeStats 按设计文档的口径算指标。所有除法分母为 0 时返回 0，由前端显示「—」。
func computeStats(samples []store.ReviewSample, backlog store.ReviewBacklog) reviewStats {
	st := reviewStats{Completed: backlog.Completed, Marked: backlog.Marked, Invalid: backlog.Invalid}
	// 矩阵固定 3 × 2，保证前端表格形状稳定。
	counts := map[[2]string]int{}

	for _, s := range samples {
		agent, truth := s.Verdict, s.Truth
		switch truth {
		case store.TruthInvalid:
			continue // 样本无效，不进任何分子分母
		case store.TruthFail, store.TruthPass:
		default:
			continue
		}
		st.Samples++
		counts[[2]string{agent, truth}]++

		if truth == store.TruthFail {
			st.HumanFail++
			switch agent {
			case "pass":
				st.MissedClear++
			case "partial":
				st.MissedDoubt++
			case "fail":
				st.Hit++
			}
		} else {
			st.HumanPass++
			switch agent {
			case "pass":
				st.Real++
			case "partial":
				st.AlarmDoubt++
			case "fail":
				st.AlarmClear++
			}
		}
	}

	st.MissedRate = ratio(st.MissedClear+st.MissedDoubt, st.HumanFail)
	st.AlarmRate = ratio(st.AlarmClear+st.AlarmDoubt, st.HumanPass)

	for _, a := range []string{"pass", "partial", "fail"} {
		for _, t := range []string{store.TruthPass, store.TruthFail} {
			st.Matrix = append(st.Matrix, reviewCell{Agent: a, Truth: t, Count: counts[[2]string{a, t}]})
		}
	}
	return st
}

// reviewStatsHandler 返回指标与样本清单。筛选：repo_id、tier、days（默认 90，0 表示不限）。
func (s *Server) reviewStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReviewFilter{Tier: strings.TrimSpace(q.Get("tier"))}
	f.RepoID, _ = strconv.ParseInt(q.Get("repo_id"), 10, 64)
	days := intOr(q.Get("days"), 90)
	if days > 0 {
		f.Since = time.Now().AddDate(0, 0, -days)
	}

	samples, err := s.st.ReviewSamples(r.Context(), f)
	if err != nil {
		failErr(w, err)
		return
	}
	backlog, err := s.st.ReviewBacklog(r.Context(), f)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, map[string]any{
		"stats":   computeStats(samples, backlog),
		"samples": samples,
		"days":    days,
	})
}
