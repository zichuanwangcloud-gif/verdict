# Verdict

让编码 agent 当验收测试工程师：给一个 Linear issue（需求背景 + 验收标准）和一个仓库分支，Verdict 把测试派到你接入的运行时上，由那台机器上的 **Claude Code** 或 **Codex** 实际跑起来去验证，逐条给出「通过 / 未通过 / 无法验证」和证据，并把结果回写到 Linear。

```
 浏览器 ──HTTPS──▶ Verdict 后端（Docker 镜像，任意服务器） ◀──WSS── verdict agent（任意运行时）
                         │                                            │
                         └── Linear API                               └── git / claude / codex
```

- 后端和运行时可以在任意两台机器上，**只要求运行时能访问后端地址**（agent 主动连出，不需要开放运行时端口）。
- 每次测试都在锁定到具体 commit 的干净 git worktree 里进行；仓库缓存、依赖缓存长期复用。
- 测试按**档位**编排成多个阶段，每个阶段是一次独立的 harness 会话，平台负责在阶段之间传递计划、证据和质疑：

| 档位 | 流程 | 时间预算 |
|---|---|---|
| **S 对抗** | 计划 → 托管服务 → 执行验证（关键检查跑两遍）→ 全量 UI 走查 → 红蓝对抗最多 3 轮（红队优先用另一种模型）→ 主动探索 → 裁决 | 180 分钟 |
| **A 深度** | 计划 → 托管服务 → 执行验证 → 定向 UI 走查 → 1 轮红队交叉复核 → 裁决 | 90 分钟 |
| **B 标准** | 计划 → 托管服务 → 执行验证（输出前自查） | 45 分钟 |
| **C 快速** | 简版计划 → 只用命令行、单测和读代码验证 | 20 分钟 |

  也可以选「自定义」组合开关；任何档位都可以打开「计划生成后由我确认」。预算不够时跳过可选阶段，并在报告里注明。
- 最终结论由逐条结果汇总得出：有一条未通过即「未通过」，有无法验证或存疑的即「部分无法验证」。没有证据的「通过」会被平台降为「无法验证」。验收标准以外的发现单独列为「额外发现」，不改变结论。
- 测试过程中若被测源码被改动，报告会明确标出并附 diff。

## 版本与回归

- **版本**：在「版本」页按仓库自己建版本，新建测试时把 issue 归进去，也可以直接在版本页添加。
- **冻结回归**：版本冻结时填分支 / tag / commit，平台让运行时解析成 commit，在那份代码上把本版本的 issue 和积累的回归用例整体重测。「分支上通过、冻结节点上未通过」会标为**集成回归**，排在矩阵最前。
- **回归基线**：版本发布时，回归通过的 issue 自动沉淀为长期用例（可手动加入、设为核心）。以后每轮回归默认带上核心用例，「上一轮通过、这一轮未通过」标为**历史回归**。用例未通过时可以一键处理：确认是缺陷、计划过时（用本次计划更新）、需求已变更（停用）。
- 回归复用之前测试的计划，不重新规划；子测试由空闲运行时自动分摊；结论不会回写 Linear 刷屏。

## 并发

一台运行时可以同时跑多个测试：

- 在「运行时」页设置并发度（1–32；留空按机器自动：CPU 核数的一半、每 8GB 内存一个，上限 8）。默认值随机器给，随时可以改。
- 仓库配置里声明「需要的端口数」，启动命令 / 健康检查 / 前端地址用 `${VERDICT_PORT}`（第 2、3 个用 `${VERDICT_PORT2}`、`${VERDICT_PORT3}`）占位，agent 会给每个测试分配独立端口。写死 3000 这类端口号在并发时会互相冲突。
- 同一仓库的多个测试可以并发；工作区、托管服务、compose 项目、产物全部按测试隔离，仓库缓存上的 git 操作在 agent 内串行。
- 回归子测试也会并发执行，普通测试优先派单。

> 协议升到 v4，升级后端后每台运行时都要重新执行一次安装命令。

设计细节见 [docs/versions/design.md](docs/versions/design.md)（版本与回归）、[docs/mvp/design.md](docs/mvp/design.md)（整体架构）和 [docs/tiers/design.md](docs/tiers/design.md)（分档测试）。

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
| `VERDICT_ADDR` / `VERDICT_DB` | 监听地址（镜像内 `:8791`）/ 数据库路径（镜像内 `/data/verdict.db`）；截图等产物存在数据库同目录的 `artifacts/` 下 |

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
2. 在目标机器上用**普通用户**（不要用 root）执行。该用户需要已经能运行并登录 `claude` 和/或 `codex`，并能 `git clone` 被测仓库。A/S 档的 UI 走查还需要 Node.js 和浏览器（`npx playwright install chromium`；也可以用 `VERDICT_BROWSER_PATH` 指定已有的 Chrome/Chromium）。
3. agent 以 systemd 用户服务（没有 systemd 时用 nohup）常驻，页面上会显示在线状态、harness 与浏览器的探测结果。
4. **升级**：后端升级后，旧版 agent 会被标记为「版本过旧」，不再接收测试。在运行时卡片上点「生成升级命令」，到那台机器上重新执行即可，缓存会保留。

运行时上的目录（默认 `~/.verdict-agent`）：

```
config.json        后端地址、令牌、安装时的 PATH
repos/<key>.git    仓库 bare 缓存（长期保留）
runs/<run_id>/     每次测试的 worktree（成功即删，失败保留 24 小时）
tmp/<run_id>/      托管服务日志、截图等产物（测试结束后上传并删除）
env/<key>/         可选：该仓库的私密文件（如 .env），每次测试复制到代码根目录，不经过后端
journal/           事件日志，断线重连后补发
```

`<key>` 在页面「仓库」里每个仓库下方有显示，例如 `github.com_org_repo`。

## 使用

1. 「设置」里填 Linear API Key。
2. 「仓库」里添加仓库，填写**运行方式**（安装命令、前台启动命令、健康检查地址、前端入口地址）和**测试说明**（依赖的外部服务、测试账号等）。运行方式留空时由计划阶段的 agent 推断；写清楚了，托管启动和 UI 走查会稳定得多。
3. 「测试」→「新建测试」：填 issue 编号，选仓库 / 分支 / 档位 / 运行时 / harness。也可以不用 Linear，直接手动输入需求（不会回写）。
4. 报告页右侧显示阶段流程（点击可筛选该阶段的日志），主栏实时更新当前结论、红蓝对抗记录、额外发现和 UI 走查截图；结束后自动在 issue 下发评论。

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

# 测试（e2e 用真实的 server + agent + git + 托管服务，harness 为假实现）
go test -race ./...
```

| 目录 | 职责 |
|---|---|
| `cmd/verdict` | 唯一入口：`server` / `agent init|run|probe` |
| `internal/protocol` | 后端与 agent 之间的消息、测试报告结构、结论汇总 |
| `internal/server` | REST API、agent WebSocket hub、调度、Linear 回写、安装脚本 |
| `internal/server/store` | SQLite 持久层 |
| `internal/testplan` | 档位预设、流水线 `Next()`、各角色提示词与 Schema、结论汇总 `Consolidate()`——**优化「怎么让模型测」主要改这里** |
| `internal/linear` | Linear GraphQL 最小客户端 |
| `internal/agent` | 运行时常驻进程：连接、事件日志、工作区、托管服务、产物上传 |
| `internal/agent/harness` | Claude Code / Codex 适配器 |
| `web` | Vue 3 前端 |

旧版「发布验证台账」实现保留在 tag `v0-ledger`。
