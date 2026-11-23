<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  ArrowLeft, RotateCcw, Square, GitBranch, GitCommitHorizontal, Server, Clock, Coins, ExternalLink,
  Sparkles, CircleCheck, CircleX, CircleHelp, Scale, ChevronRight, TriangleAlert, FileDiff, ScrollText,
  MessageSquare, Send, Braces, Radio, Swords, Bug, Images, Workflow, Layers, Filter, ShieldCheck, Snowflake, PlayCircle,
} from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import {
  acText, acTone, fmtTime, fmtDuration, harnessName, isLive, tierMeta, confidenceText, severityText,
  methodText, challengeKindText, roleMeta,
} from '../format'
import StatusPill from '../components/StatusPill.vue'
import VerdictRing from '../components/VerdictRing.vue'
import LogStream from '../components/LogStream.vue'
import StageTimeline from '../components/StageTimeline.vue'
import ArtifactGallery from '../components/ArtifactGallery.vue'
import PlanPanel from '../components/PlanPanel.vue'
import TruthPanel from '../components/TruthPanel.vue'

const props = defineProps({ id: String })
const run = ref(null)
const logs = ref([])
const err = ref('')
const actionErr = ref('')
const canResume = computed(() => {
  const r = run.value
  if (!r || r.regression_id || r.status === 'canceled') return false
  return r.status === 'error' || (r.status === 'completed' && r.coverage?.degraded)
})
const busy = ref(false)
const closed = ref(new Set())
const stageFilter = ref('')
let lastSeq = 0
let timer
let loading = false

const live = computed(() => isLive(run.value))
const legacy = computed(() => run.value && !run.value.tier)
const stages = computed(() => run.value?.stages || [])

const plan = computed(() => {
  const p = stages.value.find((s) => s.role === 'plan' && s.status === 'ok')
  return p?.output || null
})

const shownLogs = computed(() => (stageFilter.value ? logs.value.filter((l) => l.stage === stageFilter.value) : logs.value))

const currentStage = computed(() => stages.value.find((s) => s.id === run.value?.current_stage))

// shotState 汇总各阶段对截图的判定，文件名 → 原因。
// 优先级：独立质检 > 机器体检 > UI 阶段自检（与后端 shotVerdict 保持一致）。
// hidden 是最终判定被遮挡的截图：前端不再展示，只留一行文字说明（自动清理）。
const shotState = computed(() => {
  const issues = {}
  const hidden = {}
  const settled = new Set() // 已有更高优先级的判定，低优先级不再改写
  const shots = stages.value.find((s) => s.role === 'shots' && s.status === 'ok')
  for (const r of shots?.output?.shot_reviews || []) {
    settled.add(r.file)
    if (r.verdict === 'unusable' || r.occluded) {
      const why = [...(r.problems || [])].join('；') || (r.occluded ? '画面被遮挡' : '质检判定不可用')
      issues[r.file] = why
      if (r.occluded) hidden[r.file] = why
    }
  }
  for (const st of stages.value) {
    for (const i of st.artifact_issues || []) {
      if (!i.file || settled.has(i.file)) continue
      settled.add(i.file)
      issues[i.file] = i.reason
    }
  }
  const ui = stages.value.find((s) => s.role === 'ui' && s.status === 'ok')
  for (const s of ui?.output?.shots || []) {
    if (!s.file || settled.has(s.file)) continue
    settled.add(s.file)
    const hasIssue = (s.issues || []).length > 0
    // covered 必须伴随具体遮挡说明（或同时不可读）才作数，避免模型误填 covered 把整批截图清空。
    if (s.covered && (hasIssue || s.readable === false)) {
      const why = hasIssue ? s.issues.join('；') : '画面被遮挡'
      issues[s.file] = why
      hidden[s.file] = why
    } else if (s.readable === false) {
      issues[s.file] = '内容不清晰'
    }
  }
  return { issues, hidden }
})
const shotIssues = computed(() => shotState.value.issues)
const hiddenShots = computed(() => shotState.value.hidden)
// 展示计数只算真正会出现的截图。
const shotCount = computed(() =>
  (run.value?.artifacts || []).filter((a) => a.content_type.startsWith('image/') && !hiddenShots.value[a.name]).length,
)
const liveLabel = computed(() => {
  const r = run.value
  if (!r) return ''
  if (r.status === 'queued') return '等待运行时空闲'
  if (r.status === 'preparing' || r.status === 'dispatched') return '准备工作区'
  if (r.status === 'awaiting_approval') return '等待你确认测试计划'
  if (r.status === 'finalizing') return '收尾：停止服务、检查源码改动'
  const s = currentStage.value
  if (!s) return 'agent 正在工作'
  return (roleMeta[s.role]?.name || s.role) + (s.round ? ` · 第 ${s.round} 轮` : '')
})

