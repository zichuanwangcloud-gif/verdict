<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { Plus, Server, RefreshCw, PencilLine, Trash2, Copy, Check, Terminal, ShieldAlert, X, Cpu, GitBranch, Container, Sparkles, MonitorSmartphone, KeyRound, ArrowUpCircle, Gauge, Layers } from 'lucide-vue-next'
import { api } from '../api'
import { ago } from '../format'
import HarnessLogo from '../components/HarnessLogo.vue'

const list = ref([])
const loaded = ref(false)
const err = ref('')
const name = ref('')
const created = ref(null)
const copied = ref(false)
const probing = ref(new Set())
let timer

async function load() {
  try {
    list.value = await api.runtimes()
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
    setTimeout(() => (copied.value = false), 2000)
  } catch {
    /* 非 HTTPS 下剪贴板不可用：命令文本可直接全选 */
  }
}

async function probe(rt) {
  probing.value = new Set(probing.value).add(rt.id)
  try {
    await api.probeRuntime(rt.id)
    setTimeout(load, 3000)
  } catch (e) {
    err.value = e.message
  } finally {
    setTimeout(() => {
      const s = new Set(probing.value)
      s.delete(rt.id)
      probing.value = s
      load()
    }, 8000)
  }
}

async function rotate(rt, upgrade) {
  const msg = upgrade
    ? `为「${rt.name}」生成新的安装命令？在那台机器上执行后即完成升级；旧令牌立即失效。`
    : `重新生成「${rt.name}」的令牌？旧令牌立即失效，agent 需要用新命令重新安装。`
  if (!confirm(msg)) return
  try {
    created.value = await api.rotateToken(rt.id)
    copied.value = false
    window.scrollTo({ top: 0, behavior: 'smooth' })
    load()
  } catch (e) {
    err.value = e.message
  }
}

async function editConcurrency(rt) {
  const cur = rt.concurrency > 0 ? rt.concurrency : ''
  const v = prompt(`「${rt.name}」的并发度（同时跑几个测试，1–32）。留空表示按机器自动（建议 ${rt.suggested_concurrency}）。`, cur)
  if (v === null) return
  const n = v.trim() === '' ? 0 : Number(v)
  if (!Number.isInteger(n) || n < 0 || n > 32) {
    err.value = '并发度必须是 0（自动）到 32 之间的整数'
    return
  }
  try {
    await api.setConcurrency(rt.id, n)
    load()
  } catch (e) {
    err.value = e.message
  }
}

