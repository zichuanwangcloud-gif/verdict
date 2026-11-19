<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { Sparkles, BarChart3, RefreshCw, TriangleAlert, CircleCheck, CircleX, CircleHelp, ExternalLink, Inbox, CircleAlert, ClipboardCheck, ListChecks } from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { ago, tierMeta, truthSourceText } from '../format'

const repos = ref([])
const repoId = ref('')
const tier = ref('')
const days = ref(90)
const data = ref(null)
const loading = ref(false)
const err = ref('')

const dayOptions = [
  { v: 7, label: '近 7 天' },
  { v: 30, label: '近 30 天' },
  { v: 90, label: '近 90 天' },
  { v: 0, label: '全部' },
]

onMounted(async () => {
  try {
    repos.value = await api.repos()
    if (repos.value.length) repoId.value = repos.value[0].id
  } catch (e) {
    err.value = e.message
  }
})

watch([repoId, tier, days], load)

async function load() {
  if (!repoId.value) return
  loading.value = true
  err.value = ''
  try {
    data.value = await api.reviewStats({ repoId: repoId.value, tier: tier.value, days: days.value })
  } catch (e) {
    err.value = e.message
    data.value = null
  } finally {
    loading.value = false
  }
}

const st = computed(() => data.value?.stats || null)
const samples = computed(() => data.value?.samples || [])

// 分母为 0 时不该显示 0%，那会被读成「完全没有漏报」。
function pct(n, d) {
  if (!d) return '—'
  return (n / d * 100).toFixed(0) + '%'
}
const missedRate = computed(() => (st.value && st.value.human_fail ? pct(st.value.missed_clear + st.value.missed_doubt, st.value.human_fail) : '—'))
const alarmRate = computed(() => (st.value && st.value.human_pass ? pct(st.value.alarm_clear + st.value.alarm_doubt, st.value.human_pass) : '—'))
const coverage = computed(() => (st.value && st.value.completed ? pct(st.value.marked, st.value.completed) : '—'))

// 混淆矩阵：行是 AI 的三档结论，列是人工的两档。
const agentRows = [
  { key: 'pass', label: '通过', hint: 'AI 说通过' },
  { key: 'partial', label: '部分无法验证', hint: 'AI 说无法验证' },
  { key: 'fail', label: '未通过', hint: 'AI 说未通过' },
]
const truthCols = [
  { key: 'pass', label: '人工：通过' },
  { key: 'fail', label: '人工：未通过' },
]

