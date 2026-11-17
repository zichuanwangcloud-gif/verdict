// Package store 是后端的 SQLite 持久层。
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"

	"verdict/internal/linear"
	"verdict/internal/protocol"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtimes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  info TEXT NOT NULL DEFAULT 'null',
  last_seen TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS repos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  test_notes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  branch TEXT NOT NULL,
  commit_sha TEXT NOT NULL DEFAULT '',
  issue_key TEXT NOT NULL,
  issue TEXT NOT NULL,
  runtime_id INTEGER NOT NULL REFERENCES runtimes(id),
  harness TEXT NOT NULL,
  timeout_minutes INTEGER NOT NULL,
  status TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  acs TEXT NOT NULL DEFAULT '[]',
  raw TEXT NOT NULL DEFAULT '',
  prompt TEXT NOT NULL,
  source_modified INTEGER NOT NULL DEFAULT 0,
  diff TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  linear_comment_url TEXT NOT NULL DEFAULT '',
  linear_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS runs_runtime ON runs(runtime_id, status);
CREATE TABLE IF NOT EXISTS run_logs (
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  ts INTEGER NOT NULL,
  text TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);
CREATE TABLE IF NOT EXISTS run_seq (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  last_seq INTEGER NOT NULL
);`

// 测试状态。
const (
	StatusQueued     = "queued"
	StatusDispatched = "dispatched"
	StatusPreparing  = "preparing"
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusError      = "error"
	StatusCanceled   = "canceled"
)

// Active 表示测试已交给运行时、尚未结束。
func Active(status string) bool {
	return status == StatusDispatched || status == StatusPreparing || status == StatusRunning
}

func Terminal(status string) bool {
	return status == StatusCompleted || status == StatusError || status == StatusCanceled
}

var ErrNotFound = errors.New("不存在")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// SQLite 单写者；串行化连接避免 SQLITE_BUSY。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	return &t
}

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// ---- settings ----

func (s *Store) Setting(ctx context.Context, k string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, k, v string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// ---- runtimes ----

type Runtime struct {
	ID        int64              `json:"id"`
	Name      string             `json:"name"`
	Info      *protocol.HostInfo `json:"info"`
	LastSeen  *time.Time         `json:"last_seen"`
	CreatedAt *time.Time         `json:"created_at"`
	Online    bool               `json:"online"` // 由 hub 填充
}

// CreateRuntime 返回运行时与明文令牌（只此一次）。
func (s *Store) CreateRuntime(ctx context.Context, name string) (Runtime, string, error) {
	token := "vrt_" + RandomHex(24)
	res, err := s.db.ExecContext(ctx, `INSERT INTO runtimes(name, token_hash, created_at) VALUES(?,?,?)`, name, HashToken(token), now())
	if err != nil {
		return Runtime{}, "", err
	}
	id, _ := res.LastInsertId()
	rt, err := s.Runtime(ctx, id)
	return rt, token, err
}

const runtimeCols = `id, name, info, last_seen, created_at`

func scanRuntime(sc interface{ Scan(...any) error }) (Runtime, error) {
	var rt Runtime
	var info, seen, created string
	if err := sc.Scan(&rt.ID, &rt.Name, &info, &seen, &created); err != nil {
		return rt, err
	}
	_ = json.Unmarshal([]byte(info), &rt.Info)
	rt.LastSeen, rt.CreatedAt = parseTime(seen), parseTime(created)
	return rt, nil
}

func (s *Store) Runtime(ctx context.Context, id int64) (Runtime, error) {
	rt, err := scanRuntime(s.db.QueryRowContext(ctx, `SELECT `+runtimeCols+` FROM runtimes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return rt, ErrNotFound
	}
	return rt, err
}

func (s *Store) RuntimeByToken(ctx context.Context, token string) (Runtime, error) {
	rt, err := scanRuntime(s.db.QueryRowContext(ctx, `SELECT `+runtimeCols+` FROM runtimes WHERE token_hash=?`, HashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return rt, ErrNotFound
	}
	return rt, err
}

func (s *Store) Runtimes(ctx context.Context) ([]Runtime, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runtimeCols+` FROM runtimes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Runtime{}
	for rows.Next() {
		rt, err := scanRuntime(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRuntimeInfo(ctx context.Context, id int64, info protocol.HostInfo) error {
	b, _ := json.Marshal(info)
	_, err := s.db.ExecContext(ctx, `UPDATE runtimes SET info=?, last_seen=? WHERE id=?`, string(b), now(), id)
	return err
}

func (s *Store) TouchRuntime(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runtimes SET last_seen=? WHERE id=?`, now(), id)
	return err
}

