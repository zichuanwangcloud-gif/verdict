# Verdict

让编码 agent 当验收测试工程师：给一个 Linear issue（需求背景 + 验收标准）和一个仓库分支，Verdict 把测试派到你接入的运行时上，由那台机器上的 **Claude Code** 或 **Codex** 实际跑起来去验证，逐条给出「通过 / 未通过 / 无法验证」和证据，并把结果回写到 Linear。

```
 浏览器 ──HTTPS──▶ Verdict 后端（Docker 镜像，任意服务器） ◀──WSS── verdict agent（任意运行时）
                         │                                            │
                         └── Linear API                               └── git / claude / codex
```

- 后端和运行时可以在任意两台机器上，**只要求运行时能访问后端地址**（agent 主动连出，不需要开放运行时端口）。
- 每次测试都在锁定到具体 commit 的干净 git worktree 里进行；仓库缓存、依赖缓存长期复用。
- 最终结论由逐条结果汇总得出：有一条未通过即「未通过」，有无法验证的即「部分无法验证」。
- 测试过程中若被测源码被改动，报告会明确标出并附 diff。

设计细节见 [docs/mvp/design.md](docs/mvp/design.md)。

## 部署后端

```bash
docker build -t verdict .
docker run -d --name verdict -p 8791:8791 \
  -e VERDICT_ADMIN_TOKEN=<自己生成一个长随机串> \
  -e VERDICT_PUBLIC_URL=https://verdict.example.com \
  -v verdict-data:/data \
  verdict
```

| 环境变量 | 说明 |
|---|---|
| `VERDICT_ADMIN_TOKEN` | 页面登录令牌。不设置时每次启动随机生成并打印在日志里 |
| `VERDICT_PUBLIC_URL` | 对外地址，用于生成安装命令和 Linear 评论里的报告链接。也可在页面「设置」里改 |
| `VERDICT_LINEAR_API_KEY` | Linear Personal API Key。也可在页面「设置」里填 |
| `VERDICT_ADDR` / `VERDICT_DB` | 监听地址（镜像内 `:8791`）/ 数据库路径（镜像内 `/data/verdict.db`） |

### 反向代理

agent 通过 WebSocket 连接 `/api/agent/ws`。放在 nginx 后面时必须透传 Upgrade 头并调大超时，否则 agent 会反复断线：

```nginx
location / {
    proxy_pass http://127.0.0.1:8791;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;   # 需在 http{} 里定义 map
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 3600s;
}
```

⚠️ 后端能向所有运行时下发「以跳过权限确认方式运行 harness」的任务，等同于在这些机器上执行任意操作。务必使用 HTTPS、设置强管理员令牌，不要把运行时设在生产机器上。

## 接入运行时

1. 页面「运行时」→「添加运行时」，复制生成的安装命令。
2. 在目标机器上用**普通用户**（不要用 root）执行。该用户需要已经能运行并登录 `claude` 和/或 `codex`，并能 `git clone` 被测仓库。
3. agent 以 systemd 用户服务（没有 systemd 时用 nohup）常驻，页面上会显示在线状态和 harness 探测结果。

运行时上的目录（默认 `~/.verdict-agent`）：

```
config.json        后端地址、令牌、安装时的 PATH
repos/<key>.git    仓库 bare 缓存（长期保留）
runs/<run_id>/     每次测试的 worktree（成功即删，失败保留 24 小时）
env/<key>/         可选：该仓库的私密文件（如 .env），每次测试复制到代码根目录，不经过后端
journal/           事件日志，断线重连后补发
```

`<key>` 在页面「仓库」里每个仓库下方有显示，例如 `github.com_org_repo`。

## 使用

1. 「设置」里填 Linear API Key。
2. 「仓库」里添加仓库，并尽量写好**测试说明**：怎么装依赖、怎么启动服务、依赖哪些外部服务、测试账号等。这些会交给模型，对成功率影响最大。
3. 「测试」→「新建测试」：填 issue 编号、选仓库 / 分支 / 运行时 / harness。也可以不用 Linear，直接手动输入需求（不会回写）。
4. 报告页实时显示执行日志；结束后自动在 issue 下发评论。

同一个运行时一次只跑一个测试，其余排队。

## 开发

```bash
# 后端（默认 127.0.0.1:8791）
go run ./cmd/verdict server

# 前端（127.0.0.1:5191，/api 代理到后端）
cd web && pnpm install && pnpm dev

# 本机当运行时
go build -o dist/verdict-linux-amd64 ./cmd/verdict     # 供 /download 使用
go run ./cmd/verdict agent init --server http://127.0.0.1:8791 --token <令牌>
go run ./cmd/verdict agent run

# 测试（含真实 server + agent + git 的端到端用例，harness 为假实现）
go test -race ./...
```

| 目录 | 职责 |
|---|---|
| `cmd/verdict` | 唯一入口：`server` / `agent init|run|probe` |
| `internal/protocol` | 后端与 agent 之间的消息、测试报告结构、结论汇总 |
| `internal/server` | REST API、agent WebSocket hub、调度、Linear 回写、安装脚本 |
| `internal/server/store` | SQLite 持久层 |
| `internal/testplan` | 提示词与结果 Schema——**优化「怎么让模型测」主要改这里** |
| `internal/linear` | Linear GraphQL 最小客户端 |
| `internal/agent` | 运行时常驻进程：连接、事件日志、工作区 |
| `internal/agent/harness` | Claude Code / Codex 适配器 |
| `web` | Vue 3 前端 |

旧版「发布验证台账」实现保留在 tag `v0-ledger`。
