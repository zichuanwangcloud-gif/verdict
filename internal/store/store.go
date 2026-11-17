// Package store 是 SQLite 持久层。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	_ "modernc.org/sqlite"

	"verdict/internal/domain"
)

const schema = `
CREATE TABLE IF NOT EXISTS areas (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS requirements (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  disposition TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS acs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  requirement_id INTEGER NOT NULL REFERENCES requirements(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  text TEXT NOT NULL,
  areas TEXT NOT NULL DEFAULT '[]',
  verifier TEXT NOT NULL DEFAULT '',
  params TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS changesets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ref TEXT NOT NULL UNIQUE,
  digest TEXT NOT NULL DEFAULT '',
  touched_areas TEXT NOT NULL DEFAULT '[]',
  analysis TEXT NOT NULL DEFAULT 'null',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS evidence (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ac_id INTEGER NOT NULL REFERENCES acs(id) ON DELETE CASCADE,
  changeset_id INTEGER NOT NULL REFERENCES changesets(id) ON DELETE CASCADE,
  verifier TEXT NOT NULL,
  passed INTEGER NOT NULL,
  output TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 单写者，避免 SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func strs(s string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ---- areas ----

func (s *Store) UpsertArea(ctx context.Context, a domain.Area) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO areas(name, description) VALUES(?, ?)
		 ON CONFLICT(name) DO UPDATE SET description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE areas.description END`,
		a.Name, a.Description)
	return err
}

func (s *Store) Areas(ctx context.Context) ([]domain.Area, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, description FROM areas ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Area{}
	for rows.Next() {
		var a domain.Area
		if err := rows.Scan(&a.Name, &a.Description); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- requirements & ACs ----

func (s *Store) CreateRequirement(ctx context.Context, r domain.Requirement) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO requirements(key, title, body, disposition, created_at) VALUES(?,?,?,?,?)`,
		r.Key, r.Title, r.Body, domain.DispositionActive, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetDisposition(ctx context.Context, id int64, d domain.Disposition) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requirements SET disposition = ? WHERE id = ?`, d, id)
	return err
}

// ReplaceACs 整体替换一个需求的验收条件，并登记其中出现的区域。
// 注意：替换会连带删除旧验收条件的证据。
func (s *Store) ReplaceACs(ctx context.Context, reqID int64, acs []domain.AC) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM acs WHERE requirement_id = ?`, reqID); err != nil {
		return err
	}
	for i, a := range acs {
		params := string(a.Params)
		if !json.Valid(a.Params) {
			params = "{}"
		}
		if a.Areas == nil {
			a.Areas = []string{}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO acs(requirement_id, seq, text, areas, verifier, params) VALUES(?,?,?,?,?,?)`,
			reqID, i+1, a.Text, toJSON(a.Areas), a.Verifier, params); err != nil {
			return err
		}
		for _, name := range a.Areas {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO areas(name) VALUES(?)`, name); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) Requirements(ctx context.Context) ([]domain.Requirement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, key, title, body, disposition, created_at FROM requirements ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := []domain.Requirement{}
	idx := map[int64]int{}
	for rows.Next() {
		var r domain.Requirement
		var created string
		if err := rows.Scan(&r.ID, &r.Key, &r.Title, &r.Body, &r.Disposition, &created); err != nil {
			rows.Close()
			return nil, err
		}
		r.CreatedAt = parseTime(created)
		r.ACs = []domain.AC{}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	arows, err := s.db.QueryContext(ctx, `SELECT id, requirement_id, seq, text, areas, verifier, params FROM acs ORDER BY requirement_id, seq`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var a domain.AC
		var areas, params string
		if err := arows.Scan(&a.ID, &a.RequirementID, &a.Seq, &a.Text, &areas, &a.Verifier, &params); err != nil {
			return nil, err
		}
		a.Areas = strs(areas)
		a.Params = json.RawMessage(params)
		if i, ok := idx[a.RequirementID]; ok {
			out[i].ACs = append(out[i].ACs, a)
		}
	}
	return out, arows.Err()
}

func (s *Store) Requirement(ctx context.Context, id int64) (domain.Requirement, bool, error) {
	all, err := s.Requirements(ctx)
	if err != nil {
		return domain.Requirement{}, false, err
	}
	for _, r := range all {
		if r.ID == id {
			return r, true, nil
		}
	}
	return domain.Requirement{}, false, nil
}

// ---- changesets ----

func (s *Store) CreateChangeSet(ctx context.Context, c domain.ChangeSet) (int64, error) {
	if c.TouchedAreas == nil {
		c.TouchedAreas = []string{}
	}
	analysis := string(c.Analysis)
	if !json.Valid(c.Analysis) {
		analysis = "null"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO changesets(ref, digest, touched_areas, analysis, created_at) VALUES(?,?,?,?,?)`,
		c.Ref, c.Digest, toJSON(c.TouchedAreas), analysis, now())
	if err != nil {
		return 0, err
	}
	for _, name := range c.TouchedAreas {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO areas(name) VALUES(?)`, name); err != nil {
			return 0, err
		}
	}
	id, _ := res.LastInsertId()
	return id, tx.Commit()
}

// ChangeSets 按先后返回；Seq 取自增主键。
func (s *Store) ChangeSets(ctx context.Context) ([]domain.ChangeSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ref, digest, touched_areas, analysis, created_at FROM changesets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ChangeSet{}
	for rows.Next() {
		var c domain.ChangeSet
		var areas, analysis, created string
		if err := rows.Scan(&c.ID, &c.Ref, &c.Digest, &areas, &analysis, &created); err != nil {
			return nil, err
		}
		c.Seq = c.ID
		c.TouchedAreas = strs(areas)
		c.Analysis = json.RawMessage(analysis)
		c.CreatedAt = parseTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- evidence ----

func (s *Store) AddEvidence(ctx context.Context, e domain.Evidence) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO evidence(ac_id, changeset_id, verifier, passed, output, created_at) VALUES(?,?,?,?,?,?)`,
		e.ACID, e.ChangeSetID, e.Verifier, e.Passed, e.Output, now())
	return err
}

func (s *Store) Evidence(ctx context.Context) ([]domain.Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ac_id, changeset_id, verifier, passed, output, created_at FROM evidence ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Evidence{}
	for rows.Next() {
		var e domain.Evidence
		var created string
		if err := rows.Scan(&e.ID, &e.ACID, &e.ChangeSetID, &e.Verifier, &e.Passed, &e.Output, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
