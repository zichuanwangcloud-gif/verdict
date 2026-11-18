package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/testplan"
)

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
	Tier             string              `json:"tier"`
	Config           testplan.Config     `json:"config"`
	TimeoutMinutes   int                 `json:"timeout_minutes"`
	Status           string              `json:"status"`
	CurrentStage     string              `json:"current_stage"`
	Verdict          string              `json:"verdict"`
	Summary          string              `json:"summary"`
	ACs              []protocol.ACResult `json:"acs"`
	Findings         []protocol.Finding  `json:"findings"`
	Pages            []testplan.UIPage   `json:"pages"`
	Approved         bool                `json:"approved"`
	ApprovalNote     string              `json:"approval_note"`
	AwaitingSince    *time.Time          `json:"awaiting_since"`
	SourceModified   bool                `json:"source_modified"`
	Diff             string              `json:"diff,omitempty"`
	Prompt           string              `json:"prompt,omitempty"` // 旧版单阶段测试的提示词
	Raw              string              `json:"raw,omitempty"`    // 旧版单阶段测试的原始输出
	CostUSD          float64             `json:"cost_usd"`
	Error            string              `json:"error"`
	LinearCommentURL string              `json:"linear_comment_url"`
	LinearError      string              `json:"linear_error"`
	VersionID        int64               `json:"version_id"`
	RegressionID     string              `json:"regression_id"`
	CaseID           int64               `json:"case_id"`
	PinRef           string              `json:"pin_ref"`
	PinSHA           string              `json:"pin_sha"`
	PlanSource       string              `json:"plan_source"`
	PresetPlan       string              `json:"-"`
	Attempt          int                 `json:"attempt"`
	CreatedAt        *time.Time          `json:"created_at"`
	StartedAt        *time.Time          `json:"started_at"`
	FinishedAt       *time.Time          `json:"finished_at"`
}

const runCols = `r.id, r.repo_id, p.url, p.name, r.branch, r.commit_sha, r.issue_key, r.issue, r.runtime_id, t.name,
 r.harness, r.tier, r.config, r.timeout_minutes, r.status, r.current_stage, r.verdict, r.summary, r.acs, r.findings, r.pages,
 r.approved, r.approval_note, r.awaiting_since, r.source_modified, r.diff, r.prompt, r.raw, r.cost_usd, r.error,
 r.linear_comment_url, r.linear_error, r.version_id, r.regression_id, r.case_id, r.pin_ref, r.pin_sha, r.plan_source,
 r.preset_plan, r.attempt, r.created_at, r.started_at, r.finished_at
 FROM runs r JOIN repos p ON p.id=r.repo_id JOIN runtimes t ON t.id=r.runtime_id`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var issue, cfg, acs, findings, pages, awaiting, created, started, finished string
	var modified, approved int
	err := sc.Scan(&r.ID, &r.RepoID, &r.RepoURL, &r.RepoName, &r.Branch, &r.CommitSHA, &r.IssueKey, &issue, &r.RuntimeID,
		&r.RuntimeName, &r.Harness, &r.Tier, &cfg, &r.TimeoutMinutes, &r.Status, &r.CurrentStage, &r.Verdict, &r.Summary,
		&acs, &findings, &pages, &approved, &r.ApprovalNote, &awaiting, &modified, &r.Diff, &r.Prompt, &r.Raw, &r.CostUSD,
		&r.Error, &r.LinearCommentURL, &r.LinearError, &r.VersionID, &r.RegressionID, &r.CaseID, &r.PinRef, &r.PinSHA,
		&r.PlanSource, &r.PresetPlan, &r.Attempt, &created, &started, &finished)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(issue), &r.Issue)
	_ = json.Unmarshal([]byte(cfg), &r.Config)
	_ = json.Unmarshal([]byte(acs), &r.ACs)
	_ = json.Unmarshal([]byte(findings), &r.Findings)
	_ = json.Unmarshal([]byte(pages), &r.Pages)
	if r.ACs == nil {
		r.ACs = []protocol.ACResult{}
	}
	if r.Findings == nil {
		r.Findings = []protocol.Finding{}
	}
	if r.Pages == nil {
		r.Pages = []testplan.UIPage{}
	}
	r.SourceModified, r.Approved = modified != 0, approved != 0
	r.AwaitingSince = parseTime(awaiting)
	r.CreatedAt, r.StartedAt, r.FinishedAt = parseTime(created), parseTime(started), parseTime(finished)
	return r, nil
}

// Legacy 表示旧版（单阶段）测试记录。
func (r Run) Legacy() bool { return r.Tier == "" }

type NewRun struct {
	RepoID    int64
	Branch    string
	Issue     linear.Issue
	RuntimeID int64
	Config    testplan.Config
	// 以下用于版本与回归测试，普通测试留空。
	VersionID    int64
	RegressionID string
	CaseID       int64
	PinRef       string
	PinSHA       string
	PlanSource   string
	PresetPlan   string
	Attempt      int
}

