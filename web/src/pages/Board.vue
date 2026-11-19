<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  Sparkles, Search, RefreshCw, Play, Loader, Inbox, UserRound, Users, CircleCheck,
  CircleX, CircleAlert, Clock, Ban, Hand, TriangleAlert, GitCommitHorizontal, ChevronRight,
  ListChecks, Filter, Bot, Server, Layers, Timer,
} from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { ago, harnessName, tierMeta, isLive } from '../format'

const repos = ref([])
const runtimes = ref([])
const tiers = ref([])
const teams = ref([])
const rows = ref([])
const loading = ref(false)
const booted = ref(false)
const err = ref('')
const msg = ref('')
const saving = ref(false)
const launched = ref({}) // issueKey -> 启动结果（run id 或错误）

// 顶部统一配置（记忆到后端 settings）。
const cfg = ref({ repo_id: '', branch: '', runtime_id: '', harness: 'claude', tier: 'B', approve_plan: false, max_minutes: 45, max_cost_usd: null })
// 筛选条件。
const filter = ref({ team: '', states: [], assignee: 'all', q: '' })

const selected = ref(new Set())
const busy = ref(false)

let timer
onMounted(async () => {
  try {
    const [s, rp, rt, tl] = await Promise.all([api.settings(), api.repos(), api.runtimes(), api.tiers()])
    repos.value = rp
    runtimes.value = rt
    tiers.value = tl
    applyDefaults(s.board_defaults)
    await loadTeams()
    await load()
    await refreshRuns()
  } catch (e) {
    err.value = e.message
  } finally {
    booted.value = true
  }
  timer = setInterval(refreshRuns, 8000)
})
onUnmounted(() => clearInterval(timer))

function applyDefaults(d) {
  if (d) {
    cfg.value = { ...cfg.value, ...d }
    filter.value = { team: d.team || '', states: d.states || [], assignee: d.assignee || 'all', q: filter.value.q }
  }
  // 没记住过的项按默认值兜底，避免页面上出现空的档位 / 0 分钟。
  if (!cfg.value.harness) cfg.value.harness = 'claude'
  if (!cfg.value.tier) cfg.value.tier = 'B'
  if (!cfg.value.max_minutes) cfg.value.max_minutes = presetMinutes(cfg.value.tier)
}

function presetMinutes(t) {
  return tiers.value.find((x) => x.tier === t)?.max_minutes || 45
}

// 换档位时把时间预算带到该档位的预设值,避免沿用上一个档位的预算。
watch(() => cfg.value.tier, (t, prev) => {
  if (prev && cfg.value.max_minutes === presetMinutes(prev)) cfg.value.max_minutes = presetMinutes(t)
})

async function loadTeams() {
  try {
    teams.value = await api.boardTeams()
  } catch (e) {
    // 团队接口失败不影响列表：留空表示不限团队。
    teams.value = []
  }
}

const team = computed(() => teams.value.find((t) => t.key === filter.value.team))

// 状态筛选项按 Linear 的状态类型分组（未开始 / 进行中 / 已完成 / 已取消），比状态名稳定。
const stateGroups = [
  { type: 'unstarted', label: '未开始' },
  { type: 'started', label: '进行中' },
  { type: 'completed', label: '已完成' },
  { type: 'canceled', label: '已取消' },
  { type: 'backlog', label: '积压' },
]

function toggleState(t) {
  const i = filter.value.states.indexOf(t)
  if (i >= 0) filter.value.states.splice(i, 1)
  else filter.value.states.push(t)
  load()
}

const runtime = computed(() => runtimes.value.find((r) => r.id === cfg.value.runtime_id))
const harnessInfo = computed(() => runtime.value?.info?.harnesses || [])
const harnessReady = computed(() => {
  const h = harnessInfo.value.find((x) => x.name === cfg.value.harness)
  if (!runtime.value) return false
  if (!h) return true // 尚未探测，交给后端判断
  return h.installed && h.logged_in
})

async function load() {
  if (loading.value) return
  loading.value = true
  err.value = ''
  try {
    const res = await api.boardIssues({
      team: filter.value.team,
      states: filter.value.states,
      assignee: filter.value.assignee,
      q: filter.value.q,
      repoId: Number(cfg.value.repo_id) || 0,
    })
    rows.value = res.issues || []
    // 清掉已不在列表里的选中项
    const alive = new Set(rows.value.map((r) => r.identifier))
    selected.value = new Set([...selected.value].filter((k) => alive.has(k)))
  } catch (e) {
    err.value = e.message
    rows.value = []
  } finally {
    loading.value = false
  }
}