// 每格的语义：命中 / 真放行 / 漏报 / 误报 / 存疑。
function cellOf(agent, truth) {
  if (agent === truth) return agent === 'pass' ? 'real' : 'hit'
  if (agent === 'partial') return truth === 'fail' ? 'missed-doubt' : 'alarm-doubt'
  return truth === 'fail' ? 'missed' : 'alarm'
}
const cellMeta = {
  hit: { label: '命中', tone: 'ok' },
  real: { label: '真放行', tone: 'ok' },
  missed: { label: '漏报', tone: 'bad' },
  'missed-doubt': { label: '漏报 · 存疑', tone: 'warn' },
  alarm: { label: '误报', tone: 'bad' },
  'alarm-doubt': { label: '误报 · 存疑', tone: 'warn' },
}
function cell(a, t) {
  return st.value?.matrix?.find((c) => c.agent === a && c.truth === t)?.count || 0
}
// 底色按语义区分好坏，不按数量：数量已经写成数字了，再用深浅编码一遍没有信息量。
// 0 格用虚线框表示「没有样本」，而不是画一个 0 色块。
function cellClass(agent, truth) {
  const n = cell(agent, truth)
  return [cellOf(agent, truth), { zero: !n }]
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Review</div>
        <h2>复盘</h2>
        <p class="sub">人工标过结论的测试才有真值。这里看 AI 测得准不准、错在哪一类。</p>
      </div>
      <button class="btn ghost" :disabled="loading" @click="load"><RefreshCw :size="15" :class="{ spin: loading }" /> 刷新</button>
    </div>

    <div v-if="err" class="alert bad">{{ err }}</div>

    <section class="card stack" style="gap: 12px">
      <div class="row" style="gap: 12px; flex-wrap: wrap; align-items: end">
        <div class="field" style="min-width: 160px">
          <label>仓库</label>
          <select v-model="repoId">
            <option :value="''" disabled>选择仓库</option>
            <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
          </select>
        </div>
        <div class="field" style="min-width: 120px">
          <label>档位</label>
          <select v-model="tier">
            <option value="">全部档位</option>
            <option v-for="t in ['S', 'A', 'B', 'C']" :key="t" :value="t">{{ tierMeta[t]?.name || t }}</option>
          </select>
        </div>
        <div class="field" style="min-width: 130px">
          <label>时间范围</label>
          <select v-model.number="days">
            <option v-for="d in dayOptions" :key="d.v" :value="d.v">{{ d.label }}</option>
          </select>
        </div>
      </div>
    </section>

    <template v-if="st">
      <div class="stats">
        <div class="card stat">
          <div class="stat-label"><BarChart3 :size="13" /> 有效样本</div>
          <div class="stat-value">{{ st.samples }}</div>
          <div class="hint" style="margin-top: 2px">人工标过且不是「无效」</div>
        </div>
        <div class="card stat">
          <div class="stat-label"><CircleAlert :size="13" /> 漏报率</div>
          <div class="stat-value" :style="{ color: st.missed_clear ? 'var(--bad)' : '' }">{{ missedRate }}</div>
          <div class="hint" style="margin-top: 2px">人工未通过的 {{ st.human_fail }} 条里，AI 没判出来的</div>
        </div>
        <div class="card stat">
          <div class="stat-label"><CircleX :size="13" /> 误报率</div>
          <div class="stat-value">{{ alarmRate }}</div>
          <div class="hint" style="margin-top: 2px">人工确认没问题的 {{ st.human_pass }} 条里，AI 判错的</div>
        </div>
        <div class="card stat">
          <div class="stat-label"><ClipboardCheck :size="13" /> 复核覆盖率</div>
          <div class="stat-value">{{ coverage }}</div>
          <div class="hint" style="margin-top: 2px">{{ st.marked }} / {{ st.completed }} 条已完成测试</div>
        </div>
      </div>

      <div v-if="st.samples === 0" class="card">
        <div class="empty">
          <span class="empty-icon"><Inbox :size="22" /></span>
          <h3>还没有有效样本</h3>
          <p class="muted">去「验收测试」里给已完成的测试标一个人工结论，这里才有东西可看。</p>
          <button class="btn primary" @click="go('/runs')">去标记</button>
        </div>
      </div>

      <template v-else>
        <div class="review-grid">
          <!-- 混淆矩阵 -->
          <section class="card stack" style="gap: 14px">
            <div class="card-head" style="margin: 0">
              <div class="card-title"><BarChart3 :size="16" /> 混淆矩阵</div>
              <span class="hint">行 = AI 的结论，列 = 人工复测结果</span>
            </div>
            <table class="cm">
              <thead>
                <tr>
                  <th></th>
                  <th v-for="c in truthCols" :key="c.key">{{ c.label }}</th>
                  <th class="muted">合计</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in agentRows" :key="r.key">
                  <th :title="r.hint">{{ r.label }}</th>
                  <td v-for="c in truthCols" :key="c.key">
                    <div class="cm-cell" :class="cellClass(r.key, c.key)">
                      <span class="cm-num">{{ cell(r.key, c.key) }}</span>
                      <span v-if="cell(r.key, c.key)" class="cm-tag" :class="cellMeta[cellOf(r.key, c.key)].tone">
                        {{ cellMeta[cellOf(r.key, c.key)].label }}
                      </span>
                    </div>
                  </td>
                  <td class="muted cm-sum">{{ cell(r.key, 'pass') + cell(r.key, 'fail') }}</td>
                </tr>
              </tbody>
              <tfoot>
                <tr>
                  <th class="muted">合计</th>
                  <td class="muted cm-sum">{{ st.human_pass }}</td>
                  <td class="muted cm-sum">{{ st.human_fail }}</td>
                  <td class="muted cm-sum">{{ st.samples }}</td>
                </tr>
              </tfoot>
            </table>
            <div class="hint">
              撇开「存疑」不算：明确漏报 {{ st.missed_clear }} 条、明确误报 {{ st.alarm_clear }} 条。
              AI 说「部分无法验证」却被人工证实的，单独算存疑（漏 {{ st.missed_doubt }} / 误 {{ st.alarm_doubt }}）。
            </div>
          </section>

          <!-- 口径说明 -->
          <section class="card stack" style="gap: 12px">
            <div class="card-title"><CircleHelp :size="16" /> 这些数字怎么算</div>
            <ul class="rules">
              <li><b>漏报</b>：人工测出问题，AI 判「通过」（明确）或「部分无法验证」（存疑）。</li>
              <li><b>误报</b>：人工确认没问题，AI 判「未通过」（明确）或「部分无法验证」（存疑）。</li>
              <li><b>命中</b>：AI 判未通过，人工也确认有问题。</li>
              <li><b>真放行</b>：两边都判通过。</li>
              <li>人工标「无效」的测试不进任何分子分母。</li>
            </ul>
            <div class="alert warn" style="margin-top: 2px">
              <TriangleAlert :size="16" />
              <span class="body">
                样本只覆盖人工回访过的测试（{{ st.marked }} / {{ st.completed }}）。
                如果只测了少数 issue，这里的比率只代表那批 issue，不要外推到整个仓库。
              </span>
            </div>
          </section>
        </div>

        <!-- 样本清单 -->
        <div class="card flush">
          <div class="board-head">
            <div class="card-title" style="margin: 0"><ListChecks :size="16" /> 样本清单（{{ samples.length }}）</div>
            <span class="hint">按标记时间倒序</span>
          </div>
          <div class="review-list">
            <a
              v-for="x in samples"
              :key="x.run_id"
              :href="'/runs/' + x.run_id"
              class="review-row"
              :class="{ invalid: x.truth === 'invalid' }"
              @click.prevent="go('/runs/' + x.run_id)"
            >
              <span class="pill" :class="{ ok: x.truth === 'pass', bad: x.truth === 'fail' }">
                <component :is="{ pass: CircleCheck, fail: CircleX, invalid: CircleHelp }[x.truth]" />
                {{ { pass: '人工通过', fail: '人工未通过', invalid: '无效' }[x.truth] || x.truth }}
              </span>
              <div class="board-main">
                <div class="row" style="gap: 8px">
                  <span class="issue-key">{{ x.issue_key }}</span>
                  <span class="run-title" style="flex: 1; min-width: 0">{{ x.summary || '（没有结论摘要）' }}</span>
                </div>
                <div class="row board-meta">
                  <span v-if="x.tier" class="tier-tag sm" :class="'tier-' + (tierMeta[x.tier]?.tone || 'custom')">{{ tierMeta[x.tier]?.short }}</span>
                  <span class="chip">AI：{{ { pass: '通过', fail: '未通过', partial: '部分无法验证' }[x.verdict] || x.verdict }}</span>
                  <span class="chip">{{ truthSourceText[x.source] || x.source }}</span>
                  <span v-if="x.ticket_ref" class="chip mono">{{ x.ticket_ref }}</span>
                  <span class="muted">{{ ago(x.marked_at) }}</span>
                </div>
              </div>
              <ExternalLink :size="14" class="muted" />
            </a>
          </div>
        </div>
      </template>
    </template>

    <div v-else-if="loading" class="card">
      <div class="empty"><RefreshCw :size="20" class="spin" /><span>加载中…</span></div>
    </div>
  </div>
</template>
