<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { ShieldCheck, ShieldAlert, ShieldOff, Sparkles, Trash2, RefreshCw, X, TriangleAlert } from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { ago, caseLevelText, caseStatusText, verdictText, statusText, isLive, fmtTime } from '../format'

const repos = ref([])
const repoId = ref('')
const cases = ref([])
const err = ref('')
const busy = ref('')
const triaging = ref(null)
const reason = ref('')

onMounted(async () => {
  try {
    repos.value = await api.repos()
    if (repos.value.length) repoId.value = repos.value[0].id
  } catch (e) {
    err.value = e.message
  }
})

watch(repoId, load)

async function load() {
  if (!repoId.value) return
  try {
    cases.value = await api.cases(repoId.value)
    err.value = ''
  } catch (e) {
    err.value = e.message
  }
}

const active = computed(() => cases.value.filter((c) => c.status === 'active'))
const retired = computed(() => cases.value.filter((c) => c.status === 'retired'))
const flaky = computed(() => cases.value.filter((c) => c.flaky))
const notCovered = computed(() => cases.value.length === 0)

// 最近一次回归结果。
function last(c) {
  return c.results?.[c.results.length - 1]
}
function lastTone(c) {
  const r = last(c)
  if (!r) return ''
  if (!isLive(r)) return { pass: 'ok', fail: 'bad', partial: 'warn' }[r.verdict] || ''
  return 'info'
}

