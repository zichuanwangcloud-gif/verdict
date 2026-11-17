<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  ArrowLeft, RotateCcw, Square, GitBranch, GitCommitHorizontal, Server, Clock, Coins, ExternalLink,
  Sparkles, CircleCheck, CircleX, CircleHelp, ChevronRight, TriangleAlert, FileDiff, ScrollText,
  MessageSquare, Send, Braces, Radio,
} from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { acText, fmtTime, fmtDuration, harnessName, isLive } from '../format'
import StatusPill from '../components/StatusPill.vue'
import VerdictRing from '../components/VerdictRing.vue'
import LogStream from '../components/LogStream.vue'

const props = defineProps({ id: String })
const run = ref(null)
const logs = ref([])
const err = ref('')
const actionErr = ref('')
const closed = ref(new Set())
let lastSeq = 0
let timer
let loading = false

const live = computed(() => isLive(run.value))

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
  try {
    return await fn()
  } catch (e) {
    actionErr.value = e.message
  }
}
async function cancel() {
  if (!confirm('确定取消这次测试？')) return
  await act(() => api.cancelRun(props.id))
  load()
}
async function rerun() {
  const r = await act(() => api.rerun(props.id))
  if (r) go('/runs/' + r.id)
}
async function writeBack() {
  const r = await act(() => api.writeBack(props.id))
  if (r) run.value = r
}

function toggle(id) {
  const s = new Set(closed.value)
  s.has(id) ? s.delete(id) : s.add(id)
  closed.value = s
}

const acIcon = { pass: CircleCheck, fail: CircleX, unverifiable: CircleHelp }
const acTone = { pass: 'ok', fail: 'bad', unverifiable: 'warn' }

// 执行阶段进度
const steps = computed(() => {
  const s = run.value?.status
  const order = ['queued', 'preparing', 'running', 'done']
  const idx = { queued: 0, dispatched: 0, preparing: 1, running: 2, completed: 3, error: 3, canceled: 3 }[s] ?? 0
  const labels = ['排队', '准备工作区', 'Agent 测试', s === 'completed' ? '得出结论' : s === 'canceled' ? '已取消' : s === 'error' ? '出错' : '得出结论']
  return order.map((_, i) => ({
    label: labels[i],
    state: i < idx ? 'done' : i === idx ? (i === 3 ? (s === 'completed' ? 'done' : 'fail') : 'now') : '',
  }))
})
</script>

<template>
  <div class="page">
    <a href="/runs" class="row muted" style="gap: 4px" @click.prevent="go('/runs')"><ArrowLeft :size="14" /> 验收测试</a>
    <div v-if="err && !run" class="alert bad">{{ err }}</div>

    <template v-if="run">
      <!-- 头部 -->
      <section class="card glow" :class="{ shimmer: live }">
        <div class="hero">
          <VerdictRing :run="run" />
          <div style="min-width: 0">
            <div class="row between">
              <StatusPill :run="run" large />
              <div class="row">
                <button v-if="live" class="btn sm danger" @click="cancel"><Square :size="13" /> 取消</button>
                <button class="btn sm" @click="rerun"><RotateCcw :size="13" /> 重新测试</button>
              </div>
            </div>
            <div class="hero-title"><span class="issue-key">{{ run.issue_key }}</span>{{ run.issue.title }}</div>
            <div class="meta-chips">
              <span class="chip"><GitBranch /> {{ run.repo_name }} · <span class="mono">{{ run.branch }}</span></span>
              <span v-if="run.commit_sha" class="chip mono" :title="run.commit_sha"><GitCommitHorizontal /> {{ run.commit_sha.slice(0, 10) }}</span>
              <span class="chip"><Server /> {{ harnessName(run.harness) }} @ {{ run.runtime_name }}</span>
              <span class="chip" :title="'创建于 ' + fmtTime(run.created_at)"><Clock /> {{ fmtDuration(run.started_at, run.finished_at) }}</span>
              <span v-if="run.cost_usd" class="chip"><Coins /> ${{ run.cost_usd.toFixed(4) }}</span>
              <a v-if="run.issue.url" class="chip" :href="run.issue.url" target="_blank" rel="noopener"><ExternalLink /> Linear</a>
            </div>
          </div>
        </div>
        <div class="steps" style="margin-top: 18px">
          <template v-for="(s, i) in steps" :key="i">
            <span v-if="i" class="step-line" />
            <span class="step" :class="s.state"><span class="dot" /> {{ s.label }}</span>
          </template>
        </div>
      </section>

      <div v-if="actionErr" class="alert bad">{{ actionErr }}</div>
      <div v-if="run.error" class="alert bad"><TriangleAlert :size="16" /><span class="body">{{ run.error }}</span></div>
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

      <!-- 结论 -->
      <section v-if="run.status === 'completed'" class="card stack" style="gap: 16px">
        <div class="card-title"><Sparkles :size="16" /> AI 结论</div>
        <div v-if="run.summary" class="summary"><Sparkles :size="16" /><p class="body">{{ run.summary }}</p></div>
        <div class="ac-list">
          <div v-for="a in run.acs" :key="a.id" class="ac">
            <div class="ac-head" @click="toggle(a.id)">
              <span class="ac-icon" :class="acTone[a.status] || 'warn'"><component :is="acIcon[a.status] || CircleHelp" :size="15" /></span>
              <div style="flex: 1; min-width: 0">
                <div class="row" style="gap: 8px">
                  <span class="ac-id">{{ a.id }}</span>
                  <span class="pill" :class="acTone[a.status] || 'warn'">{{ acText[a.status] || a.status }}</span>
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
            </div>
          </div>
        </div>
      </section>

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
        <button v-if="!live && run.status !== 'canceled'" class="btn sm" @click="writeBack"><Send :size="13" /> 重新回写</button>
      </section>

      <!-- 思考流 -->
      <section class="card stack" style="gap: 12px">
        <div class="card-head" style="margin: 0">
          <div class="card-title"><ScrollText :size="16" /> Agent 执行流</div>
          <span v-if="live" class="pill live"><Radio :size="13" /> 实时</span>
          <span v-else class="muted" style="font-size: 12px">{{ logs.length }} 条</span>
        </div>
        <LogStream :logs="logs" :live="live" />
      </section>

      <details class="card">
        <summary><ChevronRight :size="14" class="chev" /><Braces :size="14" /> 交给 agent 的提示词</summary>
        <pre>{{ run.prompt }}</pre>
      </details>
      <details v-if="run.raw" class="card">
        <summary><ChevronRight :size="14" class="chev" /><Braces :size="14" /> 原始结构化输出</summary>
        <pre>{{ run.raw }}</pre>
      </details>
    </template>
  </div>
</template>
