package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ---- 日志与事件序号 ----

func (s *Store) LastSeq(ctx context.Context, runID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT last_seq FROM run_seq WHERE run_id=?`, runID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// AcceptSeq 记录已处理的 seq；seq 不大于已记录值时返回 false（重复事件）。
func (s *Store) AcceptSeq(ctx context.Context, runID string, seq int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO run_seq(run_id,last_seq) VALUES(?,?)
 ON CONFLICT(run_id) DO UPDATE SET last_seq=excluded.last_seq WHERE excluded.last_seq > run_seq.last_seq`, runID, seq)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) AppendLog(ctx context.Context, runID, stage string, seq, ts int64, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO run_logs(run_id,seq,ts,text,stage) VALUES(?,?,?,?,?)`, runID, seq, ts, text, stage)
	return err
}

type LogLine struct {
	Seq   int64  `json:"seq"`
	TS    int64  `json:"ts"`
	Stage string `json:"stage"`
	Text  string `json:"text"`
}

func (s *Store) Logs(ctx context.Context, runID string, after int64, limit int) ([]LogLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,ts,stage,text FROM run_logs WHERE run_id=? AND seq>? ORDER BY seq LIMIT ?`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Seq, &l.TS, &l.Stage, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- 产物 ----

type Artifact struct {
	RunID       string     `json:"run_id"`
	Name        string     `json:"name"`
	StageID     string     `json:"stage_id"`
	Size        int64      `json:"size"`
	ContentType string     `json:"content_type"`
	CreatedAt   *time.Time `json:"created_at"`
}

func (s *Store) AddArtifact(ctx context.Context, a Artifact) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO artifacts(run_id,name,stage_id,size,content_type,created_at) VALUES(?,?,?,?,?,?)
 ON CONFLICT(run_id,name) DO UPDATE SET stage_id=excluded.stage_id, size=excluded.size, content_type=excluded.content_type`,
		a.RunID, a.Name, a.StageID, a.Size, a.ContentType, now())
	return err
}

func (s *Store) Artifacts(ctx context.Context, runID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,name,stage_id,size,content_type,created_at FROM artifacts WHERE run_id=? ORDER BY created_at, name`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		var created string
		if err := rows.Scan(&a.RunID, &a.Name, &a.StageID, &a.Size, &a.ContentType, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Artifact(ctx context.Context, runID, name string) (Artifact, error) {
	var a Artifact
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,name,stage_id,size,content_type,created_at FROM artifacts WHERE run_id=? AND name=?`, runID, name).
		Scan(&a.RunID, &a.Name, &a.StageID, &a.Size, &a.ContentType, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.CreatedAt = parseTime(created)
	return a, err
}

func (s *Store) CountArtifacts(ctx context.Context, runID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE run_id=?`, runID).Scan(&n)
	return n, err
}
