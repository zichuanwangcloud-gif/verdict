<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
import { go } from '../router'
import { runTone, runLabel, acText, acTone, fmtTime, fmtDuration } from '../format'

const props = defineProps({ id: String })
const run = ref(null)
const logs = ref([])
const err = ref('')
const actionErr = ref('')
const follow = ref(true)
const logBox = ref(null)
let lastSeq = 0
let timer
let loading = false

const terminal = computed(() => run.value && ['completed', 'error', 'canceled'].includes(run.value.status))

async function load() {
  if (loading) return
  loading = true
  try {
    run.value = await api.run(props.id)
    // 一次最多 2000 行，循环取完
    for (;;) {
      const batch = await api.logs(props.id, lastSeq)
      if (!batch.length) break
      logs.value.push(...batch)
      lastSeq = batch[batch.length - 1].seq
      if (batch.length < 2000) break
    }
    err.value = ''
    if (follow.value) {
      await nextTick()
      if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    }
    if (terminal.value) clearInterval(timer)
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

function onScroll() {
  const el = logBox.value
  if (el) follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

function fmtLogTime(ts) {
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
}
</script>

<template>
  <div v-if="err && !run" class="error">{{ err }}</div>
  <template v-if="run">
    <div class="row between">
      <div>
        <a href="/runs" @click.prevent="go('/runs')">← 测试列表</a>
        <h2 class="title">
          <span class="tag big" :class="runTone(run)">{{ runLabel(run) }}</span>
          <span class="mono">{{ run.issue_key }}</span> {{ run.issue.title }}
        </h2>
      </div>
      <div class="row">
        <button v-if="!terminal" @click="cancel">取消测试</button>
        <button @click="rerun">重新测试</button>
      </div>
    </div>
    <div v-if="actionErr" class="error">{{ actionErr }}</div>

    <div class="card">
      <dl class="meta">
        <dt>需求</dt>
        <dd>
          <a v-if="run.issue.url" :href="run.issue.url" target="_blank" rel="noopener">{{ run.issue.identifier }}</a>
          <span v-else>手动输入</span>
        </dd>
        <dt>仓库</dt>
        <dd>{{ run.repo_name }} <span class="muted mono">{{ run.repo_url }}</span></dd>
        <dt>分支</dt>
        <dd class="mono">{{ run.branch }} <span v-if="run.commit_sha" class="muted">@ {{ run.commit_sha }}</span></dd>
        <dt>执行</dt>
        <dd>{{ run.harness }} @ {{ run.runtime_name }}</dd>
        <dt>时间</dt>
        <dd>
          {{ fmtTime(run.created_at) }} 创建，耗时 {{ fmtDuration(run.started_at, run.finished_at) }}
          <span v-if="run.cost_usd" class="muted">，模型费用 ${{ run.cost_usd.toFixed(4) }}</span>
        </dd>
        <template v-if="run.issue.url">
          <dt>Linear 回写</dt>
          <dd>
            <a v-if="run.linear_comment_url" :href="run.linear_comment_url" target="_blank" rel="noopener">已回写评论</a>
            <span v-else-if="run.linear_error" class="bad-text">{{ run.linear_error }}</span>
            <span v-else class="muted">{{ terminal ? '回写中…' : '测试结束后自动回写' }}</span>
            <button v-if="terminal && run.status !== 'canceled'" class="small" @click="writeBack">重新回写</button>
          </dd>
        </template>
      </dl>
    </div>

    <div v-if="run.error" class="error">{{ run.error }}</div>

    <div v-if="run.source_modified" class="warn-box">
      ⚠ 测试过程中被测源码发生了改动，结论可信度降低。
      <details>
        <summary>查看 diff</summary>
        <pre>{{ run.diff }}</pre>
      </details>
    </div>

    <div v-if="run.status === 'completed'" class="card">
      <h3>总结</h3>
      <p class="body">{{ run.summary }}</p>
      <h3>验收标准（{{ run.acs.length }} 条）</h3>
      <div v-for="a in run.acs" :key="a.id" class="ac">
        <div class="row">
          <span class="tag" :class="acTone[a.status]">{{ acText[a.status] || a.status }}</span>
          <b class="mono">{{ a.id }}</b>
          <span>{{ a.criterion }}</span>
        </div>
        <div v-if="a.evidence">
          <div class="muted">证据</div>
          <pre>{{ a.evidence }}</pre>
        </div>
        <div v-if="a.reasoning">
          <div class="muted">判断依据</div>
          <p class="body">{{ a.reasoning }}</p>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="row between">
        <h3>执行日志</h3>
        <label class="inline muted"><input v-model="follow" type="checkbox" /> 自动滚动</label>
      </div>
      <div ref="logBox" class="logs" @scroll="onScroll">
        <div v-if="!logs.length" class="muted">{{ terminal ? '没有日志' : '等待运行时开始执行…' }}</div>
        <div v-for="l in logs" :key="l.seq" class="log-line">
          <span class="muted">{{ fmtLogTime(l.ts) }}</span> {{ l.text }}
        </div>
      </div>
    </div>

    <details class="card">
      <summary>交给模型的提示词</summary>
      <pre>{{ run.prompt }}</pre>
    </details>
    <details v-if="run.raw" class="card">
      <summary>模型原始结构化输出</summary>
      <pre>{{ run.raw }}</pre>
    </details>
  </template>
</template>
