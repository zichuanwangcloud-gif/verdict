<script setup>
import { ref, computed, onMounted } from 'vue'
import { PackageCheck, Plus, X, Sparkles, GitBranch, CircleCheck, Lock, Rocket } from 'lucide-vue-next'
import { api } from '../api'
import { go } from '../router'
import { fmtTime, ago, versionStatusText } from '../format'

const repos = ref([])
const versions = ref([])
const err = ref('')
const adding = ref(false)
const form = ref({ repo_id: '', name: '', note: '' })

async function load() {
  try {
    ;[repos.value, versions.value] = await Promise.all([api.repos(), api.versions()])
    if (!form.value.repo_id && repos.value.length) form.value.repo_id = repos.value[0].id
  } catch (e) {
    err.value = e.message
  }
}
onMounted(load)

const grouped = computed(() => {
  const by = {}
  for (const v of versions.value) {
    ;(by[v.repo_name] ||= []).push(v)
  }
  return Object.entries(by)
})

const tone = { open: 'info', frozen: 'warn', released: 'ok' }

async function add() {
  err.value = ''
  try {
    await api.createVersion({ repo_id: form.value.repo_id, name: form.value.name, note: form.value.note })
    form.value.name = ''
    form.value.note = ''
    adding.value = false
    load()
  } catch (e) {
    err.value = e.message
  }
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Releases</div>
        <h2>版本</h2>
        <p class="sub">把一批 issue 归到一个版本里。版本冻结到某个 commit 后，在这一份最终代码上整体重测这批 issue 和积累下来的回归用例。</p>
      </div>
      <button v-if="!adding" class="btn primary" :disabled="!repos.length" @click="adding = true"><Plus :size="16" /> 新建版本</button>
    </div>

    <div v-if="err" class="alert bad">{{ err }}</div>

    <form v-if="adding" class="card glow stack" style="gap: 14px" @submit.prevent="add">
      <div class="card-head" style="margin: 0">
        <div class="card-title"><PackageCheck :size="16" /> 新建版本</div>
        <button type="button" class="btn ghost icon" aria-label="关闭" @click="adding = false"><X :size="16" /></button>
      </div>
      <div class="grid-2">
        <div class="field">
          <label>仓库</label>
          <select v-model="form.repo_id">
            <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>版本名</label>
          <input v-model="form.name" placeholder="1.4.0" />
        </div>
      </div>
      <div class="field">
        <label>备注（可选）</label>
        <input v-model="form.note" placeholder="这个版本要做什么" />
      </div>
      <div class="row"><button class="btn primary" :disabled="!form.name.trim() || !form.repo_id"><Plus :size="15" /> 创建</button></div>
    </form>

    <div v-if="!versions.length && !adding" class="card">
      <div class="empty">
        <span class="empty-icon"><PackageCheck :size="24" /></span>
        <h3>还没有版本</h3>
        <p class="muted">先建一个版本，然后在新建测试时把 issue 归进去。</p>
        <button v-if="repos.length" class="btn primary" @click="adding = true"><Plus :size="16" /> 新建版本</button>
      </div>
    </div>

    <section v-for="[repo, list] in grouped" :key="repo" class="card flush">
      <div style="padding: 14px 16px; border-bottom: 1px solid var(--border)" class="row" >
        <GitBranch :size="15" /> <b>{{ repo }}</b>
        <span class="muted" style="font-size: 12px">{{ list.length }} 个版本</span>
      </div>
      <a
        v-for="v in list"
        :key="v.id"
        :href="'/versions/' + v.id"
        class="run-row"
        @click.prevent="go('/versions/' + v.id)"
      >
        <div class="run-status"><span class="pill" :class="tone[v.status]"><component :is="v.status === 'released' ? CircleCheck : v.status === 'frozen' ? Lock : Rocket" /> {{ versionStatusText[v.status] }}</span></div>
        <div class="run-main" style="min-width: 0">
          <div class="run-title">{{ v.name }}</div>
          <div class="run-sub">{{ v.note || (v.issue_count + ' 个 issue') }}</div>
        </div>
        <div class="run-meta hide-sm">
          <span class="chip">{{ v.issue_count }} 个 issue</span>
          <span class="muted" :title="fmtTime(v.created_at)">{{ v.status === 'released' ? '发布于 ' + fmtTime(v.released_at) : ago(v.created_at) }}</span>
        </div>
      </a>
    </section>
  </div>
</template>
