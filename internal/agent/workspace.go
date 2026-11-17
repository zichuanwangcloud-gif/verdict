package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxDiff = 64 << 10

// Workspace 管理 repos/（bare 缓存，长期保留）、runs/（每次测试一个 worktree）、env/（私密文件）。
type Workspace struct {
	Root string
}

func (w Workspace) reposDir() string { return filepath.Join(w.Root, "repos") }
func (w Workspace) runsDir() string  { return filepath.Join(w.Root, "runs") }
func (w Workspace) envDir() string   { return filepath.Join(w.Root, "env") }
func (w Workspace) TempDir(runID string) string {
	return filepath.Join(w.Root, "tmp", runID)
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// RepoKey 把仓库地址转换成目录名，例如 git@github.com:org/repo.git → github.com_org_repo。
func RepoKey(url string) string {
	u := url
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.LastIndex(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	u = unsafeChars.ReplaceAllString(u, "_")
	u = strings.Trim(u, "._-")
	if u == "" {
		u = "repo"
	}
	return u
}

func gitEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")
	}
	return env
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		return out, fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

// Prepared 是一份可供测试的代码副本。
type Prepared struct {
	Dir      string
	Commit   string
	bare     string
	baseline []string // 复制 env 文件后就已变化的已跟踪文件，检查改动时排除
}

// Prepare 更新 bare 缓存、把分支解析成 commit，并为本次测试建立独立 worktree。
func (w Workspace) Prepare(ctx context.Context, runID, repoURL, branch string, log func(string)) (*Prepared, error) {
	for _, d := range []string{w.reposDir(), w.runsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	key := RepoKey(repoURL)
	bare := filepath.Join(w.reposDir(), key+".git")
	if _, err := os.Stat(bare); err != nil {
		log("克隆仓库缓存 " + repoURL)
		tmp := bare + ".partial"
		_ = os.RemoveAll(tmp)
		if _, err := git(ctx, w.reposDir(), "clone", "--bare", "--", repoURL, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		if err := os.Rename(tmp, bare); err != nil {
			return nil, err
		}
	} else {
		// 仓库地址可能被修改过，以本次为准。
		_, _ = git(ctx, bare, "remote", "set-url", "origin", repoURL)
	}

	log("拉取分支 " + branch)
	ref := "refs/heads/" + branch
	if _, err := git(ctx, bare, "fetch", "--force", "origin", "+"+ref+":"+ref); err != nil {
		return nil, fmt.Errorf("拉取分支 %s 失败（检查分支是否存在、运行时是否有仓库访问权限）: %w", branch, err)
	}
	sha, err := git(ctx, bare, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, err
	}
	log("被测 commit " + sha)

	dir := filepath.Join(w.runsDir(), runID)
	_ = os.RemoveAll(dir)
	_, _ = git(ctx, bare, "worktree", "prune")
	if _, err := git(ctx, bare, "worktree", "add", "--detach", dir, sha); err != nil {
		return nil, err
	}
	p := &Prepared{Dir: dir, Commit: sha, bare: bare}

	envSrc := filepath.Join(w.envDir(), key)
	if st, err := os.Stat(envSrc); err == nil && st.IsDir() {
		n, err := copyTree(envSrc, dir)
		if err != nil {
			return p, fmt.Errorf("复制 env 文件失败: %w", err)
		}
		log(fmt.Sprintf("已从 %s 复制 %d 个私密文件", envSrc, n))
		if out, err := git(ctx, dir, "diff", "--name-only", sha); err == nil && out != "" {
			p.baseline = strings.Split(out, "\n")
		}
	}
	return p, nil
}

// Changes 返回测试期间对已跟踪源码的改动（含模型可能做的提交），已截断。
func (p *Prepared) Changes(ctx context.Context) (bool, string) {
	args := []string{"diff", p.Commit, "--", "."}
	for _, b := range p.baseline {
		args = append(args, ":(exclude)"+b)
	}
	out, err := git(ctx, p.Dir, args...)
	if err != nil || strings.TrimSpace(out) == "" {
		return false, ""
	}
	if len(out) > maxDiff {
		out = out[:maxDiff] + "\n…(diff 已截断)"
	}
	return true, out
}

// Remove 删除 worktree。
func (p *Prepared) Remove(ctx context.Context) {
	if _, err := git(ctx, p.bare, "worktree", "remove", "--force", p.Dir); err != nil {
		_ = os.RemoveAll(p.Dir)
		_, _ = git(ctx, p.bare, "worktree", "prune")
	}
}

// GC 清理超过 keep 的 worktree 与临时目录。
func (w Workspace) GC(ctx context.Context, keep time.Duration, active string) {
	for _, d := range []string{w.runsDir(), filepath.Join(w.Root, "tmp")} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if e.Name() == active {
				continue
			}
			info, err := e.Info()
			if err != nil || time.Since(info.ModTime()) < keep {
				continue
			}
			_ = os.RemoveAll(filepath.Join(d, e.Name()))
		}
	}
	bares, _ := filepath.Glob(filepath.Join(w.reposDir(), "*.git"))
	for _, b := range bares {
		_, _ = git(ctx, b, "worktree", "prune")
	}
}

func copyTree(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		info, _ := d.Info()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		n++
		return out.Close()
	})
	return n, err
}

// composeDown 清理本次测试约定名下的 docker compose 项目。
func composeDown(ctx context.Context, runID string, log func(string)) {
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	project := "verdict-" + runID
	cmd := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+project)
	out, err := cmd.Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return
	}
	log("清理 docker compose 项目 " + project)
	_ = exec.CommandContext(ctx, "docker", "compose", "-p", project, "down", "-v", "--remove-orphans").Run()
}