func (s *Store) CreateRun(ctx context.Context, n NewRun) (Run, error) {
	id, err := s.insertRun(ctx, s.db, n)
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) insertRun(ctx context.Context, db execer, n NewRun) (string, error) {
	id := RandomHex(6)
	_, err := db.ExecContext(ctx, `INSERT INTO runs(id, repo_id, branch, issue_key, issue, runtime_id, harness, tier, config, timeout_minutes, status, prompt,
 version_id, regression_id, case_id, pin_ref, pin_sha, plan_source, preset_plan, attempt, created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,'',?,?,?,?,?,?,?,?,?)`, id, n.RepoID, n.Branch, n.Issue.Identifier, toJSON(n.Issue), n.RuntimeID, n.Config.Harness,
		n.Config.Tier, toJSON(n.Config), n.Config.MaxMinutes, StatusQueued, n.VersionID, n.RegressionID, n.CaseID, n.PinRef, n.PinSHA,
		n.PlanSource, n.PresetPlan, max(n.Attempt, 1), now())
	return id, err
}

func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Runs 返回普通测试记录（不含回归子测试，它们在版本页里看）。
func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` WHERE r.regression_id='' ORDER BY r.created_at DESC, r.rowid DESC LIMIT ?`, limit)
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
		r.Prompt, r.Raw, r.Diff, r.Pages, r.PresetPlan = "", "", "", nil, ""
		for i := range r.ACs {
			r.ACs[i].Evidence, r.ACs[i].Reasoning, r.ACs[i].Challenges = "", "", nil
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsOnRuntime 返回某运行时上处于给定状态的测试，按创建顺序。
func (s *Store) RunsOnRuntime(ctx context.Context, runtimeID int64, statuses ...string) ([]Run, error) {
	ph, args := placeholders(statuses)
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` WHERE r.runtime_id=? AND r.status IN (`+ph+`) ORDER BY r.created_at, r.rowid`,
		append([]any{runtimeID}, args...)...)
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

// ActiveRuns 返回所有已下发、尚未结束的测试。
func (s *Store) ActiveRuns(ctx context.Context) ([]Run, error) {
	ph, args := placeholders(ActiveStatuses)
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` WHERE r.status IN (`+ph+`) ORDER BY r.created_at`, args...)
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
	case to == StatusAwaiting:
		q += `, awaiting_since=?`
		args = append(args, now())
	case Terminal(to):
		q += `, finished_at=?, current_stage=''`
		args = append(args, now())
	}
	q += ` WHERE id=?`
	args = append(args, id)
	if len(from) > 0 {
		ph, fa := placeholders(from)
		q += ` AND status IN (` + ph + `)`
		args = append(args, fa...)
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

func (s *Store) SetCurrentStage(ctx context.Context, id, stage string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET current_stage=? WHERE id=?`, stage, id)
	return err
}

// SetResult 写入当前汇总结论（执行过程中随阶段更新）。
func (s *Store) SetResult(ctx context.Context, id string, res testplan.Result, cost float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET summary=?, acs=?, findings=?, pages=?, cost_usd=? WHERE id=?`,
		res.Summary, toJSON(res.ACs), toJSON(res.Findings), toJSON(res.Pages), cost, id)
	return err
}

// SetVerdict 在进入收尾时写入最终结论。
func (s *Store) SetVerdict(ctx context.Context, id, verdict string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET verdict=? WHERE id=?`, verdict, id)
	return err
}

// Approve 记录计划确认。只对等待确认的测试生效。
func (s *Store) Approve(ctx context.Context, id, note string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET approved=1, approval_note=?, status=?, awaiting_since='' WHERE id=? AND status=?`,
		note, StatusRunning, id, StatusAwaiting)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CloseRun 写入收尾结果与终态；已处于终态的测试不会被覆盖。
func (s *Store) CloseRun(ctx context.Context, id, status, errMsg string, c protocol.ClosedResult) (bool, error) {
	modified := 0
	if c.SourceModified {
		modified = 1
	}
	q := `UPDATE runs SET status=?, source_modified=?, diff=?, finished_at=?, current_stage=''`
	args := []any{status, modified, c.Diff, now()}
	if errMsg != "" {
		q += `, error=?`
		args = append(args, errMsg)
	}
	if status != StatusCompleted {
		q += `, verdict=''`
	}
	q += ` WHERE id=? AND status NOT IN (?,?,?)`
	args = append(args, id, StatusCompleted, StatusError, StatusCanceled)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) SetLinearResult(ctx context.Context, id, url, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET linear_comment_url=?, linear_error=? WHERE id=?`, url, errMsg, id)
	return err
}
