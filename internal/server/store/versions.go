package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"verdict/internal/linear"
	"verdict/internal/testplan"
)

// 版本状态。
const (
	VersionOpen     = "open"
	VersionFrozen   = "frozen"
	VersionReleased = "released"
)

// 回归轮次状态与结论。
const (
	RegressionRunning   = "running"
	RegressionCompleted = "completed"
	RegressionCanceled  = "canceled"

	RegressionIncomplete = "incomplete" // 有子测试出错或被取消
)

// 回归用例。
const (
	CaseCore    = "core"
	CaseNormal  = "normal"
	CaseActive  = "active"
	CaseRetired = "retired"
)

// 回归范围里的基线选项。
const (
	BaselineCore = "core"
	BaselineAll  = "all"
	BaselineNone = "none"
)

var (
	ErrConflict = errors.New("冲突")
	// ErrIssueInOtherVersion 表示 issue 已属于同一仓库的另一个版本。
	ErrIssueInOtherVersion = errors.New("该 issue 已在本仓库的另一个版本里")
)

// ---- 版本 ----

type Version struct {
	ID          int64      `json:"id"`
	RepoID      int64      `json:"repo_id"`
	RepoName    string     `json:"repo_name"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Note        string     `json:"note"`
	ReleaseNote string     `json:"release_note"`
	CreatedAt   *time.Time `json:"created_at"`
	ReleasedAt  *time.Time `json:"released_at"`
	IssueCount  int        `json:"issue_count"`
}

const versionCols = `v.id, v.repo_id, p.name, v.name, v.status, v.note, v.release_note, v.created_at, v.released_at,
 (SELECT COUNT(*) FROM version_issues i WHERE i.version_id=v.id)
 FROM versions v JOIN repos p ON p.id=v.repo_id`

func scanVersion(sc interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var created, released string
	err := sc.Scan(&v.ID, &v.RepoID, &v.RepoName, &v.Name, &v.Status, &v.Note, &v.ReleaseNote, &created, &released, &v.IssueCount)
	v.CreatedAt, v.ReleasedAt = parseTime(created), parseTime(released)
	return v, err
}

func (s *Store) Versions(ctx context.Context, repoID int64) ([]Version, error) {
	q, args := `SELECT `+versionCols, []any{}
	if repoID > 0 {
		q += ` WHERE v.repo_id=?`
		args = append(args, repoID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY v.created_at DESC, v.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Version(ctx context.Context, id int64) (Version, error) {
	v, err := scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionCols+` WHERE v.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func uniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (s *Store) CreateVersion(ctx context.Context, repoID int64, name, note string) (Version, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO versions(repo_id, name, status, note, created_at) VALUES(?,?,?,?,?)`,
		repoID, name, VersionOpen, note, now())
	if uniqueErr(err) {
		return Version{}, ErrConflict
	}
	if err != nil {
		return Version{}, err
	}
	id, _ := res.LastInsertId()
	return s.Version(ctx, id)
}

func (s *Store) UpdateVersion(ctx context.Context, id int64, name, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE versions SET name=?, note=? WHERE id=?`, name, note, id)
	if uniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) SetVersionStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE versions SET status=? WHERE id=? AND status<>?`, status, id, VersionReleased)
	return err
}

// DeleteVersion 只删除没有回归轮次的版本；版本下的分支测试保留，只解除归属。
func (s *Store) DeleteVersion(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM regressions WHERE version_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("版本已有回归记录，不能删除")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET version_id=0 WHERE version_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM versions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RepoHasVersions 用于阻止删除仍有版本的仓库。
func (s *Store) RepoHasVersions(ctx context.Context, repoID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM versions WHERE repo_id=?`, repoID).Scan(&n)
	return n > 0, err
}

// ---- 版本成员 ----

type VersionIssue struct {
	VersionID int64        `json:"version_id"`
	IssueKey  string       `json:"issue_key"`
	Issue     linear.Issue `json:"issue"`
	AddedAt   *time.Time   `json:"added_at"`
}

