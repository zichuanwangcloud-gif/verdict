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
	"fmt"
	"time"

	_ "modernc.org/sqlite"

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
);
CREATE TABLE IF NOT EXISTS stages (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  round INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL,
  harness TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  spec TEXT NOT NULL DEFAULT '{}',
  output TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  service TEXT NOT NULL DEFAULT 'null',
  artifacts TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  UNIQUE (run_id, seq)
);
CREATE TABLE IF NOT EXISTS artifacts (
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  stage_id TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL,
  content_type TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (run_id, name)
);
CREATE TABLE IF NOT EXISTS versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  release_note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  released_at TEXT NOT NULL DEFAULT '',
  UNIQUE (repo_id, name)
);
CREATE TABLE IF NOT EXISTS version_issues (
  version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL,
  issue_key TEXT NOT NULL,
  issue TEXT NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY (version_id, issue_key),
  UNIQUE (repo_id, issue_key)
);
CREATE TABLE IF NOT EXISTS regressions (
  id TEXT PRIMARY KEY,
  version_id INTEGER NOT NULL REFERENCES versions(id),
  seq INTEGER NOT NULL,
  freeze_ref TEXT NOT NULL,
  freeze_full_ref TEXT NOT NULL DEFAULT '',
  freeze_sha TEXT NOT NULL,
  scope TEXT NOT NULL,
  config TEXT NOT NULL,
  runtime_ids TEXT NOT NULL,
  status TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS cases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  issue_key TEXT NOT NULL,
  issue TEXT NOT NULL,
  plan TEXT NOT NULL DEFAULT '',
  source_run_id TEXT NOT NULL,
  source_version_id INTEGER NOT NULL DEFAULT 0,
  level TEXT NOT NULL,
  status TEXT NOT NULL,
  retired_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (repo_id, issue_key)
);
CREATE TABLE IF NOT EXISTS case_history (
  case_id INTEGER NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
  ts TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL
);`

// migrations 给旧库补列：表名 → 列定义。只增不改，旧数据原样保留。
var migrations = []struct{ table, column, def string }{
	{"repos", "setup_cmd", "TEXT NOT NULL DEFAULT ''"},
	{"repos", "start_cmd", "TEXT NOT NULL DEFAULT ''"},
	{"repos", "health_url", "TEXT NOT NULL DEFAULT ''"},
	{"repos", "app_url", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "tier", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "config", "TEXT NOT NULL DEFAULT '{}'"},
	{"runs", "findings", "TEXT NOT NULL DEFAULT '[]'"},
	{"runs", "pages", "TEXT NOT NULL DEFAULT '[]'"},
	{"runs", "approved", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "approval_note", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "current_stage", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "awaiting_since", "TEXT NOT NULL DEFAULT ''"},
	{"run_logs", "stage", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "version_id", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "regression_id", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "case_id", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "pin_ref", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "pin_sha", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "plan_source", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "preset_plan", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "attempt", "INTEGER NOT NULL DEFAULT 1"},
}

// 测试状态。
const (
	StatusQueued     = "queued"
	StatusDispatched = "dispatched"
	StatusPreparing  = "preparing"
	StatusRunning    = "running"
	StatusAwaiting   = "awaiting_approval"
	StatusFinalizing = "finalizing"
	StatusCompleted  = "completed"
	StatusError      = "error"
	StatusCanceled   = "canceled"
)

// ActiveStatuses 是已交给运行时、尚未结束的状态。
var ActiveStatuses = []string{StatusDispatched, StatusPreparing, StatusRunning, StatusAwaiting, StatusFinalizing}

// Active 表示测试已交给运行时、尚未结束。
func Active(status string) bool {
	for _, s := range ActiveStatuses {
		if s == status {
			return true
		}
	}
	return false
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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	have := map[string]map[string]bool{}
	for _, m := range migrations {
		if have[m.table] == nil {
			rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, m.table)
			if err != nil {
				return err
			}
			have[m.table] = map[string]bool{}
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					rows.Close()
					return err
				}
				have[m.table][n] = true
			}
			rows.Close()
		}
		if !have[m.table][m.column] {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, m.table, m.column, m.def)); err != nil {
				return err
			}
			have[m.table][m.column] = true
		}
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS stages_run ON stages(run_id, seq)`,
		`CREATE INDEX IF NOT EXISTS runs_version ON runs(version_id)`,
		`CREATE INDEX IF NOT EXISTS runs_regression ON runs(regression_id)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
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

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// placeholders 生成 "?,?,?" 与对应参数。
func placeholders(vals []string) (string, []any) {
	q := ""
	args := make([]any, 0, len(vals))
	for i, v := range vals {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, v)
	}
	return q, args
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
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, error='运行时已被删除', finished_at=? WHERE runtime_id=? AND status IN (?,?,?,?,?,?)`,
		StatusError, now(), id, StatusQueued, StatusDispatched, StatusPreparing, StatusRunning, StatusAwaiting, StatusFinalizing); err != nil {
		return err
	}
	// 测试记录保留，runtime 行改成墓碑：清掉令牌使其无法再连。
	if _, err := tx.ExecContext(ctx, `UPDATE runtimes SET token_hash='deleted:'||id, name=name||'（已删除）' WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RotateToken 为运行时生成新令牌，旧令牌立即失效。
func (s *Store) RotateToken(ctx context.Context, id int64) (string, error) {
	token := "vrt_" + RandomHex(24)
	res, err := s.db.ExecContext(ctx, `UPDATE runtimes SET token_hash=? WHERE id=? AND token_hash NOT LIKE 'deleted:%'`, HashToken(token), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}
