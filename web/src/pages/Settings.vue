<script setup>
import { ref, onMounted } from 'vue'
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
  } catch (e) {
    err.value = e.message
  }
}
</script>

<template>
  <h2>设置</h2>
  <div v-if="err" class="error">{{ err }}</div>
  <div v-if="msg" class="ok-box">{{ msg }}</div>
  <template v-if="s">
    <form class="card form" @submit.prevent="save({ linear_api_key: key })">
      <h3>Linear API Key</h3>
      <p class="muted">
        用于读取 issue 和回写评论。在 Linear 的 Settings → Security &amp; access → Personal API keys 创建。
        当前：{{ s.linear_api_key_masked || '未配置' }}
      </p>
      <div class="row">
        <input v-model="key" type="password" placeholder="lin_api_…" />
        <button class="primary" :disabled="!key.trim()">保存</button>
        <button v-if="s.linear_api_key_masked" type="button" class="danger" @click="save({ linear_api_key: '' })">清除</button>
      </div>
    </form>
    <form class="card form" @submit.prevent="save({ public_url: publicURL })">
      <h3>对外地址</h3>
      <p class="muted">
        运行时连接后端、Linear 评论里的报告链接都用这个地址。留空时根据当前访问地址推断（现在是 <code>{{ s.public_url_effective }}</code>）。
      </p>
      <div class="row">
        <input v-model="publicURL" placeholder="https://verdict.example.com" />
        <button class="primary">保存</button>
      </div>
    </form>
  </template>
</template>
