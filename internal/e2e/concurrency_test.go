package e2e

import (
	"testing"

	"verdict/internal/server/store"
)

// TestConcurrency 验证一台运行时能同时跑多个测试，并且派单数量不超过并发度。
func TestConcurrency(t *testing.T) {
	e := setup(t)
	// 默认按机器给建议并发度，这里显式设成 2，避免依赖测试机的核数。
	if code := e.call("PATCH", "/api/runtimes/"+itoa(e.rtID), map[string]any{"concurrency": 2}, nil); code != 200 {
		t.Fatalf("设置并发度失败: %d", code)
	}

	// 并发数上限由 fake harness 记录：两个测试同时在跑时，峰值应该到 2。
	peak := e.claude.peak()
	if peak != 0 {
		t.Fatalf("初始峰值应为 0: %d", peak)
	}

	// 前两个带 SLOW：执行验证阶段会停留几秒，保证两个测试真正重叠。
	const n = 4
	ids := make([]string, 0, n)
	for i := range n {
		desc := "并发"
		if i < 2 {
			desc = "并发 SLOW"
		}
		ids = append(ids, e.newRun("C", desc, nil).ID)
	}
	for _, id := range ids {
		if r := e.waitDone(id); r.Status != store.StatusCompleted {
			t.Fatalf("测试 %s 应完成: %s %s", id, r.Status, r.Error)
		}
	}
	// fake harness 的并发峰值说明至少有两个测试同时在执行阶段。
	if got := e.claude.peak(); got < 2 {
		t.Fatalf("应该出现并发执行，实际峰值 %d", got)
	}
	if got := e.claude.peak(); got > 2 {
		t.Fatalf("并发数不应超过设置的 2，实际峰值 %d", got)
	}
}

// TestConcurrencyOneKeepsPreviousBehavior 并发度 1 时行为和以前一致：同一时间只有一个测试在执行。
func TestConcurrencyOneKeepsPreviousBehavior(t *testing.T) {
	e := setup(t)
	if code := e.call("PATCH", "/api/runtimes/"+itoa(e.rtID), map[string]any{"concurrency": 1}, nil); code != 200 {
		t.Fatalf("设置并发度失败: %d", code)
	}
	a := e.newRun("C", "串行", nil)
	b := e.newRun("C", "串行", nil)
	e.waitDone(a.ID)
	got := e.waitDone(b.ID)
	if got.Status != store.StatusCompleted {
		t.Fatalf("应完成: %s %s", got.Status, got.Error)
	}
	if peak := e.claude.peak(); peak > 1 {
		t.Fatalf("并发度 1 时不应并发，实际峰值 %d", peak)
	}
}

// peak 返回同时执行的最大 harness 调用数。
func (f *fake) peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}
