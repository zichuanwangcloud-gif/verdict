# MVP 任务拆分

| # | 任务 | 依赖 | 状态 |
|---|---|---|---|
| T1 | 清理旧领域代码（ledger/verifier/agent/area/changeset），统一入口 `cmd/verdict`，提供 server / agent / version 子命令 | — | ✅ |
| T2 | `internal/protocol`：WebSocket 消息、harness 探测结果、测试结果结构、结论汇总函数，以及对应单测 | T1 | ✅ |
| T3 | `internal/server/store`：新表结构和读写方法 | T2 | ✅ |
| T4 | `internal/server`：管理员认证、REST API、install.sh、二进制下载 | T3 | ✅ |
| T5 | `internal/server/hub`：agent WebSocket 连接、hello/sync 处理、调度、事件落库、取消 | T3 | ✅ |
| T6 | `internal/linear`：拉取 issue、发评论 | — | ✅ |
| T7 | `internal/testplan`：提示词和结果 Schema | T2 | ✅ |
| T8 | `internal/agent`：配置、连接循环（断线重连、事件日志补发）、启动时修复中断的测试 | T2 | ✅ |
| T9 | `internal/agent/workspace`：bare 缓存、worktree、env 复制、源码改动检查、清理 | T8 | ✅ |
| T10 | `internal/agent/harness`：Claude Code、Codex 适配器 | T8 | ✅ |
| T11 | 前端：登录、运行时、仓库、测试列表和报告、设置 | T4 | ✅ |
| T12 | Dockerfile、README、部署说明（反向代理的 WebSocket 配置） | 全部 | ✅ |
| T13 | 端到端验证：本机启动后端和 agent，用 Claude Code 测一个示例仓库 | 全部 | ✅ |

## 验证记录（2026-11-17）

- `go test -race ./...` 全部通过，其中 `internal/e2e` 用真实后端 + 真实 agent + 本地 git 仓库覆盖：完整链路、源码改动检测、取消、排队与分支不存在、断线重连补发、认证。
- 本机真实运行：用 `install.sh` 安装 agent，Claude Code 测示例仓库（故意埋一个不满足 AC-3 的 bug），结论为「未通过」，AC-1/AC-2 通过、AC-3 未通过，证据与原因准确，耗时 26 秒、费用约 $0.21。
- Docker 镜像构建并启动，宿主机 agent 从镜像下载二进制后连入，探测到 Claude Code / Codex。
- Codex 适配器按其公开事件格式实现并有解析单测；本机 Codex 连不上它配置的模型网关，未做真实端到端。
- 真实 Linear 回写未验证（没有 Linear Key），GraphQL 调用用假服务做了单测。
