package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Repo struct {
	ID        int64      `json:"id"`
	URL       string     `json:"url"`
	Name      string     `json:"name"`
	TestNotes string     `json:"test_notes"`
	SetupCmd  string     `json:"setup_cmd"`
	StartCmd  string     `json:"start_cmd"`
	HealthURL string     `json:"health_url"`
	AppURL    string     `json:"app_url"`
	CreatedAt *time.Time `json:"created_at"`
}

const repoCols = `id, url, name, test_notes, setup_cmd, start_cmd, health_url, app_url, created_at`

func scanRepo(sc interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var created string
	err := sc.Scan(&r.ID, &r.URL, &r.Name, &r.TestNotes, &r.SetupCmd, &r.StartCmd, &r.HealthURL, &r.AppURL, &created)
	r.CreatedAt = parseTime(created)
	return r, err
}

func (s *Store) Repos(ctx context.Context) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repoCols+` FROM repos ORDER BY name`)
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
	r, err := scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) CreateRepo(ctx context.Context, r Repo) (Repo, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO repos(url, name, test_notes, setup_cmd, start_cmd, health_url, app_url, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		r.URL, r.Name, r.TestNotes, r.SetupCmd, r.StartCmd, r.HealthURL, r.AppURL, now())
	if err != nil {
		return Repo{}, err
	}
	id, _ := res.LastInsertId()
	return s.Repo(ctx, id)
}

func (s *Store) UpdateRepo(ctx context.Context, r Repo) error {
	res, err := s.db.ExecContext(ctx, `UPDATE repos SET url=?, name=?, test_notes=?, setup_cmd=?, start_cmd=?, health_url=?, app_url=? WHERE id=?`,
		r.URL, r.Name, r.TestNotes, r.SetupCmd, r.StartCmd, r.HealthURL, r.AppURL, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM versions WHERE repo_id=?`, id).Scan(&v); err != nil {
		return err
	}
	if v > 0 {
		return errors.New("该仓库还有版本，先删除版本")
	}
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
