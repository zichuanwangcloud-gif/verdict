package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"verdict/internal/linear"
	"verdict/internal/server/store"
	"verdict/internal/testplan"
)

// ---- Linear 列表（看板） ----

func (s *Server) listIssueTeams(w http.ResponseWriter, r *http.Request) {
	key := s.linearKey(r.Context())
	if key == "" {
		fail(w, http.StatusBadRequest, "尚未配置 Linear API Key，请先在「设置」里填写")
		return
	}
	teams, err := linearClient(key).Teams(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, teams)
}

// issueBoard 返回列表页的 issue，并附上每条在这个仓库上最近一次测试的状态。
//   - team：团队 key，留空表示不限团队（跨团队，按最近更新排序）
//   - states：状态类型，逗号分隔（started,unstarted,completed…），留空表示不限
//   - assignee：all（默认）| me | <user id>
//   - repo_id：附最近一次测试时只看这个仓库；0 表示跨仓库
func (s *Server) issueBoard(w http.ResponseWriter, r *http.Request) {
	key := s.linearKey(r.Context())
	if key == "" {
		fail(w, http.StatusBadRequest, "尚未配置 Linear API Key，请先在「设置」里填写")
		return
	}
	q := r.URL.Query()
	in := linear.BoardQuery{
		TeamKey:  strings.TrimSpace(q.Get("team")),
		Assignee: strings.TrimSpace(q.Get("assignee")),
		Query:    strings.TrimSpace(q.Get("q")),
	}
	if v := strings.TrimSpace(q.Get("states")); v != "" {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				in.StateTypes = append(in.StateTypes, t)
			}
		}
	}
	in.Limit = intOr(q.Get("limit"), 100)

	repoID, _ := strconv.ParseInt(q.Get("repo_id"), 10, 64)
	if repoID > 0 {
		if _, err := s.st.Repo(r.Context(), repoID); err != nil {
			fail(w, http.StatusBadRequest, "仓库不存在")
			return
		}
	}

	client := linearClient(key)
	list, err := client.BoardIssues(r.Context(), in)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}

	keys := make([]string, 0, len(list))
	for _, it := range list {
		keys = append(keys, it.Identifier)
	}
	last, err := s.st.LastRunsByIssue(r.Context(), repoID, keys)
	if err != nil {
		failErr(w, err)
		return
	}

	type row struct {
		linear.BoardIssue
		LastRun *store.IssueRunBrief `json:"last_run"`
	}
	// 顺带查出每行最近一次测试的人工真值，行内才好标「已复核 / 待复核」。
	runIDs := make([]string, 0, len(list))
	for _, it := range list {
		if b, found := last[it.Identifier]; found {
			runIDs = append(runIDs, b.RunID)
		}
	}
	truths, err := s.st.TruthsFor(r.Context(), runIDs)
	if err != nil {
		failErr(w, err)
		return
	}
	rows := make([]row, 0, len(list))
	for _, it := range list {
		rw := row{BoardIssue: it}
		if b, found := last[it.Identifier]; found {
			rw.LastRun = &b
			if t, ok := truths[b.RunID]; ok {
				rw.LastRun.Truth = &t
			}
		}
		rows = append(rows, rw)
	}
	ok(w, map[string]any{"issues": rows, "repo_id": repoID})
}

// batchRunInput 是一次给多个 issue 排测试。仓库、分支、档位等统一给，节省逐条配置。
type batchRunInput struct {
	IssueKeys   []string         `json:"issue_keys"`
	RepoID      int64            `json:"repo_id"`
	Branch      string           `json:"branch"`
	RuntimeID   int64            `json:"runtime_id"`
	Harness     string           `json:"harness"`
	Tier        string           `json:"tier"`
	Config      *testplan.Config `json:"config"`
	ApprovePlan *bool            `json:"approve_plan"`
	MaxMinutes  int              `json:"max_minutes"`
	MaxCostUSD  *float64         `json:"max_cost_usd"`
}

// batchResult 是批量启动里单条的结果。单条失败不影响其余条目。
type batchResult struct {
	IssueKey string `json:"issue_key"`
	RunID    string `json:"run_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// startBatchRuns 给多个 issue 各建一条测试。跳过已在运行中的 issue，重复点击不会重复排队。
func (s *Server) startBatchRuns(w http.ResponseWriter, r *http.Request) {
	var in batchRunInput
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.IssueKeys) == 0 {
		fail(w, http.StatusBadRequest, "请选择要测试的 issue")
		return
	}
	if len(in.IssueKeys) > maxBatchIssues {
		fail(w, http.StatusBadRequest, "一次最多启动 "+strconv.Itoa(maxBatchIssues)+" 个 issue")
		return
	}

	// 已经在这个仓库上排队或执行中的 issue 先查出来，避免重复排单。
	actives, err := s.st.ActiveRunsByIssue(r.Context(), in.RepoID)
	if err != nil {
		failErr(w, err)
		return
	}

	results := make([]batchResult, 0, len(in.IssueKeys))
	for _, key := range in.IssueKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if rid, busy := actives[key]; busy {
			results = append(results, batchResult{IssueKey: key, RunID: rid, Error: "已有测试在进行中"})
			continue
		}
		run, code, err := s.startRun(r.Context(), runInput{
			IssueKey: key, RepoID: in.RepoID, Branch: in.Branch, RuntimeID: in.RuntimeID, Harness: in.Harness,
			Tier: in.Tier, Config: in.Config, ApprovePlan: in.ApprovePlan, MaxMinutes: in.MaxMinutes, MaxCostUSD: in.MaxCostUSD,
		})
		if err != nil {
			msg := err.Error()
			if code >= 500 {
				msg = "服务暂时不可用：" + msg
			}
			results = append(results, batchResult{IssueKey: key, Error: msg})
			continue
		}
		results = append(results, batchResult{IssueKey: key, RunID: run.ID})
	}
	ok(w, map[string]any{"results": results})
}

// maxBatchIssues 限制一次批量启动的条数，防止误选整个团队把机器排满很久。
const maxBatchIssues = 20

// boardDefaults 是 Linear 列表页顶部的统一配置与筛选，存进 settings 记住。
// 启动测试时页面把它连同选中的 issue 一起提交，省得每次重选。
type boardDefaults struct {
	RepoID      int64    `json:"repo_id"`
	Branch      string   `json:"branch"`
	RuntimeID   int64    `json:"runtime_id"`
	Harness     string   `json:"harness"`
	Tier        string   `json:"tier"`
	ApprovePlan bool     `json:"approve_plan"`
	MaxMinutes  int      `json:"max_minutes"`
	MaxCostUSD  *float64 `json:"max_cost_usd"`
	// 筛选：团队、状态类型、负责人范围（all / me / user id）。
	Team     string   `json:"team"`
	States   []string `json:"states"`
	Assignee string   `json:"assignee"`
}

// boardDefaults 读取记住的列表页配置；读不到或损坏时返回零值，页面按默认值兜底。
func (s *Server) boardDefaults(ctx context.Context) boardDefaults {
	var d boardDefaults
	raw, _ := s.st.Setting(ctx, settingBoardDefaults)
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &d)
	}
	return d
}
