<script setup>
import { ref } from 'vue'
import { ScanSearch, ArrowRight, KeyRound } from 'lucide-vue-next'
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
  <div class="login">
    <form class="card glow login-card" @submit.prevent="submit">
      <span class="login-mark"><ScanSearch :size="26" /></span>
      <div>
        <h2>欢迎使用 <span class="grad-text">Verdict</span></h2>
        <p class="text-2" style="margin-top: 6px">让 AI agent 按验收标准，替你把每个分支真正跑一遍。</p>
      </div>
      <div class="field">
        <label for="tok"><KeyRound :size="12" /> 管理员令牌</label>
        <input id="tok" v-model="token" type="password" placeholder="VERDICT_ADMIN_TOKEN" autofocus />
        <span class="hint">未设置环境变量时，令牌打印在后端启动日志里。</span>
      </div>
      <div v-if="err" class="alert bad">{{ err }}</div>
      <button class="btn primary" :disabled="!token || busy">
        {{ busy ? '验证中…' : '进入控制台' }} <ArrowRight :size="16" />
      </button>
    </form>
  </div>
</template>
