<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { ago } from '../format'

const list = ref([])
const err = ref('')
const name = ref('')
const created = ref(null)
const copied = ref(false)
let timer

async function load() {
  try {
    list.value = await api.runtimes()
    err.value = ''
  } catch (e) {
    err.value = e.message
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 5000)
})
onUnmounted(() => clearInterval(timer))

async function add() {
  err.value = ''
  try {
    created.value = await api.createRuntime(name.value.trim())
    name.value = ''
    copied.value = false
    load()
  } catch (e) {
    err.value = e.message
  }
}

async function copy() {
  try {
    await navigator.clipboard.writeText(created.value.install_command)
    copied.value = true
  } catch {
    copied.value = false
  }
}

async function probe(rt) {
  try {
    await api.probeRuntime(rt.id)
    setTimeout(load, 3000)
    setTimeout(load, 10000)
  } catch (e) {
    err.value = e.message
  }
}

async function rename(rt) {
  const n = prompt('新名称', rt.name)
  if (!n || n.trim() === rt.name) return
  try {
    await api.renameRuntime(rt.id, n.trim())
    load()
  } catch (e) {
    err.value = e.message
  }
}

async function remove(rt) {
  if (!confirm(`删除运行时「${rt.name}」？它的令牌会立即失效，未结束的测试会被标记为出错。`)) return
  try {
    await api.deleteRuntime(rt.id)
    load()
  } catch (e) {
    err.value = e.message
  }
}

function hState(h) {
  if (!h.installed) return { tone: '', text: '未安装' }
  if (!h.logged_in) return { tone: 'warn', text: '未登录' }
  return { tone: 'ok', text: '可用' }
}
</script>

<template>
  <h2>运行时</h2>
  <p class="muted">运行时是真正执行测试的机器。在目标机器上运行安装命令后，agent 会主动连接本后端（只需要它能访问本站地址），并自动探测 Claude Code / Codex。</p>
  <div v-if="err" class="error">{{ err }}</div>

  <form class="card row" @submit.prevent="add">
    <input v-model="name" placeholder="运行时名称，如 测试服务器-1" />
    <button class="primary" :disabled="!name.trim()">添加运行时</button>
  </form>

  <div v-if="created" class="card highlight">
    <div class="row between">
      <h3>在「{{ created.runtime.name }}」上执行以下命令</h3>
      <button class="link" @click="created = null">关闭</button>
    </div>
    <pre class="cmd">{{ created.install_command }}</pre>
    <div class="row">
      <button @click="copy">{{ copied ? '已复制' : '复制命令' }}</button>
      <span class="muted">令牌只显示这一次。</span>
    </div>
    <ul class="muted tips">
      <li>用<b>普通用户</b>执行（不要用 root），agent 会以该用户身份运行 claude / codex，沿用其登录状态与 git 凭据。</li>
      <li>建议使用专门的测试机或专用系统用户：harness 以跳过权限确认的方式运行。</li>
      <li>私有仓库需要该用户能直接 <code>git clone</code>（SSH key 或凭据助手）。</li>
    </ul>
  </div>

  <div v-if="!list.length" class="card muted">还没有运行时。</div>
  <div v-for="rt in list" :key="rt.id" class="card">
    <div class="row between">
      <div class="row">
        <span class="dot" :class="rt.online ? 'on' : 'off'" />
        <b>{{ rt.name }}</b>
        <span class="muted">{{ rt.online ? '在线' : '离线 · 最后在线 ' + ago(rt.last_seen) }}</span>
      </div>
      <div class="row">
        <button class="small" :disabled="!rt.online" @click="probe(rt)">重新探测</button>
        <button class="small" @click="rename(rt)">改名</button>
        <button class="small danger" @click="remove(rt)">删除</button>
      </div>
    </div>
    <template v-if="rt.info">
      <div class="muted">
        {{ rt.info.hostname }} · {{ rt.info.os }}/{{ rt.info.arch }} · agent {{ rt.info.agent_version }}
        <template v-if="rt.info.git"> · git {{ rt.info.git }}</template>
        <template v-if="rt.info.docker"> · {{ rt.info.docker }}</template>
      </div>
      <table class="harness">
        <tr v-for="h in rt.info.harnesses" :key="h.name">
          <td class="status"><span class="tag" :class="hState(h).tone">{{ hState(h).text }}</span></td>
          <td class="nowrap"><b>{{ h.name === 'claude' ? 'Claude Code' : h.name === 'codex' ? 'Codex' : h.name }}</b></td>
          <td class="mono">{{ h.version }}</td>
          <td class="muted">{{ h.detail }}</td>
        </tr>
      </table>
    </template>
    <div v-else class="muted">尚未连接过，等待安装。</div>
  </div>
</template>
