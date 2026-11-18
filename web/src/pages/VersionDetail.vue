<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ArrowLeft, PackageCheck, Lock, Rocket, Plus, Trash2, RotateCcw, Snowflake, ShieldAlert, CircleCheck, CircleX, CircleDashed, Timer, Coins, X } from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { fmtTime, ago, isLive, versionStatusText, regressionVerdictText, statusText, verdictText, tierMeta, caseLevelText } from '../format'

const props = defineProps({ id: String })
const data = ref(null)
const runtimes = ref([])
const err = ref('')
const busy = ref('')
const addKey = ref('')
const addErr = ref('')
const freezing = ref(false)
const freeze = ref({ ref: '', baseline: 'core', harness: 'claude', issue_tier: 'B', case_tier: 'C', runtime_ids: [] })
const releasing = ref(false)
const releaseNote = ref('')

let timer
async function load() {
  try {
    data.value = await api.version(props.id)
    err.value = ''
  } catch (e) {
    err.value = e.message
  }
}
onMounted(async () => {
  await load()
  try {
    runtimes.value = await api.runtimes()
  } catch {
    /* 只在冻结时用 */
  }
  timer = setInterval(load, 5000)
})
onUnmounted(() => clearInterval(timer))

const v = computed(() => data.value?.version)
const rows = computed(() => data.value?.rows || [])
const regs = computed(() => data.value?.regressions || [])
const frozen = computed(() => v.value && v.value.status !== 'open')
const statusTone = { open: 'info', frozen: 'warn', released: 'ok' }

// 最需要注意的项排在最前：集成回归、历史回归、未通过。
const sorted = computed(() => {
  const rank = (r) => (r.integration ? 0 : r.history ? 1 : 2)
  return [...rows.value].sort((a, b) => rank(a) - rank(b))
})

function cellTone(c) {
  if (!c) return 'none'
  if (!c.finished) return isLive({ status: c.status }) ? 'live' : 'none'
  if (c.verdict === 'pass') return 'ok'
  if (c.verdict === 'fail') return 'bad'
  if (c.verdict === 'partial') return 'warn'
  if (c.status === 'canceled') return 'none'
  return 'bad'
}

function cellText(c) {
  if (!c) return '—'
  if (!c.finished) return statusText[c.status] || c.status
  if (c.verdict === 'pass') return '通过'
  if (c.verdict === 'fail') return '未通过'
  if (c.verdict === 'partial') return '部分'
  if (c.status === 'canceled') return '已取消'
  return '出错'
}

async function addIssue() {
  addErr.value = ''
  if (!addKey.value.trim()) return
  try {
    data.value = await api.addVersionIssue(props.id, addKey.value.trim(), false)
    addKey.value = ''
  } catch (e) {
    if (String(e.message).includes('另一个版本') && confirm(`${e.message}\n要把它移动到这个版本吗？`)) {
      try {
        data.value = await api.addVersionIssue(props.id, addKey.value.trim(), true)
        addKey.value = ''
      } catch (e2) {
        addErr.value = e2.message
      }
      return
    }
    addErr.value = e.message
  }
}

async function removeIssue(key) {
  if (!confirm(`把 ${key} 移出这个版本？（回归记录不会删除）`)) return
  try {
    data.value = await api.removeVersionIssue(props.id, key)
  } catch (e) {
    err.value = e.message
  }
}

function openFreeze() {
  freezing.value = true
  freeze.value.runtime_ids = runtimes.value.filter((r) => r.online).map((r) => r.id)
}