async function load() {
  if (loading) return
  loading = true
  try {
    run.value = await api.run(props.id)
    for (;;) {
      const batch = await api.logs(props.id, lastSeq)
      if (!batch.length) break
      logs.value.push(...batch)
      lastSeq = batch[batch.length - 1].seq
      if (batch.length < 2000) break
    }
    err.value = ''
    if (!live.value) clearInterval(timer)
  } catch (e) {
    err.value = e.message
  } finally {
    loading = false
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 2000)
})
onUnmounted(() => clearInterval(timer))

async function act(fn) {
  actionErr.value = ''
  busy.value = true
  try {
    return await fn()
  } catch (e) {
    actionErr.value = e.message
  } finally {
    busy.value = false
  }
}
async function cancel() {
  if (!confirm('确定取消这次测试？')) return
  await act(() => api.cancelRun(props.id))
  load()
}
async function approve(note) {
  await act(() => api.approveRun(props.id, note))
  load()
}
async function rerun() {
  const r = await act(() => api.rerun(props.id))
  if (r) go('/runs/' + r.id)
}
const addedToBaseline = ref(false)
async function addToBaseline() {
  if (!run.value?.repo_id) return
  await act(async () => {
    await api.createCase(run.value.repo_id, run.value.id, 'normal')
    addedToBaseline.value = true
  })
}

async function resume() {
  const r = await act(() => api.resumeRun(props.id))
  if (r?.id) go('/runs/' + r.id)
}

async function writeBack() {
  const r = await act(() => api.writeBack(props.id))
  if (r) run.value = { ...run.value, ...r }
}

function toggle(id) {
  const s = new Set(closed.value)
  s.has(id) ? s.delete(id) : s.add(id)
  closed.value = s
}

const acIcon = { pass: CircleCheck, fail: CircleX, unverifiable: CircleHelp, disputed: Scale }
const sevTone = { critical: 'bad', high: 'bad', medium: 'warn', low: '' }

// 父单整批的结论：四个状态 + 未完成（与后端 store 的取值一致）。
const setVerdictText = {
  pass: '通过', partial_pass: '部分通过', partial_fail: '部分未通过',
  fail: '未通过', partial: '部分无法验证', incomplete: '未完成',
}
const setVerdictTone = {
  pass: 'ok', partial_pass: 'warn', partial_fail: 'bad', fail: 'bad', partial: 'warn', incomplete: 'info',
}

function childText(c) {
  if (c.missing) return '未创建'
  if (!c.verdict) return c.status === 'completed' ? '无结论' : '未完成'
  return { pass: '通过', fail: '未通过', partial: '部分无法验证' }[c.verdict] || c.verdict
}
function childTone(c) {
  if (c.missing || !c.verdict) return 'warn'
  return { pass: 'ok', fail: 'bad', partial: 'warn' }[c.verdict] || ''
}

// 旧版测试（单阶段）的进度条
const legacySteps = computed(() => {
  const s = run.value?.status
  const idx = { queued: 0, dispatched: 0, preparing: 1, running: 2, completed: 3, error: 3, canceled: 3 }[s] ?? 0
  const labels = ['排队', '准备工作区', 'Agent 测试', s === 'completed' ? '得出结论' : s === 'canceled' ? '已取消' : s === 'error' ? '出错' : '得出结论']
  return labels.map((label, i) => ({ label, state: i < idx ? 'done' : i === idx ? (i === 3 ? (s === 'completed' ? 'done' : 'fail') : 'now') : '' }))
})
</script>

