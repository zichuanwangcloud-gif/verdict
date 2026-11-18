<script setup>
import { ref, onMounted } from 'vue'
import { KeyRound, Globe, Sparkles, Check, Trash2, RefreshCw } from 'lucide-vue-next'
import { api } from '../api'

const s = ref(null)
const key = ref('')
const publicURL = ref('')
const retries = ref(5)
const retryDelay = ref(1)
const err = ref('')
const msg = ref('')

async function load() {
  try {
    s.value = await api.settings()
    publicURL.value = s.value.public_url
    retries.value = s.value.stage_retries ?? 5
    retryDelay.value = Math.max(1, Math.round((s.value.stage_retry_delay ?? 60) / 60))
  } catch (e) {
    err.value = e.message
  }
}
onMounted(load)

async function save(body) {
  err.value = ''
  msg.value = ''
  try {
    s.value = await api.saveSettings(body)
    key.value = ''
    msg.value = '已保存'
    setTimeout(() => (msg.value = ''), 2500)
  } catch (e) {
    err.value = e.message
  }
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <div class="eyebrow"><Sparkles :size="12" /> Settings</div>
        <h2>设置</h2>
      </div>
    </div>
    <div v-if="err" class="alert bad">{{ err }}</div>
    <div v-if="msg" class="alert ok"><Check :size="16" /> {{ msg }}</div>

    <template v-if="s">
      <form class="card stack" style="gap: 14px" @submit.prevent="save({ linear_api_key: key })">
        <div class="card-head" style="margin: 0">
          <div class="card-title"><KeyRound :size="16" /> Linear API Key</div>
          <span class="pill" :class="s.linear_api_key_masked ? 'ok' : ''">{{ s.linear_api_key_masked || '未配置' }}</span>
        </div>
        <p class="text-2">用于读取 issue 和回写评论。在 Linear 的 Settings → Security &amp; access → Personal API keys 创建。</p>
        <div class="input-group">
          <input v-model="key" type="password" placeholder="lin_api_…" />
          <button class="btn primary" :disabled="!key.trim()">保存</button>
          <button v-if="s.linear_api_key_masked" type="button" class="btn danger" @click="save({ linear_api_key: '' })"><Trash2 :size="14" /></button>
        </div>
      </form>

      <form class="card stack" style="gap: 14px" @submit.prevent="save({ stage_retries: Number(retries), stage_retry_delay: Number(retryDelay) * 60 })">
        <div class="card-title"><RefreshCw :size="16" /> 阶段重试</div>
        <p class="text-2">模型网关抖动、限流、连接中断这类失败会自动重试。重试的耗时计入测试的时间预算；次数用尽仍未成功时，报告会标注「覆盖强度降级」，缺少裁决时结论降为存疑。</p>
        <div class="grid-2">
          <div class="field">
            <label>单个阶段最多尝试次数</label>
            <input v-model.number="retries" type="number" min="0" max="20" />
            <span class="hint">含第一次执行，默认 5。0 表示不重试。</span>
          </div>
          <div class="field">
            <label>首次重试间隔（分钟）</label>
            <input v-model.number="retryDelay" type="number" min="1" max="60" />
            <span class="hint">之后指数退避（×2），上限 8 分钟。</span>
          </div>
        </div>
        <div class="row"><button class="btn primary">保存</button></div>
      </form>

      <form class="card stack" style="gap: 14px" @submit.prevent="save({ public_url: publicURL })">
        <div class="card-title"><Globe :size="16" /> 对外地址</div>
        <p class="text-2">运行时连接本站、Linear 评论里的报告链接都用这个地址。留空时按当前访问地址推断（现在是 <code>{{ s.public_url_effective }}</code>）。</p>
        <div class="input-group">
          <input v-model="publicURL" class="mono" placeholder="https://verdict.example.com" />
          <button class="btn primary">保存</button>
        </div>
      </form>
    </template>
  </div>
</template>
