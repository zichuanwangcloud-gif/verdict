package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"verdict/internal/protocol"
	"verdict/internal/testplan"
)

// Stage 是一个阶段的完整记录。
type Stage struct {
	testplan.StageRecord
	RunID      string             `json:"run_id"`
	Spec       testplan.StageSpec `json:"spec"`
	// FailureKind 是平台对失败原因的分类（transient / auth / other）。
	FailureKind string     `json:"failure_kind"`
	Attempt     int        `json:"attempt"`
	NextAttempt *time.Time `json:"next_attempt_at"`
	CreatedAt   *time.Time `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

const stageCols = `id, run_id, seq, role, round, kind, harness, status, note, error, spec, output, cost_usd, duration_ms, service, artifacts,
 failure_kind, attempt, next_attempt_at, created_at, started_at, finished_at`

func scanStage(sc interface{ Scan(...any) error }) (Stage, error) {
	var st Stage
	var spec, output, service, artifacts, next, created, started, finished string
	err := sc.Scan(&st.ID, &st.RunID, &st.Seq, &st.Role, &st.Round, &st.Kind, &st.Harness, &st.Status, &st.Note, &st.Error,
		&spec, &output, &st.CostUSD, &st.DurationMS, &service, &artifacts, &st.FailureKind, &st.Attempt, &next, &created, &started, &finished)
	st.NextAttempt = parseTime(next)
	if err != nil {
		return st, err
	}
	_ = json.Unmarshal([]byte(spec), &st.Spec)
	if output != "" {
		st.Output = json.RawMessage(output)
	}
	_ = json.Unmarshal([]byte(service), &st.Service)
	_ = json.Unmarshal([]byte(artifacts), &st.Artifacts)
	if st.Artifacts == nil {
		st.Artifacts = []string{}
	}
	if st.Attempt <= 0 {
		st.Attempt = 1
	}
	st.CreatedAt, st.StartedAt, st.FinishedAt = parseTime(created), parseTime(started), parseTime(finished)
	return st, nil
}

func (s *Store) Stages(ctx context.Context, runID string) ([]Stage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stageCols+` FROM stages WHERE run_id=? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Stage{}
	for rows.Next() {
		st, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) Stage(ctx context.Context, id string) (Stage, error) {
	st, err := scanStage(s.db.QueryRowContext(ctx, `SELECT `+stageCols+` FROM stages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}

// Records 把阶段转换成流水线使用的记录。
func Records(stages []Stage) []testplan.StageRecord {
	out := make([]testplan.StageRecord, len(stages))
	for i, st := range stages {
		out[i] = st.StageRecord
	}
	return out
}

// AddStage 追加一个阶段。status 为 pending（待派发）或 skipped（直接记录跳过）。
func (s *Store) AddStage(ctx context.Context, runID string, spec testplan.StageSpec, status, note string, attempt int) (Stage, error) {
	id := runID + "-" + RandomHex(3)
	if attempt <= 0 {
		attempt = 1
	}
	// 规格里的提示词很大，但需要留档供报告页查看。
	_, err := s.db.ExecContext(ctx, `INSERT INTO stages(id, run_id, seq, role, round, kind, harness, status, note, spec, attempt, created_at, finished_at)
 VALUES(?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM stages WHERE run_id=?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, runID, spec.Role, spec.Round, spec.Kind, spec.Harness, status, note, toJSON(spec), attempt, now(),
		map[bool]string{true: now(), false: ""}[status == testplan.StatusSkipped])
	if err != nil {
		return Stage{}, err
	}
	return s.Stage(ctx, id)
}

// AddPresetPlan 直接写入一个已完成的计划阶段（回归测试复用旧计划时用）。
func (s *Store) AddPresetPlan(ctx context.Context, runID, source string, plan string) error {
	spec := testplan.StageSpec{Role: testplan.RolePlan, Kind: protocol.StageKindHarness}
	id := runID + "-" + RandomHex(3)
	t := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO stages(id, run_id, seq, role, round, kind, harness, status, note, spec, output, created_at, started_at, finished_at)
 VALUES(?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM stages WHERE run_id=?), ?, 0, ?, '', ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, runID, testplan.RolePlan, protocol.StageKindHarness, testplan.StatusOK,
		"复用测试 "+source+" 的计划（回归不重新规划）", toJSON(spec), plan, t, t, t)
	return err
}

func (s *Store) MarkStage(ctx context.Context, id, status string) error {
	q := `UPDATE stages SET status=?`
	args := []any{status}
	if status == testplan.StatusRunning {
		q += `, started_at=CASE WHEN started_at='' THEN ? ELSE started_at END`
		args = append(args, now())
	}
	q += ` WHERE id=? AND status IN (?,?,?)`
	args = append(args, id, testplan.StatusPending, testplan.StatusSent, testplan.StatusRunning)
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// FinishStage 写入阶段结果；已有结果时不覆盖，返回是否写入。
func (s *Store) FinishStage(ctx context.Context, id string, r protocol.StageResult) (bool, error) {
	kind := ""
	if r.Status == testplan.StatusError || r.Status == testplan.StatusTimeout {
		kind = testplan.ClassifyFailure(r.Status, r.Error)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE stages SET status=?, error=?, output=?, cost_usd=?, duration_ms=?, service=?, artifacts=?, failure_kind=?, finished_at=?,
 started_at=CASE WHEN started_at='' THEN ? ELSE started_at END
 WHERE id=? AND status IN (?,?,?)`,
		r.Status, r.Error, string(r.Structured), r.CostUSD, r.DurationMS, toJSON(r.Service), toJSON(nonNil(r.Artifacts)), kind, now(), now(),
		id, testplan.StatusPending, testplan.StatusSent, testplan.StatusRunning)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetStageRetry 把失败的阶段标为等待重试，并记下重试时间。
func (s *Store) SetStageRetry(ctx context.Context, id string, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stages SET status=?, next_attempt_at=?, error=error||'（等待重试）' WHERE id=? AND status IN (?,?)`,
		testplan.StatusRetryWait, next.UTC().Format(time.RFC3339), id, testplan.StatusError, testplan.StatusTimeout)
	return err
}

// ClearStageRetry 把到点的重试阶段恢复成失败状态（保留失败原因），让流水线重新创建尝试。
func (s *Store) ClearStageRetry(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stages SET status=CASE WHEN failure_kind='transient' AND error LIKE '%超时%' THEN ? ELSE ? END,
 next_attempt_at='' WHERE id=? AND status=?`, testplan.StatusTimeout, testplan.StatusError, id, testplan.StatusRetryWait)
	return err
}

// DueRetries 返回已经到点、等待重试的阶段。
func (s *Store) DueRetries(ctx context.Context, now time.Time) ([]Stage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stageCols+` FROM stages WHERE status=? AND next_attempt_at<>'' AND next_attempt_at<=? ORDER BY next_attempt_at`,
		testplan.StatusRetryWait, now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stage
	for rows.Next() {
		st, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// CancelOpenStages 把还没有结果的阶段标记为取消（测试被取消或中断时）。
func (s *Store) CancelOpenStages(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stages SET status=?, finished_at=? WHERE run_id=? AND status IN (?,?,?)`,
		testplan.StatusCanceled, now(), runID, testplan.StatusPending, testplan.StatusSent, testplan.StatusRunning)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
