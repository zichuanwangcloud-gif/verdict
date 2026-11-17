// Package server 是 Verdict 后端：REST API、agent WebSocket、调度与 Linear 回写。
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

const (
	settingLinearKey = "linear_api_key"
	settingPublicURL = "public_url"
)

var harnesses = []string{"claude", "codex"}

type Config struct {
	AdminToken string
	WebDir     string // 前端构建产物
	BinDir     string // 供运行时下载的 agent 二进制：verdict-linux-amd64 等
}

type Server struct {
	cfg Config
	st  *store.Store
	hub *Hub
}

func New(cfg Config, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, hub: NewHub(st)}
	s.hub.OnFinish = s.writeBack
	return s
}

func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, map[string]any{"ok": true, "version": protocol.Version})
	})
	mux.HandleFunc("GET /api/agent/ws", s.hub.ServeWS)
	mux.HandleFunc("GET /install.sh", s.installScript)
	mux.HandleFunc("GET /download/{file}", s.download)

	api.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) { ok(w, map[string]bool{"ok": true}) })
	api.HandleFunc("GET /api/settings", s.getSettings)
	api.HandleFunc("PUT /api/settings", s.putSettings)

	api.HandleFunc("GET /api/runtimes", s.listRuntimes)
	api.HandleFunc("POST /api/runtimes", s.createRuntime)
	api.HandleFunc("PATCH /api/runtimes/{id}", s.renameRuntime)
	api.HandleFunc("DELETE /api/runtimes/{id}", s.deleteRuntime)
	api.HandleFunc("POST /api/runtimes/{id}/probe", s.probeRuntime)

	api.HandleFunc("GET /api/repos", s.listRepos)
	api.HandleFunc("POST /api/repos", s.createRepo)
	api.HandleFunc("PUT /api/repos/{id}", s.updateRepo)
	api.HandleFunc("DELETE /api/repos/{id}", s.deleteRepo)

	api.HandleFunc("GET /api/issues/{key}", s.previewIssue)

	api.HandleFunc("GET /api/runs", s.listRuns)
	api.HandleFunc("POST /api/runs", s.createRun)
	api.HandleFunc("GET /api/runs/{id}", s.getRun)
	api.HandleFunc("GET /api/runs/{id}/logs", s.runLogs)
	api.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	api.HandleFunc("POST /api/runs/{id}/rerun", s.rerun)
	api.HandleFunc("POST /api/runs/{id}/linear", s.retryWriteBack)

	mux.Handle("/api/", s.requireAdmin(api))
	if s.cfg.WebDir != "" {
		if _, err := os.Stat(s.cfg.WebDir); err == nil {
			mux.Handle("/", spa(s.cfg.WebDir))
		}
	}
	return mux
}

