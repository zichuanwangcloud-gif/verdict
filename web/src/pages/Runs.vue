<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { go } from '../router'
import { runTone, runLabel, fmtTime, fmtDuration } from '../format'

const runs = ref([])
const err = ref('')
let timer

async function load() {
  try {
    runs.value = await api.runs()
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
</script>

<template>
  <div class="row between">
    <h2>测试</h2>
    <button class="primary" @click="go('/runs/new')">新建测试</button>
  </div>
  <div v-if="err" class="error">{{ err }}</div>
  <div v-if="!runs.length && !err" class="card muted">
    还没有测试。先在「运行时」里接入一台机器、在「仓库」里添加仓库，然后新建测试。
  </div>
  <div v-else class="card flush">
    <table class="list">
      <thead>
        <tr><th>结果</th><th>需求</th><th>仓库 / 分支</th><th class="hide-sm">执行</th><th class="hide-sm">时间</th></tr>
      </thead>
      <tbody>
        <tr v-for="r in runs" :key="r.id" class="clickable" @click="go('/runs/' + r.id)">
          <td class="status"><span class="tag" :class="runTone(r)">{{ runLabel(r) }}</span></td>
          <td>
            <div><span class="mono">{{ r.issue_key }}</span> {{ r.issue.title }}</div>
            <div v-if="r.summary" class="muted clamp">{{ r.summary }}</div>
            <div v-else-if="r.error" class="muted clamp">{{ r.error }}</div>
          </td>
          <td>
            <div>{{ r.repo_name }}</div>
            <div class="mono muted">{{ r.branch }}<template v-if="r.commit_sha"> @ {{ r.commit_sha.slice(0, 8) }}</template></div>
          </td>
          <td class="hide-sm">
            <div>{{ r.harness }}</div>
            <div class="muted">{{ r.runtime_name }}</div>
          </td>
          <td class="hide-sm nowrap">
            <div>{{ fmtTime(r.created_at) }}</div>
            <div class="muted">{{ fmtDuration(r.started_at, r.finished_at) }}</div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
