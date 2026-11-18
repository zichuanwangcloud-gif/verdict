# 版本与回归测试设计

## 目标

现在每次测试都是孤立的：一个 issue，一个 feature 分支，测完就结束。这里补两件事：

1. **版本**：把同一个仓库里的一批 issue 归到一个版本下；版本冻结到某个 commit 后，在这份最终代码上整体重测这批 issue，发现「分支上通过、合并后坏掉」的集成问题。
2. **回归基线**：每个版本发布后，把通过的 issue 沉淀成「回归用例」长期积累；以后每个版本冻结时都把历史用例一起重测，发现「新改动把旧功能弄坏」的问题。

## 决策记录（2026-11-18）

用户确认：版本在 Verdict 里自己建，不和 Linear 的 Project / Cycle 绑定；「本版本 issue」和「历史回归基线」两层都做。以下细节按我的推荐执行，可以再调：

| # | 问题 | 采用 |
|---|---|---|
| 1 | 版本归属 | 一个版本只属于一个仓库；版本名在仓库内唯一，自由填写（如 `1.4.0`） |
| 2 | issue 归入版本 | 新建测试时可选版本；也可在版本页直接添加 issue（还没测过的显示「未测」）。同一个 issue 在一个仓库里同一时间只属于一个版本，可以移到别的版本 |
| 3 | 手动输入的需求 | 不能归入版本，也不能进基线（没有稳定编号，无法跨版本追踪） |
| 4 | 冻结节点 | 填分支名、tag 或完整 SHA；平台立刻通过运行时解析成 SHA 存下来，之后只认 SHA。重新冻结会生成新的回归轮次，不覆盖旧的 |
| 5 | 基线怎么积累 | 版本标记「已发布」时，本版本在最终回归中**通过**的 issue 自动沉淀为回归用例；也可以从任意一次通过的测试手动加入。用例分「核心」和「普通」两级 |
| 6 | 回归范围默认值 | 本版本 issue 全部 + 基线里的核心用例；冻结时可改成「全部用例」或「不含基线」 |
| 7 | 回归档位默认值 | 本版本 issue 用 B，基线用例用 C；冻结时可改 |
| 8 | 计划复用 | 回归不重新生成计划，直接用来源测试的计划，跳过 plan 阶段 |
| 9 | Linear 回写 | 回归里的子测试默认不回写 Linear（避免刷屏）；单个子测试可以手动回写 |
| 10 | 发布前提 | 「标记为已发布」要求最近一轮回归已结束；结论不是「通过」时必须填写放行理由 |

## 概念

```
仓库
 ├─ 版本 1.3.0（已发布）
 ├─ 版本 1.4.0（已冻结）
 │    ├─ 成员 issue：ENG-12、ENG-15、ENG-20 …（各自的分支测试照常进行）
 │    └─ 回归轮次 #1（freeze_sha=ab12…）、#2（freeze_sha=cd34…）
 │          └─ 子测试：每个成员 issue 一个 + 每个选中的基线用例一个，全部锁定到 freeze_sha
 └─ 回归基线：用例 ENG-3、ENG-7、ENG-12 …（来自已发布版本，持续积累）
```

- **版本（version）**：状态 `open`（开发中）→ `frozen`（已冻结，至少有过一轮回归）→ `released`（已发布）。已发布的版本只读。
- **成员（version issue）**：版本和 issue 的归属关系，记录这个 issue 在本版本里的需求快照和最近一次分支测试。
- **回归轮次（regression）**：一次冻结 = 一个 SHA = 一批子测试。结论从子测试汇总得出。
- **回归用例（case）**：仓库级的长期资产，一个 issue 最多一条。保存当时的需求快照、测试计划、来源测试和历史结果。

## 冻结与 SHA 解析

后端（Docker 镜像）里没有被测仓库的访问权限，所以 SHA 交给运行时解析：

1. 用户在版本页点「冻结并回归」，填写 ref（分支 / tag / SHA）、回归范围、档位和参与的运行时。
2. 后端向其中一个在线运行时发送新消息 `resolve {repo_url, ref}`，agent 在 bare 仓库缓存里执行 fetch（`refs/heads/<ref>` 和 `refs/tags/<ref>` 都试，SHA 则直接校验），返回 `resolved {sha, subject}`。
3. 解析成功后创建回归轮次，记下 `freeze_ref` 和 `freeze_sha`，并批量创建子测试。解析失败直接报错，不建轮次。

## 子测试的执行

### 锁定 commit（协议 v3）

现在 agent 只会拉 `refs/heads/<branch>` 的最新提交。`start` 消息新增 `commit` 字段：

- 有 `commit` 时，agent 先 fetch `ref`（分支或 tag），再确认 `commit` 存在，然后把 worktree 建在这个 commit 上；commit 不存在时报错「冻结 commit 已不在 <ref> 上（可能被 force push）」。
- 旧 agent 不认这个字段，会去测分支的最新提交，结果就错了，所以协议升到 v3。`startRun` 要求协议号完全相等，因此升级后**所有运行时都要重新执行安装命令**，普通测试也一样。这一点要写进发版说明。

