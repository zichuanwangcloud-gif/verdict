<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { ArrowLeft, Sparkles, FileText, PencilLine, GitBranch, Server, Bot, Play, Eye, MessageSquare, Timer, Layers, Swords, MonitorSmartphone, ShieldCheck, Gavel, Compass, ClipboardCheck, Coins, Container } from 'lucide-vue-next'
import { tierMeta } from '../format'
import { api } from '../api'
import { go } from '../router'
import HarnessLogo from '../components/HarnessLogo.vue'

const repos = ref([])
const runtimes = ref([])
const err = ref('')
const busy = ref(false)

const mode = ref('linear')
const issueKey = ref('')
const manual = ref({ title: '', description: '' })
const preview = ref(null)
const previewErr = ref('')
const previewing = ref(false)

const repoId = ref('')
const branch = ref('')
const runtimeId = ref('')
const harness = ref('claude')
const tiers = ref([])
const tier = ref('B')
const custom = ref(null)
const approvePlan = ref(false)
const maxMinutes = ref(45)
const maxCost = ref('')

onMounted(async () => {
  try {
    ;[repos.value, runtimes.value, tiers.value] = await Promise.all([api.repos(), api.runtimes(), api.tiers()])
    custom.value = { ...preset('B'), tier: 'custom' }
    if (repos.value.length === 1) repoId.value = repos.value[0].id
    const online = runtimes.value.filter((r) => r.online)
    if (online.length >= 1) runtimeId.value = online[0].id
  } catch (e) {
    err.value = e.message
  }
})

const runtime = computed(() => runtimes.value.find((r) => r.id === runtimeId.value))

function preset(t) {
  return tiers.value.find((x) => x.tier === t) || {}
}
// 当前生效的开关：选了预设就看预设，选了自定义就看自定义面板。
const cfg = computed(() => (tier.value === 'custom' ? custom.value || {} : preset(tier.value)))
watch(tier, (t) => {
  if (t !== 'custom') maxMinutes.value = preset(t).max_minutes || 45
  else if (custom.value) maxMinutes.value = custom.value.max_minutes || 45
})

function features(c) {
  if (!c) return []
  const f = [{ icon: ClipboardCheck, text: c.brief_plan ? '简版计划' : '测试计划' }]
  if (c.static_only) f.push({ icon: FileText, text: '只做静态与命令行验证' })
  if (c.service) f.push({ icon: Container, text: '托管启动服务' })
  f.push({ icon: ShieldCheck, text: '执行验证' + (c.self_review ? '（自查）' : '') + (c.stability ? '（重复两次）' : '') })
  if (c.ui) f.push({ icon: MonitorSmartphone, text: c.ui === 'full' ? '全量 UI 走查' : '定向 UI 走查' })
  if (c.red_rounds) f.push({ icon: Swords, text: `红蓝对抗 ×${c.red_rounds}` + (c.cross_model ? '（交叉模型）' : '') })
  if (c.explore) f.push({ icon: Compass, text: '主动探索' })
  if (c.judge) f.push({ icon: Gavel, text: '裁决' })
  return f
}

const otherHarness = computed(() => (harness.value === 'claude' ? 'codex' : 'claude'))
const redInfo = computed(() => {
  const c = cfg.value
  if (!c || !c.red_rounds) return ''
  if (!c.cross_model) return '红队：同一模型新开会话'
  const h = harnessInfo.value.find((x) => x.name === otherHarness.value)
  const n = otherHarness.value === 'claude' ? 'Claude Code' : 'Codex'
  return h && h.installed && h.logged_in ? `红队：${n}（交叉模型）` : `红队：${n} 不可用，降级为同一模型新开会话`
})
const browserWarn = computed(() => cfg.value?.ui && runtime.value?.info && !runtime.value.info.browser?.available)
const harnessInfo = computed(() => runtime.value?.info?.harnesses || [])

function hInfo(name) {
  const h = harnessInfo.value.find((x) => x.name === name)
  if (!runtime.value) return { ok: true, text: '先选择运行时' }
  if (!h) return { ok: true, text: '尚未探测' }
  if (!h.installed) return { ok: false, text: '未安装' }
  if (!h.logged_in) return { ok: true, text: '未登录 · ' + (h.version || '') }
  return { ok: true, text: h.version || '可用' }
}

watch(runtimeId, () => {
  const usable = harnessInfo.value.filter((h) => h.installed && h.logged_in)
  if (usable.length && !usable.some((h) => h.name === harness.value)) harness.value = usable[0].name
})

async function doPreview() {
  previewErr.value = ''
  preview.value = null
  previewing.value = true
  try {
    preview.value = await api.issue(issueKey.value.trim())
  } catch (e) {
    previewErr.value = e.message
  } finally {
    previewing.value = false
  }
}

const canSubmit = computed(
  () =>
    repoId.value &&
    branch.value.trim() &&
    runtimeId.value &&
    hInfo(harness.value).ok &&
    (mode.value === 'linear' ? issueKey.value.trim() : manual.value.description.trim()),
)

