<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../api'

const list = ref([])
const err = ref('')
const form = ref({ url: '', name: '', test_notes: '' })
const editing = ref(null)

async function load() {
  try {
    list.value = await api.repos()
  } catch (e) {
    err.value = e.message
  }
}
onMounted(load)

async function add() {
  err.value = ''
  try {
    await api.createRepo(form.value)
    form.value = { url: '', name: '', test_notes: '' }
    load()
  } catch (e) {
    err.value = e.message
  }
}

function edit(r) {
  editing.value = { ...r }
}

async function save() {
  err.value = ''
  try {
    await api.updateRepo(editing.value.id, editing.value)
    editing.value = null
    load()
  } catch (e) {
    err.value = e.message
  }
}

async function remove(r) {
  if (!confirm(`删除仓库「${r.name}」？`)) return
  try {
    await api.deleteRepo(r.id)
    load()
  } catch (e) {
    err.value = e.message
  }
}

// 与 agent 的 RepoKey 保持一致：运行时上私密文件放在 ~/.verdict-agent/env/<key>/
function repoKey(url) {
  let u = url
  const i = u.indexOf('://')
  if (i >= 0) u = u.slice(i + 3)
  const j = u.lastIndexOf('@')
  if (j >= 0) u = u.slice(j + 1)
  u = u.replace(/\/+$/, '').replace(/\.git$/, '')
  u = u.replace(/[^A-Za-z0-9._-]+/g, '_').replace(/^[._-]+|[._-]+$/g, '')
  return u || 'repo'
}
</script>

<template>
  <h2>仓库</h2>
  <div v-if="err" class="error">{{ err }}</div>

  <form class="card form" @submit.prevent="add">
    <h3>添加仓库</h3>
    <input v-model="form.url" placeholder="git 地址，如 git@github.com:org/repo.git 或 https://github.com/org/repo.git" />
    <input v-model="form.name" placeholder="显示名称（可选，默认取 org/repo）" />
    <textarea
      v-model="form.test_notes"
      rows="5"
      placeholder="测试说明（可选，会交给模型）：怎么安装依赖、怎么启动服务、依赖哪些外部服务、测试账号、注意事项…"
    />
    <div><button class="primary" :disabled="!form.url.trim()">添加</button></div>
  </form>

  <div v-for="r in list" :key="r.id" class="card">
    <template v-if="editing && editing.id === r.id">
      <div class="form">
        <input v-model="editing.url" />
        <input v-model="editing.name" />
        <textarea v-model="editing.test_notes" rows="8" placeholder="测试说明" />
        <div class="row">
          <button class="primary" @click="save">保存</button>
          <button @click="editing = null">取消</button>
        </div>
      </div>
    </template>
    <template v-else>
      <div class="row between">
        <div>
          <b>{{ r.name }}</b>
          <div class="mono muted">{{ r.url }}</div>
        </div>
        <div class="row">
          <button class="small" @click="edit(r)">编辑</button>
          <button class="small danger" @click="remove(r)">删除</button>
        </div>
      </div>
      <div class="muted">测试说明</div>
      <p class="body">{{ r.test_notes || '（未填写）' }}</p>
      <div class="muted small-text">
        私密文件（如 .env）放在运行时的 <code>~/.verdict-agent/env/{{ repoKey(r.url) }}/</code>，每次测试会复制到代码根目录，不经过本后端。
      </div>
    </template>
  </div>
</template>