async function editServiceSlots(rt) {
  const cur = rt.service_slots > 0 ? rt.service_slots : ''
  const v = prompt(`「${rt.name}」同时运行几个被测服务（1–32）。留空表示按并发度自动（建议 ${rt.suggested_service_slots}）。服务是内存大户，调低可以避免并发测试把机器内存打满。`, cur)
  if (v === null) return
  const n = v.trim() === '' ? 0 : Number(v)
  if (!Number.isInteger(n) || n < 0 || n > 32) {
    err.value = '服务槽位必须是 0（自动）到 32 之间的整数'
    return
  }
  try {
    await api.setServiceSlots(rt.id, n)
    load()
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
  return { tone: 'ok', text: '就绪' }
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Runtimes</div>
        <h2>运行时</h2>
        <p class="sub">真正执行测试的机器。agent 会主动连回本站（只需要它能访问本站地址），并自动识别机器上的 Claude Code / Codex。</p>
      </div>
    </div>
    <div v-if="err" class="alert bad">{{ err }}</div>

    <form class="card row add-form" @submit.prevent="add">
      <span class="choice-icon"><Plus :size="17" /></span>
      <input v-model="name" placeholder="给新运行时起个名字，如 测试服务器-1" style="flex: 1; width: auto" />
      <button class="btn primary" :disabled="!name.trim()"><Plus :size="15" /> 添加运行时</button>
    </form>

    <section v-if="created" class="card glow stack" style="gap: 14px">
      <div class="card-head" style="margin: 0">
        <div class="card-title"><Terminal :size="16" /> 在「{{ created.runtime?.name }}」上执行</div>
        <button class="btn ghost icon" aria-label="关闭" @click="created = null"><X :size="16" /></button>
      </div>
      <div class="cmd">
        <pre>{{ created.install_command }}</pre>
        <button class="btn sm" @click="copy"><component :is="copied ? Check : Copy" :size="13" /> {{ copied ? '已复制' : '复制' }}</button>
      </div>
      <div class="alert warn">
        <ShieldAlert :size="16" />
        <div class="stack body">
          <b>令牌只显示这一次。</b>
          <ul class="tips">
            <li>用<b>普通用户</b>执行，不要用 root。agent 会以该用户身份运行 claude / codex，沿用其登录状态和 git 凭据。</li>
            <li>agent 以跳过权限确认的方式运行，建议使用专门的测试机或专用系统用户。</li>
            <li>私有仓库需要该用户能直接 <code>git clone</code>。</li>
            <li>UI 走查需要 Node.js 和浏览器：<code>npx playwright install chromium</code>。</li>
          </ul>
        </div>
      </div>
    </section>

    <div v-if="loaded && !list.length" class="card">
      <div class="empty">
        <span class="empty-icon"><Server :size="24" /></span>
        <h3>还没有运行时</h3>
        <p class="muted">在上方添加一个，然后在那台机器上执行安装命令。</p>
      </div>
    </div>

    <div class="rt-grid">
      <section v-for="rt in list" :key="rt.id" class="card rt-card">
        <div class="row between">
          <div class="row" style="gap: 10px; min-width: 0">
            <span class="choice-icon"><Server :size="17" /></span>
            <div style="min-width: 0">
              <div class="row" style="gap: 8px"><b>{{ rt.name }}</b><span class="dot" :class="{ on: rt.online }" /></div>
              <div class="choice-sub">{{ rt.online ? '在线' : '离线 · 最后在线 ' + ago(rt.last_seen) }}</div>
            </div>
          </div>
          <div class="row" style="gap: 2px">
            <button class="btn ghost icon" title="设置并发度" @click="editConcurrency(rt)"><Gauge :size="15" /></button>
            <button class="btn ghost icon" title="重新探测" :disabled="!rt.online" @click="probe(rt)">
              <RefreshCw :size="15" :class="{ spin: probing.has(rt.id) }" />
            </button>
            <button class="btn ghost icon" title="改名" @click="rename(rt)"><PencilLine :size="15" /></button>
            <button class="btn ghost icon" title="重新生成令牌与安装命令" @click="rotate(rt, false)"><KeyRound :size="15" /></button>
            <button class="btn ghost icon danger" title="删除" @click="remove(rt)"><Trash2 :size="15" /></button>
          </div>
        </div>
        <div v-if="rt.outdated" class="alert warn">
          <ArrowUpCircle :size="16" />
          <div class="stack body" style="flex: 1">
            <b>agent 版本过旧（{{ rt.info?.agent_version }}），不会再接收测试</b>
            <span>在那台机器上执行新的安装命令即可原地升级，已有缓存会保留。</span>
            <div><button class="btn sm" @click="rotate(rt, true)"><ArrowUpCircle :size="13" /> 生成升级命令</button></div>
          </div>
        </div>
        <template v-if="rt.info">
          <div class="rt-host">
            <span class="chip"><Cpu /> {{ rt.info.hostname }} · {{ rt.info.os }}/{{ rt.info.arch }}</span>
            <span v-if="rt.info.git" class="chip"><GitBranch /> git {{ rt.info.git }}</span>
            <span v-if="rt.info.docker" class="chip"><Container /> docker</span>
            <span class="chip mono">agent {{ rt.info.agent_version }}</span>
            <span v-if="rt.info.cpu" class="chip"><Cpu /> {{ rt.info.cpu }} 核<template v-if="rt.info.mem_total_mb"> · {{ Math.round(rt.info.mem_total_mb / 1024) }}GB</template></span>
            <button class="chip" :title="rt.concurrency > 0 ? '手动设置（点击修改）' : '按机器自动（点击修改）'" @click="editConcurrency(rt)" style="cursor: pointer">
              <Gauge /> 并发 {{ rt.effective_concurrency }}<template v-if="rt.info.busy"> · 忙 {{ rt.info.busy }}</template>
              <span v-if="!rt.concurrency" class="muted">（自动）</span>
            </button>
            <button class="chip" :title="rt.service_slots > 0 ? '手动设置（点击修改）' : '按并发度自动（点击修改）'" @click="editServiceSlots(rt)" style="cursor: pointer">
              <Layers /> 服务槽位 {{ rt.effective_service_slots }}
              <span v-if="!rt.service_slots" class="muted">（自动）</span>
            </button>
          </div>
          <div class="stack" style="gap: 8px">
            <div v-for="h in rt.info.harnesses" :key="h.name" class="harness-row">
              <HarnessLogo :name="h.name" />
              <div style="min-width: 0; flex: 1">
                <div class="row" style="gap: 8px">
                  <span class="name">{{ h.name === 'claude' ? 'Claude Code' : h.name === 'codex' ? 'Codex' : h.name }}</span>
                  <span v-if="h.version" class="ver">{{ h.version }}</span>
                </div>
                <div class="detail" :title="h.detail">{{ h.detail }}</div>
              </div>
              <span class="pill" :class="hState(h).tone">{{ hState(h).text }}</span>
            </div>
            <div class="harness-row">
              <span class="harness-logo" style="background: var(--grad)"><MonitorSmartphone :size="15" /></span>
              <div style="min-width: 0; flex: 1">
                <span class="name">无头浏览器</span>
                <div class="detail" :title="rt.info.browser?.detail">{{ rt.info.browser?.detail || '未探测（agent 版本过旧）' }}</div>
              </div>
              <span class="pill" :class="rt.info.browser?.available ? 'ok' : ''">{{ rt.info.browser?.available ? '就绪' : '不可用' }}</span>
            </div>
          </div>
        </template>
        <div v-else class="muted">等待 agent 首次连接…</div>
      </section>
    </div>
  </div>
</template>
