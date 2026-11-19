# 人工真值与差错复盘

## 目标

现在是单方向流程：agent 给出结论 → 回写 Linear → 结束。没有人工真值，就不知道 agent 测得准不准。

补一个闭环：**人工标真值 → 和 agent 结论对比 → 对不上的那条派一次分析，在仓库里复现并归因 → 结果沉淀成复盘记录**。

要回答的问题：

- agent 判「通过」而人工测出了问题，当时为什么没测出来？
- 准确率是多少，错在哪一类？
- 是不是某一类需求、某个档位、某个仓库系统性地更容易漏？

### 本次不做（用户明确选择）

- 分析结论**不自动回灌**测试计划提示词。
- 不自动生成回归用例。
- 不回写 Linear。

结果只存下来做复盘。数据模型给这些扩展留了位置，见文末。

## 概念

```
测试 run（agent 的结论）
   └── 真值 truth（人工的结论，一条测试至多一条）
          └── 差错 postmortem（只在两者对不上时产生）

postmortem
   ├── kind: escaped（漏报）| false_alarm（误报）
   ├── analysis run：一次独立的 agent 任务，在仓库里复现 + 归因
   │      ├── reproduce 阶段：只复现，不归因
   │      └── attribute 阶段：拿复现证据归因
   └── conclusion：结构化结论（类别、解释、改进建议、置信度）
```

- **真值（truth）**：人工对某一次测试的判定，逐次记录，不按 issue 汇总。取值 `pass` / `fail` / `invalid`。
  - `invalid` 用于这次测试本身没有意义的情况（测的不是最新代码、服务没起来等），不进入准确率统计。
- **差错（postmortem）**：一次「agent 结论 vs 人工真值」的不一致，加上对这个不一致的分析。
- **分析 run**：复用现有 run 的基础设施（worktree、阶段下发、重试、日志、产物、看门狗），
  用 `runs.kind = 'postmortem'` 区分，不出现在「验收测试」列表和统计里。

### 为什么真值逐次记而不是按 issue

一个 issue 会被测多轮（分支测试、回归、不同档位）。按 issue 记只能得到「这个 issue 最终过没过」，
拿不到「第几次测试判错了」——而复盘要的正是一次具体测试的计划、证据和截图。
issue 级别的汇总视图可以后续从逐次记录里推出来。

## 用户流程

### 循环一：日常标记

1. 测试跑完，报告页顶部出现「人工复核」区块（未标记时高亮）。
2. 人工跑完测试后回来：
   - 一致 → 点「通过」或「未通过」，完事。
   - 人工测出问题、agent 说通过 → 点「未通过」，粘单子（或填单号），点「分析为什么没测出来」。
   - agent 说未通过、人工确认没问题 → 点「通过」，可顺手发起误报分析。
   - 这次测试没意义 → 点「无效」。
3. 「待测试」页和「验收测试」列表里，标过的行右上角显示一个真值角标（✓ / ✗ / 无效）。

### 循环二：复盘

「复盘」页按仓库 / 时间 / 档位筛选：

- 顶部：准确率、漏报率、误报率、样本数。
- 混淆矩阵：agent 结论（通过 / 存疑 / 未通过）× 人工真值（通过 / 未通过）。
- 按归因类别的分布：哪一类错误最多。
- 复盘清单：每次差错一行，点开看分析结论和当时的报告。

## 数据模型

```sql
-- 一条测试至多一条真值。改标记就地更新（保留 updated_at）。
CREATE TABLE IF NOT EXISTS run_truths (
  run_id     TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  verdict    TEXT NOT NULL,              -- pass / fail / invalid
  source     TEXT NOT NULL,              -- manual（人工复测）/ ticket（测试人员开单）
  note       TEXT NOT NULL DEFAULT '',   -- 人工的说明
  ticket     TEXT NOT NULL DEFAULT '',   -- 单子原文（粘贴）
  ticket_ref TEXT NOT NULL DEFAULT '',   -- 单号或链接
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- 一次差错分析。run_id 是分析型 run。
CREATE TABLE IF NOT EXISTS postmortems (
  run_id        TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  target_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,  -- 被分析的测试
  kind          TEXT NOT NULL,            -- escaped / false_alarm
  category      TEXT NOT NULL DEFAULT '', -- 归因类别，见下表
  confidence    TEXT NOT NULL DEFAULT '',
  conclusion    TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL,
  finished_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS postmortems_target ON postmortems(target_run_id);
```

`runs` 加一列：

```sql
{"runs", "kind", "TEXT NOT NULL DEFAULT 'test'"}   -- test / postmortem
```

迁移只增不改，旧库自动补列，旧 run 全是 `test`。

**影响面**：`Runs()` 加 `AND r.kind='test'`，列表和统计不受分析 run 干扰；
`RunsOnRuntime` 保持不过滤（分析 run 也要占并发槽位、也要被调度）。

## 分析 run 的编排

### 复用而不是新造

