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
	CreatedAt  *time.Time         `json:"created_at"`
	StartedAt  *time.Time         `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at"`
}

const stageCols = `id, run_id, seq, role, round, kind, harness, status, note, error, spec, output, cost_usd, duration_ms, service, artifacts, created_at, started_at, finished_at`

func scanStage(sc interface{ Scan(...any) error }) (Stage, error) {
	var st Stage
	var spec, output, service, artifacts, created, started, finished string
	err := sc.Scan(&st.ID, &st.RunID, &st.Seq, &st.Role, &st.Round, &st.Kind, &st.Harness, &st.Status, &st.Note, &st.Error,
		&spec, &output, &st.CostUSD, &st.DurationMS, &service, &artifacts, &created, &started, &finished)
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
func (s *Store) AddStage(ctx context.Context, runID string, spec testplan.StageSpec, status, note string) (Stage, error) {
	id := runID + "-" + RandomHex(3)
	// 规格里的提示词很大，但需要留档供报告页查看。
	_, err := s.db.ExecContext(ctx, `INSERT INTO stages(id, run_id, seq, role, round, kind, harness, status, note, spec, created_at, finished_at)
 VALUES(?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM stages WHERE run_id=?), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, runID, spec.Role, spec.Round, spec.Kind, spec.Harness, status, note, toJSON(spec), now(),
		map[bool]string{true: now(), false: ""}[status == testplan.StatusSkipped])
	if err != nil {
		return Stage{}, err
	}
	return s.Stage(ctx, id)
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
	res, err := s.db.ExecContext(ctx, `UPDATE stages SET status=?, error=?, output=?, cost_usd=?, duration_ms=?, service=?, artifacts=?, finished_at=?,
 started_at=CASE WHEN started_at='' THEN ? ELSE started_at END
 WHERE id=? AND status IN (?,?,?)`,
		r.Status, r.Error, string(r.Structured), r.CostUSD, r.DurationMS, toJSON(r.Service), toJSON(nonNil(r.Artifacts)), now(), now(),
		id, testplan.StatusPending, testplan.StatusSent, testplan.StatusRunning)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
