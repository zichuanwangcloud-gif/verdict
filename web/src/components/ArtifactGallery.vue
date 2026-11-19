<script setup>
import { ref, watchEffect } from 'vue'
import { ImageOff, X } from 'lucide-vue-next'
import { artifactURL } from '../api'

const props = defineProps({
  runId: String,
  artifacts: Array,
  // issues：文件名 → 不可用原因（来自截图质检、机器体检或 UI 阶段自检）。
  issues: { type: Object, default: () => ({}) },
})
const urls = ref({})
const failed = ref({})
const open = ref(null)

const isImage = (a) => a.content_type.startsWith('image/')

watchEffect(() => {
  for (const a of props.artifacts.filter(isImage)) {
    if (urls.value[a.name] || failed.value[a.name]) continue
    artifactURL(props.runId, a.name)
      .then((u) => (urls.value = { ...urls.value, [a.name]: u }))
      .catch(() => (failed.value = { ...failed.value, [a.name]: true }))
  }
})

async function download(a) {
  const u = await artifactURL(props.runId, a.name)
  const el = document.createElement('a')
  el.href = u
  el.download = a.name
  el.click()
}
</script>

<template>
  <div class="shots">
    <figure v-for="a in artifacts.filter(isImage)" :key="a.name" class="shot" @click="urls[a.name] && (open = a)"
      :class="{ 'shot-bad': issues[a.name] }" :title="issues[a.name] || ''">
      <img v-if="urls[a.name]" :src="urls[a.name]" :alt="a.name" loading="lazy" />
      <div v-else class="shot-ph"><ImageOff v-if="failed[a.name]" :size="18" /><span v-else class="spin-dot" /></div>
      <span v-if="issues[a.name]" class="shot-badge">不可用</span>
      <figcaption class="mono">{{ a.name }}</figcaption>
      <span v-if="issues[a.name]" class="shot-why">{{ issues[a.name] }}</span>
    </figure>
  </div>
  <div v-if="artifacts.some((a) => !isImage(a))" class="row" style="margin-top: 10px; gap: 6px">
    <button v-for="a in artifacts.filter((x) => !isImage(x))" :key="a.name" class="chip" @click="download(a)">📎 {{ a.name }}</button>
  </div>
  <div v-if="open" class="lightbox" @click="open = null">
    <button class="btn ghost icon lb-close" aria-label="关闭"><X :size="18" /></button>
    <img :src="urls[open.name]" :alt="open.name" @click.stop />
    <div class="mono lb-cap">{{ open.name }}</div>
  </div>
</template>