async function setLevel(c, level) {
  busy.value = c.id
  try {
    await api.updateCase(c.id, { level })
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

async function retire(c) {
  if (!confirm(`停用用例 ${c.issue_key}？停用后不再参与回归。`)) return
  const reasonText = prompt('停用原因（例如：需求已变更，旧行为不再要求）')
  if (reasonText === null) return
  busy.value = c.id
  try {
    await api.updateCase(c.id, { status: 'retired', reason: reasonText || '需求已变更' })
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

async function enable(c) {
  busy.value = c.id
  try {
    await api.updateCase(c.id, { status: 'active', reason: '' })
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

async function remove(c) {
  if (!confirm(`彻底删除用例 ${c.issue_key}？历史记录也会一起删除，只有确实不需要这个回归了才这么做。`)) return
  busy.value = c.id
  try {
    await api.deleteCase(c.id)
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

// 未通过时的三种处理：确认是缺陷（只记录）、需求已变更（停用）、计划过时（用这次测试的计划更新）。
async function triage(c, action) {
  busy.value = c.id
  err.value = ''
  try {
    const r = last(c)
    if (action === 'retire') {
      const why = reason.value.trim() || '需求已变更'
      await api.updateCase(c.id, { status: 'retired', reason: why })
    } else if (action === 'refresh') {
      await api.updateCase(c.id, { plan_run_id: r.run_id })
    }
    triaging.value = null
    reason.value = ''
    load()
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = ''
  }
}

function openTriage(c) {
  triaging.value = c
  reason.value = ''
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Regression baseline</div>
        <h2>回归基线</h2>
        <p class="sub">每个版本发布后，通过的 issue 会沉淀成这里的用例。以后每次冻结回归，这些用例都会在冻结节点上重测，用来发现新改动把旧功能弄坏。</p>
      </div>
      <div class="row">
        <select v-model="repoId" style="min-width: 200px">
          <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
        </select>
        <button class="btn ghost icon" title="刷新" :disabled="!repoId" @click="load"><RefreshCw :size="15" /></button>
      </div>
    </div>

    <div v-if="err" class="alert bad">{{ err }}</div>

    <div class="stats">
      <div class="card stat">
        <div class="stat-label"><ShieldCheck :size="13" /> 启用用例</div>
        <div class="stat-value">{{ active.length }}</div>
      </div>
      <div class="card stat">
        <div class="stat-label"><ShieldCheck :size="13" /> 核心</div>
        <div class="stat-value">{{ active.filter((c) => c.level === 'core').length }}</div>
      </div>
      <div class="card stat">
        <div class="stat-label"><TriangleAlert :size="13" /> 不稳定</div>
        <div class="stat-value" :class="{ 'grad-text': flaky.length }">{{ flaky.length }}</div>
      </div>
      <div class="card stat">
        <div class="stat-label"><ShieldOff :size="13" /> 已停用</div>
        <div class="stat-value">{{ retired.length }}</div>
      </div>
    </div>

    <div v-if="notCovered && !err" class="card">
      <div class="empty">
        <span class="empty-icon"><ShieldCheck :size="24" /></span>
        <h3>还没有回归用例</h3>
        <p class="muted">版本发布时会自动沉淀；也可以在一次通过的测试详情页点「加入回归基线」。</p>
      </div>
    </div>

    <div v-else-if="cases.length" class="card flush">
      <div class="card-head" style="margin: 0; padding: 14px 16px; border-bottom: 1px solid var(--border)">
        <div class="card-title"><ShieldCheck :size="16" /> 用例</div>
        <span class="muted" style="font-size: 12px">核心用例默认参与每轮回归；普通用例只在选择「全部用例」时跑</span>
      </div>
      <div v-for="c in cases" :key="c.id" class="case-row" :class="{ dim: c.status === 'retired' }">
        <div class="row between" style="gap: 12px; align-items: flex-start">
          <div style="min-width: 0; flex: 1">
            <div class="row" style="gap: 8px">
              <span class="issue-key">{{ c.issue_key }}</span>
              <span class="chip">{{ caseLevelText[c.level] }}</span>
              <span class="pill" :class="c.status === 'active' ? 'ok' : ''">{{ caseStatusText[c.status] }}</span>
              <span v-if="c.flaky" class="pill warn"><TriangleAlert /> 不稳定</span>
              <span v-if="last(c)" class="pill" :class="lastTone(c)">最近：{{ isLive(last(c)) ? statusText[last(c).status] : verdictText[last(c).verdict] || statusText[last(c).status] }}</span>
            </div>
            <div class="muted" style="font-size: 13px; margin-top: 4px; overflow-wrap: anywhere">{{ c.issue.title }}</div>
            <div class="row muted" style="font-size: 12px; gap: 12px; margin-top: 6px">
              <span v-if="c.source_version_id">来源：版本 <a :href="'/versions/' + c.source_version_id" @click.prevent="go('/versions/' + c.source_version_id)">#{{ c.source_version_id }}</a></span>
              <span v-else>手动加入</span>
              <span :title="fmtTime(c.updated_at)">更新于 {{ ago(c.updated_at) }}</span>
              <span v-if="c.retired_reason">停用原因：{{ c.retired_reason }}</span>
            </div>
            <div v-if="last(c) && last(c).verdict === 'fail' && c.status === 'active'" class="triage">
              <div v-if="triaging?.id !== c.id" class="row" style="gap: 6px">
                <span class="hint" style="margin-right: 4px">这次回归未通过，怎么处理？</span>
                <a class="btn sm ghost" :href="'/runs/' + last(c).run_id" @click.prevent="go('/runs/' + last(c).run_id)">看报告</a>
                <button class="btn sm ghost" @click="openTriage(c)">处理</button>
              </div>
              <div v-else class="stack" style="gap: 8px">
                <div class="row" style="gap: 6px">
                  <button class="btn sm danger" :disabled="!!busy" @click="triage(c, 'confirm')">确认是缺陷</button>
                  <button class="btn sm" :disabled="!!busy" @click="triage(c, 'refresh')">计划过时，用这次的计划更新</button>
                  <button class="btn sm" :disabled="!!busy" @click="triage(c, 'retire')">需求已变更，停用</button>
                  <button class="btn sm ghost" @click="triaging = null"><X :size="13" /> 取消</button>
                </div>
                <input v-model="reason" placeholder="停用原因（选「需求已变更」时填写）" />
                <span class="hint">「确认是缺陷」只记录一条历史，用例保持不变，下轮回归会继续检查。</span>
              </div>
            </div>
          </div>
          <div class="stack" style="gap: 2px; align-items: flex-end">
            <div class="row" style="gap: 2px">
              <button class="btn sm ghost" :disabled="!!busy" :title="c.level === 'core' ? '改为普通' : '改为核心'" @click="setLevel(c, c.level === 'core' ? 'normal' : 'core')">
                {{ c.level === 'core' ? '改为普通' : '设为核心' }}
              </button>
              <button v-if="c.status === 'active'" class="btn ghost icon sm danger" title="停用" :disabled="!!busy" @click="retire(c)"><ShieldOff :size="14" /></button>
              <button v-else class="btn ghost icon sm" title="重新启用" :disabled="!!busy" @click="enable(c)"><ShieldCheck :size="14" /></button>
              <button class="btn ghost icon sm danger" title="删除" :disabled="!!busy" @click="remove(c)"><Trash2 :size="14" /></button>
            </div>
            <div v-if="c.results?.length" class="mini-history">
              <span
                v-for="r in c.results.slice(-8)"
                :key="r.id"
                class="mini-dot"
                :class="{ pass: r.verdict === 'pass', fail: r.verdict === 'fail', partial: r.verdict === 'partial', error: !r.verdict }"
                :title="(r.pin_sha || '').slice(0, 7) + ' ' + (verdictText[r.verdict] || statusText[r.status]) + ' · ' + fmtTime(r.created_at)"
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
