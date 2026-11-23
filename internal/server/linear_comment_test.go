package server

import (
	"strings"
	"testing"

	"verdict/internal/linear"
	"verdict/internal/server/store"
)

func TestAppendSetSummary(t *testing.T) {
	sm := store.IssueSetSummary{
		Overall: store.SetPartialPass,
		Children: []store.SetChild{
			{IssueKey: "ENG-2", Title: "子一", Verdict: "pass"},
			{IssueKey: "ENG-3", Title: "子二", Verdict: "pass"},
		},
		Uncovered: []linear.ChildRef{{Identifier: "ENG-4", Title: "子三"}},
	}
	body := appendSetSummary("正文", sm)
	for _, want := range []string{
		"### 子单汇总", "部分通过", "覆盖 2/3 个子单",
		"| ENG-2 子一 | ✅ 通过 |", "未覆盖：ENG-4",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("缺少 %q:\n%s", want, body)
		}
	}
}

func TestAppendSetSummaryMissingChild(t *testing.T) {
	sm := store.IssueSetSummary{
		Overall:  store.SetIncomplete,
		Children: []store.SetChild{{IssueKey: "ENG-2", Title: "子一", Missing: true}},
	}
	body := appendSetSummary("正文", sm)
	if !strings.Contains(body, "未创建") || !strings.Contains(body, "未完成") {
		t.Fatalf("缺失子单应标未创建:\n%s", body)
	}
}
