# 主子 issue 验收测试设计

> 目标：Linear 的父 issue（汇总需求）和子 issue（拆分需求）都能被验收测试。父单负责汇总性要求与子单之间的集成，子单负责各自的细节；结论在父单上聚合，区分「通过 / 部分通过 / 部分未通过 / 未通过」。
> 状态：方案已与用户确认（2026-11-23），按 `tasks.md` 实现。

## 1. 背景

Linear 支持子 issue。团队的习惯是：父单写汇总需求，子单把需求拆成可独立实现和验收的条目。Verdict 目前完全忽略这层关系：

- `linear.Issue` / `IssueSummary` 没有 `parent` / `children`（`internal/linear/linear.go:29`、`:109`），三个 GraphQL 查询也都没取父子字段。
- `runs.issue_key` 单值（`internal/server/store/store.go:40`），没有父子关联；看板接口 `issueBoard`（`internal/server/board.go:35`）平铺返回。
- `testplan.Input.Issue` 是单条（`internal/testplan/pipeline.go:77`），prompt 只喂这一条的描述和评论（`internal/testplan/prompts.go:86`）。

后果有两个：父单的汇总需求没有测试归宿；子单的结论无法汇总回父单。

## 2. 决策记录（2026-11-23）

| # | 问题 | 采用 |
|---|---|---|
| 1 | 父单是否测试 | **测**。父单验证汇总性要求 + 子单间的集成/串联；子单验证各自细节 |
| 2 | 父子是否绑定批次 | **是**。一次父单测试 = 一个 set，包含父 run 和被选子单的 run |
| 3 | 子单范围 | 每次测父单时由用户勾选**任意子集**（`{1,2}`、`{1,3}`、单选均可），默认全选 |
| 4 | 勾父单是否自动带子单 | **不自动**，弹窗每次确认，保持自由 |
| 5 | 子单是否复用历史结果 | **不复用**，一律重跑（同一分支/commit），避免结果过期的歧义 |
| 6 | 子单上下文给父单 | 编号 + 标题 + 状态 + 描述正文（约 800 字/条），**不给评论** |
| 7 | 父单上下文给子单 | 只给一行「所属父单：ENG-123 标题」 |
| 8 | 整体结论 | 四态 + 未完成，见 §6.2 |
| 9 | 回写 Linear | **默认开**：子单各写各的；父单写一条汇总评论（结论表，不贴明细） |
| 10 | 回归基线 | **父单和子单都沉淀**为用例，用例记录当时的 scope |
| 11 | 版本 | 加父单时确认子单范围；父子都进版本；矩阵按父单分组 |
| 12 | 父单无独立需求 | 照常跑，报告标注「父单无独立需求，结论仅来自集成验证」 |
| 13 | 父子 run 共享服务部署 | 非目标，第一版不做 |

## 3. 概念

```
父单 ENG-123 · 集成测试 run（汇总 + 跨子单串联）   set_id = S1
  ├─ 子单 ENG-124 · 细节 run                      set_id = S1, issue_parent_key = ENG-123
  ├─ 子单 ENG-125 · 细节 run                      set_id = S1
  └─ 子单 ENG-126 · 本次未选中（不建 run，标「未覆盖」）
```

- **set（批次）**：一次父子测试组合。父 run 生成 `set_id`，本次创建的每个子 run 沿用同一个值。用 `set_id` 聚合，不靠时间或分支猜测。
- **父 run**：父单的集成测试。`scope` 记录本次选中了哪些子单；`issue` 快照里存父单当时的完整 `children` 列表。
- **子 run**：子单的细节测试。`issue_parent_key` 指向父单，`set_id` 指向本次批次。
- **覆盖**：`scope` 内 = 本次覆盖；`父单全部子单 − scope` = 未覆盖。未覆盖永远不计入通过率。

## 4. 数据模型

### 4.1 runs 新增列（走 `store.go` 的 `migrations`）