### 复用计划

`testplan.Input` 新增 `PresetPlan *Plan` 和 `PlanSource string`（来源测试 ID）：

- 有预置计划时，流水线跳过 plan 阶段，后续阶段直接使用它。
- verify 提示词里加一段：「这是回归测试，计划来自测试 X。如果计划里的步骤和当前代码对不上（例如页面路径变了），按验收标准本身判断，并在 reasoning 里说明计划哪里过时了。」
- 计划来源：本版本 issue 用这个 issue 在本版本里最近一次带计划的分支测试；基线用例用用例里保存的计划。两者都没有时（例如加入版本后一直没测过），照常生成计划。

### 需求快照

- 本版本 issue：回归开始时重新从 Linear 拉一次（开发期间验收标准可能改过），拉取失败时退回成员记录里的快照。
- 基线用例：始终用沉淀时保存的快照。旧 issue 在 Linear 上可能已经关闭或被改写，基线验证的是「当时上线的行为」。

### 调度

子测试可能很多，而每个运行时并发数仍是 1，所以要能分摊到多个运行时：

- 子测试创建时 `runtime_id = 0`、状态 `queued`，归属的回归轮次记着参与运行时列表。
- `Hub.Schedule(rt)` 先派这个运行时自己队列里的测试；没有的话，再从「允许这个运行时参与」的回归轮次里领一个 `runtime_id = 0` 的子测试，领取时写入 `runtime_id`（用条件更新保证只被领一次）。
- 运行时空闲、上线或子测试结束时都会触发 `Schedule`，所以快的机器会自动多领。
- 普通测试优先于回归子测试，回归不会把日常测试堵住。

## 结果

### 子测试结论

沿用现有规则：逐条 AC 汇总，有一条未通过即「未通过」。

### 回归轮次结论

| 结论 | 条件 |
|---|---|
| 通过 | 所有子测试都通过 |
| 未通过 | 任一子测试未通过 |
| 部分无法验证 | 没有未通过，但有无法验证或存疑 |
| 未完成 | 有子测试出错或被取消（可以只重跑这些） |

### 回归标记

报告里最需要看到的是**以前好、现在坏**的项，单独标红并排在最前：

- 本版本 issue：分支上最近一次测试「通过」，冻结节点上「未通过」→ **集成回归**。
- 基线用例：上一次回归「通过」，这一次「未通过」→ **历史回归**。

### 版本页

- 顶部：版本状态、各轮回归（SHA、时间、结论、花费）。
- 矩阵：每个 issue / 用例一行，列为「分支测试结果」「第 1 轮」「第 2 轮」…，点格子进入对应子测试的报告。
- 操作：只重跑未通过 / 出错的子测试（在同一轮里生成新的尝试，保留旧结果）；重新冻结；标记为已发布。

## 回归基线

### 沉淀

- 版本标记「已发布」时，本版本最后一轮回归里结论为「通过」的 issue，自动生成或更新用例：保存需求快照、来源子测试的计划、来源测试 ID，级别默认「普通」。
- 已有用例的 issue 再次出现在新版本里（需求迭代），发布时用新的快照和计划**覆盖**旧用例，并保留历史记录。
- 也可以从任意一次「通过」的测试点「加入回归基线」。

### 维护

基线用例在回归中未通过时，在报告里给出三个处理按钮：

| 操作 | 含义 | 效果 |
|---|---|---|
| 确认是缺陷 | 真的被弄坏了 | 不改用例；可以一键用本次结果在 Linear 上建 issue（后续再做，MVP 只记录） |
| 需求已变更 | 旧行为本来就该变 | 用例停用，记录停用原因和对应版本 |
| 计划过时 | 行为没错，是计划步骤对不上 | 用本次测试的计划更新用例 |

基线页（每个仓库一个）列出全部用例：级别、状态（启用 / 停用）、来源版本、最近几次回归结果。可以手动改级别、停用或删除。

### 不稳定用例

每条用例保留历史结果。同一个 SHA 上结论翻转过的用例标为「不稳定」，在矩阵里单独提示，结论汇总时仍然计入。是否隔离先人工决定，后续再看要不要自动处理。

## 数据模型

新增表，并给 `runs` 补列（迁移方式沿用 `migrations`，只增不改）：

