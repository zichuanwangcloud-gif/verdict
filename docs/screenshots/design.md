# 截图有效性设计

## 目标

现在 UI 走查产出的截图，只以文件名的形式出现在 `pages[].screenshots` 和 `acs[].evidence` 的文本里。**没有任何一个环节真正打开过这些图片**，所以画面上的问题——弹窗盖住正文、白屏、骨架屏还没消失、缩放到看不清字——全都没人发现，报告却照样标「通过」。

要解决的不是「多截几张」，而是三件事：

1. **截出来能看**：截图前把画面收拾干净，别把 loading、toast、意外遮挡截进去。
2. **截完有人看**：截图后立刻回看，确认可读、可辨、能支撑结论。
3. **看了要算数**：一张不可用的截图不能充当证据，平台在合成结论时强制拦下。

## 现状诊断

| # | 缺口 | 位置 | 后果 |
|---|---|---|---|
| 1 | 提示词只要求「关键状态都要截图」，没有净场要求 | `internal/testplan/prompts.go` `writeUITask` | 动画未结束、遮挡层未关就截 |
| 2 | 截图内容从未进入任何模型 | `prompts.go` 全文；`consolidate.go` 只解析文件名 | 红队、裁决只能读到「截图 home.png」这句文本 |
| 3 | 平台侧零校验 | `internal/agent/artifacts.go` `uploadNew` 只跳过 `size == 0` | 全白图、纯色图、重复图都算有效产物 |
| 4 | 结论不区分截图可用性 | `consolidate.go` 硬约束只检查「证据为空」 | 证据写着「见 xxx.png」而图是空白，仍判 pass |

关键前提已确认：UI 阶段和评审阶段的 harness 都以跳过权限 / 绕过沙箱的方式运行（`--dangerously-skip-permissions`、`--dangerously-bypass-approvals-and-sandbox`），产物目录 `VERDICT_ARTIFACTS` 在 agent 的临时目录下，**模型有读图能力，只是没人让它读**。

## 方案

分四层，按成本从低到高。前三层所有带 UI 的档位都开，第四层只在 A/S 开。

### 1. 净场规范（提示词，零成本）

`writeUITask` 增加「截图纪律」，逐条写死：

- 截图前等页面稳定：动画、过渡、请求未结束时不要截；必要时等 300–500ms 或等 loading 元素消失。
- **遮挡分流**：待验证对象本身是弹窗/抽屉/对话框时，截「打开」一张，再关掉截「关闭后」一张作为基线，文件名体现状态；只是意外遮挡（引导层、公告条、遮罩）时，先关掉再截。
- 不截 loading、骨架屏、进度条、toast（除非它就是验收对象）。
- 固定视口：桌面 1280×800，移动 390×844；超长页面分段截，不要 `full_page` 把内容压到看不清。
- 文件名带语义：`ac-3-dialog-open.png`、`ac-3-form-error.png`，能看出这张图证明什么。
- 每张图都要在输出里登记用途，不允许「截了但没说为什么」。

### 2. 结构化截图元信息（协议，低成本）

`UISchema` 顶层新增 `shots` 数组，`pages[].screenshots` 保持原样（兼容旧数据和现有测试）。每张截图登记：

```go
type Shot struct {
    File     string   `json:"file"`
    URL      string   `json:"url"`
    State    string   `json:"state"`    // 这张图对应的界面状态，如「弹窗打开」「表单报错」
    Purpose  string   `json:"purpose"`  // 用来证明哪条验收标准
    Covered  bool     `json:"covered"`  // 画面是否被遮挡
    Readable bool     `json:"readable"` // 内容是否清晰可辨
    Issues   []string `json:"issues"`   // 自检发现的问题
}
```

`Covered` / `Readable` 由模型自检后填写，是第四层质检的输入，也是第 5 层硬约束的依据之一。

### 3. 截图后自检（提示词 + 读图）

同一个 UI 会话里，截完就用读图能力回看，规则：

- 逐张确认：关键区域是否可见、文字是否可辨、有没有被意外遮挡、有没有停在半渲染状态。
- 不达标就重截（收拾画面后再截），不要带着坏图往下走。
- 确实无法截好的（比如必须登录、必须特定数据），把原因写进 `shots[].issues`，对应验收标准判 `unverifiable`，**不要用坏图凑一个 pass**。
- 张数上限 12 张，超出部分优先保留每条 AC 的主证据图。

成本：每张图约 1–2k token，上限 12 张在可控范围。

### 4. 机器校验（agent，零模型成本）

`uploadNew` 前对每张 PNG 做一次快速体检，用标准库 `image/png` 解码后降采样：

