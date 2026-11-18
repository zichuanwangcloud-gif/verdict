// Package linear 是 Linear GraphQL API 的最小客户端：读 issue、发评论。
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const endpoint = "https://api.linear.app/graphql"

type Comment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type Issue struct {
	ID          string    `json:"id"` // Linear 内部 UUID，发评论用
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	Comments    []Comment `json:"comments"`
}

type Client struct {
	APIKey   string
	Endpoint string // 测试时可替换
	HTTP     *http.Client

	viewerOnce sync.Once
	viewerID   string
	viewerErr  error
}

// MyID 返回 API Key 所属账号的 user id（用于判断 issue 是否分配给我），结果会被缓存。
func (c *Client) MyID(ctx context.Context) (string, error) {
	c.viewerOnce.Do(func() {
		var out struct {
			Viewer struct {
				ID string `json:"id"`
			} `json:"viewer"`
		}
		if err := c.do(ctx, `query { viewer { id } }`, nil, &out); err != nil {
			c.viewerErr = err
			return
		}
		c.viewerID = out.Viewer.ID
	})
	return c.viewerID, c.viewerErr
}

func New(key string) *Client {
	ep := endpoint
	if v := os.Getenv("VERDICT_LINEAR_ENDPOINT"); v != "" {
		ep = v // 自建或代理时覆盖
	}
	return &Client{APIKey: key, Endpoint: ep, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	if c.APIKey == "" {
		return errors.New("未配置 Linear API Key")
	}
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Linear 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("Linear 返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("Linear: %s", env.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Linear 返回 HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(env.Data, out)
}

// IssueSummary 是搜索/列表结果里的一条 issue（不含评论）。
type IssueSummary struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Team       string `json:"team"`
	Assignee   string `json:"assignee"`
	// AssigneeID 是负责人的 user id；Mine 表示是否分配给我（API Key 所属账号）。
	AssigneeID string `json:"-"`
	Mine       bool   `json:"mine"`
	UpdatedAt  string `json:"updated_at"`
	URL        string `json:"url"`
}

// 搜 issue 时一次最多返回多少条（Linear 的 first 上限是 250）。
const searchCap = 50

var identifierRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-(\d*)$`)

// SearchIssues 搜索 issue。
//
//   - query 为空：只返回分配给我（API Key 所属账号）的 issue，按最近更新排序——点开下拉时的默认列表。
//   - query 非空：搜索全部 issue（编号、标题、全文），分配给我的排在前面。
//
// Linear 的 IssueFilter 不支持按 identifier 过滤，searchIssues 又是模糊语义搜索，所以按输入形态组合查询，
// 再在本地合并、排序：分配给我 > 编号完全一致 > 编号前缀匹配 > 其余（最近更新）。
func (c *Client) SearchIssues(ctx context.Context, query string, limit int) ([]IssueSummary, error) {
	if limit <= 0 || limit > searchCap {
		limit = searchCap
	}
	query = strings.TrimSpace(query)

	myID, err := c.MyID(ctx)
	if err != nil {
		return nil, err
	}

	if query == "" {
		list, err := c.listIssues(ctx, map[string]any{"assignee": map[string]any{"isMe": map[string]any{"eq": true}}}, limit)
		if err != nil {
			return nil, err
		}
		markMine(list, myID)
		return capList(sortIssues(list, "", myID), limit), nil
	}

	cands := map[string]IssueSummary{}
	add := func(list []IssueSummary) {
		for _, s := range list {
			if prev, ok := cands[s.Identifier]; !ok || prev.UpdatedAt < s.UpdatedAt {
				cands[s.Identifier] = s
			}
		}
	}

	if m := identifierRe.FindStringSubmatch(query); m != nil {
		team, digits := strings.ToUpper(m[1]), m[2]
		filter := map[string]any{"team": map[string]any{"key": map[string]any{"eq": team}}}
		if digits != "" {
			// 数字部分是按前缀匹配的：142 应命中 142、1420…，而数字前缀不是连续区间，
			// 要展开成 1、10-19、100-199… 这样的多段区间。
			filter = map[string]any{"and": []any{filter, map[string]any{"or": numberPrefixRanges(digits)}}}
		}
		list, err := c.listIssues(ctx, filter, searchCap)
		if err == nil {
			add(list)
		}
		// 团队 key 不存在、区间不被支持或没有命中时，退回全文搜索（用户可能输错了团队）。
		if err != nil || len(cands) == 0 {
			got, serr := c.searchIssues(ctx, query, limit)
			if serr != nil {
				if err != nil {
					return nil, err
				}
				return nil, serr
			}
			add(got)
		}
	} else {
		list, err := c.listIssues(ctx, map[string]any{"title": map[string]any{"containsIgnoreCase": query}}, searchCap)
		if err != nil {
			return nil, err
		}
		add(list)
		// 标题没搜到时不打扰用户；关键词可能出现在描述或评论里，再补一轮全文搜索。
		if len(cands) < limit {
			if got, err := c.searchIssues(ctx, query, limit); err == nil {
				add(got)
			}
		}
	}

	out := make([]IssueSummary, 0, len(cands))
	for _, s := range cands {
		out = append(out, s)
	}
	markMine(out, myID)
	return capList(sortIssues(out, query, myID), limit), nil
}

func markMine(list []IssueSummary, myID string) {
	if myID == "" {
		return
	}
	for i := range list {
		list[i].Mine = list[i].AssigneeID == myID
	}
}

// sortIssues 排序：分配给我的在前，其次编号完全一致、编号前缀匹配，最后按最近更新。
func sortIssues(list []IssueSummary, query, myID string) []IssueSummary {
	up := strings.ToUpper(strings.TrimSpace(query))
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Mine != list[j].Mine {
			return list[i].Mine
		}
		ri, rj := 3, 3
		if up != "" {
			ri, rj = idRank(list[i].Identifier, up), idRank(list[j].Identifier, up)
		}
		if ri != rj {
			return ri < rj
		}
		return list[i].UpdatedAt > list[j].UpdatedAt
	})
	return list
}

func idRank(id, up string) int {
	id = strings.ToUpper(id)
	switch {
	case id == up:
		return 0
	case strings.HasPrefix(id, up):
		return 1
	}
	return 3
}

func capList(list []IssueSummary, n int) []IssueSummary {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// numberPrefixRanges 把数字前缀展开成若干闭区间。前缀匹配的编号是
// 14、140-149、1400-1499……，不是一段连续区间，所以要展开：
// "1" → [1,1] [10,19] [100,199]…，"14" → [14,14] [140,149] [1400,1499]…
func numberPrefixRanges(digits string) []any {
	const maxDigits = 7 // 超过这个位数就不做前缀展开（量级不现实，也避免过多区间）
	lo, err := strconv.Atoi(digits)
	if err != nil || lo < 0 {
		return nil
	}
	var out []any
	span := 1
	for i := 0; i < maxDigits; i++ {
		out = append(out, map[string]any{"number": map[string]any{"gte": lo, "lte": lo + span - 1}})
		lo *= 10
		span *= 10
		if lo > 100_000_000 {
			break
		}
	}
	return out
}

type issueNode = struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	UpdatedAt  string `json:"updatedAt"`
	State      *struct {
		Name string `json:"name"`
	} `json:"state"`
	Team *struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	} `json:"team"`
	Assignee *struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	} `json:"assignee"`
}

func toSummary(n issueNode) IssueSummary {
	s := IssueSummary{ID: n.ID, Identifier: n.Identifier, Title: n.Title, URL: n.URL, UpdatedAt: n.UpdatedAt}
	if n.State != nil {
		s.State = n.State.Name
	}
	if n.Team != nil {
		s.Team = n.Team.Name
	}
	if n.Assignee != nil {
		s.Assignee, s.AssigneeID = n.Assignee.DisplayName, n.Assignee.ID
	}
	return s
}

// listIssues 用 issues(filter:) 查询，按最近更新排序。
func (c *Client) listIssues(ctx context.Context, filter map[string]any, limit int) ([]IssueSummary, error) {
	const q = `query($filter: IssueFilter, $first: Int) {
  issues(filter: $filter, first: $first, orderBy: updatedAt) {
    nodes { id identifier title url updatedAt state { name } team { name key } assignee { id displayName } }
  }
}`
	var out struct {
		Issues struct {
			Nodes []issueNode `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, q, map[string]any{"filter": filter, "first": limit}, &out); err != nil {
		return nil, err
	}
	res := make([]IssueSummary, 0, len(out.Issues.Nodes))
	for _, n := range out.Issues.Nodes {
		res = append(res, toSummary(n))
	}
	return res, nil
}

