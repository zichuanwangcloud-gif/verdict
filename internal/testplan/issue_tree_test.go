package testplan

import (
	"fmt"
	"strings"
	"testing"

	"verdict/internal/linear"
)

func parentInput() Input {
	return Input{
		RunID: "r1",
		Issue: linear.Issue{
			Identifier: "ENG-1", Title: "父单", Description: "汇总需求",
			Children: []linear.ChildRef{
				{Identifier: "ENG-2", Title: "子一", State: "Todo", Description: "子一的细节"},
				{Identifier: "ENG-3", Title: "子二", State: "Done"},
			},
		},
		Scope: []string{"ENG-2"},
	}
}

func TestPromptParentIncludesSelectedChildren(t *testing.T) {
	p := buildPrompt(RolePlan, 0, Config{}, parentInput(), nil)
	if !strings.Contains(p, "## 子单（这是汇总单，子单已拆分实现）") {
		t.Fatalf("父单 prompt 缺少子单清单:\n%s", p)
	}
	if !strings.Contains(p, "ENG-2 子一（Todo）") || !strings.Contains(p, "子一的细节") {
		t.Fatalf("选中子单的标题/状态/描述缺失:\n%s", p)
	}
	if !strings.Contains(p, "本次未覆盖的子单（不要验证）：ENG-3 子二") {
		t.Fatalf("未覆盖子单缺失:\n%s", p)
	}
	if !strings.Contains(p, "不要重复验证子单各自的细节") {
		t.Fatalf("缺少去重规则:\n%s", p)
	}
	if !strings.Contains(p, "只保留跨子单的组合条件") {
		t.Fatalf("计划阶段缺少集成规则:\n%s", p)
	}
}

func TestPromptChildShowsParent(t *testing.T) {
	in := Input{
		RunID: "r2",
		Issue: linear.Issue{
			Identifier: "ENG-2", Title: "子一", Description: "细节",
			Parent: &linear.ParentRef{Identifier: "ENG-1", Title: "父单"},
		},
	}
	p := buildPrompt(RolePlan, 0, Config{}, in, nil)
	if !strings.Contains(p, "## 所属父单") || !strings.Contains(p, "ENG-1 父单") {
		t.Fatalf("子单 prompt 缺少父单上下文:\n%s", p)
	}
	if strings.Contains(p, "## 子单（这是汇总单") {
		t.Fatalf("子单不应被当成父单:\n%s", p)
	}
}

func TestPromptWithoutScopeHasNoTree(t *testing.T) {
	in := Input{
		RunID: "r3",
		Issue: linear.Issue{Identifier: "ENG-1", Title: "父单", Description: "需求",
			Children: []linear.ChildRef{{Identifier: "ENG-2", Title: "子一"}}},
	}
	p := buildPrompt(RolePlan, 0, Config{}, in, nil)
	if strings.Contains(p, "## 子单") {
		t.Fatalf("没选子单时不应注入父单集成约束:\n%s", p)
	}
}

// 子单超过 8 个时，描述压缩到 200 字，避免 prompt 过长。
func TestPromptClipsChildDescriptionWhenMany(t *testing.T) {
	long := strings.Repeat("细", 300)
	in := parentInput()
	var children []linear.ChildRef
	for i := 0; i < 9; i++ {
		children = append(children, linear.ChildRef{
			Identifier: fmt.Sprintf("ENG-%d", i+2), Title: fmt.Sprintf("子%d", i+1), Description: long,
		})
	}
	in.Issue.Children = children
	in.Scope = []string{"ENG-2"}
	p := buildPrompt(RolePlan, 0, Config{}, in, nil)
	if strings.Contains(p, long) {
		t.Fatal("子单过多时描述应被压缩")
	}
	if !strings.Contains(p, strings.Repeat("细", 200)+"…") {
		t.Fatal("应保留 200 字并加省略号")
	}
}