// 定时只刷新测试状态：查本地 runs 而不是重拉 Linear，避免把 Linear 的限流打满。
const liveByIssue = ref({}) // issueKey -> 最近一次测试（本地数据，比 Linear 返回的更新）

async function refreshRuns() {
  if (!booted.value || loading.value || busy.value) return
  try {
    const runs = await api.runs()
    const latest = {}
    const repoId = Number(cfg.value.repo_id) || 0
    for (const r of runs) {
      if (r.regression_id) continue
      if (repoId && r.repo_id !== repoId) continue
      if (latest[r.issue_key]) continue // runs 已按创建时间倒序
      latest[r.issue_key] = {
        run_id: r.id, status: r.status, verdict: r.verdict, summary: r.summary, error: r.error,
        branch: r.branch, commit_sha: r.commit_sha, acs: r.acs, created_at: r.created_at, finished_at: r.finished_at,
      }
    }
    liveByIssue.value = latest
  } catch {
    /* 静默：下次再试 */
  }
}

// 某行当前应显示的测试：本地最新优先，否则用列表接口带回的那次。
function lastRun(r) {
  return liveByIssue.value[r.identifier] || r.last_run
}

let searchTimer
function onSearch() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(load, 300)
}

async function saveDefaults(silent) {
  saving.value = true
  try {
    await api.saveSettings({
      board_defaults: {
        repo_id: Number(cfg.value.repo_id) || 0,
        branch: cfg.value.branch || '',
        runtime_id: Number(cfg.value.runtime_id) || 0,
        harness: cfg.value.harness,
        tier: cfg.value.tier,
        approve_plan: !!cfg.value.approve_plan,
        max_minutes: Number(cfg.value.max_minutes) || 0,
        max_cost_usd: cfg.value.max_cost_usd === '' || cfg.value.max_cost_usd == null ? null : Number(cfg.value.max_cost_usd),
        team: filter.value.team,
        states: filter.value.states,
        assignee: filter.value.assignee,
      },
    })
    if (!silent) flash('已记住当前配置')
  } catch (e) {
    err.value = e.message
  } finally {
    saving.value = false
  }
}

function flash(text) {
  msg.value = text
  setTimeout(() => (msg.value = ''), 2500)
}

const missing = computed(() => {
  const m = []
  if (!cfg.value.repo_id) m.push('仓库')
  if (!cfg.value.branch?.trim()) m.push('分支')
  if (!cfg.value.runtime_id) m.push('运行时')
  return m
})
const configReady = computed(() => missing.value.length === 0 && harnessReady.value)

function toggleSelect(key) {
  const s = new Set(selected.value)
  if (s.has(key)) s.delete(key)
  else s.add(key)
  selected.value = s
}

const allChecked = computed(() => rows.value.length > 0 && rows.value.every((r) => selected.value.has(r.identifier)))

function toggleAll() {
  selected.value = allChecked.value ? new Set() : new Set(rows.value.map((r) => r.identifier))
}

// 已经在排队或执行中的 issue 不再允许重复启动。
function running(r) {
  const l = lastRun(r)
  return l && isLive(l)
}

const launchable = computed(() => rows.value.filter((r) => selected.value.has(r.identifier) && !running(r)))