async function submit() {
  err.value = ''
  busy.value = true
  try {
    const body = {
      repo_id: repoId.value,
      branch: branch.value.trim(),
      runtime_id: runtimeId.value,
      harness: harness.value,
      tier: tier.value,
      approve_plan: approvePlan.value,
      max_minutes: Number(maxMinutes.value) || 0,
      max_cost_usd: maxCost.value === '' ? 0 : Number(maxCost.value),
    }
    if (tier.value === 'custom') body.config = { ...custom.value, max_minutes: Number(maxMinutes.value) || 0 }
    if (mode.value === 'linear') body.issue_key = issueKey.value.trim()
    else body.manual_issue = manual.value
    const run = await api.createRun(body)
    go('/runs/' + run.id)
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <form class="page" @submit.prevent="submit">
    <div class="page-head">
      <div>
        <a href="/runs" class="row muted" style="gap: 4px; margin-bottom: 10px" @click.prevent="go('/runs')"><ArrowLeft :size="14" /> 返回</a>
        <div class="eyebrow"><Sparkles :size="12" /> New run</div>
        <h2>新建验收测试</h2>
        <p class="sub">告诉 agent 要验证什么、测哪段代码、在哪台机器上用哪个模型跑。</p>
      </div>
    </div>

    <section class="card stack" style="gap: 14px">
      <div class="card-title"><FileText :size="16" /> 1 · 需求</div>
      <div class="choice-grid">
        <button type="button" class="choice" :class="{ selected: mode === 'linear' }" @click="mode = 'linear'">
          <span class="choice-icon"><MessageSquare :size="17" /></span>
          <span>
            <div class="choice-title">Linear issue</div>
            <div class="choice-sub">自动读取描述与评论，结果回写为评论</div>
          </span>
        </button>
        <button type="button" class="choice" :class="{ selected: mode === 'manual' }" @click="mode = 'manual'">
          <span class="choice-icon"><PencilLine :size="17" /></span>
          <span>
            <div class="choice-title">手动输入</div>
            <div class="choice-sub">直接粘贴需求与验收标准，不回写</div>
          </span>
        </button>
      </div>

      <template v-if="mode === 'linear'">
        <div class="field">
          <label>issue 编号</label>
          <div class="input-group">
            <input v-model="issueKey" placeholder="ENG-123" />
            <button type="button" class="btn" :disabled="!issueKey.trim() || previewing" @click="doPreview">
              <Eye :size="15" /> {{ previewing ? '读取中…' : '预览' }}
            </button>
          </div>
        </div>
        <div v-if="previewErr" class="alert bad">{{ previewErr }}</div>
        <div v-if="preview" class="summary">
          <FileText :size="16" />
          <div class="stack" style="min-width: 0">
            <div><span class="issue-key">{{ preview.identifier }}</span><b>{{ preview.title }}</b></div>
            <div class="body text-2" style="max-height: 9em; overflow: hidden">{{ preview.description || '（无描述）' }}</div>
            <div class="hint">{{ preview.comments?.length || 0 }} 条评论会一并交给 agent</div>
          </div>
        </div>
      </template>
      <template v-else>
        <div class="field">
          <label>标题</label>
          <input v-model="manual.title" placeholder="可选" />
        </div>
        <div class="field">
          <label>需求背景与验收标准</label>
          <textarea v-model="manual.description" rows="8" placeholder="## 背景&#10;…&#10;&#10;## 验收标准&#10;1. …&#10;2. …" />
        </div>
      </template>
    </section>

    <section class="card stack" style="gap: 14px">
      <div class="card-title"><GitBranch :size="16" /> 2 · 代码</div>
      <div class="grid-2">
        <div class="field">
          <label>仓库</label>
          <select v-model="repoId">
            <option value="" disabled>选择仓库</option>
            <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
          </select>
          <span class="hint">没有？<a href="/repos" @click.prevent="go('/repos')">去添加仓库</a></span>
        </div>
        <div class="field">
          <label>分支</label>
          <input v-model="branch" placeholder="feature/login" class="mono" />
          <span class="hint">会被解析成具体 commit，报告绑定在 commit 上</span>
        </div>
      </div>
    </section>

    <section class="card stack" style="gap: 14px">
      <div class="card-title"><Layers :size="16" /> 3 · 测试档位</div>
      <div class="tier-grid">
        <button
          v-for="t in ['S', 'A', 'B', 'C', 'custom']"
          :key="t"
          type="button"
          class="tier-card"
          :class="['tier-' + tierMeta[t].tone, { selected: tier === t }]"
          @click="tier = t"
        >
          <span class="tier-badge">{{ tierMeta[t].short }}</span>
          <span class="tier-name">{{ tierMeta[t].name }}</span>
          <span class="tier-line">{{ tierMeta[t].tagline }}</span>
          <span v-if="t !== 'custom' && preset(t).max_minutes" class="tier-time">≤ {{ preset(t).max_minutes }} 分钟</span>
        </button>
      </div>

      <div class="feature-chips">
        <span v-for="(f, i) in features(cfg)" :key="i" class="chip"><component :is="f.icon" /> {{ f.text }}</span>
      </div>

      <div v-if="tier === 'custom' && custom" class="switch-grid">
        <label class="switch"><input v-model="custom.static_only" type="checkbox" /><span>只做静态与命令行验证</span></label>
        <label class="switch"><input v-model="custom.brief_plan" type="checkbox" /><span>简版计划</span></label>
        <label class="switch"><input v-model="custom.service" type="checkbox" :disabled="custom.static_only" /><span>托管启动服务</span></label>
        <label class="switch"><input v-model="custom.self_review" type="checkbox" /><span>蓝队自查</span></label>
        <label class="switch"><input v-model="custom.stability" type="checkbox" /><span>关键检查重复两次</span></label>
        <label class="switch"><input v-model="custom.explore" type="checkbox" /><span>主动探索</span></label>
        <label class="switch"><input v-model="custom.judge" type="checkbox" /><span>裁决</span></label>
        <label class="switch"><input v-model="custom.cross_model" type="checkbox" :disabled="!custom.red_rounds" /><span>红队用交叉模型</span></label>
        <div class="field">
          <label>UI 走查</label>
          <select v-model="custom.ui" :disabled="custom.static_only">
            <option value="">不做</option>
            <option value="targeted">定向（只走验收涉及的页面）</option>
            <option value="full">全量（含控制台、多种宽度）</option>
          </select>
        </div>
        <div class="field">
          <label>红蓝对抗轮数</label>
          <input v-model.number="custom.red_rounds" type="number" min="0" max="5" />
        </div>
      </div>

      <div class="grid-2">
        <div class="field">
          <label><Timer :size="12" /> 时间预算（分钟）</label>
          <input v-model="maxMinutes" type="number" min="5" max="1440" />
          <span class="hint">按各阶段耗时累计；不够时跳过可选阶段并在报告里注明</span>
        </div>
        <div class="field">
          <label><Coins :size="12" /> 费用上限（美元，可选）</label>
          <input v-model="maxCost" type="number" min="0" step="0.5" placeholder="不限" />
          <span class="hint">只有 Claude Code 会报告费用</span>
        </div>
      </div>
      <label class="switch"><input v-model="approvePlan" type="checkbox" /><span>计划生成后暂停，由我确认后再执行</span></label>
    </section>

    <section class="card stack" style="gap: 14px">
      <div class="card-title"><Server :size="16" /> 4 · 执行环境</div>
      <div v-if="!runtimes.length" class="hint">还没有运行时，<a href="/runtimes" @click.prevent="go('/runtimes')">先接入一台</a>。</div>
      <div class="choice-grid">
        <button
          v-for="r in runtimes"
          :key="r.id"
          type="button"
          class="choice"
          :class="{ selected: runtimeId === r.id }"
          @click="runtimeId = r.id"
        >
          <span class="choice-icon"><Server :size="17" /></span>
          <span style="min-width: 0">
            <div class="choice-title row" style="gap: 8px"><span class="dot" :class="{ on: r.online }" /> {{ r.name }}</div>
            <div class="choice-sub">{{ r.online ? r.info?.hostname || '在线' : '离线 · 上线后执行' }}</div>
          </span>
        </button>
      </div>

      <div class="label" style="margin-top: 4px"><Bot :size="12" /> Agent</div>
      <div class="choice-grid">
        <button
          v-for="h in ['claude', 'codex']"
          :key="h"
          type="button"
          class="choice"
          :class="{ selected: harness === h, disabled: !hInfo(h).ok }"
          :disabled="!hInfo(h).ok"
          @click="harness = h"
        >
          <HarnessLogo :name="h" />
          <span>
            <div class="choice-title">{{ h === 'claude' ? 'Claude Code' : 'Codex' }}</div>
            <div class="choice-sub">{{ hInfo(h).text }}</div>
          </span>
        </button>
      </div>

      <div v-if="redInfo" class="hint row" style="gap: 6px"><Swords :size="12" /> {{ redInfo }}</div>
      <div v-if="browserWarn" class="alert warn">
        <MonitorSmartphone :size="16" />
        <span class="body">所选运行时没有可用的无头浏览器（{{ runtime.info.browser?.detail || '未探测' }}）。UI 走查会尝试降级，界面条目可能判为无法验证。</span>
      </div>
    </section>

    <div v-if="err" class="alert bad">{{ err }}</div>
    <div class="row">
      <button class="btn primary" :disabled="!canSubmit || busy"><Play :size="15" /> {{ busy ? '提交中…' : '开始测试' }}</button>
      <button type="button" class="btn ghost" @click="go('/runs')">取消</button>
    </div>
  </form>
</template>