func (s *Store) VersionIssues(ctx context.Context, versionID int64) ([]VersionIssue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version_id, issue_key, issue, added_at FROM version_issues WHERE version_id=? ORDER BY added_at, issue_key`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionIssue{}
	for rows.Next() {
		var vi VersionIssue
		var issue, added string
		if err := rows.Scan(&vi.VersionID, &vi.IssueKey, &issue, &added); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(issue), &vi.Issue)
		vi.AddedAt = parseTime(added)
		out = append(out, vi)
	}
	return out, rows.Err()
}

// IssueVersion 返回 issue 在该仓库里所属的版本（没有时返回 0）。
func (s *Store) IssueVersion(ctx context.Context, repoID int64, issueKey string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT version_id FROM version_issues WHERE repo_id=? AND issue_key=?`, repoID, issueKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// AddVersionIssue 把 issue 加入版本（已在本版本时更新快照）。issue 在同仓库的另一个版本里时，
// move=false 返回 ErrIssueInOtherVersion，move=true 则移动过来（已发布版本里的不能移动）。
func (s *Store) AddVersionIssue(ctx context.Context, v Version, issue linear.Issue, move bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur int64
	var curStatus string
	err = tx.QueryRowContext(ctx, `SELECT i.version_id, v.status FROM version_issues i JOIN versions v ON v.id=i.version_id
 WHERE i.repo_id=? AND i.issue_key=?`, v.RepoID, issue.Identifier).Scan(&cur, &curStatus)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case cur == v.ID:
		_, err = tx.ExecContext(ctx, `UPDATE version_issues SET issue=? WHERE version_id=? AND issue_key=?`, toJSON(issue), v.ID, issue.Identifier)
		if err != nil {
			return err
		}
		return tx.Commit()
	case curStatus == VersionReleased:
		// 已发布版本里的 issue 再次开发（需求迭代），这是新一轮成员，解除旧版本的唯一占用。
		if _, err := tx.ExecContext(ctx, `UPDATE version_issues SET repo_id=-repo_id WHERE version_id=? AND issue_key=?`, cur, issue.Identifier); err != nil {
			return err
		}
	case !move:
		return ErrIssueInOtherVersion
	default:
		if _, err := tx.ExecContext(ctx, `DELETE FROM version_issues WHERE version_id=? AND issue_key=?`, cur, issue.Identifier); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET version_id=? WHERE version_id=? AND issue_key=? AND regression_id=''`, v.ID, cur, issue.Identifier); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO version_issues(version_id, repo_id, issue_key, issue, added_at) VALUES(?,?,?,?,?)`,
		v.ID, v.RepoID, issue.Identifier, toJSON(issue), now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveVersionIssue(ctx context.Context, versionID int64, issueKey string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM version_issues WHERE version_id=? AND issue_key=?`, versionID, issueKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET version_id=0 WHERE version_id=? AND issue_key=? AND regression_id=''`, versionID, issueKey); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- 回归轮次 ----

type RegressionScope struct {
	Baseline string `json:"baseline"` // core / all / none
}

type RegressionConfig struct {
	Harness   string `json:"harness"`
	IssueTier string `json:"issue_tier"` // 本版本 issue 的档位
	CaseTier  string `json:"case_tier"`  // 基线用例的档位
}

type Regression struct {
	ID            string           `json:"id"`
	VersionID     int64            `json:"version_id"`
	Seq           int              `json:"seq"`
	FreezeRef     string           `json:"freeze_ref"`      // 用户填写的
	FreezeFullRef string           `json:"freeze_full_ref"` // 解析到的完整引用名；填 SHA 时为空
	FreezeSHA     string           `json:"freeze_sha"`
	Scope         RegressionScope  `json:"scope"`
	Config        RegressionConfig `json:"config"`
	RuntimeIDs    []int64          `json:"runtime_ids"`
	Status        string           `json:"status"`
	Verdict       string           `json:"verdict"`
	CreatedAt     *time.Time       `json:"created_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
}

const regressionCols = `id, version_id, seq, freeze_ref, freeze_full_ref, freeze_sha, scope, config, runtime_ids, status, verdict, created_at, finished_at`

func scanRegression(sc interface{ Scan(...any) error }) (Regression, error) {
	var g Regression
	var scope, cfg, rts, created, finished string
	err := sc.Scan(&g.ID, &g.VersionID, &g.Seq, &g.FreezeRef, &g.FreezeFullRef, &g.FreezeSHA, &scope, &cfg, &rts, &g.Status, &g.Verdict, &created, &finished)
	_ = json.Unmarshal([]byte(scope), &g.Scope)
	_ = json.Unmarshal([]byte(cfg), &g.Config)
	_ = json.Unmarshal([]byte(rts), &g.RuntimeIDs)
	g.CreatedAt, g.FinishedAt = parseTime(created), parseTime(finished)
	return g, err
}

func (s *Store) Regression(ctx context.Context, id string) (Regression, error) {
	g, err := scanRegression(s.db.QueryRowContext(ctx, `SELECT `+regressionCols+` FROM regressions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// Regressions 按轮次顺序返回版本的全部回归。
func (s *Store) Regressions(ctx context.Context, versionID int64) ([]Regression, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+regressionCols+` FROM regressions WHERE version_id=? ORDER BY seq`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Regression{}
	for rows.Next() {
		g, err := scanRegression(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RunningRegressionsFor 返回允许某运行时参与、仍在进行的回归。
func (s *Store) RunningRegressionsFor(ctx context.Context, runtimeID int64) ([]Regression, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+regressionCols+` FROM regressions WHERE status=? ORDER BY created_at`, RegressionRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Regression
	for rows.Next() {
		g, err := scanRegression(rows)
		if err != nil {
			return nil, err
		}
		for _, id := range g.RuntimeIDs {
			if id == runtimeID {
				out = append(out, g)
				break
			}
		}
	}
	return out, rows.Err()
}

// CreateRegression 在一个事务里创建回归轮次和全部子测试，并把版本标为已冻结。
func (s *Store) CreateRegression(ctx context.Context, g Regression, runs []NewRun) (Regression, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Regression{}, err
	}
	defer tx.Rollback()
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM regressions WHERE version_id=? AND status=?`, g.VersionID, RegressionRunning).Scan(&running); err != nil {
		return Regression{}, err
	}
	if running > 0 {
		return Regression{}, errors.New("这个版本还有一轮回归在进行，先等它结束或取消")
	}
	g.ID = RandomHex(6)
	if _, err := tx.ExecContext(ctx, `INSERT INTO regressions(id, version_id, seq, freeze_ref, freeze_full_ref, freeze_sha, scope, config, runtime_ids, status, created_at)
 VALUES(?, ?, (SELECT COALESCE(MAX(seq),0)+1 FROM regressions WHERE version_id=?), ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.VersionID, g.VersionID, g.FreezeRef, g.FreezeFullRef, g.FreezeSHA, toJSON(g.Scope), toJSON(g.Config), toJSON(g.RuntimeIDs),
		RegressionRunning, now()); err != nil {
		return Regression{}, err
	}
	for _, n := range runs {
		n.RegressionID = g.ID
		if _, err := s.insertRun(ctx, tx, n); err != nil {
			return Regression{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE versions SET status=? WHERE id=? AND status=?`, VersionFrozen, g.VersionID, VersionOpen); err != nil {
		return Regression{}, err
	}
	if err := tx.Commit(); err != nil {
		return Regression{}, err
	}
	return s.Regression(ctx, g.ID)
}

// FinishRegression 写入回归结论；只对进行中的轮次生效。
func (s *Store) FinishRegression(ctx context.Context, id, status, verdict string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE regressions SET status=?, verdict=?, finished_at=? WHERE id=? AND status=?`,
		status, verdict, now(), id, RegressionRunning)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReopenRegression 在重跑部分子测试时把轮次恢复为进行中。
func (s *Store) ReopenRegression(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE regressions SET status=?, verdict='', finished_at='' WHERE id=?`, RegressionRunning, id)
	return err
}

// AddRegressionRuns 给已有轮次追加子测试（重跑）。
func (s *Store) AddRegressionRuns(ctx context.Context, regressionID string, runs []NewRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range runs {
		n.RegressionID = regressionID
		if _, err := s.insertRun(ctx, tx, n); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE regressions SET status=?, verdict='', finished_at='' WHERE id=?`, RegressionRunning, regressionID); err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseVersion 标记版本已发布。
func (s *Store) ReleaseVersion(ctx context.Context, id int64, note string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE versions SET status=?, release_note=?, released_at=? WHERE id=? AND status<>?`,
		VersionReleased, note, now(), id, VersionReleased)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- 轻量测试记录（矩阵与历史用） ----

type RunBrief struct {
	ID           string     `json:"id"`
	RepoID       int64      `json:"repo_id"`
	IssueKey     string     `json:"issue_key"`
	Branch       string     `json:"branch"`
	CommitSHA    string     `json:"commit_sha"`
	VersionID    int64      `json:"version_id"`
	RegressionID string     `json:"regression_id"`
	CaseID       int64      `json:"case_id"`
	PinSHA       string     `json:"pin_sha"`
	Tier         string     `json:"tier"`
	Status       string     `json:"status"`
	Verdict      string     `json:"verdict"`
	Attempt      int        `json:"attempt"`
	RuntimeID    int64      `json:"runtime_id"`
	CostUSD      float64    `json:"cost_usd"`
	CreatedAt    *time.Time `json:"created_at"`
}

func (s *Store) briefs(ctx context.Context, where string, args ...any) ([]RunBrief, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, repo_id, issue_key, branch, commit_sha, version_id, regression_id, case_id, pin_sha, tier,
 status, verdict, attempt, runtime_id, cost_usd, created_at FROM runs WHERE `+where+` ORDER BY created_at, rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunBrief{}
	for rows.Next() {
		var b RunBrief
		var created string
		if err := rows.Scan(&b.ID, &b.RepoID, &b.IssueKey, &b.Branch, &b.CommitSHA, &b.VersionID, &b.RegressionID, &b.CaseID, &b.PinSHA,
			&b.Tier, &b.Status, &b.Verdict, &b.Attempt, &b.RuntimeID, &b.CostUSD, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// VersionRuns 返回版本下的全部测试（分支测试与回归子测试），按创建顺序。
func (s *Store) VersionRuns(ctx context.Context, versionID int64) ([]RunBrief, error) {
	return s.briefs(ctx, `version_id=?`, versionID)
}

// RegressionRuns 返回一轮回归的全部子测试（含重跑），按创建顺序。
func (s *Store) RegressionRuns(ctx context.Context, regressionID string) ([]RunBrief, error) {
	return s.briefs(ctx, `regression_id=?`, regressionID)
}

// RepoRegressionRuns 返回仓库里全部回归子测试，用于用例历史。
func (s *Store) RepoRegressionRuns(ctx context.Context, repoID int64) ([]RunBrief, error) {
	return s.briefs(ctx, `repo_id=? AND regression_id<>''`, repoID)
}

// LatestBranchRunWithPlan 返回某 issue 在版本里最近一次（非回归）测试中成功生成的计划。
func (s *Store) LatestBranchRunWithPlan(ctx context.Context, versionID int64, issueKey string) (string, *testplan.Plan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs WHERE version_id=? AND issue_key=? AND regression_id='' ORDER BY created_at DESC, rowid DESC LIMIT 10`,
		versionID, issueKey)
	if err != nil {
		return "", nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if p, err := s.RunPlan(ctx, id); err == nil && p != nil {
			return id, p, nil
		}
	}
	return "", nil, nil
}

// RunPlan 返回测试实际使用的计划（计划阶段的结果，含复用的计划）。
func (s *Store) RunPlan(ctx context.Context, runID string) (*testplan.Plan, error) {
	stages, err := s.Stages(ctx, runID)
	if err != nil {
		return nil, err
	}
	return testplan.ParsePlan(Records(stages)), nil
}

// ---- 回归用例 ----

type Case struct {
	ID              int64          `json:"id"`
	RepoID          int64          `json:"repo_id"`
	IssueKey        string         `json:"issue_key"`
	Issue           linear.Issue   `json:"issue"`
	Plan            *testplan.Plan `json:"plan"`
	SourceRunID     string         `json:"source_run_id"`
	SourceVersionID int64          `json:"source_version_id"`
	Level           string         `json:"level"`
	Status          string         `json:"status"`
	RetiredReason   string         `json:"retired_reason"`
	CreatedAt       *time.Time     `json:"created_at"`
	UpdatedAt       *time.Time     `json:"updated_at"`
}

const caseCols = `id, repo_id, issue_key, issue, plan, source_run_id, source_version_id, level, status, retired_reason, created_at, updated_at`

func scanCase(sc interface{ Scan(...any) error }) (Case, error) {
	var c Case
	var issue, plan, created, updated string
	err := sc.Scan(&c.ID, &c.RepoID, &c.IssueKey, &issue, &plan, &c.SourceRunID, &c.SourceVersionID, &c.Level, &c.Status, &c.RetiredReason, &created, &updated)
	_ = json.Unmarshal([]byte(issue), &c.Issue)
	if plan != "" {
		_ = json.Unmarshal([]byte(plan), &c.Plan)
	}
	c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
	return c, err
}

func (s *Store) Cases(ctx context.Context, repoID int64) ([]Case, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+caseCols+` FROM cases WHERE repo_id=? ORDER BY status, level, issue_key`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Case{}
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Case(ctx context.Context, id int64) (Case, error) {
	c, err := scanCase(s.db.QueryRowContext(ctx, `SELECT `+caseCols+` FROM cases WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) CaseByIssue(ctx context.Context, repoID int64, issueKey string) (Case, error) {
	c, err := scanCase(s.db.QueryRowContext(ctx, `SELECT `+caseCols+` FROM cases WHERE repo_id=? AND issue_key=?`, repoID, issueKey))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) addHistory(ctx context.Context, db execer, caseID int64, action, detail string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO case_history(case_id, ts, action, detail) VALUES(?,?,?,?)`, caseID, now(), action, detail)
	return err
}

// UpsertCase 新建或覆盖用例（同仓库同 issue 只有一条）。level 为空时新建用 normal、覆盖保留原级别；覆盖时重新启用。
func (s *Store) UpsertCase(ctx context.Context, c Case, action, detail string) (Case, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Case{}, err
	}
	defer tx.Rollback()
	plan := ""
	if c.Plan != nil {
		plan = toJSON(c.Plan)
	}
	level := c.Level
	if level == "" {
		level = CaseNormal
	}
	t := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO cases(repo_id, issue_key, issue, plan, source_run_id, source_version_id, level, status, created_at, updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(repo_id, issue_key) DO UPDATE SET issue=excluded.issue, plan=excluded.plan, source_run_id=excluded.source_run_id,
 source_version_id=excluded.source_version_id, status=excluded.status, retired_reason='', updated_at=excluded.updated_at`+
		map[bool]string{true: `, level=excluded.level`, false: ``}[c.Level != ""],
		c.RepoID, c.IssueKey, toJSON(c.Issue), plan, c.SourceRunID, c.SourceVersionID, level, CaseActive, t, t); err != nil {
		return Case{}, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM cases WHERE repo_id=? AND issue_key=?`, c.RepoID, c.IssueKey).Scan(&id); err != nil {
		return Case{}, err
	}
	if err := s.addHistory(ctx, tx, id, action, detail); err != nil {
		return Case{}, err
	}
	if err := tx.Commit(); err != nil {
		return Case{}, err
	}
	return s.Case(ctx, id)
}

// UpdateCase 修改级别、状态或计划，并记一条历史。plan 为 nil 表示不改计划。
func (s *Store) UpdateCase(ctx context.Context, id int64, level, status, reason string, plan *testplan.Plan, planSource, action, detail string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q, args := `UPDATE cases SET updated_at=?`, []any{now()}
	if level != "" {
		q += `, level=?`
		args = append(args, level)
	}
	if status != "" {
		q += `, status=?, retired_reason=?`
		args = append(args, status, reason)
	}
	if plan != nil {
		q += `, plan=?, source_run_id=?`
		args = append(args, toJSON(plan), planSource)
	}
	res, err := tx.ExecContext(ctx, q+` WHERE id=?`, append(args, id)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := s.addHistory(ctx, tx, id, action, detail); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteCase(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET case_id=0 WHERE case_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cases WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

type CaseEvent struct {
	TS     *time.Time `json:"ts"`
	Action string     `json:"action"`
	Detail string     `json:"detail"`
}

func (s *Store) CaseHistory(ctx context.Context, id int64) ([]CaseEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, action, detail FROM case_history WHERE case_id=? ORDER BY rowid DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaseEvent{}
	for rows.Next() {
		var e CaseEvent
		var ts string
		if err := rows.Scan(&ts, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = parseTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddCaseHistory 只记录，不修改用例（例如「确认是缺陷」）。
func (s *Store) AddCaseHistory(ctx context.Context, id int64, action, detail string) error {
	return s.addHistory(ctx, s.db, id, action, detail)
}

// SetRunVersion 把测试归入版本（或 0 解除）。
func (s *Store) SetRunVersion(ctx context.Context, runID string, versionID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET version_id=? WHERE id=?`, versionID, runID)
	return err
}

// ClaimRun 把排队中的测试改派到另一个运行时（空闲运行时领取回归子测试）。
func (s *Store) ClaimRun(ctx context.Context, runID string, from, to int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET runtime_id=? WHERE id=? AND runtime_id=? AND status=?`, to, runID, from, StatusQueued)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// QueuedRegressionRuns 返回某轮回归里仍在排队的子测试。
func (s *Store) QueuedRegressionRuns(ctx context.Context, regressionID string) ([]RunBrief, error) {
	return s.briefs(ctx, `regression_id=? AND status=?`, regressionID, StatusQueued)
}
