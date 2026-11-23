package linear

import (
	"context"
	"strings"
)

// TeamState 是团队的工作流状态（In Progress、Done 等）。
type TeamState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"` // backlog / unstarted / started / completed / canceled
	Color    string  `json:"color"`
	Position float64 `json:"position"`
}

// Team 是 Linear 团队，带上它的工作流状态，供列表页做状态筛选。
type Team struct {
	ID     string      `json:"id"`
	Key    string      `json:"key"`
	Name   string      `json:"name"`
	States []TeamState `json:"states"`
}

// Teams 返回 API Key 可见的团队及其状态。
func (c *Client) Teams(ctx context.Context) ([]Team, error) {
	const q = `query {
  teams(first: 100) {
    nodes { id key name states { nodes { id name type color position } } }
  }
}`
	var out struct {
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				Key    string `json:"key"`
				Name   string `json:"name"`
				States struct {
					Nodes []TeamState `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.do(ctx, q, nil, &out); err != nil {
		return nil, err
	}
	list := make([]Team, 0, len(out.Teams.Nodes))
	for _, n := range out.Teams.Nodes {
		list = append(list, Team{ID: n.ID, Key: n.Key, Name: n.Name, States: n.States.Nodes})
	}
	return list, nil
}

// BoardQuery 是列表页的筛选条件。零值表示不限制该项。
type BoardQuery struct {
	TeamKey string
	// StateTypes 按状态类型筛选（started / unstarted / completed …），比状态名稳定：
	// 团队改状态名时不会失效。为空表示不限。
	StateTypes []string
	// Assignee：空或 all 不限；me 只看分给 API Key 所属账号的；其他值按 user id 匹配。
	// 不做成「只看我的」，是因为有的 issue 做完就流转给别人，但仍然要测。
	Assignee string
	Query    string
	Limit    int
}

// BoardIssue 是列表页的一行：比 IssueSummary 多状态类型、颜色和标签，前端据此分组与配色。
type BoardIssue struct {
	IssueSummary
	StateType  string   `json:"state_type"`
	StateColor string   `json:"state_color"`
	Labels     []string `json:"labels"`
	Priority   int      `json:"priority"`
}

// boardCap 是列表一次最多取的条数（Linear first 上限 250）。
const boardCap = 250

// BoardIssues 按团队、状态、负责人筛选 issue，按最近更新排序。
func (c *Client) BoardIssues(ctx context.Context, q BoardQuery) ([]BoardIssue, error) {
	if q.Limit <= 0 || q.Limit > boardCap {
		q.Limit = 100
	}
	var and []any
	if q.TeamKey != "" {
		and = append(and, map[string]any{"team": map[string]any{"key": map[string]any{"eq": strings.ToUpper(q.TeamKey)}}})
	}
	if len(q.StateTypes) > 0 {
		and = append(and, map[string]any{"state": map[string]any{"type": map[string]any{"in": q.StateTypes}}})
	}
	switch strings.TrimSpace(q.Assignee) {
	case "", "all":
		// 不限负责人。
	case "me":
		and = append(and, map[string]any{"assignee": map[string]any{"isMe": map[string]any{"eq": true}}})
	default:
		and = append(and, map[string]any{"assignee": map[string]any{"id": map[string]any{"eq": strings.TrimSpace(q.Assignee)}}})
	}
	if term := strings.TrimSpace(q.Query); term != "" {
		and = append(and, map[string]any{"title": map[string]any{"containsIgnoreCase": term}})
	}

	var filter map[string]any
	switch len(and) {
	case 0:
	case 1:
		filter = and[0].(map[string]any)
	default:
		filter = map[string]any{"and": and}
	}

	const gq = `query($filter: IssueFilter, $first: Int) {
  issues(filter: $filter, first: $first, orderBy: updatedAt) {
    nodes {
      id identifier title url updatedAt priority
      state { name type color }
      team { name key }
      assignee { id displayName }
      labels { nodes { name } }
      parent { id identifier title }
      children(first: 100) { nodes { id identifier title state { name type } } }
    }
  }
}`
	var out struct {
		Issues struct {
			Nodes []struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
				URL        string `json:"url"`
				UpdatedAt  string `json:"updatedAt"`
				Priority   int    `json:"priority"`
				State      *struct {
					Name  string `json:"name"`
					Type  string `json:"type"`
					Color string `json:"color"`
				} `json:"state"`
				Team *struct {
					Name string `json:"name"`
					Key  string `json:"key"`
				} `json:"team"`
				Assignee *struct {
					ID          string `json:"id"`
					DisplayName string `json:"displayName"`
				} `json:"assignee"`
				Labels struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"labels"`
				Parent *struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					Title      string `json:"title"`
				} `json:"parent"`
				Children struct {
					Nodes []struct {
						ID         string `json:"id"`
						Identifier string `json:"identifier"`
						Title      string `json:"title"`
						State      *struct {
							Name string `json:"name"`
							Type string `json:"type"`
						} `json:"state"`
					} `json:"nodes"`
				} `json:"children"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, gq, map[string]any{"filter": filter, "first": q.Limit}, &out); err != nil {
		return nil, err
	}

	myID, _ := c.MyID(ctx) // 标出哪些是分给我的；失败不影响列表
	list := make([]BoardIssue, 0, len(out.Issues.Nodes))
	for _, n := range out.Issues.Nodes {
		b := BoardIssue{IssueSummary: IssueSummary{ID: n.ID, Identifier: n.Identifier, Title: n.Title, URL: n.URL,
			UpdatedAt: n.UpdatedAt}, Priority: n.Priority, Labels: []string{}}
		if n.State != nil {
			b.State, b.StateType, b.StateColor = n.State.Name, n.State.Type, n.State.Color
		}
		if n.Team != nil {
			b.Team = n.Team.Name
		}
		if n.Assignee != nil {
			b.Assignee, b.AssigneeID = n.Assignee.DisplayName, n.Assignee.ID
			b.Mine = myID != "" && n.Assignee.ID == myID
		}
		if n.Parent != nil {
			b.Parent = &ParentRef{ID: n.Parent.ID, Identifier: n.Parent.Identifier, Title: n.Parent.Title}
		}
		for _, c := range n.Children.Nodes {
			ch := ChildRef{ID: c.ID, Identifier: c.Identifier, Title: c.Title}
			if c.State != nil {
				ch.State, ch.StateType = c.State.Name, c.State.Type
			}
			b.Children = append(b.Children, ch)
		}
		for _, l := range n.Labels.Nodes {
			b.Labels = append(b.Labels, l.Name)
		}
		list = append(list, b)
	}
	return list, nil
}
