package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

const (
	maxArtifactSize = 10 << 20
	maxArtifacts    = 200
)

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.st.Runs(r.Context(), 200)
	if err != nil {
		failErr(w, err)
		return
	}
	// 附带人工真值，列表上才能标出哪些测试还没复核。
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	truths, err := s.st.TruthsFor(r.Context(), ids)
	if err != nil {
		failErr(w, err)
		return
	}
	type runRow struct {
		store.Run
		Truth *store.Truth `json:"truth"`
	}
	out := make([]runRow, 0, len(runs))
	for _, run := range runs {
		row := runRow{Run: run}
		if t, found := truths[run.ID]; found {
			row.Truth = &t
		}
		out = append(out, row)
	}
	ok(w, out)
}

type manualIssue struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type runInput struct {
	IssueKey    string           `json:"issue_key"`
	ManualIssue *manualIssue     `json:"manual_issue"` // 不使用 Linear 时手动提供需求（不会回写）
	RepoID      int64            `json:"repo_id"`
	Branch      string           `json:"branch"`
	RuntimeID   int64            `json:"runtime_id"`
	Harness     string           `json:"harness"`
	Tier        string           `json:"tier"`         // S / A / B / C / custom，默认 B
	Config      *testplan.Config `json:"config"`       // tier=custom 时的开关
	ApprovePlan *bool            `json:"approve_plan"` // 覆盖预设
	MaxMinutes  int              `json:"max_minutes"`  // 覆盖预设
	MaxCostUSD  *float64         `json:"max_cost_usd"` // 覆盖预设
	VersionID   int64            `json:"version_id"`   // 归入版本（可选）
	VersionMove bool             `json:"version_move"` // issue 已在别的版本里时是否移动过来
}

func validBranch(b string) bool {
	return b != "" && !strings.HasPrefix(b, "-") && !strings.ContainsAny(b, " \t\n~^:?*[\\") && !strings.Contains(b, "..")
}

func otherHarness(h string) string {
	if h == "claude" {
		return "codex"
	}
	return "claude"
}

// resolveConfig 把档位与覆盖项合成流程配置，并按运行时的 harness 情况决定红队用哪个模型。
func resolveConfig(in runInput, info *protocol.HostInfo) testplan.Config {
	tier := strings.TrimSpace(in.Tier)
	if tier == "" {
		tier = testplan.TierB
	}
	var cfg testplan.Config
	if strings.EqualFold(tier, testplan.TierCustom) && in.Config != nil {
		cfg = *in.Config
		cfg.Tier = testplan.TierCustom
	} else {
		cfg = testplan.Preset(tier)
	}
	if in.ApprovePlan != nil {
		cfg.ApprovePlan = *in.ApprovePlan
	}
	if in.MaxMinutes > 0 {
		cfg.MaxMinutes = in.MaxMinutes
	}
	if in.MaxCostUSD != nil {
		cfg.MaxCostUSD = *in.MaxCostUSD
	}
	cfg = cfg.Normalize()
	cfg.Harness, cfg.RedHarness = in.Harness, in.Harness
	if cfg.CrossModel {
		if h, found := info.Harness(otherHarness(in.Harness)); found && h.Ready() {
			cfg.RedHarness = h.Name
		}
	}
	return cfg
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var in runInput
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	run, code, err := s.startRun(r.Context(), in)
	if err != nil {
		fail(w, code, err.Error())
		return
	}
	ok(w, run)
}

