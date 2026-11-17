<script setup>
import { ref } from 'vue'
import { api, setToken } from '../api'

const token = ref('')
const err = ref('')
const busy = ref(false)

async function submit() {
  err.value = ''
  busy.value = true
  setToken(token.value.trim())
  try {
    await api.me()
  } catch (e) {
    setToken('')
    err.value = e.message === '需要登录' ? '令牌不正确' : e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="narrow">
    <div class="card">
      <h2>登录 Verdict</h2>
      <p class="muted">输入后端的管理员令牌（环境变量 <code>VERDICT_ADMIN_TOKEN</code>；未设置时见后端启动日志）。</p>
      <form class="row" @submit.prevent="submit">
        <input v-model="token" type="password" placeholder="管理员令牌" autofocus />
        <button class="primary" :disabled="!token || busy">登录</button>
      </form>
      <div v-if="err" class="error">{{ err }}</div>
    </div>
  </main>
</template>
