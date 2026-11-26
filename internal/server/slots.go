package server

import (
	"context"
	"log"

	"verdict/internal/server/store"
)

// serviceSlotBusy 返回该运行时上已经占用的服务槽位数（不含 waitID 自己）。
func (h *Hub) serviceSlotBusy(ctx context.Context, runtimeID int64, waitID string) (int, error) {
	return h.st.CountServiceHolders(ctx, runtimeID, waitID)
}

// mayStartService 判断这次测试现在能不能启动托管服务。
// 已经占着槽位的测试（服务已常驻）永远放行，避免自己把自己卡住。
func (h *Hub) mayStartService(ctx context.Context, run store.Run) bool {
	rt, err := h.st.Runtime(ctx, run.RuntimeID)
	if err != nil {
		return true // 读不到运行时信息时不拦，交给后续阶段报错
	}
	limit := effectiveServiceSlots(rt)
	busy, err := h.serviceSlotBusy(ctx, run.RuntimeID, run.ID)
	if err != nil {
		log.Printf("测试 %s 统计服务槽位失败，按放行处理: %v", run.ID, err)
		return true
	}
	return busy < limit
}

// holdServiceSlot 在创建服务阶段之前先把等待标记清掉。
func (h *Hub) markWaitingSlot(ctx context.Context, runID string, waiting bool) {
	if err := h.st.SetWaitingSlot(ctx, runID, waiting); err != nil {
		log.Printf("测试 %s 更新等待槽位标记失败: %v", runID, err)
	}
}

// WakeSlotWaiters 在释放了一个服务槽位后，把正在等槽位的测试重新推进一遍。
// 只挑 waiting_slot=1 的活跃测试，避免对整台运行时做无谓的推进。
func (h *Hub) WakeSlotWaiters(runtimeID int64) {
	ctx := context.Background()
	runs, err := h.st.RunsOnRuntime(ctx, runtimeID, store.StatusRunning)
	if err != nil {
		return
	}
	for _, r := range runs {
		if !r.WaitingSlot {
			continue
		}
		go h.Advance(r.ID)
	}
}