async function launch(keys) {
  if (!configReady.value || !keys.length) return
  busy.value = true
  err.value = ''
  try {
    const res = await api.startBatchRuns({
      issue_keys: keys,
      repo_id: Number(cfg.value.repo_id),
      branch: cfg.value.branch.trim(),
      runtime_id: Number(cfg.value.runtime_id),
      harness: cfg.value.harness,
      tier: cfg.value.tier,
      approve_plan: !!cfg.value.approve_plan,
      max_minutes: Number(cfg.value.max_minutes) || 0,
      max_cost_usd: cfg.value.max_cost_usd === '' || cfg.value.max_cost_usd == null ? 0 : Number(cfg.value.max_cost_usd),
    })
    const next = { ...launched.value }
    let started = 0
    for (const r of res.results || []) {
      next[r.issue_key] = r
      if (r.run_id && !r.error) started++
    }
    launched.value = next
    selected.value = new Set()
    flash(`已启动 ${started} 个测试`)
    saveDefaults(true)
    await refreshRuns()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}

function launchOne(r) {
  if (running(r)) {
    go('/runs/' + lastRun(r).run_id)
    return
  }
  launch([r.identifier])
}

function openLast(r) {
  const l = lastRun(r)
  if (l?.run_id) go('/runs/' + l.run_id)
}

// ---- 行内展示 ----

const stateTone = { started: 'info', unstarted: '', completed: 'ok', canceled: '', backlog: '' }

function runPill(last) {
  if (!last) return null
  if (last.status === 'completed') {
    return { text: { pass: '通过', fail: '未通过', partial: '部分无法验证' }[last.verdict] || '已完成',
      tone: { pass: 'ok', fail: 'bad', partial: 'warn' }[last.verdict] || '',
      icon: { pass: CircleCheck, fail: CircleX, partial: CircleAlert }[last.verdict] || CircleCheck }
  }
  const map = {
    queued: { text: '排队中', tone: 'info', icon: Clock },
    dispatched: { text: '已下发', tone: 'live', icon: Loader },
    preparing: { text: '准备中', tone: 'live', icon: Loader },
    running: { text: '执行中', tone: 'live', icon: Loader },
    awaiting_approval: { text: '待确认计划', tone: 'warn', icon: Hand },
    finalizing: { text: '收尾中', tone: 'live', icon: Loader },
    error: { text: '出错', tone: 'bad', icon: TriangleAlert },
    canceled: { text: '已取消', tone: '', icon: Ban },
  }
  return map[last.status] || null
}

const acOk = (acs) => (acs || []).filter((a) => a.status === 'pass').length
</script>

<template>
  <div class="page board">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Linear board</div>
        <h2>待测试</h2>
        <p class="sub">从 Linear 拉取 issue，选好仓库、分支和档位后一键开测。每行显示这条 issue 在这个仓库上最近一次的结论。</p>
      </div>
      <div class="row">
        <button class="btn ghost" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ spin: loading }" /> 刷新</button>
        <button class="btn" :disabled="!configReady || saving" @click="saveDefaults(false)">记住配置</button>
        <button class="btn primary" :disabled="!configReady || !launchable.length || busy" @click="launch(launchable.map((r) => r.identifier))">
          <Play :size="15" /> {{ busy ? '启动中…' : `测试选中${launchable.length ? '（' + launchable.length + '）' : ''}` }}
        </button>
      </div>
    </div>

    <div v-if="err" class="alert bad">{{ err }}</div>
    <div v-if="msg" class="alert ok"><CircleCheck :size="16" /> {{ msg }}</div>
    <div v-if="missing.length" class="alert warn">
      <Filter :size="16" />
      <span class="body">先在下面选好{{ missing.join('、') }}，才能开始测试。</span>
    </div>

    <!-- 统一配置：一次设定，之后每行一键启动 -->
    <section class="card stack board-cfg">
      <div class="cfg-grid">
        <div class="field">
          <label><GitCommitHorizontal :size="12" /> 仓库</label>
          <select v-model="cfg.repo_id" @change="load">
            <option :value="''" disabled>选择仓库</option>
            <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>分支</label>
          <input v-model="cfg.branch" class="mono" placeholder="feature/login" />
        </div>
        <div class="field">
          <label><Layers :size="12" /> 档位</label>
          <select v-model="cfg.tier">
            <option v-for="t in ['S', 'A', 'B', 'C']" :key="t" :value="t">{{ tierMeta[t]?.name }} · ≤{{ tiers.find((x) => x.tier === t)?.max_minutes }} 分</option>
          </select>
        </div>
        <div class="field">
          <label><Server :size="12" /> 运行时</label>
          <select v-model="cfg.runtime_id">
            <option :value="''" disabled>选择运行时</option>
            <option v-for="r in runtimes" :key="r.id" :value="r.id">{{ r.name }}{{ r.online ? '' : '（离线）' }}</option>
          </select>
        </div>
        <div class="field">
          <label><Bot :size="12" /> Agent</label>
          <select v-model="cfg.harness">
            <option value="claude">Claude Code</option>
            <option value="codex">Codex</option>
          </select>
        </div>
        <div class="field">
          <label><Timer :size="12" /> 时间预算（分钟）</label>
          <input v-model.number="cfg.max_minutes" type="number" min="5" max="1440" />
        </div>
      </div>
      <div class="row between">
        <label class="switch"><input v-model="cfg.approve_plan" type="checkbox" /><span>计划生成后由我确认</span></label>
        <span v-if="!harnessReady && runtime" class="hint bad">这个运行时上的 {{ harnessName(cfg.harness) }} 不可用，换一个或换运行时</span>
      </div>
    </section>

    <!-- 筛选 -->
    <section class="card stack filter-bar">
      <div class="row" style="gap: 10px; flex-wrap: wrap">
        <div class="field" style="min-width: 160px">
          <label>团队</label>
          <select v-model="filter.team" @change="load">
            <option value="">全部团队</option>
            <option v-for="t in teams" :key="t.key" :value="t.key">{{ t.name }}</option>
          </select>
        </div>
        <div class="field" style="min-width: 160px">
          <label>负责人</label>
          <select v-model="filter.assignee" @change="load">
            <option value="all">所有人</option>
            <option value="me">分给我的</option>
          </select>
        </div>
        <div class="field" style="flex: 1; min-width: 200px">
          <label><Search :size="12" /> 搜索标题</label>
          <input v-model="filter.q" placeholder="关键字" @input="onSearch" />
        </div>
      </div>
      <div class="row" style="gap: 8px; flex-wrap: wrap">
        <span class="label" style="margin-right: 2px">状态</span>
        <button
          v-for="g in stateGroups"
          :key="g.type"
          type="button"
          class="chip bt"
          :class="{ on: filter.states.includes(g.type) }"
          @click="toggleState(g.type)"
        >{{ g.label }}</button>
        <span v-if="!filter.states.length" class="hint">不限状态（默认）</span>
      </div>
    </section>

    <!-- 列表 -->
    <div class="card flush">
      <div class="board-head">
        <label class="row" style="gap: 8px; cursor: pointer">
          <input type="checkbox" :checked="allChecked" :disabled="!rows.length" @change="toggleAll" />
          <span class="label">全选（{{ rows.length }}）</span>
        </label>
        <span class="hint">{{ loading ? '加载中…' : '每行右侧的按钮只为这一条排测试' }}</span>
      </div>

      <div v-if="loading && !rows.length" class="empty"><Loader :size="20" class="spin" /><span>正在拉取 Linear issue…</span></div>
      <div v-else-if="!rows.length" class="empty">
        <span class="empty-icon"><Inbox :size="22" /></span>
        <h3>没有匹配的 issue</h3>
        <p class="muted">换个团队或状态试试，或在「设置」里确认 Linear API Key。</p>
      </div>

      <div v-else class="board-list">
        <div v-for="r in rows" :key="r.identifier" class="board-row" :class="{ picked: selected.has(r.identifier) }">
          <input type="checkbox" :checked="selected.has(r.identifier)" @change="toggleSelect(r.identifier)" />

          <div class="board-main">
            <div class="row" style="gap: 8px; flex-wrap: wrap">
              <a :href="r.url" target="_blank" rel="noopener" class="issue-key">{{ r.identifier }}</a>
              <span class="run-title" style="flex: 1; min-width: 0">{{ r.title }}</span>
            </div>
            <div class="row board-meta">
              <span v-if="r.state" class="state-dot" :class="stateTone[r.state_type]">{{ r.state }}</span>
              <span v-if="r.mine" class="chip mine"><UserRound :size="11" /> 我的</span>
              <span v-else-if="r.assignee" class="chip"><Users :size="11" /> {{ r.assignee }}</span>
              <span v-for="l in (r.labels || []).slice(0, 3)" :key="l" class="chip">{{ l }}</span>
              <span class="muted" :title="r.updated_at">{{ ago(r.updated_at) }}</span>
            </div>
          </div>

          <div class="board-run">
            <button
              v-if="runPill(lastRun(r))"
              class="pill bt"
              :class="[runPill(lastRun(r)).tone]"
              :title="lastRun(r).summary || lastRun(r).error || '查看报告'"
              @click="openLast(r)"
            >
              <component :is="runPill(lastRun(r)).icon" :class="{ spin: runPill(lastRun(r)).tone === 'live' }" />
              {{ runPill(lastRun(r)).text }}
              <span v-if="lastRun(r).acs?.length" class="muted">{{ acOk(lastRun(r).acs) }}/{{ lastRun(r).acs.length }}</span>
            </button>
            <span v-else class="muted" style="font-size: 12px">未测试</span>
          </div>

          <div class="board-act">
            <button v-if="running(r)" class="btn sm" @click="openLast(r)"><ChevronRight :size="14" /> 查看</button>
            <button v-else class="btn sm primary" :disabled="!configReady || busy" :title="configReady ? '' : '先选好仓库、分支和运行时'" @click="launchOne(r)">
              <Play :size="14" /> 开始测试
            </button>
          </div>
        </div>
      </div>
    </div>

    <p class="hint" style="text-align: center">
      一次最多启动 20 条；已经在排队或执行中的 issue 会自动跳过，不会重复排单。
    </p>
  </div>
</template>
