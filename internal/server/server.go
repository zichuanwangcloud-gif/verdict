// Package server 是 Verdict 后端：REST API、agent WebSocket、测试编排与 Linear 回写。
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"verdict/internal/linear"
	"verdict/internal/protocol"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

const (
	settingLinearKey = "linear_api_key"
	settingPublicURL = "public_url"
	// settingBoardDefaults 记住 Linear 列表页顶部的统一配置（仓库 / 分支 / 档位 / 运行时 / harness / 筛选）。
	settingBoardDefaults = "board_defaults"
	// SettingStageRetries / SettingRetryDelay 是阶段重试设置，页面「设置」可改。
	SettingStageRetries = "stage_retries"
	SettingRetryDelay   = "stage_retry_delay_seconds"
)

var harnesses = []string{"claude", "codex"}

// maxConcurrency 是并发度的硬上限，防止误填一个巨大值把机器打满。
const maxConcurrency = 32

// suggestConcurrency 按机器规模给出建议并发度：CPU 核数的一半、每 8GB 内存一个，取小的，夹在 [1, 8]。
// 每个测试里都有模型子进程、构建和浏览器，不是纯 CPU 负载，所以不给满核数。
func suggestConcurrency(info *protocol.HostInfo) int {
	if info == nil || (info.CPU == 0 && info.MemTotalMB == 0) {
		return 1
	}
	n := 8
	if info.CPU > 0 {
		n = min(n, max(info.CPU/2, 1))
	}
	if info.MemTotalMB > 0 {
		n = min(n, max(info.MemTotalMB/8192, 1))
	}
	return max(1, n)
}

// effectiveConcurrency 是实际生效的并发度：用户设置优先，0 表示跟随建议值。
func effectiveConcurrency(rt store.Runtime) int {
	if rt.Concurrency > 0 {
		return rt.Concurrency
	}
	return suggestConcurrency(rt.Info)
}

// LinearEndpoint 便于测试时替换 Linear API 地址，生产环境为空。
var LinearEndpoint string

type Config struct {
	AdminToken string
	WebDir     string // 前端构建产物
	BinDir     string // 供运行时下载的 agent 二进制：verdict-linux-amd64 等
	DataDir    string // 产物（截图等）存放目录
}

type Server struct {
	cfg Config
	st  *store.Store
	hub *Hub
}

func New(cfg Config, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, hub: NewHub(st)}
	s.hub.OnFinish = func(runID string) {
		s.writeBack(runID)
		if run, err := s.st.Run(context.Background(), runID); err == nil && run.RegressionID != "" {
			s.regressionDone(context.Background(), run.RegressionID)
		}
	}
	return s
}

