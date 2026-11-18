<script setup>
import { ref, onMounted } from 'vue'
import { Plus, GitBranch, PencilLine, Trash2, NotebookPen, KeyRound, Sparkles, X, Rocket, Package, HeartPulse, Globe } from 'lucide-vue-next'
import { api } from '../api'

const list = ref([])
const err = ref('')
const adding = ref(false)
const blank = () => ({ url: '', name: '', test_notes: '', setup_cmd: '', start_cmd: '', health_url: '', app_url: '', ports: 1 })
const form = ref(blank())
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
    form.value = blank()
    adding.value = false
    load()
  } catch (e) {
    err.value = e.message
  }
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

const notesPlaceholder = '可以不填，agent 会读代码推断；写了会更稳。例如：\n- 依赖：运行时上已有 postgres 容器 my-pg（用户 app / 密码 xxx）和 redis 容器 my-redis，建独立库即可\n- 初始化：服务启动时自动建表；管理员账号由 ADMIN_EMAIL / ADMIN_PASSWORD 生成\n- 注意：前端在 apps/web 下，单独 pnpm install'
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Repositories</div>
        <h2>仓库</h2>
        <p class="sub">被测仓库与它的「测试说明」。说明写得越具体，agent 越容易把服务跑起来，结论也越可靠。</p>
      </div>
      <button v-if="!adding" class="btn primary" @click="adding = true"><Plus :size="16" /> 添加仓库</button>
    </div>
    <div v-if="err" class="alert bad">{{ err }}</div>

    <form v-if="adding" class="card glow stack" style="gap: 14px" @submit.prevent="add">
      <div class="card-head" style="margin: 0">
        <div class="card-title"><GitBranch :size="16" /> 添加仓库</div>
        <button type="button" class="btn ghost icon" aria-label="关闭" @click="adding = false"><X :size="16" /></button>
      </div>
      <div class="grid-2">
        <div class="field">
          <label>git 地址</label>
          <input v-model="form.url" class="mono" placeholder="git@github.com:org/repo.git" />
        </div>
        <div class="field">
          <label>显示名称</label>
          <input v-model="form.name" placeholder="可选，默认取 org/repo" />
        </div>
      </div>
      <div class="label row" style="gap: 6px"><Rocket :size="12" /> 运行方式（可选，B 档及以上由 agent 托管启动）</div>
      <div class="grid-2">
        <div class="field">
          <label>安装依赖</label>
          <input v-model="form.setup_cmd" class="mono" placeholder="pnpm install" />
        </div>
        <div class="field">
          <label>启动命令（前台运行）</label>
          <input v-model="form.start_cmd" class="mono" placeholder="pnpm dev --port ${VERDICT_PORT}" />
        </div>
        <div class="field">
          <label>健康检查地址</label>
          <input v-model="form.health_url" class="mono" placeholder="http://127.0.0.1:${VERDICT_PORT}/health" />
        </div>
        <div class="field">
          <label>前端入口地址（UI 走查）</label>
          <input v-model="form.app_url" class="mono" placeholder="http://127.0.0.1:${VERDICT_PORT}" />
        </div>
        <div class="field">
          <label>需要的端口数</label>
          <input v-model.number="form.ports" type="number" min="0" max="8" />
        </div>
      </div>
      <span class="hint">留空时由计划阶段的 agent 根据代码推断。启动命令不要加 &amp; 或 nohup。端口用 <code>${VERDICT_PORT}</code> 占位，平台会为每个测试分配独立端口，并发时不会互相冲突。</span>
      <div class="field">
        <label>测试说明</label>
        <textarea v-model="form.test_notes" rows="6" :placeholder="notesPlaceholder" />
      </div>
      <div class="row"><button class="btn primary" :disabled="!form.url.trim()"><Plus :size="15" /> 添加</button></div>
    </form>

    <div v-if="!list.length && !adding" class="card">
      <div class="empty">
        <span class="empty-icon"><GitBranch :size="24" /></span>
        <h3>还没有仓库</h3>
        <button class="btn primary" @click="adding = true"><Plus :size="16" /> 添加仓库</button>
      </div>
    </div>

    <section v-for="r in list" :key="r.id" class="card stack" style="gap: 14px">
      <template v-if="editing && editing.id === r.id">
        <div class="grid-2">
          <div class="field"><label>git 地址</label><input v-model="editing.url" class="mono" /></div>
          <div class="field"><label>显示名称</label><input v-model="editing.name" /></div>
        </div>
      <div class="label row" style="gap: 6px"><Rocket :size="12" /> 运行方式（可选，B 档及以上由 agent 托管启动）</div>
      <div class="grid-2">
        <div class="field">
          <label>安装依赖</label>
          <input v-model="editing.setup_cmd" class="mono" placeholder="pnpm install" />
        </div>
        <div class="field">
          <label>启动命令（前台运行）</label>
          <input v-model="editing.start_cmd" class="mono" placeholder="pnpm dev --port ${VERDICT_PORT}" />
        </div>
        <div class="field">
          <label>健康检查地址</label>
          <input v-model="editing.health_url" class="mono" placeholder="http://127.0.0.1:${VERDICT_PORT}/health" />
        </div>
        <div class="field">
          <label>前端入口地址（UI 走查）</label>
          <input v-model="editing.app_url" class="mono" placeholder="http://127.0.0.1:${VERDICT_PORT}" />
        </div>
        <div class="field">
          <label>需要的端口数</label>
          <input v-model.number="editing.ports" type="number" min="0" max="8" />
        </div>
      </div>
      <span class="hint">留空时由计划阶段的 agent 根据代码推断。启动命令不要加 &amp; 或 nohup。端口用 <code>${VERDICT_PORT}</code> 占位，平台会为每个测试分配独立端口，并发时不会互相冲突。</span>
        <div class="field"><label>测试说明</label><textarea v-model="editing.test_notes" rows="8" :placeholder="notesPlaceholder" /></div>
        <div class="row">
          <button class="btn primary" @click="save">保存</button>
          <button class="btn ghost" @click="editing = null">取消</button>
        </div>
      </template>
      <template v-else>
        <div class="row between">
          <div class="row" style="gap: 10px; min-width: 0">
            <span class="choice-icon"><GitBranch :size="17" /></span>
            <div style="min-width: 0">
              <b>{{ r.name }}</b>
              <div class="mono muted" style="overflow-wrap: anywhere">{{ r.url }}</div>
            </div>
          </div>
          <div class="row" style="gap: 2px">
            <button class="btn ghost icon" title="编辑" @click="editing = { ...r }"><PencilLine :size="15" /></button>
            <button class="btn ghost icon danger" title="删除" @click="remove(r)"><Trash2 :size="15" /></button>
          </div>
        </div>
        <div v-if="r.start_cmd || r.setup_cmd || r.health_url || r.app_url" class="meta-chips">
          <span v-if="r.setup_cmd" class="chip"><Package /> <span class="mono">{{ r.setup_cmd }}</span></span>
          <span v-if="r.start_cmd" class="chip"><Rocket /> <span class="mono">{{ r.start_cmd }}</span></span>
          <span v-if="r.health_url" class="chip"><HeartPulse /> <span class="mono">{{ r.health_url }}</span></span>
          <span v-if="r.app_url" class="chip"><Globe /> <span class="mono">{{ r.app_url }}</span></span>
          <span class="chip">{{ r.ports }} 个端口</span>
        </div>
        <div>
          <div class="label row" style="gap: 6px; margin-bottom: 6px"><NotebookPen :size="12" /> 测试说明</div>
          <p class="body text-2" :class="{ muted: !r.test_notes }">{{ r.test_notes || '未填写，建议补充启动方式和依赖。' }}</p>
        </div>
        <div class="hint row" style="gap: 6px">
          <KeyRound :size="12" /> 私密文件放在运行时的 <code>~/.verdict-agent/env/{{ repoKey(r.url) }}/</code>，每次测试复制到代码根目录，不经过本站。
        </div>
      </template>
    </section>
  </div>
</template>
