package server

import (
	"testing"

	"verdict/internal/protocol"
	"verdict/internal/server/store"
)

func TestSuggestConcurrencyUsesMemoryBudget(t *testing.T) {
	old := memPerTestMB
	memPerTestMB = 12288
	defer func() { memPerTestMB = old }()

	cases := []struct {
		info *protocol.HostInfo
		want int
	}{
		{nil, 1},
		{&protocol.HostInfo{CPU: 8, MemTotalMB: 16 * 1024}, 1},   // 16GB / 12GB = 1
		{&protocol.HostInfo{CPU: 16, MemTotalMB: 32 * 1024}, 2},  // 2
		{&protocol.HostInfo{CPU: 16, MemTotalMB: 64 * 1024}, 5},  // 5
		{&protocol.HostInfo{CPU: 4, MemTotalMB: 128 * 1024}, 2},  // CPU 限制
		{&protocol.HostInfo{CPU: 32, MemTotalMB: 256 * 1024}, 8}, // 上限 8
	}
	for _, c := range cases {
		if got := suggestConcurrency(c.info); got != c.want {
			t.Errorf("suggestConcurrency(%+v)=%d，期望 %d", c.info, got, c.want)
		}
	}
}

func TestServiceSlotDefaults(t *testing.T) {
	rt := store.Runtime{Info: &protocol.HostInfo{CPU: 16, MemTotalMB: 64 * 1024}}
	if got := effectiveConcurrency(rt); got != 5 {
		t.Fatalf("并发度应为 5，得到 %d", got)
	}
	if got := suggestServiceSlots(rt); got != 3 {
		t.Fatalf("并发度 5 时建议服务槽位应为 3，得到 %d", got)
	}
	if got := effectiveServiceSlots(rt); got != 3 {
		t.Fatalf("自动服务槽位应为 3，得到 %d", got)
	}

	rt.ServiceSlots = 1
	if got := effectiveServiceSlots(rt); got != 1 {
		t.Fatalf("手动设置 1 应生效，得到 %d", got)
	}
	// 不管填多大，都不能超过并发度。
	rt.ServiceSlots = 32
	if got := effectiveServiceSlots(rt); got != 5 {
		t.Fatalf("服务槽位应被并发度截断为 5，得到 %d", got)
	}

	rt = store.Runtime{Concurrency: 2}
	if got := effectiveServiceSlots(rt); got != 2 {
		t.Fatalf("并发度 2 时服务槽位应为 2，得到 %d", got)
	}
	rt = store.Runtime{Concurrency: 1}
	if got := effectiveServiceSlots(rt); got != 1 {
		t.Fatalf("并发度 1 时服务槽位应为 1，得到 %d", got)
	}
}
