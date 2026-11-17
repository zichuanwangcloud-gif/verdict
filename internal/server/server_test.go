package server

import (
	"strings"
	"testing"

	"verdict/internal/protocol"
	"verdict/internal/server/store"
)

func TestCommentBody(t *testing.T) {
	r := store.Run{ID: "abc", Status: store.StatusCompleted, Verdict: protocol.VerdictFail, RepoName: "org/repo",
		Branch: "feat", CommitSHA: "0123456789abcdef", Harness: "claude", RuntimeName: "rt", SourceModified: true,
		ACs: []protocol.ACResult{{ID: "AC-1", Criterion: "a|b", Status: "fail", Evidence: "line1\nline2"}}}
	b := CommentBody(r, "https://v")
	for _, want := range []string{"❌ 未通过", "`0123456789ab`", `a\|b`, "line1<br>line2", "修改了被测源码", "https://v/runs/abc"} {
		if !strings.Contains(b, want) {
			t.Errorf("评论缺少 %q:\n%s", want, b)
		}
	}
	r.Status, r.Error = store.StatusError, "拉取失败"
	if !strings.Contains(CommentBody(r, ""), "测试未完成：拉取失败") {
		t.Error("出错时应说明原因")
	}
}

func TestRepoNameAndBranch(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:org/repo.git":      "org/repo",
		"https://github.com/org/repo.git/": "org/repo",
		"https://gitlab.com/a/b/c":         "b/c",
	} {
		if got := repoName(in); got != want {
			t.Errorf("repoName(%q)=%q want %q", in, got, want)
		}
	}
	for b, ok := range map[string]bool{"feature/x": true, "-rf": false, "a..b": false, "a b": false, "": false, "v1.2": true} {
		if validBranch(b) != ok {
			t.Errorf("validBranch(%q) 应为 %v", b, ok)
		}
	}
}
