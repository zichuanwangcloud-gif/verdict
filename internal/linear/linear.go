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
	"sort"
	"strings"
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
	UpdatedAt  string `json:"updated_at"`
	URL        string `json:"url"`
}

// SearchIssues 按关键字搜索 issue。query 为空时返回最近更新的 issue。
// 关键字同时匹配编号（如 ENG-12）和标题；匹配到编号时把精确命中的排在前面。
func (c *Client) SearchIssues(ctx context.Context, query string, limit int) ([]IssueSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	query = strings.TrimSpace(query)
	const q = `query($filter: IssueFilter, $first: Int) {
  issues(filter: $filter, first: $first, orderBy: updatedAt) {
    nodes {
      id identifier title url updatedAt
      state { name }
      team { name key }
      assignee { displayName }
    }
  }
}`
	vars := map[string]any{"first": limit}
	if query != "" {
		// 编号或标题包含关键字，都要能搜到。
		vars["filter"] = map[string]any{"or": []any{
			map[string]any{"identifier": map[string]any{"containsIgnoreCase": query}},
			map[string]any{"title": map[string]any{"containsIgnoreCase": query}},
		}}
	}
	var out struct {
		Issues struct {
			Nodes []struct {
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
					DisplayName string `json:"displayName"`
				} `json:"assignee"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, q, vars, &out); err != nil {
		return nil, err
	}
	res := make([]IssueSummary, 0, len(out.Issues.Nodes))
	for _, n := range out.Issues.Nodes {
		s := IssueSummary{ID: n.ID, Identifier: n.Identifier, Title: n.Title, URL: n.URL, UpdatedAt: n.UpdatedAt}
		if n.State != nil {
			s.State = n.State.Name
		}
		if n.Team != nil {
			s.Team = n.Team.Name
		}
		if n.Assignee != nil {
			s.Assignee = n.Assignee.DisplayName
		}
		res = append(res, s)
	}
	// 排序：编号完全相同的最前，其次是编号以关键字开头的，其余保持接口返回的最近更新顺序。
	if up := strings.ToUpper(query); up != "" {
		rank := func(s string) int {
			id := strings.ToUpper(s)
			switch {
			case id == up:
				return 0
			case strings.HasPrefix(id, up):
				return 1
			}
			return 2
		}
		sort.SliceStable(res, func(i, j int) bool { return rank(res[i].Identifier) < rank(res[j].Identifier) })
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
