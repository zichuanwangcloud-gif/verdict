package store

import (
	"context"
	"fmt"
)

// serviceHoldingStatuses 是「这个测试正占着一个服务槽位」的阶段状态。
// pending/sent/running 表示服务正在启动（先占位，避免多个测试同时启动服务）；
// ok 表示服务已经常驻，直到测试收尾才释放。
var serviceHoldingStatuses = []string{"pending", "sent", "running", "ok"}

// CountServiceHolders 返回该运行时上正在占用服务槽位的测试数。
// 只看活跃测试，并且只看每个测试最新的一条 service 阶段：
// 服务阶段失败 / 跳过、或测试已经结束，都不再占槽位。
func (s *Store) CountServiceHolders(ctx context.Context, runtimeID int64, excludeRunID string) (int, error) {
	active, activeArgs := placeholders(ActiveStatuses)
	holding, holdingArgs := placeholders(serviceHoldingStatuses)
	args := []any{runtimeID}
	args = append(args, activeArgs...)
	args = append(args, holdingArgs...)
	q := `SELECT COUNT(*) FROM runs r
		WHERE r.runtime_id=? AND r.status IN (` + active + `)
		  AND (SELECT s.status FROM stages s WHERE s.run_id=r.id AND s.kind='service' ORDER BY s.seq DESC LIMIT 1) IN (` + holding + `)`
	if excludeRunID != "" {
		q += ` AND r.id<>?`
		args = append(args, excludeRunID)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计服务槽位占用失败: %w", err)
	}
	return n, nil
}

// SetWaitingSlot 标记测试是否正在等待服务槽位。
func (s *Store) SetWaitingSlot(ctx context.Context, id string, waiting bool) error {
	v := 0
	if waiting {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET waiting_slot=? WHERE id=?`, v, id)
	return err
}
