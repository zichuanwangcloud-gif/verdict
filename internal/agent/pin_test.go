package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"verdict/internal/protocol"
)

// gitRepo 建一个本地仓库，返回仓库路径与两次提交的 SHA。
func gitRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	git("add", ".")
	git("commit", "-m", "one")
	first = git("rev-parse", "HEAD")
	git("tag", "v1.0")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2\n"), 0o644)
	git("commit", "-am", "two")
	second = git("rev-parse", "HEAD")
	git("checkout", "-b", "release")
	git("checkout", "main")
	return dir, first, second
}

func TestResolve(t *testing.T) {
	dir, first, _ := gitRepo(t)
	log := func(string) {}
	for _, tc := range []struct{ ref, wantRef, wantSHA string }{
		{"main", "refs/heads/main", ""},
		{"v1.0", "refs/tags/v1.0", first},
		{"refs/tags/v1.0", "refs/tags/v1.0", first},
		{first, "", first},
	} {
		res := Resolve(context.Background(), protocol.Resolve{Type: protocol.TypeResolve, ReqID: "r", RepoURL: dir, Ref: tc.ref})
		if res.Error != "" {
			t.Fatalf("解析 %q 失败: %s", tc.ref, res.Error)
		}
		if res.Ref != tc.wantRef || len(res.SHA) != 40 {
			t.Fatalf("解析 %q 得到 ref=%q sha=%q", tc.ref, res.Ref, res.SHA)
		}
		if tc.wantSHA != "" && res.SHA != tc.wantSHA {
			t.Fatalf("解析 %q 应指向 %s，得到 %s", tc.ref, tc.wantSHA, res.SHA)
		}
	}
	if res := Resolve(context.Background(), protocol.Resolve{RepoURL: dir, Ref: "nope"}); res.Error == "" || !strings.Contains(res.Error, "nope") {
		t.Fatalf("不存在的引用应报错: %+v", res)
	}
	_ = log
}

func TestPreparePinnedCommit(t *testing.T) {
	dir, first, second := gitRepo(t)
	ws := Workspace{Root: t.TempDir()}
	log := func(string) {}

	// 锁定到旧 commit：即使分支和 tag 都指向别处，工作区也必须是那一个 commit。
	p, err := ws.Prepare(context.Background(), "r1", dir, Target{Ref: "refs/tags/v1.0", Commit: first}, log)
	if err != nil {
		t.Fatal(err)
	}
	if p.Commit != first {
		t.Fatalf("应锁定到 %s，得到 %s", first, p.Commit)
	}
	b, _ := os.ReadFile(filepath.Join(p.Dir, "a.txt"))
	if string(b) != "1\n" {
		t.Fatalf("工作区内容不是旧 commit 的: %q", b)
	}

	// 不锁定时测的是分支最新提交。
	p2, err := ws.Prepare(context.Background(), "r2", dir, Target{Branch: "main"}, log)
	if err != nil || p2.Commit != second {
		t.Fatalf("应测 main 最新提交 %s: %v %s", second, err, p2.Commit)
	}

	// 伪造的 SHA 要报错，不能悄悄测分支最新提交。
	if _, err := ws.Prepare(context.Background(), "r3", dir, Target{Branch: "main", Commit: strings.Repeat("a", 40)}, log); err == nil ||
		!strings.Contains(err.Error(), "冻结 commit") {
		t.Fatalf("不存在的 commit 应报错: %v", err)
	}
}