func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, map[string]any{"ok": true, "version": protocol.Version, "protocol": protocol.ProtocolVersion})
	})
	mux.HandleFunc("GET /api/agent/ws", s.hub.ServeWS)
	mux.HandleFunc("POST /api/agent/runs/{id}/artifacts", s.uploadArtifact)
	mux.HandleFunc("GET /install.sh", s.installScript)
	mux.HandleFunc("GET /download/{file}", s.download)

	api.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) { ok(w, map[string]bool{"ok": true}) })
	api.HandleFunc("GET /api/settings", s.getSettings)
	api.HandleFunc("PUT /api/settings", s.putSettings)
	api.HandleFunc("GET /api/tiers", func(w http.ResponseWriter, _ *http.Request) { ok(w, testplan.Presets()) })

	api.HandleFunc("GET /api/runtimes", s.listRuntimes)
	api.HandleFunc("POST /api/runtimes", s.createRuntime)
	api.HandleFunc("PATCH /api/runtimes/{id}", s.renameRuntime)
	api.HandleFunc("DELETE /api/runtimes/{id}", s.deleteRuntime)
	api.HandleFunc("POST /api/runtimes/{id}/probe", s.probeRuntime)
	api.HandleFunc("POST /api/runtimes/{id}/token", s.rotateToken)

	api.HandleFunc("GET /api/repos", s.listRepos)
	api.HandleFunc("POST /api/repos", s.createRepo)
	api.HandleFunc("PUT /api/repos/{id}", s.updateRepo)
	api.HandleFunc("DELETE /api/repos/{id}", s.deleteRepo)

	api.HandleFunc("GET /api/issues", s.searchIssues)
	api.HandleFunc("GET /api/issues/{key}", s.previewIssue)
	api.HandleFunc("GET /api/board/issues", s.issueBoard)
	api.HandleFunc("GET /api/board/teams", s.listIssueTeams)
	api.HandleFunc("POST /api/board/runs", s.startBatchRuns)

	api.HandleFunc("GET /api/versions", s.listVersions)
	api.HandleFunc("POST /api/versions", s.createVersion)
	api.HandleFunc("GET /api/versions/{id}", s.getVersion)
	api.HandleFunc("PATCH /api/versions/{id}", s.updateVersion)
	api.HandleFunc("DELETE /api/versions/{id}", s.deleteVersion)
	api.HandleFunc("POST /api/versions/{id}/issues", s.addVersionIssue)
	api.HandleFunc("DELETE /api/versions/{id}/issues/{key}", s.removeVersionIssue)
	api.HandleFunc("POST /api/versions/{id}/regressions", s.createRegression)
	api.HandleFunc("POST /api/versions/{id}/release", s.releaseVersion)
	api.HandleFunc("POST /api/regressions/{id}/retry", s.retryRegression)
	api.HandleFunc("POST /api/regressions/{id}/cancel", s.cancelRegression)
	api.HandleFunc("GET /api/repos/{id}/cases", s.listCases)
	api.HandleFunc("POST /api/repos/{id}/cases", s.createCase)
	api.HandleFunc("GET /api/cases/{id}", s.getCase)
	api.HandleFunc("PATCH /api/cases/{id}", s.updateCase)
	api.HandleFunc("DELETE /api/cases/{id}", s.deleteCase)
	api.HandleFunc("GET /api/cases/{id}/history", s.caseHistory)

	api.HandleFunc("GET /api/runs", s.listRuns)
	api.HandleFunc("POST /api/runs", s.createRun)
	api.HandleFunc("GET /api/runs/{id}", s.getRun)
	api.HandleFunc("GET /api/runs/{id}/logs", s.runLogs)
	api.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	api.HandleFunc("POST /api/runs/{id}/approve", s.approveRun)
	api.HandleFunc("POST /api/runs/{id}/rerun", s.rerun)
	api.HandleFunc("POST /api/runs/{id}/resume", s.resumeRun)
	api.HandleFunc("POST /api/runs/{id}/linear", s.retryWriteBack)
	api.HandleFunc("GET /api/runs/{id}/artifacts/{name}", s.getArtifact)
	api.HandleFunc("GET /api/runs/{id}/truth", s.getTruth)
	api.HandleFunc("PUT /api/runs/{id}/truth", s.putTruth)
	api.HandleFunc("DELETE /api/runs/{id}/truth", s.deleteTruth)

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

func linearClient(key string) *linear.Client {
	c := linear.New(key)
	if LinearEndpoint != "" {
		c.Endpoint = LinearEndpoint
	}
	return c
}

func intOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
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
	retries, _ := s.st.Setting(r.Context(), SettingStageRetries)
	delay, _ := s.st.Setting(r.Context(), SettingRetryDelay)
	ok(w, map[string]any{
		"board_defaults":        s.boardDefaults(r.Context()),
		"linear_api_key_masked": masked,
		"public_url":            pub,
		"public_url_effective":  s.publicURL(r.Context(), r),
		"stage_retries":         intOr(retries, testplan.DefaultStageRetries),
		"stage_retry_delay":     intOr(delay, testplan.DefaultRetryDelaySecs),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LinearAPIKey *string `json:"linear_api_key"`
		PublicURL    *string `json:"public_url"`
		StageRetries *int    `json:"stage_retries"`
		RetryDelay   *int    `json:"stage_retry_delay"` // 秒
		// BoardDefaults 是 Linear 列表页顶部的统一配置，整块覆盖。
		BoardDefaults *boardDefaults `json:"board_defaults"`
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
	if in.StageRetries != nil {
		n := max(0, min(*in.StageRetries, testplan.MaxStageRetries))
		if err := s.st.SetSetting(r.Context(), SettingStageRetries, strconv.Itoa(n)); err != nil {
			failErr(w, err)
			return
		}
	}
	if in.RetryDelay != nil {
		n := max(1, min(*in.RetryDelay, 3600))
		if err := s.st.SetSetting(r.Context(), SettingRetryDelay, strconv.Itoa(n)); err != nil {
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
	if in.BoardDefaults != nil {
		raw, err := json.Marshal(in.BoardDefaults)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.st.SetSetting(r.Context(), settingBoardDefaults, string(raw)); err != nil {
			failErr(w, err)
			return
		}
	}
	s.getSettings(w, r)
}

// ---- runtimes ----

type runtimeView struct {
	store.Runtime
	// Outdated 表示 agent 协议版本与后端不一致，需要重新安装。
	Outdated bool `json:"outdated"`
	// EffectiveConcurrency 是实际使用的并发度（用户设置优先，0 时按机器算）。
	EffectiveConcurrency int `json:"effective_concurrency"`
	// SuggestedConcurrency 是按机器配置给出的建议值。
	SuggestedConcurrency int `json:"suggested_concurrency"`
}

func (s *Server) listRuntimes(w http.ResponseWriter, r *http.Request) {
	rts, err := s.st.Runtimes(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	out := []runtimeView{}
	for _, rt := range rts {
		if strings.HasSuffix(rt.Name, "（已删除）") {
			continue
		}
		rt.Online = s.hub.Online(rt.ID)
		out = append(out, runtimeView{Runtime: rt, Outdated: rt.Info != nil && rt.Info.Protocol != protocol.ProtocolVersion,
			EffectiveConcurrency: effectiveConcurrency(rt), SuggestedConcurrency: suggestConcurrency(rt.Info)})
	}
	ok(w, out)
}

func (s *Server) installCommand(ctx context.Context, r *http.Request, token string) string {
	base := s.publicURL(ctx, r)
	return fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- --server %s --token %s", base, base, token)
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
	ok(w, map[string]any{"runtime": rt, "token": token, "install_command": s.installCommand(r.Context(), r, token)})
}

func (s *Server) renameRuntime(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Concurrency *int    `json:"concurrency"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			fail(w, http.StatusBadRequest, "名称不能为空")
			return
		}
		if err := s.st.RenameRuntime(r.Context(), id, strings.TrimSpace(*in.Name)); err != nil {
			failErr(w, err)
			return
		}
	}
	if in.Concurrency != nil {
		v := *in.Concurrency
		if v < 0 || v > maxConcurrency {
			fail(w, http.StatusBadRequest, fmt.Sprintf("并发度只能是 0（按机器自动）到 %d", maxConcurrency))
			return
		}
		if err := s.st.SetConcurrency(r.Context(), id, v); err != nil {
			failErr(w, err)
			return
		}
		go s.hub.Schedule(id) // 调高后可能立刻有槽位
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

// rotateToken 重新生成令牌与安装命令（用于升级 agent 或令牌泄露）。旧连接会被断开。
func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "id 无效")
		return
	}
	token, err := s.st.RotateToken(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	s.hub.Disconnect(id)
	rt, _ := s.st.Runtime(r.Context(), id)
	ok(w, map[string]any{"runtime": rt, "token": token, "install_command": s.installCommand(r.Context(), r, token)})
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
	SetupCmd  string `json:"setup_cmd"`
	StartCmd  string `json:"start_cmd"`
	HealthURL string `json:"health_url"`
	AppURL    string `json:"app_url"`
	// Ports 是这个仓库的测试需要的端口数（0–8）。并发时需要给每个测试分配独立端口。
	Ports int `json:"ports"`
}

func (in *repoInput) repo(id int64) (store.Repo, error) {
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return store.Repo{}, errors.New("仓库地址不能为空")
	}
	if strings.HasPrefix(in.URL, "-") || strings.ContainsAny(in.URL, " \t\n") {
		return store.Repo{}, errors.New("仓库地址不合法")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = repoName(in.URL)
	}
	for _, u := range []string{in.HealthURL, in.AppURL} {
		if u = strings.TrimSpace(u); u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return store.Repo{}, errors.New("健康检查地址和前端地址需要以 http:// 或 https:// 开头")
		}
	}
	if in.Ports < 0 || in.Ports > 8 {
		return store.Repo{}, errors.New("需要的端口数只能是 0 到 8")
	}
	return store.Repo{ID: id, URL: in.URL, Name: in.Name, TestNotes: in.TestNotes,
		SetupCmd: strings.TrimSpace(in.SetupCmd), StartCmd: strings.TrimSpace(in.StartCmd),
		HealthURL: strings.TrimSpace(in.HealthURL), AppURL: strings.TrimSpace(in.AppURL), Ports: in.Ports}, nil
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
	repo, err := in.repo(0)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.st.CreateRepo(r.Context(), repo)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			fail(w, http.StatusConflict, "该仓库已存在")
			return
		}
		failErr(w, err)
		return
	}
	ok(w, created)
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
	repo, err := in.repo(id)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.UpdateRepo(r.Context(), repo); err != nil {
		failErr(w, err)
		return
	}
	updated, err := s.st.Repo(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	ok(w, updated)
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

// searchIssues 关键字搜索 issue；q 为空时返回最近更新的 issue。
func (s *Server) searchIssues(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	key := s.linearKey(r.Context())
	if key == "" {
		fail(w, http.StatusBadRequest, "尚未配置 Linear API Key，请先在「设置」里填写，或改用手动输入需求")
		return
	}
	list, err := linearClient(key).SearchIssues(r.Context(), q, limit)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) previewIssue(w http.ResponseWriter, r *http.Request) {
	is, err := linearClient(s.linearKey(r.Context())).GetIssue(r.Context(), strings.TrimSpace(r.PathValue("key")))
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, is)
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
