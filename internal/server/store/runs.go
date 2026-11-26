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
	RepoPorts        int                 `json:"repo_ports"` // 这个仓库需要的端口数（并发时由 agent 分配）
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
	Shots            []testplan.Shot     `json:"shots"`
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
	Coverage         testplan.Coverage   `json:"coverage"`
	Ports            []int               `json:"ports"`
	ParentRunID      string              `json:"parent_run_id"`
	Attempt          int                 `json:"attempt"`
	// 主子 issue：SetID 把一次父单测试和它带的子单测试绑成一批；
	// IssueParentKey 是这条 issue 在 Linear 里的父单；Scope 仅父 run 有，记录本次选中的子单。
	SetID          string   `json:"set_id,omitempty"`
	IssueParentKey string   `json:"issue_parent_key,omitempty"`
	Scope          []string `json:"scope,omitempty"`
	// Kind 是 run 的用途：test（验收测试）/ postmortem（差错复盘分析）。
	Kind string `json:"kind"`
	// WaitingSlot 表示测试已经跑完当前能跑的部分，正在等运行时的服务槽位。
	WaitingSlot bool       `json:"waiting_slot"`
	CreatedAt   *time.Time `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

const runCols = `r.id, r.repo_id, p.url, p.name, p.ports, r.branch, r.commit_sha, r.issue_key, r.issue, r.runtime_id, t.name,
 r.harness, r.tier, r.config, r.timeout_minutes, r.status, r.current_stage, r.verdict, r.summary, r.acs, r.findings, r.pages,
 r.approved, r.approval_note, r.awaiting_since, r.source_modified, r.diff, r.prompt, r.raw, r.cost_usd, r.error,
 r.linear_comment_url, r.linear_error, r.version_id, r.regression_id, r.case_id, r.pin_ref, r.pin_sha, r.plan_source,
 r.preset_plan, r.ports, r.parent_run_id, r.coverage, r.attempt, r.created_at, r.started_at, r.finished_at, r.shots, r.kind,
 r.set_id, r.issue_parent_key, r.scope, r.waiting_slot
 FROM runs r JOIN repos p ON p.id=r.repo_id JOIN runtimes t ON t.id=r.runtime_id`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var issue, cfg, acs, findings, pages, shots, awaiting, ports, cov, created, started, finished, scope string
	var modified, approved, waiting int
	err := sc.Scan(&r.ID, &r.RepoID, &r.RepoURL, &r.RepoName, &r.RepoPorts, &r.Branch, &r.CommitSHA, &r.IssueKey, &issue, &r.RuntimeID,
		&r.RuntimeName, &r.Harness, &r.Tier, &cfg, &r.TimeoutMinutes, &r.Status, &r.CurrentStage, &r.Verdict, &r.Summary,
		&acs, &findings, &pages, &approved, &r.ApprovalNote, &awaiting, &modified, &r.Diff, &r.Prompt, &r.Raw, &r.CostUSD,
		&r.Error, &r.LinearCommentURL, &r.LinearError, &r.VersionID, &r.RegressionID, &r.CaseID, &r.PinRef, &r.PinSHA,
		&r.PlanSource, &r.PresetPlan, &ports, &r.ParentRunID, &cov, &r.Attempt, &created, &started, &finished, &shots, &r.Kind,
		&r.SetID, &r.IssueParentKey, &scope, &waiting)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(issue), &r.Issue)
	_ = json.Unmarshal([]byte(cfg), &r.Config)
	_ = json.Unmarshal([]byte(acs), &r.ACs)
	_ = json.Unmarshal([]byte(findings), &r.Findings)
	_ = json.Unmarshal([]byte(pages), &r.Pages)
	_ = json.Unmarshal([]byte(shots), &r.Shots)
	if r.Shots == nil {
		r.Shots = []testplan.Shot{}
	}
	if r.ACs == nil {
		r.ACs = []protocol.ACResult{}
	}
	if r.Findings == nil {
		r.Findings = []protocol.Finding{}
	}
	if r.Pages == nil {
		r.Pages = []testplan.UIPage{}
	}
	_ = json.Unmarshal([]byte(cov), &r.Coverage)
	_ = json.Unmarshal([]byte(ports), &r.Ports)
	if r.Ports == nil {
		r.Ports = []int{}
	}
	_ = json.Unmarshal([]byte(scope), &r.Scope)
	if r.Scope == nil {
		r.Scope = []string{}
	}
	r.SourceModified, r.Approved = modified != 0, approved != 0
	r.WaitingSlot = waiting != 0
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
	// Kind 是 run 的用途，空值按普通测试处理。
	Kind string
	// 以下用于版本与回归测试，普通测试留空。
	VersionID    int64
	RegressionID string
	CaseID       int64
	PinRef       string
	PinSHA       string
	PlanSource   string
	PresetPlan   string
	Attempt      int
	// ParentRunID 指向续跑来源的测试。
	ParentRunID string
	// 主子 issue：SetID 绑定一次父子测试批次；IssueParentKey 是 Linear 父单；Scope 仅父 run 使用。
	SetID          string
	IssueParentKey string
	Scope          []string
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
	kind := n.Kind
	if kind == "" {
		kind = KindTest
	}
	scope := toJSON(n.Scope)
	if n.Scope == nil {
		scope = "[]"
	}
	_, err := db.ExecContext(ctx, `INSERT INTO runs(id, repo_id, branch, issue_key, issue, runtime_id, harness, tier, config, timeout_minutes, status, prompt,
 version_id, regression_id, case_id, pin_ref, pin_sha, plan_source, preset_plan, parent_run_id, attempt, created_at, kind,
 set_id, issue_parent_key, scope)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,'',?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, n.RepoID, n.Branch, n.Issue.Identifier, toJSON(n.Issue), n.RuntimeID, n.Config.Harness,
		n.Config.Tier, toJSON(n.Config), n.Config.MaxMinutes, StatusQueued, n.VersionID, n.RegressionID, n.CaseID, n.PinRef, n.PinSHA,
		n.PlanSource, n.PresetPlan, n.ParentRunID, max(n.Attempt, 1), now(), kind, n.SetID, n.IssueParentKey, scope)
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` WHERE r.regression_id='' AND `+KindTestOnly+` ORDER BY r.created_at DESC, r.rowid DESC LIMIT ?`, limit)
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

