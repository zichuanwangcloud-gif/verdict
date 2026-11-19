package server

import (
	"testing"

	"verdict/internal/server/store"
)

func sample(agent, truth string) store.ReviewSample {
	return store.ReviewSample{Verdict: agent, Truth: truth}
}

// 口径：漏报 = 人工未通过里 AI 判 pass（明确）/ partial（存疑）；
// 误报 = 人工通过里 AI 判 fail（明确）/ partial（存疑）；invalid 不进任何分子分母。
func TestComputeStats(t *testing.T) {
	samples := []store.ReviewSample{
		sample("pass", "fail"),    // 明确漏报
		sample("pass", "fail"),    // 明确漏报
		sample("partial", "fail"), // 存疑漏报
		sample("fail", "fail"),    // 命中
		sample("pass", "pass"),    // 真放行
		sample("pass", "pass"),    // 真放行
		sample("fail", "pass"),    // 明确误报
		sample("partial", "pass"), // 存疑误报
		sample("fail", "invalid"), // 无效：不进任何分子分母
	}
	st := computeStats(samples, store.ReviewBacklog{Completed: 20, Marked: 9, Invalid: 1})

	if st.Samples != 8 {
		t.Fatalf("有效样本应为 8（去掉 invalid），实际 %d", st.Samples)
	}
	if st.HumanFail != 4 || st.HumanPass != 4 {
		t.Fatalf("分母不对: 人工未通过 %d / 通过 %d", st.HumanFail, st.HumanPass)
	}
	if st.MissedClear != 2 || st.MissedDoubt != 1 || st.Hit != 1 {
		t.Fatalf("漏报与命中不对: clear=%d doubt=%d hit=%d", st.MissedClear, st.MissedDoubt, st.Hit)
	}
	if st.AlarmClear != 1 || st.AlarmDoubt != 1 || st.Real != 2 {
		t.Fatalf("误报与放行不对: clear=%d doubt=%d real=%d", st.AlarmClear, st.AlarmDoubt, st.Real)
	}
	// 漏报率 = (2+1)/4 = 0.75；误报率 = (1+1)/4 = 0.5
	if got := st.MissedRate; got < 0.749 || got > 0.751 {
		t.Fatalf("漏报率应为 0.75，实际 %v", got)
	}
	if got := st.AlarmRate; got < 0.499 || got > 0.501 {
		t.Fatalf("误报率应为 0.5，实际 %v", got)
	}
	if st.Completed != 20 || st.Marked != 9 || st.Invalid != 1 {
		t.Fatalf("覆盖率数据不对: %+v", st)
	}
	if len(st.Matrix) != 6 {
		t.Fatalf("矩阵应固定 6 格，实际 %d", len(st.Matrix))
	}
}

// 没有样本时分母为 0，比率必须是 0 而不是 NaN，前端才能显示「—」。
func TestComputeStatsEmpty(t *testing.T) {
	st := computeStats(nil, store.ReviewBacklog{})
	if st.Samples != 0 || st.MissedRate != 0 || st.AlarmRate != 0 {
		t.Fatalf("空样本应该全 0: %+v", st)
	}
	if len(st.Matrix) != 6 {
		t.Fatalf("空样本也要有形状固定的矩阵，实际 %d 格", len(st.Matrix))
	}
}

// 只有 invalid 样本时，同样不能算出比率。
func TestComputeStatsOnlyInvalid(t *testing.T) {
	st := computeStats([]store.ReviewSample{sample("pass", "invalid")}, store.ReviewBacklog{Completed: 1, Marked: 1, Invalid: 1})
	if st.Samples != 0 || st.HumanFail != 0 || st.HumanPass != 0 {
		t.Fatalf("invalid 不应进任何分母: %+v", st)
	}
	if st.MissedRate != 0 || st.AlarmRate != 0 {
		t.Fatalf("分母为 0 时比率应为 0: %+v", st)
	}
}
