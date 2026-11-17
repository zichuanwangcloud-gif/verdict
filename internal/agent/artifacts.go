package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxArtifactBytes = 10 << 20
	maxArtifactsRun  = 200
)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// snapshot 记录产物目录里已有文件的修改时间，用于找出本阶段新增或更新的文件。
func snapshot(dir string) map[string]time.Time {
	out := map[string]time.Time{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			out[p] = info.ModTime()
		}
		return nil
	})
	return out
}

// artifactName 把相对路径转换成后端接受的文件名，例如 shots/home page.png → shots-home_page.png。
func artifactName(rel string) string {
	n := strings.ReplaceAll(filepath.ToSlash(rel), "/", "-")
	n = unsafeName.ReplaceAllString(n, "_")
	n = strings.TrimLeft(n, "._-")
	if len(n) > 120 {
		ext := filepath.Ext(n)
		n = n[:120-len(ext)] + ext
	}
	if n == "" {
		n = "artifact"
	}
	return n
}

// uploadNew 上传本阶段新增或更新的产物，返回成功上传的文件名。
func (a *Agent) uploadNew(ctx context.Context, s *session, stageID string, before map[string]time.Time, logf func(string)) []string {
	after := snapshot(s.artifacts)
	var paths []string
	for p, mt := range after {
		if old, ok := before[p]; !ok || !old.Equal(mt) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var names []string
	for _, p := range paths {
		if s.uploaded >= maxArtifactsRun {
			logf(fmt.Sprintf("⚠ 产物数量超过 %d 个，其余未上传", maxArtifactsRun))
			break
		}
		info, err := os.Stat(p)
		if err != nil || info.Size() == 0 {
			continue
		}
		if info.Size() > maxArtifactBytes {
			logf(fmt.Sprintf("⚠ 产物 %s 超过 10MB，未上传", filepath.Base(p)))
			continue
		}
		rel, _ := filepath.Rel(s.artifacts, p)
		name := artifactName(rel)
		if err := a.upload(ctx, s.runID, stageID, name, p); err != nil {
			logf(fmt.Sprintf("⚠ 上传产物 %s 失败: %v", name, err))
			continue
		}
		s.uploaded++
		names = append(names, name)
	}
	if len(names) > 0 {
		logf(fmt.Sprintf("📎 已上传 %d 个产物：%s", len(names), strings.Join(names, "、")))
	}
	return names
}

func (a *Agent) upload(ctx context.Context, runID, stageID, name, path string) error {
	u := fmt.Sprintf("%s/api/agent/runs/%s/artifacts?name=%s&stage=%s", a.cfg.Server, url.PathEscape(runID), url.QueryEscape(name), url.QueryEscape(stageID))
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*2) * time.Second):
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, f)
		if err != nil {
			f.Close()
			return err
		}
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := a.http.Do(req)
		f.Close()
		if err != nil {
			last = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		last = fmt.Errorf("HTTP %d", resp.StatusCode)
		if resp.StatusCode < 500 {
			return last // 4xx 重试也没用
		}
	}
	return last
}
