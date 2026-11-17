# Verdict MVP 设计：让 harness 按验收标准测分支

## 目标

给定一个 Linear issue（需求背景 + 验收标准）、一个仓库和一个分支，由某台运行时上的 harness（Claude Code / Codex）完成功能测试，逐条给出验收结论和证据，把报告展示在页面上，并回写到 Linear。

不在 MVP 范围内：多用户和权限、SSH 接入、同一运行时并发执行多个测试、修改 issue 状态、旧版的区域/变更集台账（已打 tag `v0-ledger` 留档）。

## 部署形态

```
 浏览器(任意) ──HTTPS──▶ Verdict 后端(Docker 镜像，任意服务器) ◀──WSS── verdict agent(任意运行时)
                               │                                         │
                               └── Linear GraphQL API                    └── claude / codex / git
```

- 一个二进制、两个子命令：`verdict server` 和 `verdict agent`，CGO 关闭，纯静态编译。
- 镜像里同时带 linux/amd64 和 linux/arm64 的二进制，供运行时下载（`/download/verdict-linux-<arch>`）。
- 唯一的网络要求是运行时能访问后端的对外地址，也就是浏览器打开的那个地址。

## 认证

- **页面和 API**：使用管理员令牌 `VERDICT_ADMIN_TOKEN`，通过 `Authorization: Bearer` 携带。未设置时，启动时随机生成一个并打印到日志。
- **agent**：每个运行时一个令牌，只在创建时明文显示一次，库里只存 sha256。令牌只能用来建立 agent 的 WebSocket 连接。
- agent 只执行「运行测试」这一种任务，不提供执行任意命令的接口。

## 数据模型（SQLite）

| 表 | 关键字段 |
|---|---|
| `settings` | `linear_api_key`、`public_url`（未设置时回退到环境变量 `VERDICT_LINEAR_API_KEY` / `VERDICT_PUBLIC_URL`） |
| `runtimes` | `name`、`token_hash`、`info`（agent 上报的主机信息和 harness 探测结果，JSON）、`last_seen` |
| `repos` | `url`、`name`、`test_notes`（这个仓库的「测试说明」，会拼进提示词） |
| `runs` | `repo_id`、`branch`、`commit_sha`、`issue_key`、`issue`（创建时的 issue 快照）、`runtime_id`、`harness`、`status`、`verdict`、`summary`、`acs`（JSON）、`prompt`、`source_modified`、`diff`、`cost_usd`、`error`、`linear_comment_url`、`linear_error`、时间戳 |
| `run_logs` | `(run_id, seq)` 为主键，加上 `ts`、`text` |

### 测试状态

`status`：`queued`（排队）→ `dispatched`（已下发）→ `preparing`（拉代码、建工作区）→ `running`（harness 执行中）→ 以下终态之一：
- `completed`：正常完成；
- `error`：基础设施出错，比如拉代码失败、harness 崩溃、超时；
- `canceled`：被取消。

`verdict` 只在 `completed` 时有值，由每条验收标准的结论汇总得出，不由模型直接给出：
- 有任何一条 `fail`，则为 `fail`；
- 否则只要有一条 `unverifiable`，则为 `partial`；
- 全部 `pass` 才为 `pass`；
- 一条验收标准都没有时，也是 `partial`。

## WebSocket 协议

连接地址是 `GET /api/agent/ws`，带 `Authorization: Bearer <runtime token>`。每条消息是一个 JSON 对象 `{"type": ..., ...}`，心跳用 WebSocket 自带的 ping，每 30 秒一次。

agent 发给后端：

| type | 内容 |
|---|---|
| `hello` | 版本、主机名、系统/架构、git 版本、harness 探测结果、本地日志里有记录的测试 ID |
| `event` | `{run_id, seq, kind, ...}`，其中 kind 取 `accepted` / `phase` / `log` / `result`。**所有与测试相关的上报都走这一种消息**，按 seq 去重 |
| `probe` | 重新探测 harness 后上报的结果 |

后端发给 agent：

| type | 内容 |
|---|---|
| `start` | `{run_id, repo_url, branch, harness, prompt, schema, timeout_minutes}` |
| `cancel` | `{run_id}` |
| `sync` | `{runs: [{run_id, last_seq}]}`：后端认为还没结束的测试，以及它已经收到的最大 seq |
| `probe` | 要求 agent 重新探测 harness |

### 断线和重启

