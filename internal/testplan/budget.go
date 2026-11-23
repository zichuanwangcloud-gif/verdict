package testplan

import (
	"encoding/json"
	"fmt"
	"math"
)

// costRule 是一个环节的预计耗时规则：base + per × 条数，结果落在 [lo, hi] 分钟内。
type costRule struct {
	base, per float64
	lo, hi    int
}

// estimateRules 是各环节的预计耗时。系数来自 2026-11 的失败复盘
// （docs/failure-analysis/2026-11-23-solution-design.md 3.2.1），后续用复盘数据继续校准。
var estimateRules = map[string]costRule{
	RolePlan:    {base: 20, per: 0.3, lo: 20, hi: 40},
	RoleVerify:  {base: 15, per: 1.0, lo: 30, hi: 90},
	RoleUI:      {base: 10, per: 2.0, lo: 15, hi: 45},
	RoleRed:     {base: 10, per: 0.5, lo: 15, hi: 35},
	RoleRecheck: {base: 12, per: 2.5, lo: 15, hi: 45},
	RoleExplore: {base: 25, lo: 25, hi: 25},
	RoleJudge:   {base: 10, lo: 10, hi: 10},
	RoleShots:   {base: 12, lo: 12, hi: 12},
	RoleService: {base: 25, lo: 10, hi: 30},
}

// EstimateMinutes 估算某环节在本单里需要多少分钟。
// 输入是本单的实际工作量（验收条数、界面条目数、被质疑条数），没有计划时回退到固定值。
func EstimateMinutes(cfg Config, in Input, recs []StageRecord, role string, round int) int {
	r, ok := estimateRules[role]
	if !ok {
		return max(defaultTimeout[role], 10)
	}
	var n int
	switch role {
	case RolePlan, RoleVerify, RoleRed:
		n = acCount(recs)
	case RoleUI:
		n = uiItemCount(recs)
	case RoleRecheck:
		n = challengedCount(recs, round)
	}
	// 减去一个极小值，避免 20 + 0.3×10 这类刚好整数的情况被浮点误差进位。
	m := int(math.Ceil(r.base + r.per*float64(n) - 1e-9))
	if r.lo > 0 {
		m = max(m, r.lo)
	}
	if r.hi > 0 {
		m = min(m, r.hi)
	}
	return m
}

// ReservedMinutes 是派发某个环节时必须给后续环节留出的预算：裁决永远优先，探索开着时也要留够。
// 红队开始新一轮前要留够复测的时间，保证这一轮能完整跑完。
func ReservedMinutes(cfg Config, in Input, recs []StageRecord, role string) int {
	n := 0
	if cfg.Judge && role != RoleJudge && latest(recs, RoleJudge, 0) == nil {
		n += EstimateMinutes(cfg, in, recs, RoleJudge, 0)
	}
	if cfg.Explore && role != RoleExplore && latest(recs, RoleExplore, 0) == nil {
		n += EstimateMinutes(cfg, in, recs, RoleExplore, 0)
	}
	if role == RoleRed && cfg.RedRounds > 0 {
		n += EstimateMinutes(cfg, in, recs, RoleRecheck, 0)
	}
	return n
}

// Gate 决定某个环节实际分到多少分钟；skip 非空表示预算不足、这个环节不该派。
// 裁决环节单独处理：预算再紧也优先给它挤出一个时限，只有确实一分钟都不剩时才放弃。
func Gate(cfg Config, in Input, recs []StageRecord, role string, round int) (timeout int, skip string) {
	rem := remainingMinutes(cfg, recs)
	est := EstimateMinutes(cfg, in, recs, role, round)
	if role == RoleJudge {
		if rem < 1 {
			return 0, "剩余预算已耗尽，裁决未执行"
		}
		return max(1, min(est, rem)), ""
	}
	reserved := ReservedMinutes(cfg, in, recs, role)
	avail := rem - reserved
	if avail-est < 0 {
		return max(1, min(est, avail)), fmt.Sprintf(
			"剩余 %d 分钟，不足本环节预计 %d 分钟（需为后续环节保留 %d 分钟），因预算不足未执行",
			max(rem, 0), est, reserved)
	}
	return max(1, min(est, avail)), ""
}

// CanRetry 判断到点超时的重试是否还有预算：剩余时间至少能覆盖两次预计耗时。
func CanRetry(cfg Config, in Input, recs []StageRecord, role string, round int) bool {
	el, _ := Usage(recs)
	return cfg.MaxMinutes-int(el/60000) >= 2*EstimateMinutes(cfg, in, recs, role, round)
}

// ProgressCount 统计进度文件里已经写下的条目数，用于重试时告诉模型从哪接着做。
func ProgressCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	n := 0
	for _, k := range []string{"acs", "challenges", "shot_reviews", "findings", "pages", "shots"} {
		var xs []json.RawMessage
		if json.Unmarshal(m[k], &xs) == nil {
			n += len(xs)
		}
	}
	if n == 0 {
		n = 1 // 有结构化结果但列表为空，也说明上一轮交过卷
	}
	return n
}

func acCount(recs []StageRecord) int {
	if p := ParsePlan(recs); p != nil {
		return len(p.Items)
	}
	return 0
}

func uiItemCount(recs []StageRecord) int {
	p := ParsePlan(recs)
	if p == nil {
		return 0
	}
	n := 0
	for _, it := range p.Items {
		if it.Method == "ui" {
			n++
		}
	}
	return n
}

// challengedCount 返回某一轮红队提出的质疑条数；被质疑条数的估算天然按本轮工作量来。
func challengedCount(recs []StageRecord, round int) int {
	return len(redChallenges(latest(recs, RoleRed, round)))
}