func (s *Server) startRun(ctx context.Context, in runInput) (store.Run, int, error) {
	in.Branch = strings.TrimSpace(in.Branch)
	if !validBranch(in.Branch) {
		return store.Run{}, http.StatusBadRequest, errors.New("分支名不合法")
	}
	if !slices.Contains(harnesses, in.Harness) {
		return store.Run{}, http.StatusBadRequest, errors.New("harness 只支持 claude / codex")
	}
	repo, err := s.st.Repo(ctx, in.RepoID)
	if err != nil {
		return store.Run{}, http.StatusBadRequest, errors.New("仓库不存在")
	}
	rt, err := s.st.Runtime(ctx, in.RuntimeID)
	if err != nil || strings.HasSuffix(rt.Name, "（已删除）") {
		return store.Run{}, http.StatusBadRequest, errors.New("运行时不存在")
	}
	if rt.Info != nil {
		if rt.Info.Protocol != protocol.ProtocolVersion {
			return store.Run{}, http.StatusBadRequest, fmt.Errorf("运行时 %s 上的 agent 版本过旧，请在那台机器上重新执行安装命令升级", rt.Name)
		}
		if h, found := rt.Info.Harness(in.Harness); found && !h.Installed {
			return store.Run{}, http.StatusBadRequest, fmt.Errorf("运行时 %s 上没有安装 %s", rt.Name, in.Harness)
		}
	}

	var issue linear.Issue
	switch {
	case strings.TrimSpace(in.IssueKey) != "":
		issue, err = linearClient(s.linearKey(ctx)).GetIssue(ctx, strings.TrimSpace(in.IssueKey))
		if err != nil {
			return store.Run{}, http.StatusBadGateway, err
		}
	case in.ManualIssue != nil && strings.TrimSpace(in.ManualIssue.Description) != "":
		issue = linear.Issue{Identifier: "MANUAL", Title: strings.TrimSpace(in.ManualIssue.Title), Description: in.ManualIssue.Description}
		if issue.Title == "" {
			issue.Title = "（手动输入的需求）"
		}
	default:
		return store.Run{}, http.StatusBadRequest, errors.New("请填写 Linear issue 编号，或手动输入需求")
	}

	newRun := store.NewRun{RepoID: repo.ID, Branch: in.Branch, Issue: issue, RuntimeID: rt.ID, Config: resolveConfig(in, rt.Info)}
	if in.VersionID > 0 {
		v, err := s.st.Version(ctx, in.VersionID)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && v.RepoID != repo.ID):
			return store.Run{}, http.StatusBadRequest, errors.New("版本不存在或不属于这个仓库")
		case err == nil && v.Status == store.VersionReleased:
			return store.Run{}, http.StatusBadRequest, errors.New("版本已发布，不能再往里加测试")
		case err != nil:
			return store.Run{}, http.StatusInternalServerError, err
		case issue.Identifier == "MANUAL":
			return store.Run{}, http.StatusBadRequest, errors.New("手动输入的需求不能归入版本（版本要按 issue 编号跨版本积累回归）")
		}
		if err := s.st.AddVersionIssue(ctx, v, issue, in.VersionMove); err != nil {
			if errors.Is(err, store.ErrIssueInOtherVersion) {
				return store.Run{}, http.StatusConflict, err
			}
			return store.Run{}, http.StatusInternalServerError, err
		}
		newRun.VersionID = v.ID
	}
	run, err := s.st.CreateRun(ctx, newRun)
	if err != nil {
		return store.Run{}, http.StatusInternalServerError, err
	}
	go s.hub.Schedule(rt.ID)
	return run, 0, nil
}

type runDetail struct {
	store.Run
	Stages    []store.Stage    `json:"stages"`
	Artifacts []store.Artifact `json:"artifacts"`
	Truth     *store.Truth     `json:"truth"`
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	stages, err := s.st.Stages(r.Context(), run.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	arts, err := s.st.Artifacts(r.Context(), run.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	d := runDetail{Run: run, Stages: stages, Artifacts: arts}
	if t, err := s.st.Truth(r.Context(), run.ID); err == nil {
		d.Truth = &t
	}
	ok(w, d)
}

func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	logs, err := s.st.Logs(r.Context(), r.PathValue("id"), after, 2000)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, logs)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	if run.RegressionID != "" {
		fail(w, http.StatusConflict, "回归子测试请在回归轮次里取消整轮")
		return
	}
	if err := s.hub.Cancel(r.Context(), run); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) approveRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := s.hub.Approve(r.Context(), r.PathValue("id"), strings.TrimSpace(in.Note)); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) rerun(w http.ResponseWriter, r *http.Request) {
	old, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	in := runInput{RepoID: old.RepoID, Branch: old.Branch, RuntimeID: old.RuntimeID, Harness: old.Harness}
	if old.Legacy() {
		in.Tier = testplan.TierB
	} else {
		cfg := old.Config
		in.Tier, in.Config = cfg.Tier, &cfg
		approve, cost := cfg.ApprovePlan, cfg.MaxCostUSD
		in.ApprovePlan, in.MaxMinutes, in.MaxCostUSD = &approve, cfg.MaxMinutes, &cost
	}
	if old.RegressionID != "" {
		fail(w, http.StatusConflict, "回归子测试不能这样重跑，请在回归轮次里重跑未通过的项")
		return
	}
	if old.VersionID > 0 {
		in.VersionID, in.VersionMove = old.VersionID, true
	}
	if old.Issue.ID != "" {
		in.IssueKey = old.IssueKey // 重新拉取最新的 issue 内容
	} else {
		in.ManualIssue = &manualIssue{old.Issue.Title, old.Issue.Description}
	}
	run, code, err := s.startRun(r.Context(), in)
	if err != nil {
		fail(w, code, err.Error())
		return
	}
	ok(w, run)
}