| 列 | 说明 |
|---|---|
| `set_id TEXT NOT NULL DEFAULT ''` | 父子测试批次号 |
| `issue_parent_key TEXT NOT NULL DEFAULT ''` | 该 run 对应 issue 的 Linear 父单编号（子 run 填） |
| `scope TEXT NOT NULL DEFAULT '[]'` | 仅父 run：本次选中的子单编号 JSON 数组 |

新增索引：`runs_set ON runs(set_id)`、`runs_issue_parent ON runs(issue_parent_key)`。

**注意**：`runs.parent_run_id` 已被「续跑」占用（见 `docs/resilience/design.md`），语义不同，**不要复用**。

### 4.2 cases / version_issues 新增列

- `cases.scope TEXT NOT NULL DEFAULT '[]'`：沉淀时记录的父单范围。
- `version_issues.scope TEXT NOT NULL DEFAULT '[]'`：父单成员在版本里的范围；子单成员为空。

### 4.3 快照原则

`runs.issue`（JSON）里存**测试当时**的 `parent` 和完整 `children`；`scope` 是其中的子集。看板展示用 Linear 实时关系，报告和聚合用快照——Linear 后来调整拆分不影响历史结论。

## 5. Linear 集成

`internal/linear/linear.go`：

- `Issue` / `IssueSummary` 增加 `ParentID / ParentKey / ParentTitle` 和 `Children []ChildBrief{Identifier, Title, StateType}`。
- `GetIssue`、`listIssues`、`searchIssues`、`BoardIssues` 的 selection 增加：

```graphql
parent { id identifier title }
children { nodes { id identifier title state { name type } } }
```

Linear 新版 schema 里子 issue 也可能叫 `subIssues`。实现时先用 introspection 确认实际字段名，再写死；mock 测试两种都能覆盖。

## 6. 测试语义

### 6.1 prompt 注入（`internal/testplan/prompts.go`）

`writeRequirement` 之后追加：

父单 run：

```
## 子单（这是汇总单，子单已拆分实现）
- ENG-124 登录页改造（In Progress）
  描述摘要…
- ENG-125 权限校验（Backlog）
  描述摘要…

本次只验证两类内容：
1. 父单验收标准里未被任何子单覆盖的部分；
2. 上述子单之间的串联、一致性、端到端流程（集成测试）。
不要重复验证子单各自的细节验收项——那些由子单测试覆盖。
```

子单 run：

```
## 所属父单
ENG-123 账号安全改造（汇总单）
```

上下文裁剪规则：

- 子单 ≤ 8 个：选中的给标题 + 状态 + 描述（截断 800 字）。
- 子单 > 8 个：选中的描述压到 200 字；未选中的只给标题。
- 评论一律不给。

plan 阶段的规则里补一句：父单验收标准与子单重复的条目归到子单，父单计划只保留跨子单的组合条件。

### 6.2 父单整体结论（状态机）

在父 run 上计算，优先级从上往下匹配：

| 顺序 | 条件 | 整体结论 |
|---|---|---|
| 1 | 父 run 未跑完 / 出错 / 被取消 | **未完成** |
| 2 | 父单自身验证未通过 | **未通过** |
| 3 | 父单自身通过，选中子单里有未通过 | **部分未通过** |
| 4 | 父单自身通过，选中子单全过，`scope ⊂ 全部子单` | **部分通过** |
| 5 | 父单自身通过，选中子单全过，`scope == 全部子单` | **通过** |

补充：

- 「未通过」只表示父单自己的集成测试没过；子单没过是「部分未通过」。
- 「部分无法验证 / 存疑」（沿用现有 verdict 值）没有未通过时按本项目既有规则处理，并在报告里单列。
- 未覆盖的子单在报告里**显式列出**，标注「未覆盖」。

### 6.3 聚合查询

给定父 run：

```sql
SELECT * FROM runs WHERE set_id = ? AND id != ? ORDER BY created_at
```

子 run 结论取自己的 `verdict`；未覆盖子单来自父 run 快照 `children − scope`。父 run 自身结论取自己的 `verdict`。

## 7. 编排

创建父单测试（`POST /api/runs` 带 `child_scope`）：

