<script setup>
import { ref, onMounted } from 'vue'
import { KeyRound, Globe, Sparkles, Check, Trash2 } from 'lucide-vue-next'
import { api } from '../api'

const s = ref(null)
const key = ref('')
const publicURL = ref('')
const err = ref('')
const msg = ref('')

async function load() {
  try {
    s.value = await api.settings()
    publicURL.value = s.value.public_url
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
