# 主子 issue 验收测试任务清单

状态：⬜ 未开始 · 🔄 进行中 · ✅ 完成 · ⏸ 阻塞

设计依据：`docs/linear-tree/design.md`（下文 §x 均指该文档章节）。

| # | 任务 | 依赖 | 状态 |
|---|---|---|---|
| T1 | 设计文档与决策记录（`docs/linear-tree/design.md`） | — | ✅ |
| T2 | `linear` 客户端：`Issue` / `IssueSummary` 增加 `Parent` / `Children`；四个查询加父子字段；单测覆盖字段解析 | — | ✅ |
| T3 | `store`：`runs` 新增 `set_id` / `issue_parent_key` / `scope`，`cases` / `version_issues` 新增 `scope`，走 migrations 与索引 | — | ✅ |
| T4 | `store`：set 聚合查询——给定父 run 返回子 run 列表、未覆盖子单、整体结论（§6.2 状态机） | T3 | ✅ |
| T5 | 后端创建 run：`POST /api/runs` 接收 `child_scope`，校验子单归属，生成 `set_id`，为选中子单批量建 run（§7） | T2、T3 | ✅ |
| T6 | `testplan`：父单 prompt 注入子单清单与集成规则、子单 prompt 注入父单行、上下文裁剪（§6.1） | T2 | ✅ |
| T7 | 回写 Linear：父 run 汇总评论模板（§8），失败不阻塞 | T4、T5 | ✅ |
| T8 | 看板 API：`/api/board/issues` 返回 `parent_identifier` / `parent_title` / `child_count` / `children` | T2 | ✅ |
| T9 | 前端看板（`Board.vue`）：父子分组折叠、筛选保留父单上下文、勾父单弹窗确认子单范围（§9） | T8 | ✅ |
| T10 | 前端报告页（`RunDetail.vue`）：父 run 子单矩阵 + 未覆盖 + 整体结论；子 run 父单链接 | T4、T5 | ✅ |
| T11 | 版本与回归：父单成员 `scope`、加入版本确认子单范围、矩阵按父单分组、发布沉淀用例带 `scope`（§10） | T3、T4、T5 | ✅ |
| T12 | e2e 自测：mock Linear 返回父子结构，覆盖「选子集 → 建父/子 run → 聚合结论 → 回写评论」全流程 | T2–T11 | ✅ |
| T13 | README 与文档同步，整理 PR 描述 | T12 | ✅ |

## 执行约束（必须遵守）

1. **不碰用户在用的环境**：不要操作 Docker 容器 `verdict`、`8791` 端口、`~/.verdict-agent`。开发测试一律用临时端口、`/tmp` 下的临时 SQLite 与临时目录；单元测试不依赖外部服务。
2. **测试先行**：每个任务先写失败测试（含边界：空 scope、非该父单的子单、全部子单、子单数 > 8、Linear 字段缺失），再实现，再跑 `go test -race ./...`。
3. **不要用 `pkill -f`**；结束进程用明确的 PID。
4. **迁移只增不改**：新列走 `store.go` 的 `migrations`，旧库能直接打开。
5. **不公开用户数据**：e2e / mock 里用虚构的 issue 编号与仓库。
6. 前端改动后跑构建（`cd web && npm run build`），不引入新依赖，除非设计文档要求。

## 自测要求

- 单元：`internal/linear`、`internal/server/store`、`internal/testplan`、`internal/server` 各自新增用例。
- e2e：复用现有 mock 手法（`VERDICT_LINEAR_ENDPOINT` 可指向本地 mock），断言：父 run 的 `set_summary` 在「全选全过 / 选子集全过 / 选子集有未过 / 父单自身未过」四种输入下分别得到 通过 / 部分通过 / 部分未通过 / 未通过。
- 回归：`go test -race ./...` 全绿；前端 `npm run build` 通过。

## 实现顺序建议

1. 先做只读链路：T2 → T3 → T8（看板能显示层级）。
2. 再做执行链路：T4 → T5 → T6 → T7。
3. 前端：T9、T10 可并行。
4. 最后：T11 → T12 → T13。

## 与设计文档的差异（实现时调整，需记录在此）

1. **子单描述在父单快照里补齐**：建父单测试时先拉一次选中子单（与建子 run 共用同一次拉取），把描述补进 `runs.issue.children`；拉取失败的子单不建 run，聚合时标「未创建」。
2. **子 run 跳回父 run 用 `set_parent_run_id`**，不复用已有的 `runs.parent_run_id`（那个是续跑来源，语义不同）。
3. **状态机细节**：父单 verdict 为 `partial`（部分无法验证）时优先返回「部分无法验证」，即使只覆盖了子集；「部分通过」只用于父单通过、覆盖子集全过且有未覆盖子单。子 run 不存在或未结束时整体为「未完成」。
4. **看板额外返回 `children_runs`**：按子单编号给出最近一次测试，展开父单时不用逐条查。
5. **版本成员顺序写入**：父单先加、子单逐个加；中途遇到「已在其他版本」时可能留下父单已加入的半完成状态，需要用户移出后重试（当前不做跨成员事务）。
6. **回归重跑保留批次字段**：`retryRegression` 复制 `set_id` / `issue_parent_key` / `scope`，父单重跑后聚合仍取每个子单最新一次结果。
7. **「父单无独立需求」**用描述是否为空判断，只在报告页作提示，不改变测试流程。

## 验收记录

- `go test -race ./...`：见下方最终验收结果。
- 前端 `npm run build`：通过。
- e2e `TestParentChildSet`：假 Linear 返回父子树，验证「选子集 → 父子并行建 run → 聚合结论 → 父单汇总评论」。
