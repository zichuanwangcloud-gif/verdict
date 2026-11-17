// Package agent 是运行在运行时上的常驻进程：主动连接后端、领取测试、在隔离工作区里启动 harness。
package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	// Path 是安装时 shell 的 PATH，后台服务据此找到 claude / codex / git。
	Path string `json:"path,omitempty"`
}

// DefaultDir 返回 agent 数据目录：$VERDICT_AGENT_DIR 或 ~/.verdict-agent。
func DefaultDir() string {
	if d := os.Getenv("VERDICT_AGENT_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".verdict-agent"
	}
	return filepath.Join(home, ".verdict-agent")
}

func configPath(dir string) string { return filepath.Join(dir, "config.json") }

func LoadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, errors.New("尚未初始化：请先执行 verdict agent init --server <url> --token <token>")
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Server == "" || c.Token == "" {
		return c, errors.New("配置缺少 server 或 token")
	}
	return c, nil
}

func SaveConfig(dir string, c Config) error {
	c.Server = strings.TrimRight(strings.TrimSpace(c.Server), "/")
	if c.Server == "" || c.Token == "" {
		return errors.New("server 和 token 都不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := configPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath(dir))
}

// wsURL 把 http(s):// 转换为 ws(s)://。
func wsURL(server string) string {
	switch {
	case strings.HasPrefix(server, "https://"):
		return "wss://" + strings.TrimPrefix(server, "https://") + "/api/agent/ws"
	case strings.HasPrefix(server, "http://"):
		return "ws://" + strings.TrimPrefix(server, "http://") + "/api/agent/ws"
	default:
		return server + "/api/agent/ws"
	}
}
