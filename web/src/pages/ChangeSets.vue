<script setup>
import { onMounted, reactive, ref } from 'vue'
import { api } from '../api'

const changesets = ref([])
const selected = ref(null)
const verdict = ref(null)
const error = ref('')
const creating = ref(false)
const running = ref(false)
const form = reactive({ ref: '', digest: '', mode: 'diff', diff: '', areasText: '' })

const statusLabel = {
  verified: '已验证',
  failed: '未通过',
  stale: '已失效',
  untested: '未测',
  unverifiable: '无法自动验证',
}
const dispositionLabel = { active: '', closed: '已关闭', removed: '已摘除' }

async function load() {
  try {
    changesets.value = await api.changesets()
    if (!selected.value && changesets.value.length) {
      await select(changesets.value[changesets.value.length - 1])
    }
  } catch (e) {
    error.value = e.message
  }
}

async function select(c) {
  error.value = ''
  selected.value = c
  try {
    verdict.value = await api.verdict(c.id)
  } catch (e) {
    error.value = e.message
  }
}

async function create() {
  error.value = ''
  creating.value = true
  try {
    const body = { ref: form.ref, digest: form.digest }
    if (form.mode === 'diff') body.diff = form.diff
    else body.touched_areas = form.areasText.split(',').map((s) => s.trim()).filter(Boolean)
    const { id } = await api.createChangeSet(body)
    form.ref = form.digest = form.diff = form.areasText = ''
    selected.value = null
    await load()
    const created = changesets.value.find((c) => c.id === id)
    if (created) await select(created)
  } catch (e) {
    error.value = e.message
  } finally {
    creating.value = false
  }
}

async function run() {
  error.value = ''
  running.value = true
  try {
    verdict.value = (await api.run(selected.value.id)).verdict
  } catch (e) {
    error.value = e.message
  } finally {
    running.value = false
  }
}

onMounted(load)
</script>

<template>
  <p v-if="error" class="error">{{ error }}</p>

  <section class="card">
    <h2>登记提测版本</h2>
    <div class="row">
      <input v-model="form.ref" placeholder="版本名，如 v1.2.0-rc.1" />
      <input v-model="form.digest" placeholder="构建产物摘要（可选）" />
    </div>
    <div class="row">
      <label><input v-model="form.mode" type="radio" value="diff" /> 提供变更内容，由智能体判断触碰的区域</label>
      <label><input v-model="form.mode" type="radio" value="areas" /> 直接填写触碰的区域</label>
    </div>
    <textarea
      v-if="form.mode === 'diff'"
      v-model="form.diff"
      rows="6"
      class="mono"
      placeholder="粘贴相对上一个版本的 diff"
    ></textarea>
    <input v-else v-model="form.areasText" placeholder="区域名，逗号分隔；留空表示没有触碰任何区域" />
    <button class="primary" :disabled="!form.ref.trim() || creating" @click="create">
      {{ creating ? '登记中…' : '登记' }}
    </button>
  </section>

  <p v-if="!changesets.length" class="muted">还没有提测版本。</p>

  <div v-else class="row wrap">
    <button
      v-for="c in changesets"
      :key="c.id"
      :class="{ active: selected?.id === c.id }"
      @click="select(c)"
    >
      {{ c.ref }}
    </button>
  </div>

  <section v-if="verdict" class="card">
    <div class="row between">
      <h2>
        {{ verdict.changeset.ref }}
        <span class="tag" :class="verdict.releasable ? 'ok' : 'bad'">
          {{ verdict.releasable ? '可发布' : '不可发布' }}
        </span>
      </h2>
      <button class="primary" :disabled="running" @click="run">
        {{ running ? '执行中…' : '执行待验证项' }}
      </button>
    </div>
    <p class="muted">
      触碰的区域：
      <span v-if="!verdict.changeset.touched_areas.length">无</span>
      <span v-for="a in verdict.changeset.touched_areas" :key="a" class="tag">{{ a }}</span>
    </p>
    <p v-if="verdict.changeset.analysis?.reasoning" class="muted">
      智能体分析（风险 {{ verdict.changeset.analysis.risk }}）：{{ verdict.changeset.analysis.reasoning }}
    </p>

    <p v-if="!verdict.requirements?.length" class="muted">还没有需求，无从裁决。</p>

    <div v-for="r in verdict.requirements" :key="r.requirement.id" class="req">
      <h3>
        <span v-if="r.requirement.key" class="tag">{{ r.requirement.key }}</span>
        {{ r.requirement.title }}
        <span v-if="dispositionLabel[r.requirement.disposition]" class="tag">
          {{ dispositionLabel[r.requirement.disposition] }}
        </span>
        <span v-if="r.blocking" class="tag bad">阻断</span>
      </h3>
      <p v-if="!r.acs?.length" class="muted">没有验收条件。</p>
      <table v-else>
        <tbody>
          <tr v-for="a in r.acs" :key="a.ac.id">
            <td class="status"><span class="tag" :class="a.status">{{ statusLabel[a.status] }}</span></td>
            <td>
              {{ a.ac.text }}
              <div v-if="a.carried_from" class="muted">结论沿用自 {{ a.carried_from }}</div>
              <div v-if="a.invalidated_by" class="muted">被 {{ a.invalidated_by }} 的变更推翻，需重测</div>
              <details v-if="a.evidence">
                <summary>证据（{{ a.evidence.verifier }}）</summary>
                <pre>{{ a.evidence.output || '（无输出）' }}</pre>
              </details>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