1. 实时拉一次父单，校验 `child_scope` 里每一项都是该父单的子单；不合法直接 400。
2. 生成 `set_id`（复用现有 ID 生成方式）。
3. 创建父 run：写入 `scope`、`set_id`、`issue` 快照（含完整 children）。
4. 为每个选中子单创建 run：同 repo、branch、runtime 策略、档位、harness；写入 `set_id`、`issue_parent_key`，`scope` 为空。
5. 子 run 走现有调度，与父 run **并行、无跨 run 依赖**——父单 plan 只需要子单的描述，不需要子单的实测结果。

失败处理：父 run 建成功、某个子 run 建失败时，报告里把该子单标「未创建」并提示可单独重试；不回滚父 run。

批量接口（`batchRunInput`）：`issue_keys` 里出现父单时，同样按上面流程展开；一次请求里多个父单各自一个 `set_id`。

## 8. 回写 Linear

沿用 `internal/server/linear_comment.go` 的现有机制，父 run 增加汇总模板：

```
## 验收测试汇总 · ENG-123

整体结论：部分通过（覆盖 2/3 个子单）

| 子单 | 结论 |
|---|---|
| ENG-124 登录页改造 | 通过 |
| ENG-125 权限校验 | 通过 |
| ENG-126 审计日志 | 未覆盖 |

父单自身：通过（见报告）
```

- 子单仍各写各的。
- 父单写失败只记 `linear_error`，不阻塞测试。
- 默认开；版本/回归里的子测试沿用「默认不回写」的既有规则。

## 9. 看板与报告

### 看板（`web/src/pages/Board.vue`）

- 父单一行，子单缩进挂载，可折叠；无父单的照旧平铺。
- 父单行的测试状态显示 set 聚合结论；悬停/展开看子单明细。
- 筛选命中子单时保留父单行做上下文。
- 勾父单不自动带子单，弹出子单清单（默认全选）让用户确认后建单。
- 批量勾选多个 issue 时，父单各自弹确认或合并成一个弹窗；实现时选交互最简单的方案。

### 报告（`web/src/pages/RunDetail.vue`）

- 父 run：顶部子单矩阵（编号 / 状态 / 结论 / 最近 run 链接）+ 未覆盖列表 + 整体结论。
- 子 run：顶部「所属父单」链接。
- 「父单无独立需求」时标注说明。

## 10. 版本与回归（`docs/versions/design.md` 的扩展）

- 加父单进版本：弹窗确认子单范围；父单和选中子单都进 `version_issues`，父成员记录 `scope`。
- 冻结回归：父单按自己保存的计划做集成验证；子单各自重测。沿用 `PresetPlan` 复用机制。
- 版本矩阵按父单分组，父单行显示每轮回归的聚合结论（按 §6.2）。
- 发布沉淀：父单、子单都沉淀为 `cases`，父单用例带 `scope`。
- 回归重测时按用例保存的 `scope` 重测，不自动纳入后来的新子单——基线验证的是「当时上线的范围」。

## 11. API 变更

| 接口 | 变更 |
|---|---|
| `GET /api/board/issues` | 每行增加 `parent_identifier`、`parent_title`、`child_count`、`children`（编号+标题+状态） |
| `GET /api/issues/{key}` | 增加 `parent`、`children`（实时） |
| `POST /api/runs` | body 增加 `child_scope []string`；父单时后端自动展开子 run |
| `GET /api/runs/{id}` | 父 run 增加 `set_summary`（整体结论、子单矩阵、未覆盖）；子 run 增加 `parent_issue_key`、`set_id` |

## 12. 非目标

- 父子 run 共享一次服务部署（成本问题后续再评估）。
- 跨仓库 / 跨团队的子单。
- 子单结果自动复用。
- 父单结论自动触发 Linear 状态流转（仍由用户决定）。

## 13. 风险

- **Linear 字段名变动**：`parent/children` 与 `subIssues` 不一致时以 introspection 为准；客户端解析要有单测。
- **测试期间拆分变化**：一律用 run 快照，看板实时只是展示。
- **上下文过大**：按 §6.1 裁剪，父单子单超过 8 个时压缩描述。
- **重复部署成本**：同一分支父子各起一份服务，第一版接受。
