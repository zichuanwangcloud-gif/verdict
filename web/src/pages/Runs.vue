<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Plus, Sparkles, Activity, CircleCheck, CircleX, ListChecks, GitCommitHorizontal } from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { fmtTime, fmtDuration, harnessName, isLive, ago } from '../format'
import StatusPill from '../components/StatusPill.vue'
import AcBar from '../components/AcBar.vue'
import HarnessLogo from '../components/HarnessLogo.vue'

const runs = ref([])
const loaded = ref(false)
const err = ref('')
let timer

async function load() {
  try {
    runs.value = await api.runs()
    err.value = ''
  } catch (e) {
    err.value = e.message
  } finally {
    loaded.value = true
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 4000)
})
onUnmounted(() => clearInterval(timer))

const stats = computed(() => {
  const done = runs.value.filter((r) => r.status === 'completed')
  return {
    total: runs.value.length,
    live: runs.value.filter(isLive).length,
    pass: done.filter((r) => r.verdict === 'pass').length,
    fail: done.filter((r) => r.verdict === 'fail').length,
  }
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Acceptance runs</div>
        <h2>验收测试</h2>
        <p class="sub">每一次测试，都是一个 AI agent 在隔离的工作区里，按需求的验收标准逐条实测这个分支。</p>
      </div>
      <button class="btn primary" @click="go('/runs/new')"><Plus :size="16" /> 新建测试</button>
    </div>

    <div v-if="err" class="alert bad">{{ err }}</div>

    <div v-if="runs.length" class="stats">
      <div class="card stat">
        <div class="stat-label"><ListChecks :size="13" /> 测试总数</div>
        <div class="stat-value">{{ stats.total }}</div>
      </div>
      <div class="card stat">
        <div class="stat-label"><Activity :size="13" /> 进行中</div>
        <div class="stat-value" :class="{ 'grad-text': stats.live }">{{ stats.live }}</div>
      </div>
      <div class="card stat">
        <div class="stat-label"><CircleCheck :size="13" /> 通过</div>
        <div class="stat-value" style="color: var(--ok)">{{ stats.pass }}</div>
      </div>
      <div class="card stat">
        <div class="stat-label"><CircleX :size="13" /> 未通过</div>
        <div class="stat-value" style="color: var(--bad)">{{ stats.fail }}</div>
      </div>
    </div>

    <div v-if="loaded && !runs.length && !err" class="card">
      <div class="empty">
        <span class="empty-icon"><Sparkles :size="24" /></span>
        <h3>还没有测试</h3>
        <p class="muted">先在「运行时」接入一台装有 Claude Code 或 Codex 的机器，再在「仓库」添加被测仓库。</p>
        <button class="btn primary" @click="go('/runs/new')"><Plus :size="16" /> 新建第一个测试</button>
      </div>
    </div>

    <div v-else-if="runs.length" class="card flush">
      <div class="run-list">
        <a
          v-for="r in runs"
          :key="r.id"
          :href="'/runs/' + r.id"
          class="run-row"
          :class="{ shimmer: isLive(r) }"
          @click.prevent="go('/runs/' + r.id)"
        >
          <div class="run-status stack">
            <StatusPill :run="r" />
            <AcBar :acs="r.acs" />
          </div>
          <div class="run-main" style="min-width: 0">
            <div class="run-title"><span class="issue-key">{{ r.issue_key }}</span>{{ r.issue.title }}</div>
            <div class="run-sub">{{ r.summary || r.error || (isLive(r) ? 'agent 正在测试…' : '') }}</div>
          </div>
          <div class="run-meta hide-sm">
            <span class="row" style="gap: 6px"><GitCommitHorizontal :size="13" /> {{ r.repo_name }}</span>
            <span class="mono">{{ r.branch }}<template v-if="r.commit_sha"> · {{ r.commit_sha.slice(0, 7) }}</template></span>
          </div>
          <div class="run-meta hide-sm">
            <span class="row" style="gap: 6px"><HarnessLogo :name="r.harness" style="width: 18px; height: 18px; font-size: 10px; border-radius: 5px" /> {{ harnessName(r.harness) }}</span>
            <span class="muted" :title="fmtTime(r.created_at)">{{ ago(r.created_at) }} · {{ fmtDuration(r.started_at, r.finished_at) }}</span>
          </div>
        </a>
      </div>
    </div>
  </div>
</template>