- 文件小于 8KB —— 大概率空白页；
- 采样后唯一颜色数 ≤ 2 —— 纯色 / 近乎空白；
- 长或宽小于 200px —— 尺寸异常；
- 与本阶段上一张图的哈希相同 —— 重复截图。

结论**只作提示，不做否决**（深色主题的纯色页会被误判）。结果写进 `StageResult.ArtifactIssues`，平台在推进下一阶段时把它拼进提示词：「以下截图疑似空白，不要当作证据」；同时存进 `stages.artifact_issues`，前端在画廊里给这几张图打标。

### 5. 平台硬约束（合成结论时）

`consolidate.go` 在既有的「没有证据的 pass 降为 unverifiable」之后，再加一条同类硬约束 `applyShotGate`：

- 一条 AC 的 `evidence` 引用的截图，全部被判 `unusable`（自检 `Readable=false`、或质检阶段给出 `unusable`、或机器校验命中空白），且没有其他可用截图或非截图证据支撑 → **降为 `unverifiable`**，理由写明「截图不可用」。
- 有任何一张可用截图被引用 → 不降级，但把不可用的那几张记进 `findings`（severity `low`，标题「截图不可用」），便于修测试而不是修产品。

匹配方式：按文件名在 `evidence` 文本里做包含匹配，一张图被多处引用时逐条判断。规则简单、可单测，不引入模型判断。

### 6. 独立截图质检阶段 `shots`（A/S 档）

第 3 层的自检有个结构性缺陷：**截图的人和判断截图好坏的人是同一个会话**，容易自我说服。所以 A/S 档再加一个独立阶段：

- 角色 `shots`，流水线位置 `ui → shots → red…`，新开会话。
- 提示词给全部截图清单（文件名、URL、state、purpose）和对应的 AC 证据，要求逐张读图复核，输出 `shot_reviews[]`：`file`、`verdict`（usable / unusable）、`occluded`、`readable`、`problems[]`、`suggest`。
- 质检只看图，不改结论；它的 `unusable` 喂给第 5 层的硬约束。
- 截图超过 16 张时，优先质检被 AC 证据引用的那些。

失败策略：`shots` 失败按可选阶段处理，跳过并记一条说明，**不阻塞流水线**；此时只靠自检 + 机器校验，覆盖强度在报告的 `coverage` 里体现。

### 7. 展示与回写

- 前端 `ArtifactGallery`：可用的图正常显示，被判定不可用的加角标和原因（hover 显示 `issues`），机器校验疑似空白的标「疑似空白」。
- 报告与 Linear 评论：AC 证据里引用的截图附可用性标记；「截图不可用」的额外发现单独列出。

## 档位与开关

| 开关 | C 快速 | B 标准 | A 深度 | S 对抗 |
|---|---|---|---|---|
| 截图纪律 + 结构化 `shots` | —（无 UI） | ✅ | ✅ | ✅ |
| UI 阶段读图自检 | — | ✅ | ✅ | ✅ |
| 机器校验 | — | ✅ | ✅ | ✅ |
| 独立质检阶段 `shots` | — | — | ✅ | ✅ |
| 硬约束 `applyShotGate` | — | ✅ | ✅ | ✅ |

新增 `Config` 字段：

```go
ShotReview   bool `json:"shot_review"`   // 独立质检阶段
ShotSelfCheck bool `json:"shot_self_check"` // UI 阶段读图自检（成本敏感时可关）
```

`ShotSelfCheck` 关掉时，提示词里也要明确「不要承诺自己看过图」，避免模型在 evidence 里虚构回看动作。

## 数据结构与落点

| 改动 | 文件 |
|---|---|
| `Shot` 结构、`UISchema` 加 `shots`、新增 `ShotsSchema` | `internal/testplan/schemas.go` |
| 净场规范、自检要求 | `internal/testplan/prompts.go` `writeUITask`、新增 `writeShotsTask` |
| `RoleShots`、`Config.Shots`、流水线插入、超时与预算 | `internal/testplan/pipeline.go`、`config.go` |
| 质检结果合并、`applyShotGate` | `internal/testplan/consolidate.go` |
| PNG 体检 | `internal/agent/artifacts.go` |
| `ArtifactIssues` 传输与存储 | `internal/protocol/protocol.go`、`internal/server/store/store.go`（`stages` 加列，走现有迁移机制） |
| 下一阶段提示词注入疑似空白清单 | `internal/testplan/prompts.go` `buildPrompt` |
| 画廊标记 | `web/src/components/ArtifactGallery.vue` |

`Result` 增加 `Shots []ShotReview`，与 `Pages` 并列。

## 流水线变化

```
plan ─▶ service ─▶ verify ─▶ ui ─▶ shots ─▶ ( red ─▶ recheck )×N ─▶ explore ─▶ judge ─▶ 收尾
```