// searchIssues 用全文搜索，结果与关键词相关性有关。
func (c *Client) searchIssues(ctx context.Context, term string, limit int) ([]IssueSummary, error) {
	const q = `query($term: String!, $first: Int) {
  searchIssues(term: $term, first: $first) {
    nodes { id identifier title url updatedAt state { name } team { name key } assignee { id displayName } }
  }
}`
	var out struct {
		SearchIssues struct {
			Nodes []issueNode `json:"nodes"`
		} `json:"searchIssues"`
	}
	if err := c.do(ctx, q, map[string]any{"term": term, "first": limit}, &out); err != nil {
		return nil, err
	}
	res := make([]IssueSummary, 0, len(out.SearchIssues.Nodes))
	for _, n := range out.SearchIssues.Nodes {
		res = append(res, toSummary(n))
	}
	return res, nil
}

// GetIssue 按编号（如 ENG-123）或 UUID 读取 issue 及其评论。
func (c *Client) GetIssue(ctx context.Context, key string) (Issue, error) {
	const q = `query($id: String!) {
  issue(id: $id) {
    id identifier title description url
    comments(first: 100) { nodes { body createdAt user { name } } }
  }
}`
	var out struct {
		Issue *struct {
			ID          string `json:"id"`
			Identifier  string `json:"identifier"`
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
			Comments    struct {
				Nodes []struct {
					Body      string `json:"body"`
					CreatedAt string `json:"createdAt"`
					User      *struct {
						Name string `json:"name"`
					} `json:"user"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := c.do(ctx, q, map[string]any{"id": key}, &out); err != nil {
		return Issue{}, err
	}
	if out.Issue == nil {
		return Issue{}, fmt.Errorf("Linear 中找不到 issue %s", key)
	}
	is := Issue{ID: out.Issue.ID, Identifier: out.Issue.Identifier, Title: out.Issue.Title,
		Description: out.Issue.Description, URL: out.Issue.URL}
	for _, n := range out.Issue.Comments.Nodes {
		author := "（未知）"
		if n.User != nil {
			author = n.User.Name
		}
		is.Comments = append(is.Comments, Comment{Author: author, Body: n.Body, CreatedAt: n.CreatedAt})
	}
	// 按创建时间正序（ISO 8601 字符串可直接比较），便于模型按时间线阅读。
	sort.SliceStable(is.Comments, func(i, j int) bool { return is.Comments[i].CreatedAt < is.Comments[j].CreatedAt })
	return is, nil
}

// CreateComment 在 issue 下发评论，返回评论链接。
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (string, error) {
	const q = `mutation($input: CommentCreateInput!) {
  commentCreate(input: $input) { success comment { url } }
}`
	var out struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				URL string `json:"url"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	err := c.do(ctx, q, map[string]any{"input": map[string]any{"issueId": issueID, "body": body}}, &out)
	if err != nil {
		return "", err
	}
	if !out.CommentCreate.Success {
		return "", errors.New("Linear 未能创建评论")
	}
	return out.CommentCreate.Comment.URL, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