分析 run 和测试 run 共用同一套东西：锁定 `pin_sha` 的 worktree、阶段下发、失败重试、
日志、产物上传、看门狗。只换两处：

1. **流水线**：`testplan.Next` 换成 `testplan.PostmortemNext`。
2. **收尾**：不写 verdict，不回写 Linear，把结构化结论落到 `postmortems`。

`Hub.advance` 按 `run.Kind` 分派。`OnFinish` 里对 `kind='postmortem'` 直接跳过 `writeBack`。

### 两个阶段

沿用「取证与判断分离」的思路（和蓝队/红队是同一个理由）：先只复现，再归因，
避免模型一边猜一边给自己找理由。

| 阶段 | 角色 | 做什么 | 禁止 |
|---|---|---|---|
| 1 | `reproduce` | 按单子里的操作步骤在 worktree 里实测，记录命令、输出、截图、服务的起停 | 不判断根因，不评价当时的测试报告 |
| 2 | `attribute` | 拿复现证据 + 当时的完整报告，判断为什么没测出来 | 不重新做一遍完整验收测试 |

`attribute` 的输入包括：

- 原始需求（issue 描述 + 评论）
- 当时的逐条验收标准、结论、证据、置信度、红队质疑记录
- 当时的额外发现、UI 走查截图（截图随 prompt 引用文件名，agent 读图）
- 人工真值和单子原文
- 复现阶段产出的证据

### 归因类别

类别要和流水线的阶段对得上，复盘才知道该改哪里。

| 类别 | 含义 | 通常改哪里 |
|---|---|---|
| `plan_gap` | 计划里就没有这一项，验收标准没被拆出来 | 计划提示词 |
| `shallow_check` | 检查太浅（只看了 200，没看内容） | 验证阶段提示词 |
| `wrong_method` | 验证方式不对（界面问题只读了代码） | 计划的 method 字段 |
| `wrong_status` | 证据有了，结论判反了 | 裁决/汇总逻辑 |
| `evidence_missing` | 没有证据就判了通过 | 硬约束（无证据降为无法验证） |
| `ui_misread` | 截图信息在，但读错了 | 截图质检阶段 |
| `data_gap` | 数据不足，没造数就放弃 | 造数提示词 |
| `env_mismatch` | 环境与人工测试环境不一致，行为不同 | 仓库测试说明 |
| `ambiguous` | 需求本身模糊，agent 理解偏了 | 需求侧，不是平台问题 |
| `flaky` | 偶发，复现不出来 | 稳定性检查（重复执行） |

最后两类（`ambiguous`、`flaky`）要能在复盘里单独看到——它们不是平台的错，占比高说明指标被稀释了。

### 结论结构（agent 输出 schema）

```json
{
  "reproduced": "yes | no | partially",
  "defect_summary": "一句话描述人工发现的缺陷",
  "trigger": "复现步骤和必要数据",
  "category": "plan_gap",
  "explanation": "当时为什么没测出来，要具体到哪条验收标准、哪一步",
  "missed_signals": ["当时的报告里其实已经出现的线索"],
  "improvement": "下一次同类需求该怎么测",
  "confidence": "high | medium | low"
}
```

`missed_signals` 是这份设计里最有价值的一栏：它回答「agent 手里其实已经有线索，只是没有连起来」，
比笼统的「计划没覆盖」更能指导改进。

## 指标定义

以「有真值的测试」为样本。agent 侧三档（`pass` / `partial` / `fail`），人工侧两档。

| 项 | 定义 |
|---|---|
| 命中 | agent `fail`、人工 `fail` |
| **漏报（明确）** | agent `pass`、人工 `fail` |
| **漏报（存疑）** | agent `partial`、人工 `fail` |
| **误报（明确）** | agent `fail`、人工 `pass` |
| 误报（存疑） | agent `partial`、人工 `pass` |
| 真放行 | agent `pass`、人工 `pass` |
| 漏报率 | (明确 + 存疑漏报) / 人工 `fail` 总数 |
| 误报率 | (明确 + 存疑误报) / 人工 `pass` 总数 |

「存疑」单列而不是直接算走进/不算，因为 agent 说「无法验证」却被人测出来，
和 agent 拍胸脯说「通过」却被人测出来，严重程度差很多，复盘要能分开看。

真值为 `invalid` 的样本不进任何分子分母。

**采样偏差要写在页面上**：这里的样本只覆盖「人工回访过的测试」。
如果人工只测了某几个 issue，漏报率只代表那批 issue。页面固定显示样本数和时间范围，不做外推。

## 接口

```
GET    /api/runs/{id}/truth             读真值（没有则 404）
PUT    /api/runs/{id}/truth             标记 / 修改真值
DELETE /api/runs/{id}/truth             撤销标记

POST   /api/runs/{id}/postmortem        发起分析（自动判断 escaped / false_alarm）
GET    /api/postmortems                 复盘清单（按仓库/时间/档位/类别筛选）
GET    /api/postmortems/{id}            单条分析详情（含分析 run 的阶段和日志）
POST   /api/postmortems/{id}/cancel     取消分析

GET    /api/review/stats                混淆矩阵与比率（带筛选条件）
```