- `shots` 只在 `ui` 成功且 `ShotReview` 开启时执行；`ui` 被跳过时 `shots` 一并跳过。
- 超时：默认 12 分钟，最短 3 分钟（`minMinutes`）。
- 预算不足时跳过，理由写「因预算跳过」。

## 验收标准

1. B 档及以上，UI 阶段提示词包含净场纪律，且明确要求弹窗场景截「打开 + 关闭后」两张。
2. UI 阶段输出里每张截图都有 `state` 和 `purpose`；缺登记时平台记一条提示。
3. 一张全白 PNG 上传后，`artifact_issues` 命中「疑似空白」，下一阶段提示词中出现该文件名。
4. 某条 AC 的 `evidence` 只引用了被判 `unusable` 的截图，结论由 `pass` 降为 `unverifiable`，理由写明「截图不可用」。
5. 同一条 AC 另有一张可用截图时，结论保持不降级。
6. A 档以上，流水线出现 `shots` 阶段，且它的 `unusable` 能影响第 4 条。
7. `shots` 阶段失败时流水线继续，`coverage.degraded` 为真、理由写明。
8. 现有 e2e 与单测全绿；`screenshots` 旧格式仍可解析。

## 范围外

- 不做截图的像素级视觉回归（不引入基准图比对）。
- 不自动修复页面问题，只报告。
- 不做截图 OCR 提取文本。

## 决策记录（2026-11-18）

| # | 问题 | 采用 | 理由 |
|---|---|---|---|
| 1 | 是否改 `screenshots` 结构 | 保留为文件名数组，另加 `shots` | 改动面小，旧数据和测试不受影响 |
| 2 | 机器校验能否否决 | 只提示不否决 | 深色纯色页、渐变页会误判，否决权交给模型 |
| 3 | 自检放 UI 阶段还是独立阶段 | 都做：B 档自检，A/S 加独立阶段 | 自检便宜但同源；独立阶段能真正「换个人看」 |
| 4 | 质检失败是否阻塞 | 不阻塞，只降覆盖强度 | 与现有可选阶段的失败策略保持一致 |
| 5 | 硬约束的匹配方式 | 文件名包含匹配 | 可单测、无模型依赖；误判风险低 |
| 6 | 12 张 / 16 张上限 | 自检 12，质检 16 | 兼顾 token 成本与覆盖 |

## 修订：遮挡截图的自动清理与放大查看（2026-11-23）

上线后发现两个问题：被遮挡的截图仍以「不可用」角标显示在报告页；点击截图放大后，图片跑到视口之外，页面放大时更明显。排查后还发现一个更严重的隐性缺陷。

### 根因

1. **质检阶段的结论根本没被采纳**：`pipeline.go` 的 `schemaFor` 没有 `RoleShots` 分支，质检阶段拿到的是 `ReportSchema`，输出成了 `acs` 而不是 `shot_reviews`；平台按 `shot_reviews` 解析，等于白跑一个阶段。
2. **自检的 `covered` 被普遍误填**：模型把它理解成「这条验收标准已被覆盖」，12 张正常截图全部填了 `true`，导致前端把所有图都标成「画面被遮挡」。
3. **放大层的定位被祖先元素劫持**：报告页的卡片有 `backdrop-filter: blur(12px)`，它会成为 `position: fixed` 的包含块；`.lightbox` 于是相对整张卡片（高度 5000+ px）定位，图片落在视口之外。

### 修复

- `schemaFor` 补上 `RoleShots → ShotsSchema`，质检结论正式进入 `shotVerdict` / `applyShotGate`。
- 提示词收紧 `covered` 语义：只有遮挡物确实盖住要展示的内容才填 `true`，且必须在 `issues` 写明被什么挡住；平台侧同步要求 `covered` 伴随 `issues` 或不可读，才判为遮挡。
- UI 阶段新增「净场检查」：每张截图前用 `browser_evaluate` 检查 toast / 通知 / 弹窗 / 遮罩等遮挡物，能关就关、能等就等，仍盖住内容的不截并判 `unverifiable`。
- 质检阶段明确遮挡判定口径：看全图、边角也算；遮挡物本身就是被验证对象时填 `false`；`occluded=true` 必须 `unusable`；清单里每张都要有结论。
- 前端自动清理：最终判定被遮挡的截图不再渲染缩略图，只在画廊下方折叠显示「已自动隐藏 N 张被遮挡的截图」及原因；其他不可用截图保留角标。
- 放大层 `Teleport` 到 `body`，改为视口网格居中，图片用 `object-fit: contain` 配合 `vw/vh` 上限，页面缩放后仍完整可见；支持 Esc 关闭、打开时锁定页面滚动。
