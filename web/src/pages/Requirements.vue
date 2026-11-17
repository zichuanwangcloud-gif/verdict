<script setup>
import { onMounted, reactive, ref } from 'vue'
import { api } from '../api'

const requirements = ref([])
const verifiers = ref([])
const error = ref('')
const form = reactive({ key: '', title: '', body: '' })
// 每个需求一份编辑中的验收条件草稿：{ [id]: [{text, areasText, verifier, paramsText, reason}] }
const drafts = reactive({})
const busy = reactive({})

const dispositions = [
  { value: 'active', label: '随版本发布' },
  { value: 'closed', label: '关闭（开关关着）' },
  { value: 'removed', label: '摘除' },
]

async function load() {
  try {
    ;[requirements.value, verifiers.value] = await Promise.all([api.requirements(), api.verifiers()])
  } catch (e) {
    error.value = e.message
  }
}

function toRow(ac) {
  return {
    text: ac.text,
    areasText: (ac.areas || []).join(', '),
    verifier: ac.verifier || '',
    paramsText: JSON.stringify(ac.params ?? {}, null, 2),
    reason: ac.reason || '',
  }
}

function edit(r) {
  drafts[r.id] = r.acs.map(toRow)
}

function addRow(id) {
  drafts[id].push(toRow({ text: '', areas: [], verifier: '', params: {} }))
}

async function create() {
  error.value = ''
  try {
    await api.createRequirement({ ...form })
    form.key = form.title = form.body = ''
    await load()
  } catch (e) {
    error.value = e.message
  }
}

async function draft(r) {
  error.value = ''
  busy[r.id] = true
  try {
    drafts[r.id] = (await api.draftACs(r.id)).map(toRow)
  } catch (e) {
    error.value = e.message
  } finally {
    busy[r.id] = false
  }
}

async function save(r) {
  error.value = ''
  let acs
  try {
    acs = drafts[r.id].map((d, i) => {
      let params
      try {
        params = JSON.parse(d.paramsText || '{}')
      } catch {
        throw new Error(`第 ${i + 1} 条的验证器参数不是合法的 JSON`)
      }
      return {
        text: d.text,
        areas: d.areasText.split(',').map((s) => s.trim()).filter(Boolean),
        verifier: d.verifier,
        params,
      }
    })
    await api.saveACs(r.id, acs)
    delete drafts[r.id]
    await load()
  } catch (e) {
    error.value = e.message
  }
}

async function setDisposition(r, value) {
  error.value = ''
  try {
    await api.setDisposition(r.id, value)
    await load()
  } catch (e) {
    error.value = e.message
  }
}

onMounted(load)
</script>

<template>
  <p v-if="error" class="error">{{ error }}</p>

  <section class="card">
    <h2>新建需求</h2>
    <div class="row">
      <input v-model="form.key" placeholder="编号（可选）" style="max-width: 160px" />
      <input v-model="form.title" placeholder="标题" />
    </div>
    <textarea v-model="form.body" rows="4" placeholder="需求描述"></textarea>
    <button class="primary" :disabled="!form.title.trim()" @click="create">创建</button>
  </section>

  <p v-if="!requirements.length" class="muted">还没有需求。先创建一条，再为它添加验收条件。</p>

  <section v-for="r in requirements" :key="r.id" class="card">
    <div class="row between">
      <h2>
        <span v-if="r.key" class="tag">{{ r.key }}</span>
        {{ r.title }}
      </h2>
      <select :value="r.disposition" @change="setDisposition(r, $event.target.value)">
        <option v-for="d in dispositions" :key="d.value" :value="d.value">{{ d.label }}</option>
      </select>
    </div>
    <p v-if="r.body" class="body">{{ r.body }}</p>

    <template v-if="!drafts[r.id]">
      <ol v-if="r.acs.length">
        <li v-for="ac in r.acs" :key="ac.id">
          {{ ac.text }}
          <span v-for="a in ac.areas" :key="a" class="tag">{{ a }}</span>
          <span v-if="ac.verifier" class="tag ok">{{ ac.verifier }}</span>
          <span v-else class="tag bad">无法自动验证</span>
        </li>
      </ol>
      <p v-else class="muted">还没有验收条件，这条需求无法放行。</p>
      <div class="row">
        <button @click="edit(r)">编辑验收条件</button>
        <button :disabled="busy[r.id]" @click="draft(r)">
          {{ busy[r.id] ? '智能体起草中…' : '让智能体起草' }}
        </button>
      </div>
    </template>

    <template v-else>
      <p class="muted">保存会整体替换这条需求的验收条件，旧条件已有的验证证据会一并删除。</p>
      <div v-for="(d, i) in drafts[r.id]" :key="i" class="ac-edit">
        <div class="row">
          <strong>{{ i + 1 }}</strong>
          <input v-model="d.text" placeholder="验收条件" />
          <button @click="drafts[r.id].splice(i, 1)">删除</button>
        </div>
        <div class="row">
          <input v-model="d.areasText" placeholder="涉及区域，逗号分隔" />
          <select v-model="d.verifier">
            <option value="">无法自动验证</option>
            <option v-for="v in verifiers" :key="v.name" :value="v.name">{{ v.name }}</option>
          </select>
        </div>
        <textarea v-if="d.verifier" v-model="d.paramsText" rows="4" class="mono"></textarea>
        <p v-if="d.reason" class="muted">{{ d.reason }}</p>
      </div>
      <div class="row">
        <button @click="addRow(r.id)">添加一条</button>
        <button class="primary" @click="save(r)">保存</button>
        <button @click="delete drafts[r.id]">取消</button>
      </div>
    </template>
  </section>
</template>