// spa 托管前端；找不到的路径回落到 index.html。
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := bearer(r)
		if t == "" || subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.AdminToken)) != 1 {
			fail(w, http.StatusUnauthorized, "需要登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func failErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "不存在")
		return
	}
	fail(w, http.StatusInternalServerError, err.Error())
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil {
		return fmt.Errorf("请求体不是合法 JSON: %w", err)
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) linearKey(ctx context.Context) string {
	if v, _ := s.st.Setting(ctx, settingLinearKey); v != "" {
		return v
	}
	return os.Getenv("VERDICT_LINEAR_API_KEY")
}

// publicURL 优先用配置；否则从请求推断（反向代理需传 X-Forwarded-Proto/Host）。
func (s *Server) publicURL(ctx context.Context, r *http.Request) string {
	if v, _ := s.st.Setting(ctx, settingPublicURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := os.Getenv("VERDICT_PUBLIC_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// ---- settings ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	key := s.linearKey(r.Context())
	pub, _ := s.st.Setting(r.Context(), settingPublicURL)
	masked := ""
	if len(key) > 8 {
		masked = key[:4] + "…" + key[len(key)-4:]
	} else if key != "" {
		masked = "已配置"
	}
	ok(w, map[string]any{
		"linear_api_key_masked": masked,
		"public_url":            pub,
		"public_url_effective":  s.publicURL(r.Context(), r),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LinearAPIKey *string `json:"linear_api_key"`
		PublicURL    *string `json:"public_url"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.LinearAPIKey != nil {
		if err := s.st.SetSetting(r.Context(), settingLinearKey, strings.TrimSpace(*in.LinearAPIKey)); err != nil {
			failErr(w, err)
			return
		}
	}
	if in.PublicURL != nil {
		if err := s.st.SetSetting(r.Context(), settingPublicURL, strings.TrimRight(strings.TrimSpace(*in.PublicURL), "/")); err != nil {
			failErr(w, err)
			return
		}
	}
	s.getSettings(w, r)
}

// ---- runtimes ----

func (s *Server) listRuntimes(w http.ResponseWriter, r *http.Request) {
	rts, err := s.st.Runtimes(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	out := rts[:0]
	for _, rt := range rts {
		if strings.HasSuffix(rt.Name, "（已删除）") {
			continue
		}
		rt.Online = s.hub.Online(rt.ID)
		out = append(out, rt)
	}
	ok(w, out)
}

func (s *Server) createRuntime(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		fail(w, http.StatusBadRequest, "名称不能为空")
		return
	}
	rt, token, err := s.st.CreateRuntime(r.Context(), in.Name)
	if err != nil {
		failErr(w, err)
		return
	}
	base := s.publicURL(r.Context(), r)
	ok(w, map[string]any{
		"runtime":         rt,
		"token":           token,
		"install_command": fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- --server %s --token %s", base, base, token),
	})
}

func (s *Server) renameRuntime(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		fail(w, http.StatusBadRequest, "名称不能为空")
		return
	}
	if err := s.st.RenameRuntime(r.Context(), id, strings.TrimSpace(in.Name)); err != nil {
		failErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) deleteRuntime(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	if err := s.st.DeleteRuntime(r.Context(), id); err != nil {
		failErr(w, err)
		return
	}
	s.hub.Disconnect(id)
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) probeRuntime(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	if err := s.hub.RequestProbe(r.Context(), id); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---- repos ----

type repoInput struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	TestNotes string `json:"test_notes"`
}

func (in *repoInput) normalize() error {
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return errors.New("仓库地址不能为空")
	}
	if strings.HasPrefix(in.URL, "-") || strings.ContainsAny(in.URL, " \t\n") {
		return errors.New("仓库地址不合法")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = repoName(in.URL)
	}
	return nil
}

func repoName(url string) string {
	n := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(n, "/:"); i >= 0 {
		rest := n[:i]
		if j := strings.LastIndexAny(rest, "/:"); j >= 0 {
			return rest[j+1:] + "/" + n[i+1:]
		}
		return n[i+1:]
	}
	return n
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	rs, err := s.st.Repos(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, rs)
}

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) {
	var in repoInput
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.normalize(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	repo, err := s.st.CreateRepo(r.Context(), in.URL, in.Name, in.TestNotes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(w, http.StatusConflict, "该仓库已存在")
			return
		}
		failErr(w, err)
		return
	}
	ok(w, repo)
}

func (s *Server) updateRepo(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in repoInput
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.normalize(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.UpdateRepo(r.Context(), id, in.URL, in.Name, in.TestNotes); err != nil {
		failErr(w, err)
		return
	}
	repo, err := s.st.Repo(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, repo)
}

func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	if err := s.st.DeleteRepo(r.Context(), id); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---- issues ----

func (s *Server) previewIssue(w http.ResponseWriter, r *http.Request) {
	is, err := linear.New(s.linearKey(r.Context())).GetIssue(r.Context(), strings.TrimSpace(r.PathValue("key")))
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, is)
}

// ---- runs ----

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.st.Runs(r.Context(), 200)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, runs)
}

type runInput struct {
	IssueKey string `json:"issue_key"`
	// ManualIssue 在不使用 Linear 时手动提供需求（不会回写）。
	ManualIssue *struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"manual_issue"`
	RepoID         int64  `json:"repo_id"`
	Branch         string `json:"branch"`
	RuntimeID      int64  `json:"runtime_id"`
	Harness        string `json:"harness"`
	TimeoutMinutes int    `json:"timeout_minutes"`
}

func validBranch(b string) bool {
	return b != "" && !strings.HasPrefix(b, "-") && !strings.ContainsAny(b, " \t\n~^:?*[\\") && !strings.Contains(b, "..")
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
	if in.TimeoutMinutes <= 0 {
		in.TimeoutMinutes = 60
	}
	if in.TimeoutMinutes > 24*60 {
		return store.Run{}, http.StatusBadRequest, errors.New("超时不能超过 24 小时")
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
		for _, h := range rt.Info.Harnesses {
			if h.Name == in.Harness && !h.Installed {
				return store.Run{}, http.StatusBadRequest, fmt.Errorf("运行时 %s 上没有安装 %s", rt.Name, in.Harness)
			}
		}
	}

	var issue linear.Issue
	switch {
	case strings.TrimSpace(in.IssueKey) != "":
		issue, err = linear.New(s.linearKey(ctx)).GetIssue(ctx, strings.TrimSpace(in.IssueKey))
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

	run, err := s.st.CreateRun(ctx, store.NewRun{
		RepoID: repo.ID, Branch: in.Branch, Issue: issue, RuntimeID: rt.ID, Harness: in.Harness,
		TimeoutMinutes: in.TimeoutMinutes,
		Prompt: func(id string) string {
			return testplan.Prompt(testplan.Input{RunID: id, Issue: issue, RepoURL: repo.URL, Branch: in.Branch, TestNotes: repo.TestNotes})
		},
	})
	if err != nil {
		return store.Run{}, http.StatusInternalServerError, err
	}
	go s.hub.Schedule(rt.ID)
	return run, 0, nil
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, run)
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
	if err := s.hub.Cancel(r.Context(), run); err != nil {
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
	in := runInput{RepoID: old.RepoID, Branch: old.Branch, RuntimeID: old.RuntimeID, Harness: old.Harness, TimeoutMinutes: old.TimeoutMinutes}
	if old.Issue.ID != "" {
		in.IssueKey = old.IssueKey // 重新拉取最新的 issue 内容
	} else {
		in.ManualIssue = &struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}{old.Issue.Title, old.Issue.Description}
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
	s.writeBack(run.ID)
	run, _ = s.st.Run(r.Context(), run.ID)
	ok(w, run)
}

// ---- Linear 回写 ----

func (s *Server) writeBack(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run, err := s.st.Run(ctx, runID)
	if err != nil || run.Issue.ID == "" || run.Status == store.StatusCanceled {
		return
	}
	key := s.linearKey(ctx)
	if key == "" {
		_ = s.st.SetLinearResult(ctx, runID, "", "未配置 Linear API Key，未回写")
		return
	}
	url, err := linear.New(key).CreateComment(ctx, run.Issue.ID, CommentBody(run, s.publicURL(ctx, nil)))
	msg := ""
	if err != nil {
		msg = err.Error()
		log.Printf("测试 %s 回写 Linear 失败: %v", runID, err)
	}
	_ = s.st.SetLinearResult(ctx, runID, url, msg)
}

var verdictText = map[string]string{
	protocol.VerdictPass:    "✅ 通过",
	protocol.VerdictFail:    "❌ 未通过",
	protocol.VerdictPartial: "⚠️ 部分无法验证",
}

var acText = map[string]string{
	protocol.ACPass:         "✅ 通过",
	protocol.ACFail:         "❌ 未通过",
	protocol.ACUnverifiable: "⚠️ 无法验证",
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "<br>")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// CommentBody 生成回写到 Linear 的评论（Markdown）。
func CommentBody(run store.Run, base string) string {
	var b strings.Builder
	b.WriteString("### Verdict 验收测试结果\n\n")
	commit := run.CommitSHA
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if run.Status == store.StatusCompleted {
		fmt.Fprintf(&b, "**结论：%s**\n\n", verdictText[run.Verdict])
	} else {
		fmt.Fprintf(&b, "**测试未完成：%s**\n\n", mdCell(run.Error))
	}
	fmt.Fprintf(&b, "- 仓库：`%s` 分支 `%s`", run.RepoName, run.Branch)
	if commit != "" {
		fmt.Fprintf(&b, "（commit `%s`）", commit)
	}
	fmt.Fprintf(&b, "\n- 执行：%s @ %s\n", run.Harness, run.RuntimeName)
	if run.SourceModified {
		b.WriteString("- ⚠️ 测试过程中修改了被测源码，结论可信度降低\n")
	}
	if run.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", run.Summary)
	}
	if len(run.ACs) > 0 {
		b.WriteString("\n| # | 验收标准 | 结果 | 证据 |\n|---|---|---|---|\n")
		for _, a := range run.ACs {
			st := acText[a.Status]
			if st == "" {
				st = a.Status
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", mdCell(a.ID), mdCell(a.Criterion), st, mdCell(a.Evidence))
		}
	}
	if base != "" {
		fmt.Fprintf(&b, "\n[查看完整报告](%s/runs/%s)\n", base, run.ID)
	}
	return b.String()
}

// ---- 安装与下载 ----

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !strings.HasPrefix(name, "verdict-") || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.cfg.BinDir, name)
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "该平台的 agent 二进制不存在: "+name, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	io.WriteString(w, installSh)
}
