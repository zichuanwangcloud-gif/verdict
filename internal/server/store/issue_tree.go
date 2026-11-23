package store

import (
	"context"
	"database/sql"
	"errors"

	"verdict/internal/linear"
	"verdict/internal/protocol"
)

// 父单整体结论。只用于聚合视图，不覆盖父 run 自己的 verdict。
const (
	SetPass        = "pass"         // 父单通过，全部子单覆盖且通过
	SetPartialPass = "partial_pass" // 父单通过，选中的子单通过，但有子单未覆盖
	SetPartialFail = "partial_fail" // 父单通过，但有选中的子单未通过
	SetFail        = "fail"         // 父单自身未通过
	SetPartial     = "partial"      // 没有未通过，但存在无法验证 / 存疑
	SetIncomplete  = "incomplete"   // 有 run 未结束、出错、取消，或子单测试没建出来
)

// SetChild 是批次里的一个子单。
type SetChild struct {
	IssueKey string `json:"issue_key"`
	Title    string `json:"title"`
	RunID    string `json:"run_id,omitempty"`
	Status   string `json:"status,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Covered  bool   `json:"covered"`
	// Missing 表示选中了，但没有对应的测试记录（创建失败或还没建）。
	Missing bool `json:"missing,omitempty"`
}

// IssueSetSummary 是一次父子测试批次的聚合视图。
type IssueSetSummary struct {
	SetID     string            `json:"set_id"`
	ParentKey string            `json:"parent_issue_key"`
	Parent    SetChild          `json:"parent"`
	Overall   string            `json:"overall"`
	Scope     []string          `json:"scope"`
	Children  []SetChild        `json:"children"`  // 本次覆盖的子单
	Uncovered []linear.ChildRef `json:"uncovered"` // 父单下没选的子单
}

// SetParent 返回一个批次里的父 run（issue_parent_key 为空的那条）。
func (s *Store) SetParent(ctx context.Context, setID string) (Run, error) {
	if setID == "" {
		return Run{}, ErrNotFound
	}
	r, err := scanRun(s.db.QueryRowContext(ctx,
		`SELECT `+runCols+` WHERE r.set_id=? AND r.issue_parent_key='' ORDER BY r.created_at, r.rowid LIMIT 1`, setID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SetSummary 汇总一次父子测试批次：父 run 自身、选中的子 run、未覆盖的子单。
func (s *Store) SetSummary(ctx context.Context, parentRunID string) (IssueSetSummary, error) {
	parent, err := s.Run(ctx, parentRunID)
	if err != nil {
		return IssueSetSummary{}, err
	}
	out := IssueSetSummary{
		SetID:     parent.SetID,
		ParentKey: parent.IssueKey,
		Parent: SetChild{
			IssueKey: parent.IssueKey, Title: parent.Issue.Title,
			RunID: parent.ID, Status: parent.Status, Verdict: parent.Verdict, Covered: true,
		},
		Scope: append([]string{}, parent.Scope...),
	}
	inScope := make(map[string]bool, len(parent.Scope))
	for _, k := range parent.Scope {
		inScope[k] = true
	}

	runsByKey := map[string]Run{}
	if parent.SetID != "" {
		rows, err := s.db.QueryContext(ctx,
			`SELECT `+runCols+` WHERE r.set_id=? AND r.id<>? ORDER BY r.created_at DESC, r.rowid DESC`, parent.SetID, parent.ID)
		if err != nil {
			return IssueSetSummary{}, err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return IssueSetSummary{}, err
			}
			// 同一子单可能有重试记录，取最新一条。
			if _, seen := runsByKey[r.IssueKey]; !seen {
				runsByKey[r.IssueKey] = r
			}
		}
		if err := rows.Err(); err != nil {
			return IssueSetSummary{}, err
		}
	}

	seen := map[string]bool{}
	for _, c := range parent.Issue.Children {
		seen[c.Identifier] = true
		if !inScope[c.Identifier] {
			out.Uncovered = append(out.Uncovered, c)
			continue
		}
		out.Children = append(out.Children, setChildOf(c.Identifier, c.Title, runsByKey))
	}
	// Linear 的拆分关系可能在测试之后变过：scope 里有、快照里没有的子单也要算进来。
	for _, k := range parent.Scope {
		if seen[k] {
			continue
		}
		out.Children = append(out.Children, setChildOf(k, "", runsByKey))
	}

	out.Overall = setOverall(parent.Status, parent.Verdict, out.Children, len(out.Uncovered))
	return out, nil
}

func setChildOf(key, title string, runs map[string]Run) SetChild {
	sc := SetChild{IssueKey: key, Title: title, Covered: true}
	if r, ok := runs[key]; ok {
		sc.RunID, sc.Status, sc.Verdict = r.ID, r.Status, r.Verdict
	} else {
		sc.Missing = true
	}
	return sc
}

// setOverall 按设计 §6.2 的状态机从高到低匹配。
func setOverall(parentStatus, parentVerdict string, covered []SetChild, uncovered int) string {
	if parentStatus != StatusCompleted || parentVerdict == "" {
		return SetIncomplete
	}
	switch parentVerdict {
	case protocol.VerdictFail:
		return SetFail
	case protocol.VerdictPartial:
		return SetPartial
	}
	for _, c := range covered {
		if c.Missing || c.Status != StatusCompleted || c.Verdict == "" {
			return SetIncomplete
		}
	}
	for _, c := range covered {
		if c.Verdict == protocol.VerdictFail {
			return SetPartialFail
		}
	}
	for _, c := range covered {
		if c.Verdict == protocol.VerdictPartial {
			return SetPartial
		}
	}
	if uncovered > 0 {
		return SetPartialPass
	}
	return SetPass
}