`PUT /truth` 的请求体：

```json
{"verdict": "fail", "source": "ticket", "note": "", "ticket": "...", "ticket_ref": "BUG-123"}
```

`POST /postmortem` 的前置检查：有真值；真值与 agent 结论不一致（一致时拒绝，避免无意义开销）；
存在 `PinSHA`。没有 `PinSHA` 时降级为只读分析并在结论里注明。

## 界面

### 报告页

测试结束后新增一个「人工复核」卡片：

```
人工复核    [✓ 通过]  [✗ 未通过]  [— 无效]
（未标记 / 已标记：未通过 · 来源：测试人员开单 BUG-123 · 昨天）
单子：[粘贴框]   单号：[输入框]
[分析为什么没测出来]      ← 只在 agent 通过而人工未通过时出现
```

已标过、且已发起分析的，卡片展开显示分析结论摘要 + 链接到复盘详情。

顺带改一处：`partial` 结论现在页面上不显眼，人工复核卡片里要显式写出「agent 判：部分无法验证」。

### 待测试页 / 验收测试列表

行尾加一个小角标：`真·通过` / `真·未通过` / `无效`。没标的显示一个淡色的「待复核」。
这是让「哪些测试还没人工验过」一眼可见的主要入口——回访速度直接决定这个闭环有没有价值。

### 复盘页（新增，侧栏「复盘」）

- 顶部四个数：样本数、漏报率、误报率、分析覆盖率。
- 混淆矩阵表格，行是 agent 结论、列是人工真值。
- 类别分布：横向条形图，按次数排序。
- 清单：时间、issue、类别、置信度、报告链接、分析结论。

筛选：仓库（必选，默认第一个）、时间范围、档位。

## 决策记录（2026-11-19）

| # | 问题 | 采用 |
|---|---|---|
| 1 | 真值记在哪一层 | 逐次测试。issue 级汇总后续从逐次记录推出 |
| 2 | 分析怎么跑 | 在仓库里复现：独立分析 run，锁定原测试的 `pin_sha`，两阶段（复现 → 归因） |
| 3 | 结论做什么 | 只存下来做复盘。不回灌提示词、不生成回归用例、不回写 Linear |
| 4 | 一致时要不要分析 | 不分析，直接拒绝，避免开销 |
| 5 | 分析算不算测试 | 算 run（复用基础设施），但 `kind='postmortem'`，不进测试列表和统计 |

## 实施拆分

**P1 — 标记真值（最小可用）**
1. `runs.kind` 列 + `run_truths` 表 + store 方法
2. `PUT/GET/DELETE /api/runs/{id}/truth`
3. 报告页人工复核卡片
4. 列表角标

做完这步就能开始积累样本，可以先观察一段时间再决定要不要建复盘页。

**P2 — 指标页**（已完成）
5. `/api/review/stats`（混淆矩阵、比率、复核覆盖率）
6. 复盘页：混淆矩阵 + 样本清单

实现说明：矩阵按语义着色（命中/真放行绿、漏报/误报红、存疑黄），不用数量深浅——
数字本身已经写在格子里，再用色阶编码同一信息就是浪费唯一的颜色通道。
筛选按仓库 + 档位 + 时间范围（默认近 90 天）。

**P3 — 分析与复盘**
7. `postmortems` 表、`kind` 分流、`PostmortemNext` 流水线
8. `reproduce` / `attribute` 两个阶段的提示词和 schema
9. 发起分析、详情、取消接口
10. 复盘页的类别分布和详情

**P4 — 可选**
- 分析阶段加一轮红队复核（复用交叉模型）
- 结论一键生成回归用例（`createCase` 已有，接个按钮）
- 按类别把改进写进计划提示词（这次明确不做，数据结构已留位）

## 风险

| 风险 | 说明 | 处理 |
|---|---|---|
| worktree 已删 | 成功的测试会删 worktree，分析要按 `pin_sha` 重建 | agent 的 bare 缓存里有这个 commit 就行；分支被 force-push 后可能取不到，降级为只读分析 |
| 采样偏差 | 只有人工回访过的测试有真值 | 页面上固定显示样本数和范围，不外推；列表用「待复核」角标推动回访 |
| 不复盘就断了 | 结论不自动回灌，没人看就等于没做 | 复盘页放在侧栏一级入口；列表角标让人知道有东西可看。是否值得自动化，等跑一段时间用数据说话 |
| 分析成本 | 每次分析是一次完整 agent 任务（含复现） | 只在结论不一致时允许发起；单次预算默认 30 分钟，可在设置里改 |
| 定性结论不可证伪 | 归因是模型判断 | `reproduce` 阶段必须先拿出可复现的证据；复现不出来的（`flaky`）单独归类 |
| 真值被随意标 | 标记影响指标 | 记录 `source` 和 `updated_at`；后续可加「只统计带单号的样本」这类严格模式 |