```sql
CREATE TABLE versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  name TEXT NOT NULL,
  status TEXT NOT NULL,              -- open / frozen / released
  note TEXT NOT NULL DEFAULT '',
  release_note TEXT NOT NULL DEFAULT '',   -- 非「通过」放行时的理由
  created_at TEXT NOT NULL,
  released_at TEXT NOT NULL DEFAULT '',
  UNIQUE (repo_id, name)
);

CREATE TABLE version_issues (
  version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL,          -- 冗余，用于保证同仓库内唯一
  issue_key TEXT NOT NULL,
  issue TEXT NOT NULL,               -- 最近一次的需求快照
  added_at TEXT NOT NULL,
  PRIMARY KEY (version_id, issue_key),
  UNIQUE (repo_id, issue_key)
);

CREATE TABLE regressions (
  id TEXT PRIMARY KEY,
  version_id INTEGER NOT NULL REFERENCES versions(id),
  seq INTEGER NOT NULL,              -- 第几轮
  freeze_ref TEXT NOT NULL,
  freeze_sha TEXT NOT NULL,
  freeze_subject TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL,               -- {"baseline":"core|all|none"}
  config TEXT NOT NULL,              -- 两类子测试各自的档位与 harness
  runtime_ids TEXT NOT NULL,         -- 参与的运行时
  status TEXT NOT NULL,              -- running / completed / canceled
  verdict TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE cases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  issue_key TEXT NOT NULL,
  issue TEXT NOT NULL,               -- 沉淀时的需求快照
  plan TEXT NOT NULL DEFAULT 'null',
  source_run_id TEXT NOT NULL,
  source_version_id INTEGER,         -- 手动加入时为空
  level TEXT NOT NULL,               -- core / normal
  status TEXT NOT NULL,              -- active / retired
  retired_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (repo_id, issue_key)
);

CREATE TABLE case_history (          -- 覆盖 / 停用 / 改计划都记一条
  case_id INTEGER NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
  ts TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL
);
```

`runs` 新增列：

| 列 | 说明 |
|---|---|
| `version_id` | 所属版本（分支测试和回归子测试都填） |
| `regression_id` | 所属回归轮次；普通测试为空 |
| `case_id` | 回归子测试对应的基线用例；本版本 issue 为空 |
| `pin_sha` | 要求测试的 commit；为空表示测分支最新提交 |
| `plan_source` | 预置计划来自哪个测试 |
| `attempt` | 同一轮里同一项的第几次尝试，矩阵只显示最新一次 |

`runtime_id` 允许为 0（回归子测试尚未被领取）。

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET / POST | `/api/repos/{id}/versions` | 列出 / 新建版本 |
| GET / PATCH / DELETE | `/api/versions/{id}` | 详情（含成员、各轮回归、矩阵）/ 改名改备注 / 删除（只能删没有回归的版本） |
| POST / DELETE | `/api/versions/{id}/issues[/{key}]` | 添加 / 移除成员；添加已在别的版本里的 issue 时返回冲突，带 `move=true` 则移动 |
| POST | `/api/versions/{id}/regressions` | 冻结并回归 |
| POST | `/api/regressions/{id}/retry` | 重跑未通过 / 出错的子测试 |
| POST | `/api/regressions/{id}/cancel` | 取消整轮 |
| POST | `/api/versions/{id}/release` | 标记为已发布（可带放行理由），触发基线沉淀 |
| GET | `/api/repos/{id}/cases` | 基线列表 |
| POST / PATCH / DELETE | `/api/cases[/{id}]` | 从测试手动加入 / 改级别、停用、更新计划 / 删除 |
| POST | `/api/runs` | 已有接口，新增可选字段 `version_id` |

## 页面

- 导航新增「版本」：按仓库列出版本，进入版本页（见上文）。
- 新建测试：选完仓库后多一个「版本」下拉（可不选，可就地新建）；选了版本且 issue 已在别的版本里时提示移动。
- 测试报告：显示所属版本 / 回归轮次；通过的测试上有「加入回归基线」按钮；基线用例未通过时显示三个处理按钮。
- 仓库设置里加「回归基线」入口。

## 失败与边界

| 情况 | 处理 |
|---|---|
| 解析 SHA 时没有在线运行时 | 报错「需要至少一个在线运行时来解析冻结节点」 |
| 回归进行中又冻结 | 不允许，先取消当前轮次 |
| 回归进行中给版本加 issue | 允许，只影响下一轮 |
| 参与的运行时全部离线 | 子测试一直排队；页面提示，可以修改参与运行时 |
| 子测试所需的 harness 在领取它的运行时上没装 | 跳过这个运行时，换下一个；所有参与运行时都不行时，子测试直接判为出错 |
| 删除仓库 | 有版本时不允许删除 |

## 这一版不做

- 和 Linear Project / Cycle 同步、跨仓库版本。
- 把通过的验收固化成确定性回放脚本（`cases` 以后加 `script` 列；这一版仍由模型按计划重测）。
- 性能基线与对比。
- 不稳定用例的自动隔离。
- 回归结果汇总回写到 Linear。

## 风险

- **成本**：基线会越积越多。默认只跑核心用例，加上档位 C 和多运行时分摊，可以在一段时间内撑住；用例上百之后必须靠回放脚本，这是下一步的重点。
- **计划过时**：页面改版后，旧计划里的步骤会大量失效。提示词允许模型「按验收标准本身判断」，同时让「计划过时」的处理足够顺手。
- **协议升级**：v3 要求所有运行时重新安装 agent。