func (s *Store) SetPorts(ctx context.Context, id string, ports []int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET ports=? WHERE id=?`, toJSON(ports), id)
	return err
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
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET summary=?, acs=?, findings=?, pages=?, shots=?, coverage=?, cost_usd=? WHERE id=?`,
		res.Summary, toJSON(res.ACs), toJSON(res.Findings), toJSON(res.Pages), toJSON(res.Shots), toJSON(res.Coverage), cost, id)
	return err
}

// CopyStages 把已完成/跳过的阶段复制到新测试（续跑用），让流水线从失败处继续。
func (s *Store) CopyStages(ctx context.Context, fromRun, toRun string) (int, error) {
	src, err := s.Stages(ctx, fromRun)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, st := range src {
		if st.Status != testplan.StatusOK && st.Status != testplan.StatusSkipped {
			continue
		}
		// 服务阶段不复制：新测试的 agent 会话是全新的，服务需要重新拉起。
		// 复制过来会让流水线误以为服务已在运行，后续阶段连不上服务。
		if st.Kind == protocol.StageKindService {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO stages(id, run_id, seq, role, round, kind, harness, status, note, error, spec, output,
 cost_usd, duration_ms, service, artifacts, attempt, created_at, started_at, finished_at)
 VALUES(?, ?, (SELECT COALESCE(MAX(seq),0)+1 FROM stages WHERE run_id=?), ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			toRun+"-"+RandomHex(3), toRun, toRun, st.Role, st.Round, st.Kind, st.Harness, st.Status, st.Note, toJSON(st.Spec),
			string(st.Output), st.CostUSD, st.DurationMS, toJSON(st.Service), toJSON(st.Artifacts), st.Attempt,
			now(), timeOrEmpty(st.StartedAt), timeOrEmpty(st.FinishedAt)); err != nil {
			return n, err
		}
		n++
	}
	return n, tx.Commit()
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

// IssueRunBrief 是某个 issue 最近一次测试的摘要，供 Linear 列表页显示状态。
type IssueRunBrief struct {
	IssueKey   string              `json:"issue_key"`
	RunID      string              `json:"run_id"`
	Status     string              `json:"status"`
	Verdict    string              `json:"verdict"`
	Summary    string              `json:"summary"`
	Error      string              `json:"error"`
	Branch     string              `json:"branch"`
	CommitSHA  string              `json:"commit_sha"`
	ACs        []protocol.ACResult `json:"acs"`
	CreatedAt  *time.Time          `json:"created_at"`
	FinishedAt *time.Time          `json:"finished_at"`
	// Truth 是这次测试的人工结论（没标过时为空）。
	Truth *Truth `json:"truth,omitempty"`
}

// LastRunsByIssue 返回这些 issue 在指定仓库上最近的一次测试（不含回归子测试）。
// repoID 为 0 时跨仓库取最近一次。没有测试记录的 issue 不出现在结果里。
func (s *Store) LastRunsByIssue(ctx context.Context, repoID int64, issueKeys []string) (map[string]IssueRunBrief, error) {
	out := map[string]IssueRunBrief{}
	if len(issueKeys) == 0 {
		return out, nil
	}
	ph, args := placeholders(issueKeys)
	q := `SELECT r.issue_key, r.id, r.status, r.verdict, r.summary, r.error, r.branch, r.commit_sha, r.acs, r.created_at, r.finished_at
 FROM runs r WHERE r.issue_key IN (` + ph + `) AND r.regression_id=''`
	if repoID > 0 {
		q += ` AND r.repo_id=?`
		args = append(args, repoID)
	}
	// 每个 issue 取最新一条：按创建时间、再按 rowid 兜底（同一秒创建的按写入顺序）。
	q += ` ORDER BY r.issue_key, r.created_at DESC, r.rowid DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b IssueRunBrief
		var acs, created, finished string
		if err := rows.Scan(&b.IssueKey, &b.RunID, &b.Status, &b.Verdict, &b.Summary, &b.Error, &b.Branch, &b.CommitSHA,
			&acs, &created, &finished); err != nil {
			return nil, err
		}
		if _, seen := out[b.IssueKey]; seen {
			continue // 已经拿到更新的一条
		}
		_ = json.Unmarshal([]byte(acs), &b.ACs)
		// 列表只需要条数与结论，不带证据正文。
		for i := range b.ACs {
			b.ACs[i].Evidence, b.ACs[i].Reasoning, b.ACs[i].Challenges = "", "", nil
		}
		b.CreatedAt, b.FinishedAt = parseTime(created), parseTime(finished)
		out[b.IssueKey] = b
	}
	return out, rows.Err()
}

// ActiveRunsByIssue 返回某仓库上排队或执行中的 issue → run id。
// 批量启动前用它跳过已经在跑或排队的 issue，重复点击不会重复排队。
func (s *Store) ActiveRunsByIssue(ctx context.Context, repoID int64) (map[string]string, error) {
	ph, args := placeholders(ActiveStatuses)
	q := `SELECT r.issue_key, r.id FROM runs r WHERE r.status IN (` + ph + `)`
	if repoID > 0 {
		q += ` AND r.repo_id=?`
		args = append(args, repoID)
	}
	q += ` ORDER BY r.created_at, r.rowid`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		if _, seen := out[key]; !seen {
			out[key] = id
		}
	}
	return out, rows.Err()
}

// run 的用途。分析型 run 复用测试基础设施，但不出现在测试列表与统计里。
const (
	KindTest       = "test"
	KindPostmortem = "postmortem"
)

// KindTestOnly 是列表查询用的条件：只取验收测试，排除分析型 run。
const KindTestOnly = "(r.kind='' OR r.kind='" + KindTest + "')"

// ---- 人工真值 ----

// Truth 是人工对某一次测试的判定。一条测试至多一条。
type Truth struct {
	RunID     string     `json:"run_id"`
	Verdict   string     `json:"verdict"` // pass / fail / invalid
	Source    string     `json:"source"`  // manual（人工复测）/ ticket（测试人员开单）
	Note      string     `json:"note"`
	Ticket    string     `json:"ticket"`     // 单子原文
	TicketRef string     `json:"ticket_ref"` // 单号或链接
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

const (
	TruthPass    = "pass"
	TruthFail    = "fail"
	TruthInvalid = "invalid"
)

// SetTruth 写入或覆盖一条真值。同一测试重复标记时就地更新。
func (s *Store) SetTruth(ctx context.Context, t Truth) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_truths(run_id, verdict, source, note, ticket, ticket_ref, created_at, updated_at)
 VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(run_id) DO UPDATE SET verdict=excluded.verdict, source=excluded.source, note=excluded.note,
   ticket=excluded.ticket, ticket_ref=excluded.ticket_ref, updated_at=excluded.updated_at`,
		t.RunID, t.Verdict, t.Source, t.Note, t.Ticket, t.TicketRef, ts, ts)
	return err
}

// Truth 读一条真值；没标过返回 ErrNotFound。
func (s *Store) Truth(ctx context.Context, runID string) (Truth, error) {
	return scanTruth(s.db.QueryRowContext(ctx,
		`SELECT run_id, verdict, source, note, ticket, ticket_ref, created_at, updated_at FROM run_truths WHERE run_id=?`, runID))
}

// DeleteTruth 撤销标记。
func (s *Store) DeleteTruth(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_truths WHERE run_id=?`, runID)
	return err
}

// TruthsFor 批量读真值，列表页给多行附角标用。
func (s *Store) TruthsFor(ctx context.Context, runIDs []string) (map[string]Truth, error) {
	out := map[string]Truth{}
	if len(runIDs) == 0 {
		return out, nil
	}
	ph, args := placeholders(runIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, verdict, source, note, ticket, ticket_ref, created_at, updated_at
 FROM run_truths WHERE run_id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTruth(rows)
		if err != nil {
			return nil, err
		}
		out[t.RunID] = t
	}
	return out, rows.Err()
}

func scanTruth(sc interface{ Scan(...any) error }) (Truth, error) {
	var t Truth
	var created, updated string
	if err := sc.Scan(&t.RunID, &t.Verdict, &t.Source, &t.Note, &t.Ticket, &t.TicketRef, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, ErrNotFound
		}
		return t, err
	}
	t.CreatedAt, t.UpdatedAt = parseTime(created), parseTime(updated)
	return t, nil
}
