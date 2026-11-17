<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { api } from '../api'
import { go } from '../router'

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
const timeout = ref(60)

onMounted(async () => {
  try {
    ;[repos.value, runtimes.value] = await Promise.all([api.repos(), api.runtimes()])
    if (repos.value.length === 1) repoId.value = repos.value[0].id
    const online = runtimes.value.filter((r) => r.online)
    if (online.length === 1) runtimeId.value = online[0].id
  } catch (e) {
    err.value = e.message
  }
})

const runtime = computed(() => runtimes.value.find((r) => r.id === runtimeId.value))
const harnessInfo = computed(() => runtime.value?.info?.harnesses || [])

function harnessState(name) {
  const h = harnessInfo.value.find((x) => x.name === name)
  if (!h) return '未知'
  if (!h.installed) return '未安装'
  if (!h.logged_in) return '未登录'
  return h.version || '可用'
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
      timeout_minutes: Number(timeout.value) || 60,
    }
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
  <h2>新建测试</h2>
  <form class="card form" @submit.prevent="submit">
    <label>需求来源</label>
    <div class="row">
      <label class="inline"><input v-model="mode" type="radio" value="linear" /> Linear issue（结果会回写评论）</label>
      <label class="inline"><input v-model="mode" type="radio" value="manual" /> 手动输入</label>
    </div>

    <template v-if="mode === 'linear'">
      <div class="row">
        <input v-model="issueKey" placeholder="issue 编号，如 ENG-123" />
        <button type="button" :disabled="!issueKey.trim() || previewing" @click="doPreview">预览</button>
      </div>
      <div v-if="previewErr" class="error">{{ previewErr }}</div>
      <div v-if="preview" class="preview">
        <div><b>{{ preview.identifier }}</b> {{ preview.title }}</div>
        <div class="body muted clamp-6">{{ preview.description || '（无描述）' }}</div>
        <div class="muted">{{ preview.comments?.length || 0 }} 条评论会一并交给模型</div>
      </div>
    </template>
    <template v-else>
      <input v-model="manual.title" placeholder="标题（可选）" />
      <textarea v-model="manual.description" rows="8" placeholder="需求背景与验收标准" />
    </template>

    <label>仓库</label>
    <div class="row">
      <select v-model="repoId">
        <option value="" disabled>选择仓库</option>
        <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }} — {{ r.url }}</option>
      </select>
      <a href="/repos" @click.prevent="go('/repos')">管理仓库</a>
    </div>

    <label>分支</label>
    <input v-model="branch" placeholder="如 feature/login" />

    <label>运行时</label>
    <div class="row">
      <select v-model="runtimeId">
        <option value="" disabled>选择运行时</option>
        <option v-for="r in runtimes" :key="r.id" :value="r.id">
          {{ r.name }}{{ r.online ? '' : '（离线，上线后执行）' }}
        </option>
      </select>
      <a href="/runtimes" @click.prevent="go('/runtimes')">管理运行时</a>
    </div>

    <label>Harness</label>
    <div class="row">
      <label v-for="h in ['claude', 'codex']" :key="h" class="inline">
        <input v-model="harness" type="radio" :value="h" />
        {{ h === 'claude' ? 'Claude Code' : 'Codex' }}
        <span v-if="runtime" class="muted">（{{ harnessState(h) }}）</span>
      </label>
    </div>

    <label>超时（分钟）</label>
    <input v-model="timeout" type="number" min="1" max="1440" class="short" />

    <div v-if="err" class="error">{{ err }}</div>
    <div class="row">
      <button class="primary" :disabled="!canSubmit || busy">{{ busy ? '提交中…' : '开始测试' }}</button>
      <button type="button" @click="go('/runs')">取消</button>
    </div>
  </form>
</template>
