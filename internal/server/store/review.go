package store

import (
	"context"
	"time"
)

// ReviewSample 是一条「有真值」的测试，指标计算的最小单位。
type ReviewSample struct {
	RunID     string     `json:"run_id"`
	IssueKey  string     `json:"issue_key"`
	RepoID    int64      `json:"repo_id"`
	Tier      string     `json:"tier"`
	Harness   string     `json:"harness"`
	Verdict   string     `json:"verdict"` // agent 的三档结论
	Truth     string     `json:"truth"`   // 人工结论 pass / fail / invalid
	Source    string     `json:"source"`  // manual / ticket
	TicketRef string     `json:"ticket_ref"`
	Summary   string     `json:"summary"`
	CreatedAt *time.Time `json:"created_at"`
	MarkedAt  *time.Time `json:"marked_at"`
}

// ReviewFilter 是复盘页的筛选条件。零值表示不限。
type ReviewFilter struct {
	RepoID int64
	Tier   string
	Since  time.Time // 只看这个时间之后创建的测试
}

// ReviewSamples 返回带真值的验收测试，按标记时间倒序。
func (s *Store) ReviewSamples(ctx context.Context, f ReviewFilter) ([]ReviewSample, error) {
	q := `SELECT r.id, r.issue_key, r.repo_id, r.tier, r.harness, r.verdict, t.verdict, t.source, t.ticket_ref,
 r.summary, r.created_at, t.updated_at
 FROM runs r JOIN run_truths t ON t.run_id=r.id
 WHERE r.status='completed' AND ` + KindTestOnly
	args := []any{}
	if f.RepoID > 0 {
		q += ` AND r.repo_id=?`
		args = append(args, f.RepoID)
	}
	if f.Tier != "" {
		q += ` AND r.tier=?`
		args = append(args, f.Tier)
	}
	if !f.Since.IsZero() {
		q += ` AND r.created_at>=?`
		args = append(args, f.Since.Format(time.RFC3339))
	}
	q += ` ORDER BY t.updated_at DESC, r.rowid DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReviewSample{}
	for rows.Next() {
		var x ReviewSample
		var created, marked string
		if err := rows.Scan(&x.RunID, &x.IssueKey, &x.RepoID, &x.Tier, &x.Harness, &x.Verdict, &x.Truth,
			&x.Source, &x.TicketRef, &x.Summary, &created, &marked); err != nil {
			return nil, err
		}
		x.CreatedAt, x.MarkedAt = parseTime(created), parseTime(marked)
		out = append(out, x)
	}
	return out, rows.Err()
}

// ReviewBacklog 统计「还没标真值」的已完成测试，用来说明样本有多不完整。
type ReviewBacklog struct {
	Completed int `json:"completed"` // 已完成的验收测试总数
	Marked    int `json:"marked"`    // 其中已标真值的
	Invalid   int `json:"invalid"`   // 其中标为无效的
}

// ReviewBacklog 返回复核覆盖率所需的计数，筛选条件与 ReviewSamples 一致。
func (s *Store) ReviewBacklog(ctx context.Context, f ReviewFilter) (ReviewBacklog, error) {
	q := `SELECT COUNT(*), COUNT(t.run_id),
   SUM(CASE WHEN t.verdict='invalid' THEN 1 ELSE 0 END)
 FROM runs r LEFT JOIN run_truths t ON t.run_id=r.id
 WHERE r.status='completed' AND ` + KindTestOnly
	args := []any{}
	if f.RepoID > 0 {
		q += ` AND r.repo_id=?`
		args = append(args, f.RepoID)
	}
	if f.Tier != "" {
		q += ` AND r.tier=?`
		args = append(args, f.Tier)
	}
	if !f.Since.IsZero() {
		q += ` AND r.created_at>=?`
		args = append(args, f.Since.Format(time.RFC3339))
	}
	var b ReviewBacklog
	var invalid *int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&b.Completed, &b.Marked, &invalid); err != nil {
		return b, err
	}
	if invalid != nil {
		b.Invalid = *invalid
	}
	return b, nil
}
