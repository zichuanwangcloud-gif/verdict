# 并发执行任务清单

状态：⬜ 未开始 · 🔄 进行中 · ✅ 完成

| # | 任务 | 状态 |
|---|---|---|
| T1 | 设计文档与决策记录（`docs/concurrency/design.md`） | ✅ |
| T2 | 协议 v4：`hello.active` 改列表；`host` 增加 `cpu` / `mem_total_mb` / `concurrency` / `busy`；`start` 增加 `ports`；`ready` 回传分配到的端口；`closed.busy` 表示并发已满 | ✅ |
| T3 | agent 多会话：`sessions` map、并发上限保护、退出时收尾全部会话、GC 跳过所有活跃目录、会话变化后补报 probe | ✅ |
| T4 | agent 端口分配：绑定 `:0` 挑空闲端口，注入 `VERDICT_PORT` / `VERDICT_PORT1..N` / `VERDICT_PORT_COUNT` | ✅ |
| T5 | 同一仓库并发：clone / fetch / worktree 按 bare 仓库加锁串行，锁冲突退避重试 | ✅ |
| T6 | 后端调度：按槽位循环派单、agent `busy` 作为上限、普通测试优先于回归子测试、probe 触发重新调度 | ✅ |
| T7 | 并发已满时 `closed.busy` 放回队列，不判失败 | ✅ |
| T8 | 用户可设并发度（0–32，0 表示跟随按机器的建议值），按 CPU 一半 / 每 8GB 一个、夹在 [1,8] | ✅ |
| T9 | 仓库端口数配置（0–8），计划提示词要求用 `${VERDICT_PORT}` 占位符 | ✅ |
| T10 | 前端：运行时页显示核数 / 内存 / 并发 / 忙碌并可修改；仓库页端口数与占位符提示 | ✅ |
| T11 | 测试：并发峰值不超过设置值、并发度 1 时保持串行、同仓库并发不再抢 git 锁 | ✅ |
| T12 | 更新 README，构建镜像并在本机验证 | 🔄 |

## 与设计文档的差异

1. **端口由 agent 分配**：设计里写的是「平台分配」，实现改为 agent 收到 `start` 后在本机绑定 `:0` 挑空闲端口，通过 `ready` 事件回传。只有运行时知道本机哪些端口占用，平台按顺序分池会撞车。
2. **agent 侧并发上限**默认 0（完全信任平台调度）；只有平台派超过并发时才回 `closed.busy` 保护。
3. **同一仓库的锁**覆盖整个 `Prepare`（clone + fetch + worktree），不只是 fetch：并发首次 clone 时两个会话会同时写同一个 `.partial` 目录。
