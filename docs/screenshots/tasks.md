# 截图有效性任务清单

状态：⬜ 未开始 · 🔄 进行中 · ✅ 完成

| # | 任务 | 状态 |
|---|---|---|
| T1 | 设计文档与决策记录（`docs/screenshots/design.md`） | ✅ |
| T2 | `schemas.go`：`UISchema` 加 `shots`；新增 `ShotsSchema`、`Shot`、`ShotReview` 结构 | ✅ |
| T3 | `prompts.go`：`writeUITask` 加净场纪律与自检要求；新增 `writeShotsTask`；`buildPrompt` 注入疑似空白清单 | ✅ |
| T4 | `config.go` / `pipeline.go`：`RoleShots`、`ShotReview` / `ShotSelfCheck` 开关、档位预设、流水线插入、超时与预算 | ✅ |
| T5 | `consolidate.go`：合并 `shots` / `shot_reviews`；新增 `applyShotGate` 硬约束（`shots.go`） | ✅ |
| T6 | `artifacts.go` + `shotcheck.go`：PNG 体检（体积、纯色、尺寸、重复），结果写入 `ArtifactIssues` | ✅ |
| T7 | `protocol.go` / `store`：`StageResult.ArtifactIssues`；`stages.artifact_issues`、`runs.shots` 列 | ✅ |
| T8 | 前端 `ArtifactGallery.vue` + `RunDetail.vue`：不可用角标、原因展示、图例 | ✅ |
| T9 | Linear 评论：截图不可用提示 | ✅ |
| T10 | 测试：`shots_test.go`（硬约束/优先级/提示词）、`shotcheck_test.go`（PNG 体检）、e2e 全链路 | ✅ |
| T11 | 更新 README 与分档设计文档，开 PR | 🔄 |

## 与设计文档的差异（实现时调整）

1. **判定优先级定为「独立质检 > 机器体检 > UI 阶段自检」**，设计文档原写的是「自检 + 机器校验只作提示」。写测试时发现：UI 阶段自检的 `readable` 是必填布尔，模型总会填 `true`，等于永远给机器体检兜底，机器检查就没有生效的机会。所以把机器体检提到自检之前——客观测量比截图者自己的判断更可信，且仍然只用于「判定是否可当证据」，不改变验收结论本身。
2. **`shot_reviews` 不单独落库**，由 `testplan.ShotReviewOf(recs)` 从阶段输出推导。它是纯派生数据，存一份反而要维护一致性。
3. **`pages[].screenshots` 保持原样**，只新增顶层 `shots`，旧数据和既有测试无需迁移。
4. **`shots` 阶段在没有截图时直接跳过并写明原因**，不阻塞流水线；`ui` 阶段本身失败时不额外记跳过，避免噪音。
5. **机器体检的重复截图检测按「本阶段内」比较**，跨阶段不比较——不同阶段用同一张图并不算错。
