package server

// installSh 由运行时执行：curl -fsSL <server>/install.sh | sh -s -- --server <url> --token <token>
const installSh = `#!/bin/sh
# Verdict agent 安装脚本
set -eu

SERVER=""
TOKEN=""
DIR="${VERDICT_AGENT_DIR:-$HOME/.verdict-agent}"
while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER="$2"; shift 2 ;;
    --token)  TOKEN="$2"; shift 2 ;;
    --dir)    DIR="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done
[ -n "$SERVER" ] && [ -n "$TOKEN" ] || { echo "用法: install.sh --server <url> --token <token>" >&2; exit 2; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "不支持的架构: $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$DIR/bin"
echo "==> 下载 agent ($os/$arch)"
curl -fsSL "$SERVER/download/verdict-$os-$arch" -o "$DIR/bin/verdict.tmp"
chmod +x "$DIR/bin/verdict.tmp"
mv "$DIR/bin/verdict.tmp" "$DIR/bin/verdict"

echo "==> 写入配置"
# 记录当前 shell 的 PATH，后台服务据此找到 claude / codex / git。
"$DIR/bin/verdict" agent init --dir "$DIR" --server "$SERVER" --token "$TOKEN" --path "$PATH"

echo "==> 探测 harness"
"$DIR/bin/verdict" agent probe --dir "$DIR" || true

if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  unit="$HOME/.config/systemd/user/verdict-agent.service"
  mkdir -p "$(dirname "$unit")"
  cat > "$unit" <<EOF
[Unit]
Description=Verdict agent
After=network-online.target

[Service]
ExecStart=$DIR/bin/verdict agent run --dir $DIR
Restart=always
RestartSec=5
# 只结束 agent 主进程，测试进程组由 agent 自己清理
KillMode=mixed

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable verdict-agent.service >/dev/null 2>&1
  systemctl --user restart verdict-agent.service
  echo "==> 已作为 systemd 用户服务启动：systemctl --user status verdict-agent"
  echo "    日志：journalctl --user -u verdict-agent -f"
  if [ ! -e "/var/lib/systemd/linger/$(id -un)" ]; then
    echo "    提示：当前用户注销后服务会停止。需要常驻请执行：sudo loginctl enable-linger $(id -un)"
  fi
else
  if [ -f "$DIR/agent.pid" ] && kill -0 "$(cat "$DIR/agent.pid")" 2>/dev/null; then
    kill "$(cat "$DIR/agent.pid")" || true
    sleep 1
  fi
  nohup "$DIR/bin/verdict" agent run --dir "$DIR" >> "$DIR/agent.log" 2>&1 &
  echo $! > "$DIR/agent.pid"
  echo "==> 已在后台启动（pid $(cat "$DIR/agent.pid")），日志：$DIR/agent.log"
  echo "    注意：机器重启后需重新执行本命令，或自行配置开机启动。"
fi
`