func (s *Server) retryWriteBack(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	if !store.Terminal(run.Status) {
		fail(w, http.StatusConflict, "测试尚未结束")
		return
	}
	s.writeBackForce(run.ID, true)
	run, _ = s.st.Run(r.Context(), run.ID)
	ok(w, run)
}

// ---- 产物 ----

var artifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`)

// 只允许这些类型；其余一律按附件下载，避免在本站域名下渲染上传的 HTML/SVG。
var artifactTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
	".webm": "video/webm", ".mp4": "video/mp4", ".txt": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8",
	".md": "text/plain; charset=utf-8", ".json": "application/json",
}

func artifactType(name string) string {
	if t, found := artifactTypes[strings.ToLower(filepath.Ext(name))]; found {
		return t
	}
	return "application/octet-stream"
}

func (s *Server) artifactPath(runID, name string) string {
	return filepath.Join(s.cfg.DataDir, "artifacts", runID, name)
}

func (s *Server) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rt, err := s.st.RuntimeByToken(ctx, bearer(r))
	if err != nil {
		fail(w, http.StatusUnauthorized, "运行时令牌无效")
		return
	}
	run, err := s.st.Run(ctx, r.PathValue("id"))
	if err != nil || run.RuntimeID != rt.ID {
		fail(w, http.StatusNotFound, "测试不存在")
		return
	}
	name := r.URL.Query().Get("name")
	if !artifactName.MatchString(name) {
		fail(w, http.StatusBadRequest, "产物文件名不合法")
		return
	}
	if _, err := s.st.Artifact(ctx, run.ID, name); errors.Is(err, store.ErrNotFound) {
		if n, _ := s.st.CountArtifacts(ctx, run.ID); n >= maxArtifacts {
			fail(w, http.StatusRequestEntityTooLarge, "产物数量超过上限")
			return
		}
	}
	p := s.artifactPath(run.ID, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		failErr(w, err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		failErr(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r.Body, maxArtifactSize+1))
	tmp.Close()
	if err != nil {
		failErr(w, err)
		return
	}
	if n > maxArtifactSize {
		fail(w, http.StatusRequestEntityTooLarge, "产物超过 10MB")
		return
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		failErr(w, err)
		return
	}
	if err := s.st.AddArtifact(ctx, store.Artifact{RunID: run.ID, Name: name, StageID: r.URL.Query().Get("stage"), Size: n, ContentType: artifactType(name)}); err != nil {
		failErr(w, err)
		return
	}
	ok(w, map[string]any{"name": name, "size": n})
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	runID, name := r.PathValue("id"), r.PathValue("name")
	a, err := s.st.Artifact(r.Context(), runID, name)
	if err != nil {
		failErr(w, err)
		return
	}
	f, err := os.Open(s.artifactPath(runID, a.Name))
	if err != nil {
		fail(w, http.StatusNotFound, "产物文件不存在")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", a.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if !strings.HasPrefix(a.ContentType, "image/") && !strings.HasPrefix(a.ContentType, "video/") {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.Name))
	}
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("发送产物 %s/%s 失败: %v", runID, name, err)
	}
}