func (s *Store) RenameRuntime(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runtimes SET name=? WHERE id=?`, name, id)
	return err
}

// DeleteRuntime 删除运行时；它名下未结束的测试一并标记为 error。
func (s *Store) DeleteRuntime(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, error='运行时已被删除', finished_at=? WHERE runtime_id=? AND status IN (?,?,?,?)`,
		StatusError, now(), id, StatusQueued, StatusDispatched, StatusPreparing, StatusRunning); err != nil {
		return err
	}
	// 测试记录保留，runtime 行改成墓碑：清掉令牌使其无法再连。
	if _, err := tx.ExecContext(ctx, `UPDATE runtimes SET token_hash='deleted:'||id, name=name||'（已删除）' WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- repos ----

type Repo struct {
	ID        int64      `json:"id"`
	URL       string     `json:"url"`
	Name      string     `json:"name"`
	TestNotes string     `json:"test_notes"`
	CreatedAt *time.Time `json:"created_at"`
}

func scanRepo(sc interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var created string
	err := sc.Scan(&r.ID, &r.URL, &r.Name, &r.TestNotes, &created)
	r.CreatedAt = parseTime(created)
	return r, err
}

func (s *Store) Repos(ctx context.Context) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,url,name,test_notes,created_at FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Repo(ctx context.Context, id int64) (Repo, error) {
	r, err := scanRepo(s.db.QueryRowContext(ctx, `SELECT id,url,name,test_notes,created_at FROM repos WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) CreateRepo(ctx context.Context, url, name, notes string) (Repo, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO repos(url,name,test_notes,created_at) VALUES(?,?,?,?)`, url, name, notes, now())
	if err != nil {
		return Repo{}, err
	}
	id, _ := res.LastInsertId()
	return s.Repo(ctx, id)
}

func (s *Store) UpdateRepo(ctx context.Context, id int64, url, name, notes string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE repos SET url=?, name=?, test_notes=? WHERE id=?`, url, name, notes, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE repo_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("该仓库已有测试记录，不能删除")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM repos WHERE id=?`, id)
	return err
}

// ---- runs ----

type Run struct {
	ID               string              `json:"id"`
	RepoID           int64               `json:"repo_id"`
	RepoURL          string              `json:"repo_url"`
	RepoName         string              `json:"repo_name"`
	Branch           string              `json:"branch"`
	CommitSHA        string              `json:"commit_sha"`
	IssueKey         string              `json:"issue_key"`
	Issue            linear.Issue        `json:"issue"`
	RuntimeID        int64               `json:"runtime_id"`
	RuntimeName      string              `json:"runtime_name"`
	Harness          string              `json:"harness"`
	TimeoutMinutes   int                 `json:"timeout_minutes"`
	Status           string              `json:"status"`
	Verdict          string              `json:"verdict"`
	Summary          string              `json:"summary"`
	ACs              []protocol.ACResult `json:"acs"`
	Raw              string              `json:"raw,omitempty"`
	Prompt           string              `json:"prompt,omitempty"`
	SourceModified   bool                `json:"source_modified"`
	Diff             string              `json:"diff,omitempty"`
	CostUSD          float64             `json:"cost_usd"`
	Error            string              `json:"error"`
	LinearCommentURL string              `json:"linear_comment_url"`
	LinearError      string              `json:"linear_error"`
	CreatedAt        *time.Time          `json:"created_at"`
	StartedAt        *time.Time          `json:"started_at"`
	FinishedAt       *time.Time          `json:"finished_at"`
}

const runCols = `r.id, r.repo_id, p.url, p.name, r.branch, r.commit_sha, r.issue_key, r.issue, r.runtime_id, t.name,
 r.harness, r.timeout_minutes, r.status, r.verdict, r.summary, r.acs, r.raw, r.prompt, r.source_modified, r.diff,
 r.cost_usd, r.error, r.linear_comment_url, r.linear_error, r.created_at, r.started_at, r.finished_at
 FROM runs r JOIN repos p ON p.id=r.repo_id JOIN runtimes t ON t.id=r.runtime_id`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var issue, acs, created, started, finished string
	var modified int
	err := sc.Scan(&r.ID, &r.RepoID, &r.RepoURL, &r.RepoName, &r.Branch, &r.CommitSHA, &r.IssueKey, &issue, &r.RuntimeID,
		&r.RuntimeName, &r.Harness, &r.TimeoutMinutes, &r.Status, &r.Verdict, &r.Summary, &acs, &r.Raw, &r.Prompt,
		&modified, &r.Diff, &r.CostUSD, &r.Error, &r.LinearCommentURL, &r.LinearError, &created, &started, &finished)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(issue), &r.Issue)
	_ = json.Unmarshal([]byte(acs), &r.ACs)
	if r.ACs == nil {
		r.ACs = []protocol.ACResult{}
	}
	r.SourceModified = modified != 0
	r.CreatedAt, r.StartedAt, r.FinishedAt = parseTime(created), parseTime(started), parseTime(finished)
	return r, nil
}

type NewRun struct {
	RepoID         int64
	Branch         string
	Issue          linear.Issue
	RuntimeID      int64
	Harness        string
	TimeoutMinutes int
	// Prompt 由调用方根据 ID 生成（提示词里要用到测试 ID）。
	Prompt func(id string) string
}

func (s *Store) CreateRun(ctx context.Context, n NewRun) (Run, error) {
	id := RandomHex(6)
	issue, _ := json.Marshal(n.Issue)
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs(id, repo_id, branch, issue_key, issue, runtime_id, harness, timeout_minutes, status, prompt, created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, n.RepoID, n.Branch, n.Issue.Identifier, string(issue), n.RuntimeID, n.Harness,
		n.TimeoutMinutes, StatusQueued, n.Prompt(id), now())
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` ORDER BY r.created_at DESC, r.rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		// 列表不带大字段。
		r.Prompt, r.Raw, r.Diff = "", "", ""
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsOnRuntime 返回某运行时上处于给定状态的测试，按创建顺序。
func (s *Store) RunsOnRuntime(ctx context.Context, runtimeID int64, statuses ...string) ([]Run, error) {
	q := `SELECT ` + runCols + ` WHERE r.runtime_id=? AND r.status IN (`
	args := []any{runtimeID}
	for i, st := range statuses {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, st)
	}
	q += `) ORDER BY r.created_at, r.rowid`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetStatus 只在当前状态属于 from 之一时更新，返回是否更新成功。
func (s *Store) SetStatus(ctx context.Context, id, to, errMsg string, from ...string) (bool, error) {
	q := `UPDATE runs SET status=?`
	args := []any{to}
	if errMsg != "" {
		q += `, error=?`
		args = append(args, errMsg)
	}
	switch {
	case to == StatusPreparing || to == StatusRunning:
		q += `, started_at=CASE WHEN started_at='' THEN ? ELSE started_at END`
		args = append(args, now())
	case Terminal(to):
		q += `, finished_at=?`
		args = append(args, now())
	}
	q += ` WHERE id=?`
	args = append(args, id)
	if len(from) > 0 {
		q += ` AND status IN (`
		for i, f := range from {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, f)
		}
		q += `)`
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) SetCommit(ctx context.Context, id, sha string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET commit_sha=? WHERE id=?`, sha, id)
	return err
}

// Finish 写入终态结果；已处于终态的测试不会被覆盖。
func (s *Store) Finish(ctx context.Context, id string, res protocol.RunResult) (bool, error) {
	status := res.Status
	if !Terminal(status) {
		status = StatusError
	}
	var verdict, summary string
	acs := []byte("[]")
	if res.Report != nil && status == StatusCompleted {
		verdict, summary = res.Report.Verdict(), res.Report.Summary
		acs, _ = json.Marshal(res.Report.ACs)
	}
	modified := 0
	if res.SourceModified {
		modified = 1
	}
	r, err := s.db.ExecContext(ctx, `UPDATE runs SET status=?, verdict=?, summary=?, acs=?, raw=?, source_modified=?, diff=?, cost_usd=?, error=?, finished_at=?
 WHERE id=? AND status NOT IN (?,?,?)`, status, verdict, summary, string(acs), string(res.Raw), modified, res.Diff, res.CostUSD, res.Error, now(),
		id, StatusCompleted, StatusError, StatusCanceled)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

func (s *Store) SetLinearResult(ctx context.Context, id, url, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET linear_comment_url=?, linear_error=? WHERE id=?`, url, errMsg, id)
	return err
}

// ---- logs / seq ----

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

func (s *Store) AppendLog(ctx context.Context, runID string, seq, ts int64, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO run_logs(run_id,seq,ts,text) VALUES(?,?,?,?)`, runID, seq, ts, text)
	return err
}

type LogLine struct {
	Seq  int64  `json:"seq"`
	TS   int64  `json:"ts"`
	Text string `json:"text"`
}

func (s *Store) Logs(ctx context.Context, runID string, after int64, limit int) ([]LogLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,ts,text FROM run_logs WHERE run_id=? AND seq>? ORDER BY seq LIMIT ?`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Seq, &l.TS, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