<template>
  <div class="page">
    <a href="/runs" class="row muted" style="gap: 4px" @click.prevent="go('/runs')"><ArrowLeft :size="14" /> 验收测试</a>
    <div v-if="err && !run" class="alert bad">{{ err }}</div>

    <template v-if="run">
      <!-- 头部 -->
      <section class="card glow" :class="{ shimmer: live && run.status !== 'awaiting_approval' }">
        <div class="hero">
          <VerdictRing :run="run" />
          <div style="min-width: 0">
            <div class="row between">
              <div class="row" style="gap: 8px">
                <span v-if="run.tier" class="tier-tag" :class="'tier-' + (tierMeta[run.tier]?.tone || 'custom')">{{ tierMeta[run.tier]?.name || run.tier }}</span>
                <StatusPill :run="run" large />
              </div>
              <div class="row">
                <button v-if="live" class="btn sm danger" :disabled="busy" @click="cancel"><Square :size="13" /> 取消</button>
                <button v-if="run.status === 'completed' && run.verdict === 'pass' && run.issue_key !== 'MANUAL'" class="btn sm" :disabled="busy || addedToBaseline" @click="addToBaseline"><ShieldCheck :size="13" /> {{ addedToBaseline ? '已加入基线' : '加入回归基线' }}</button>
                <button v-if="canResume" class="btn sm primary" :disabled="busy" @click="resume"><PlayCircle :size="13" /> 从失败处继续</button>
                <button class="btn sm" :disabled="busy" @click="rerun"><RotateCcw :size="13" /> 重新测试</button>
              </div>
            </div>
            <div class="hero-title"><span class="issue-key">{{ run.issue_key }}</span>{{ run.issue.title }}</div>
            <div class="meta-chips">
              <span class="chip"><GitBranch /> {{ run.repo_name }} · <span class="mono">{{ run.branch }}</span></span>
              <a v-if="run.version_id" class="chip" :href="'/versions/' + run.version_id" @click.prevent="go('/versions/' + run.version_id)"><Snowflake /> 版本</a>
              <span v-if="run.commit_sha" class="chip mono" :title="run.commit_sha"><GitCommitHorizontal /> {{ run.commit_sha.slice(0, 10) }}</span>
              <span class="chip"><Server /> {{ harnessName(run.harness) }} @ {{ run.runtime_name }}</span>
              <span v-if="run.config?.red_rounds" class="chip"><Swords /> 红队 {{ harnessName(run.config.red_harness || run.harness) }}</span>
              <span class="chip" :title="'创建于 ' + fmtTime(run.created_at)"><Clock /> {{ fmtDuration(run.started_at, run.finished_at) }}</span>
              <span v-if="run.cost_usd" class="chip"><Coins /> ${{ run.cost_usd.toFixed(3) }}</span>
              <a v-if="run.issue.url" class="chip" :href="run.issue.url" target="_blank" rel="noopener"><ExternalLink /> Linear</a>
            </div>
          </div>
        </div>
        <div v-if="live" class="live-now"><span class="dot live" /> {{ liveLabel }}</div>
        <div v-if="legacy" class="steps" style="margin-top: 18px">
          <template v-for="(s, i) in legacySteps" :key="i">
            <span v-if="i" class="step-line" />
            <span class="step" :class="s.state"><span class="dot" /> {{ s.label }}</span>
          </template>
        </div>
      </section>

      <div v-if="actionErr" class="alert bad">{{ actionErr }}</div>
      <div v-if="run.error" class="alert bad"><TriangleAlert :size="16" /><span class="body">{{ run.error }}</span></div>
      <div v-if="run.coverage?.degraded" class="alert warn">
        <TriangleAlert :size="16" />
        <div class="stack" style="flex: 1">
          <b>覆盖强度降级{{ run.coverage.unjudged ? '：结论未经裁决' : '' }}</b>
          <span class="body">{{ run.coverage.reason }}</span>
          <span class="hint">这是模型网关故障导致的，不代表这个档位的验收强度。可以点「从失败处继续」补跑缺失的阶段。</span>
        </div>
      </div>
      <div v-if="run.parent_run_id" class="alert warn"><PlayCircle :size="16" /><span>这条测试续跑自 <a :href="'/runs/' + run.parent_run_id" @click.prevent="go('/runs/' + run.parent_run_id)">{{ run.parent_run_id }}</a>，已完成的阶段不会重跑。</span></div>
      <div v-if="run.set_parent_run_id" class="alert ok"><Layers :size="16" /><span>这是一次子单测试，属于父单批次 <a :href="'/runs/' + run.set_parent_run_id" @click.prevent="go('/runs/' + run.set_parent_run_id)">查看父单汇总</a>。</span></div>
      <div v-if="run.findings?.some((f) => f.severity === 'critical' || f.severity === 'high')" class="alert bad">
        <Bug :size="16" />
        <span class="body">另外发现 {{ run.findings.filter((f) => f.severity === 'critical' || f.severity === 'high').length }} 个严重/高危问题（不影响验收结论），见下方「额外发现」。</span>
      </div>
      <div v-if="run.source_modified" class="alert warn">
        <FileDiff :size="16" />
        <div class="stack" style="min-width: 0; flex: 1">
          <b>测试过程中被测源码发生了改动，结论可信度降低</b>
          <details>
            <summary><ChevronRight :size="14" class="chev" /> 查看 diff</summary>
            <pre>{{ run.diff }}</pre>
          </details>
        </div>
      </div>

      <!-- 父单：子单矩阵与整体结论 -->
      <section v-if="run.set_summary" class="card stack" style="gap: 12px">
        <div class="card-head" style="margin: 0">
          <div class="card-title"><Layers :size="16" /> 子单汇总</div>
          <span class="pill" :class="setVerdictTone[run.set_summary.overall] || ''">{{ setVerdictText[run.set_summary.overall] || run.set_summary.overall }}</span>
        </div>
        <p class="hint">
          父单只验证汇总要求与子单之间的集成；子单细节在各自报告里。覆盖
          {{ run.set_summary.children.length }}/{{ run.set_summary.children.length + (run.set_summary.uncovered || []).length }} 个子单。
        </p>
        <p v-if="!run.issue?.description?.trim()" class="hint">父单没有独立需求描述，整体结论仅来自集成验证与子单汇总。</p>
        <div v-for="c in run.set_summary.children" :key="c.issue_key" class="row between" style="gap: 8px; padding: 8px 0; border-top: 1px solid var(--line)">
          <div class="row" style="gap: 8px; min-width: 0">
            <span class="issue-key">{{ c.issue_key }}</span>
            <span class="muted" style="min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">{{ c.title }}</span>
          </div>
          <div class="row" style="gap: 8px">
            <span class="pill" :class="childTone(c)">{{ childText(c) }}</span>
            <a v-if="c.run_id" class="chip" :href="'/runs/' + c.run_id" @click.prevent="go('/runs/' + c.run_id)">报告</a>
            <span v-else class="muted" style="font-size: 12px">未创建</span>
          </div>
        </div>
        <div v-if="(run.set_summary.uncovered || []).length" class="hint">
          未覆盖：{{ run.set_summary.uncovered.map((c) => c.identifier).join('、') }}
        </div>
      </section>

      <div :class="{ 'report-grid': stages.length }">
        <div class="stack" style="gap: 20px; min-width: 0">
          <!-- 计划（等待确认时置顶） -->
          <PlanPanel v-if="plan && run.status === 'awaiting_approval'" :plan="plan" awaiting :busy="busy" @approve="approve" @cancel="cancel" />

          <!-- 人工复核：test 结束后的真值标记 -->
          <TruthPanel v-if="run.kind !== 'postmortem'" :run="run" @saved="(t) => (run.truth = t)" />

          <!-- 结论 -->
          <section v-if="run.acs?.length" class="card stack" style="gap: 16px">
            <div class="card-head" style="margin: 0">
              <div class="card-title"><Sparkles :size="16" /> {{ run.status === 'completed' ? 'AI 结论' : '当前结论' }}</div>
              <span v-if="run.status !== 'completed'" class="muted" style="font-size: 12px">随阶段推进实时更新</span>
            </div>
            <div v-if="run.summary" class="summary"><Sparkles :size="16" /><p class="body">{{ run.summary }}</p></div>
            <div class="ac-list">
              <div v-for="a in run.acs" :key="a.id" class="ac">
                <div class="ac-head" @click="toggle(a.id)">
                  <span class="ac-icon" :class="acTone[a.status] || 'warn'"><component :is="acIcon[a.status] || CircleHelp" :size="15" /></span>
                  <div style="flex: 1; min-width: 0">
                    <div class="row" style="gap: 8px">
                      <span class="ac-id">{{ a.id }}</span>
                      <span class="pill" :class="acTone[a.status] || 'warn'">{{ acText[a.status] || a.status }}</span>
                      <span v-if="a.method" class="chip">{{ methodText[a.method] || a.method }}</span>
                      <span v-if="a.confidence" class="conf" :class="'conf-' + a.confidence">置信度 {{ confidenceText[a.confidence] }}</span>
                      <span v-if="a.challenges?.length" class="chip"><Swords /> {{ a.challenges.length }} 次质疑</span>
                    </div>
                    <div style="margin-top: 4px">{{ a.criterion }}</div>
                  </div>
                  <ChevronRight :size="16" class="muted" :style="{ transform: closed.has(a.id) ? '' : 'rotate(90deg)', transition: 'transform .15s' }" />
                </div>
                <div v-if="!closed.has(a.id)" class="ac-body">
                  <div v-if="a.evidence">
                    <span class="label">证据</span>
                    <pre>{{ a.evidence }}</pre>
                  </div>
                  <div v-if="a.reasoning">
                    <span class="label">判断依据</span>
                    <p class="body text-2">{{ a.reasoning }}</p>
                  </div>
                  <div v-if="a.challenges?.length" class="duel">
                    <span class="label">红蓝对抗记录</span>
                    <div v-for="(c, i) in a.challenges" :key="i" class="duel-round">
                      <div class="duel-side red">
                        <div class="duel-head"><Swords :size="12" /> 红队 · 第 {{ c.round }} 轮 · {{ challengeKindText[c.kind] || c.kind }}<span v-if="c.proposed_status"> · 建议改判 {{ acText[c.proposed_status] }}</span></div>
                        <p class="body">{{ c.claim }}</p>
                        <p v-if="c.suggested_check" class="hint body">建议复核：{{ c.suggested_check }}</p>
                      </div>
                      <div class="duel-side blue" :class="{ empty: !c.response }">
                        <div class="duel-head">🛡 蓝队回应<span v-if="c.resolution"> · 复测后 {{ acText[c.resolution] }}</span></div>
                        <p class="body">{{ c.response || '没有得到复测回应' }}</p>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </section>

          <!-- 额外发现 -->
          <section v-if="run.findings?.length" class="card stack" style="gap: 12px">
            <div class="card-title"><Bug :size="16" /> 额外发现（{{ run.findings.length }}）<span class="muted" style="font-weight: 400; font-size: 12px">验收标准以外，不影响结论</span></div>
            <details v-for="(f, i) in run.findings" :key="i" class="finding" :class="'sev-' + f.severity">
              <summary>
                <span class="pill" :class="sevTone[f.severity]">{{ severityText[f.severity] || f.severity }}</span>
                <b>{{ f.title }}</b>
                <span class="muted" style="font-size: 12px; margin-left: auto">{{ roleMeta[f.source]?.name }}</span>
              </summary>
              <p class="body text-2">{{ f.detail }}</p>
              <pre v-if="f.evidence">{{ f.evidence }}</pre>
            </details>
          </section>

          <!-- 截图 -->
          <section v-if="run.artifacts?.length" class="card stack" style="gap: 12px">
            <div class="card-title"><Images :size="16" /> UI 走查截图（{{ shotCount }}）</div>
            <div v-if="run.pages?.length" class="pages">
              <div v-for="(p, i) in run.pages" :key="i" class="page-obs">
                <span class="mono">{{ p.url }}</span> <b>{{ p.title }}</b>
                <p v-if="p.observations" class="body text-2">{{ p.observations }}</p>
              </div>
            </div>
            <ArtifactGallery :run-id="run.id" :artifacts="run.artifacts" :issues="shotIssues" :hidden="hiddenShots" />
          </section>

          <!-- 计划（非等待状态） -->
          <details v-if="plan && run.status !== 'awaiting_approval'" class="card">
            <summary><ChevronRight :size="14" class="chev" /><Layers :size="14" /> 测试计划（{{ plan.items?.length || 0 }} 条）</summary>
            <PlanPanel :plan="plan" style="border: none; padding: 0; background: none" />
          </details>

          <!-- Linear 回写 -->
          <section v-if="run.issue.url" class="card row between">
            <div class="row">
              <span class="choice-icon"><MessageSquare :size="16" /></span>
              <div>
                <div class="choice-title">回写到 Linear</div>
                <div class="choice-sub">
                  <a v-if="run.linear_comment_url" :href="run.linear_comment_url" target="_blank" rel="noopener">已发布评论 ↗</a>
                  <span v-else-if="run.linear_error" style="color: var(--bad)">{{ run.linear_error }}</span>
                  <span v-else>{{ live ? '测试结束后自动回写' : '回写中…' }}</span>
                </div>
              </div>
            </div>
            <button v-if="!live && run.status !== 'canceled'" class="btn sm" :disabled="busy" @click="writeBack"><Send :size="13" /> 重新回写</button>
          </section>

          <!-- 执行流 -->
          <section class="card stack" style="gap: 12px">
            <div class="card-head" style="margin: 0">
              <div class="card-title"><ScrollText :size="16" /> Agent 执行流</div>
              <div class="row" style="gap: 8px">
                <span v-if="stageFilter" class="chip"><Filter /> {{ roleMeta[stages.find((s) => s.id === stageFilter)?.role]?.name }} <button class="link-x" @click="stageFilter = ''">×</button></span>
                <span v-if="live" class="pill live"><Radio :size="13" /> 实时</span>
                <span v-else class="muted" style="font-size: 12px">{{ shownLogs.length }} 条</span>
              </div>
            </div>
            <LogStream :logs="shownLogs" :live="live && !stageFilter" />
          </section>

          <details v-if="run.prompt" class="card">
            <summary><ChevronRight :size="14" class="chev" /><Braces :size="14" /> 交给 agent 的提示词</summary>
            <pre>{{ run.prompt }}</pre>
          </details>
          <details v-if="run.raw" class="card">
            <summary><ChevronRight :size="14" class="chev" /><Braces :size="14" /> 原始结构化输出</summary>
            <pre>{{ run.raw }}</pre>
          </details>
        </div>

        <!-- 阶段时间线 -->
        <aside v-if="stages.length" class="card side-timeline">
          <div class="card-title" style="margin-bottom: 12px"><Workflow :size="16" /> 测试流程</div>
          <StageTimeline :stages="stages" :selected="stageFilter" @select="(id) => (stageFilter = id)" />
          <p class="hint" style="margin-top: 10px">点击阶段可筛选它的执行日志</p>
          <details v-for="s in stages.filter((x) => x.spec?.prompt)" :key="s.id" class="prompt-peek">
            <summary><ChevronRight :size="12" class="chev" /> {{ roleMeta[s.role]?.name }}{{ s.round ? ' ' + s.round : '' }} 的提示词</summary>
            <pre>{{ s.spec.prompt }}</pre>
          </details>
        </aside>
      </div>
    </template>
  </div>
</template>