- agent 把每个 `event` 先追加写入本地日志 `journal/<run_id>.jsonl`，再发送；后端收到后按 `(run_id, seq)` 去重落库。
- 重连后，agent 先发 `hello`，后端回 `sync`，agent 把每个测试中 seq 大于 `last_seq` 的事件补发一遍。
- agent 进程启动时会检查本地日志：没有 `result` 的测试，说明进程被意外中断，补写一条 `error` 结果「agent 重启，测试中断」。
- 后端收到 `hello` 后，处理它认为还在执行、但 agent 本地日志里没有的测试：
  - `dispatched` 状态（还没被 agent 接收）的放回 `queued` 重新排队；
  - 已经开始执行的标记为 `error`。
- 调度规则：运行时在线，并且没有 `dispatched`、`preparing`、`running` 状态的测试时，按创建时间取最早的一条 `queued` 测试下发。

## 运行时上的项目处理

```
~/.verdict-agent/
  config.json                # 后端地址、令牌、安装时记录的 PATH
  repos/<key>.git            # bare 仓库缓存，长期保留
  runs/<run_id>/             # 每次测试一个 git worktree
  env/<key>/                 # 可选：该仓库的私密文件，每次测试时复制到工作区根目录，不经过后端
  journal/<run_id>.jsonl     # 事件日志，用于断线后补发
```

`<key>` 是由仓库地址转换出的文件名，例如 `github.com_org_repo`。

每次测试的步骤：
1. 更新缓存：没有缓存时执行 `git clone --bare`，否则执行 `git fetch`。设置 `GIT_TERMINAL_PROMPT=0`，认证失败时直接报错，不会卡在等待输入。
2. 把分支解析成 commit SHA 并上报，这次测试的报告就绑定在这个 SHA 上。
3. 执行 `git worktree add --detach runs/<id> <sha>`，再把 `env/<key>/` 下的文件复制进去。
4. 在独立进程组中启动 harness。超时或取消时，结束整个进程组。
5. 收尾：
   - 结束进程组；
   - 执行 `docker compose -p verdict-<id> down -v`（docker 不存在则跳过）；
   - 用 `git diff HEAD` 检查已跟踪的源码有没有被改动。有改动时，标记 `source_modified` 并附上 diff（截断到 64KB）。
6. 测试成功则删除 worktree；失败的保留 24 小时供人工排查，由 agent 定期清理。

## harness 适配器（agent 侧，可替换）

```go
type Harness interface {
    Name() string
    Detect(ctx) protocol.HarnessInfo            // 是否安装、版本、是否已登录
    Run(ctx, dir, prompt, schema, emit) (Output, error)
}
```

- **Claude Code**：`claude -p --output-format stream-json --verbose --json-schema <schema> --dangerously-skip-permissions`，提示词从 stdin 传入。最后一行 `type=result` 里的 `structured_output` 就是结果，同时取 `total_cost_usd`。登录状态由 `claude auth status` 输出的 `loggedIn` 判断。
- **Codex**：`codex exec --json --output-schema <file> -o <file> --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check -C <dir> -`，提示词从 stdin 传入，结果从 `-o` 指定的文件读取。登录状态由 `codex login status` 的退出码判断。
- 两者的流式事件都会转换成可读的日志行（模型说的话、工具调用、工具输出的摘要），通过 `log` 事件上报。这些日志就是报告里的「完整对话记录」。

## 提示词和结果结构（后端侧，`internal/testplan`，可替换）

提示词由以下内容拼成：
- issue 的标题、描述、评论；
- 仓库的测试说明；
- 测试纪律：不修改被测源码；每条结论必须附证据；做不到的就判 `unverifiable`，不许硬判通过；容器项目名使用 `verdict-<id>`。

结果使用严格 JSON Schema，两种 harness 共用：

```json
{
  "summary": "string",
  "acs": [{ "id": "AC-1", "criterion": "string", "status": "pass|fail|unverifiable",
            "evidence": "string", "reasoning": "string" }]
}
```

## 回写 Linear

测试进入 `completed` 或 `error` 状态后，如果配置了 Linear Key，就调用 `commentCreate` 在 issue 下发一条评论。评论内容包括：结论、被测的 commit、使用的 harness、每条验收标准的结果表、报告页链接（需要配置 `public_url`）。

评论链接或失败原因记录在 run 上，报告页提供「重新回写」按钮。

## 页面

- **登录**：输入管理员令牌。
- **运行时**：列表显示在线状态和 harness 探测结果；「添加运行时」后显示安装命令；可重新探测、删除。
- **仓库**：管理仓库地址和测试说明。
- **测试**：新建测试（issue 编号、仓库、分支、运行时、harness、超时），测试列表，报告页（结论、逐条验收结果、diff 提示、实时日志、取消、重新回写）。
- **设置**：Linear Key、对外地址。