async function doFreeze() {
  err.value = ''
  busy.value = 'freeze'
  try {
    const g = await api.freezeVersion(props.id, freeze.value)
    freezing.value = false
    await load()
    if (g?.id) go('/versions/' + props.id)
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

async function retry(g) {
  busy.value = g.id
  try {
    await api.retryRegression(g.id)
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

async function cancel(g) {
  if (!confirm('取消这一轮回归？正在跑的子测试会被取消。')) return
  busy.value = g.id
  try {
    await api.cancelRegression(g.id)
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

async function doRelease() {
  err.value = ''
  busy.value = 'release'
  try {
    const res = await api.releaseVersion(props.id, releaseNote.value)
    releasing.value = false
    releaseNote.value = ''
    await load()
    if (res?.cases_added) alert(`已发布，沉淀了 ${res.cases_added} 条回归用例。`)
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

const lastReg = computed(() => regs.value[regs.value.length - 1])
const releaseBlocked = computed(() => {
  if (!lastReg.value) return '还没有做过回归'
  if (lastReg.value.status === 'running') return '最近一轮回归还没结束'
  return ''
})

function tierOf(t) {
  return tierMeta[t]?.short || t
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <button class="btn ghost sm" style="margin-bottom: 8px" @click="go('/versions')"><ArrowLeft :size="14" /> 版本</button>
        <div class="eyebrow"><PackageCheck :size="12" /> {{ v?.repo_name }}</div>
        <h2>{{ v?.name || '版本' }}
          <span v-if="v" class="pill lg" :class="statusTone[v.status]" style="vertical-align: middle; margin-left: 8px">
            <component :is="v.status === 'released' ? CircleCheck : v.status === 'frozen' ? Lock : Rocket" /> {{ versionStatusText[v.status] }}
          </span>
        </h2>
        <p v-if="v?.note" class="sub">{{ v.note }}</p>
        <p v-if="v?.release_note" class="sub">放行理由：{{ v.release_note }}</p>
      </div>
      <div class="row" v-if="v && v.status !== 'released'">
        <button class="btn" :disabled="!!busy" @click="releasing = true"><CircleCheck :size="15" /> 标记为已发布</button>
        <button class="btn primary" :disabled="!!busy" @click="openFreeze"><Snowflake :size="15" /> 冻结并回归</button>
      </div>
    </div>

    <div v-if="err" class="alert bad">{{ err }}</div>

    <form v-if="releasing" class="card glow stack" style="gap: 12px" @submit.prevent="doRelease">
      <div class="card-head" style="margin: 0">
        <div class="card-title"><CircleCheck :size="16" /> 标记为已发布</div>
        <button type="button" class="btn ghost icon" @click="releasing = false"><X :size="16" /></button>
      </div>
      <p class="hint">发布时把最近一轮回归里通过的成员 issue 沉淀为回归用例，之后每个版本冻结都会带上它们。</p>
      <div v-if="releaseBlocked" class="alert bad">{{ releaseBlocked }}</div>
      <div v-else-if="lastReg && lastReg.verdict !== 'pass'" class="alert bad">最近一轮回归的结论是「{{ regressionVerdictText[lastReg.verdict] || lastReg.verdict }}」，放行请填写理由。</div>
      <div class="field">
        <label>放行理由（结论不是「通过」时必填）</label>
        <textarea v-model="releaseNote" rows="3" placeholder="例如：未通过的 ENG-20 已在 1.4.1 修复单中跟踪" />
      </div>
      <div class="row">
        <button class="btn primary" :disabled="!!busy || !!releaseBlocked">
          <component :is="busy === 'release' ? Timer : CircleCheck" :size="15" /> 确认发布
        </button>
      </div>
    </form>

    <form v-if="freezing" class="card glow stack" style="gap: 14px" @submit.prevent="doFreeze">
      <div class="card-head" style="margin: 0">
        <div class="card-title"><Snowflake :size="16" /> 冻结并回归</div>
        <button type="button" class="btn ghost icon" @click="freezing = false"><X :size="16" /></button>
      </div>
      <div class="grid-2">
        <div class="field">
          <label>冻结节点</label>
          <input v-model="freeze.ref" class="mono" placeholder="release/1.4 或 v1.4.0 或完整 commit" />
          <span class="hint">会立即解析成 commit 存下来（解析需要至少一个在线运行时）。重新冻结会新开一轮，旧结果保留。</span>
        </div>
        <div class="field">
          <label>回归基线</label>
          <select v-model="freeze.baseline">
            <option value="core">只跑核心用例</option>
            <option value="all">全部用例</option>
            <option value="none">不跑基线，只测本版本 issue</option>
          </select>
        </div>
        <div class="field">
          <label>本版本 issue 的档位</label>
          <select v-model="freeze.issue_tier">
            <option v-for="t in ['C', 'B', 'A', 'S']" :key="t" :value="t">{{ tierMeta[t].name }}</option>
          </select>
        </div>
        <div class="field">
          <label>基线用例的档位</label>
          <select v-model="freeze.case_tier">
            <option v-for="t in ['C', 'B', 'A']" :key="t" :value="t">{{ tierMeta[t].name }}</option>
          </select>
        </div>
      </div>
      <div class="field">
        <label>参与的运行时（空闲的会先领任务，可多选分摊）</label>
        <select v-model="freeze.runtime_ids" multiple size="4">
          <option v-for="r in runtimes" :key="r.id" :value="r.id" :disabled="!r.online">{{ r.name }}{{ r.online ? '' : '（离线）' }}</option>
        </select>
      </div>
      <div class="row">
        <button class="btn primary" :disabled="!freeze.ref.trim() || !!busy">
          <component :is="busy === 'freeze' ? Timer : Snowflake" :size="15" /> 开始回归
        </button>
      </div>
    </form>

    <div v-if="v && v.status !== 'released'" class="card stack" style="gap: 10px">
      <div class="card-title"><Plus :size="16" /> 添加 issue 到这个版本</div>
      <form class="input-group" @submit.prevent="addIssue">
        <input v-model="addKey" class="mono" placeholder="ENG-42" />
        <button class="btn" :disabled="!addKey.trim()">添加</button>
      </form>
      <div v-if="addErr" class="alert bad">{{ addErr }}</div>
      <span class="hint">之后新建测试时选这个版本，测试会自动归到这里。也可以先加进来，测试结果留空。</span>
    </div>

    <div class="card flush">
      <div class="card-head" style="margin: 0; padding: 14px 16px; border-bottom: 1px solid var(--border)">
        <div class="card-title"><ShieldAlert :size="16" /> 回归矩阵</div>
        <span class="muted" style="font-size: 12px">标红排在最前的是最需要注意的项</span>
      </div>
      <div style="overflow-x: auto">
        <table class="matrix">
          <thead>
            <tr>
              <th style="min-width: 220px">Issue</th>
              <th>分支测试</th>
              <th v-for="g in regs" :key="g.id">
                <div>第 {{ g.seq }} 轮</div>
                <div class="mono muted" style="font-size: 11px">{{ g.freeze_sha.slice(0, 7) }}</div>
              </th>
              <th v-if="!regs.length" class="muted">还没冻结</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in sorted" :key="r.kind + r.key" :class="{ hot: r.integration || r.history }">
              <td>
                <div class="row" style="gap: 6px">
                  <span class="issue-key">{{ r.key }}</span>
                  <span v-if="r.kind === 'case'" class="chip">{{ caseLevelText[r.level] }}</span>
                  <span v-if="r.integration" class="pill bad">集成回归</span>
                  <span v-else-if="r.history" class="pill bad">历史回归</span>
                </div>
                <div class="muted" style="font-size: 12px; overflow-wrap: anywhere">{{ r.title }}</div>
              </td>
              <td>
                <a v-if="r.branch" :href="'/runs/' + r.branch.run_id" class="cell" :class="cellTone(r.branch)" @click.prevent="go('/runs/' + r.branch.run_id)">
                  {{ cellText(r.branch) }}
                </a>
                <span v-else class="cell none">未测</span>
              </td>
              <td v-for="(c, i) in r.cells" :key="i">
                <a v-if="c" :href="'/runs/' + c.run_id" class="cell" :class="cellTone(c)" :title="c.runtime_name + ' · ' + (c.commit_sha || '')" @click.prevent="go('/runs/' + c.run_id)">
                  {{ cellText(c) }}<template v-if="c.status === 'error'"> · 重试</template>
                </a>
                <span v-else class="cell none">—</span>
              </td>
              <td></td>
              <td v-if="v && v.status !== 'released'">
                <button class="btn ghost icon danger sm" title="移出版本" @click="removeIssue(r.key)"><Trash2 :size="14" /></button>
              </td>
            </tr>
            <tr v-if="!rows.length">
              <td colspan="4" class="muted" style="padding: 20px">还没有成员。先添加 issue，或者用新建测试时选这个版本。</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="card stack" style="gap: 12px">
      <div class="card-title"><Snowflake :size="16" /> 冻结记录</div>
      <div v-if="!regs.length" class="muted">还没有冻结过。冻结时输入分支、tag 或 commit，平台会在那份代码上重测。</div>
      <div v-for="g in [...regs].reverse()" :key="g.id" class="freeze-row">
        <div class="row between" style="gap: 12px">
          <div class="row" style="gap: 10px">
            <b>第 {{ g.seq }} 轮</b>
            <span class="chip mono">{{ g.freeze_ref }} @ {{ g.freeze_sha.slice(0, 10) }}</span>
            <span v-if="g.freeze_full_ref" class="chip">{{ g.freeze_full_ref }}</span>
            <span class="pill" :class="g.status === 'running' ? 'info' : g.verdict === 'pass' ? 'ok' : g.verdict ? 'bad' : ''">
              {{ g.status === 'running' ? '进行中' : regressionVerdictText[g.verdict] || regressionStatusText[g.status] }}
            </span>
          </div>
          <div class="row" style="gap: 6px">
            <span class="muted" style="font-size: 12px"><Coins :size="12" style="vertical-align: -1px" /> ${{ g.cost_usd.toFixed(2) }}</span>
            <button v-if="g.status !== 'running'" class="btn sm ghost" :disabled="!!busy" @click="retry(g)"><RotateCcw :size="14" /> 重跑未通过</button>
            <button v-if="g.status === 'running'" class="btn sm ghost danger" :disabled="!!busy" @click="cancel(g)"><X :size="14" /> 取消</button>
          </div>
        </div>
        <div class="row muted" style="font-size: 12px; gap: 14px">
          <span><CircleCheck :size="12" style="vertical-align: -1px; color: var(--ok)" /> {{ g.passed }} 通过</span>
          <span><CircleX :size="12" style="vertical-align: -1px; color: var(--bad)" /> {{ g.failed }} 未通过</span>
          <span><CircleDashed :size="12" style="vertical-align: -1px; color: var(--warn)" /> {{ g.partial }} 部分无法验证</span>
          <span v-if="g.errored">{{ g.errored }} 出错</span>
          <span v-if="g.running">{{ g.running }} 进行中</span>
          <span>范围：{{ g.scope.baseline === 'core' ? '本版本 + 核心基线' : g.scope.baseline === 'all' ? '本版本 + 全部基线' : '只测本版本' }}</span>
          <span>档位：issue {{ tierOf(g.config.issue_tier) }} / 基线 {{ tierOf(g.config.case_tier) }}</span>
          <span :title="fmtTime(g.created_at)">{{ ago(g.created_at) }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
