package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"verdict/internal/agent/harness"
	"verdict/internal/protocol"
)

// PlaywrightMCP 是 UI 走查使用的浏览器 MCP 版本。
const PlaywrightMCP = "@playwright/mcp@0.0.84"

// findBrowser 返回可用的浏览器可执行文件：VERDICT_BROWSER_PATH > Playwright 缓存里最新的 chromium > 系统浏览器。
// 显式指定可执行文件，避免 MCP 要求的浏览器版本与本机缓存不一致时临时下载。
func findBrowser() string {
	if p := os.Getenv("VERDICT_BROWSER_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	cache := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if cache == "" {
		cache = filepath.Join(home, ".cache", "ms-playwright")
	}
	for _, pattern := range []string{
		"chromium-*/chrome-linux*/chrome",
		"chromium-*/chrome-mac*/Chromium.app/Contents/MacOS/Chromium",
		"chromium-*/chrome-mac*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
	} {
		matches, _ := filepath.Glob(filepath.Join(cache, pattern))
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				return m
			}
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func probeBrowser() protocol.BrowserInfo {
	if _, err := exec.LookPath("npx"); err != nil {
		return protocol.BrowserInfo{Detail: "PATH 中找不到 npx（需要 Node.js）"}
	}
	b := findBrowser()
	if b == "" {
		return protocol.BrowserInfo{Detail: "未找到浏览器：请在运行时上执行 npx playwright install chromium"}
	}
	return protocol.BrowserInfo{Available: true, Detail: b}
}

// browserMCP 返回挂载给 harness 的浏览器 MCP 配置，截图等输出写到 outDir。
func browserMCP(outDir string) *harness.MCPServer {
	args := []string{"-y", PlaywrightMCP, "--headless", "--isolated", "--output-dir", outDir, "--viewport-size", "1280,800"}
	if b := findBrowser(); b != "" {
		args = append(args, "--executable-path", b)
	}
	return &harness.MCPServer{Name: "playwright", Command: "npx", Args: args}
}
