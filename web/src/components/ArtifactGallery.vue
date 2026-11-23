<script setup>
import { computed, onMounted, onUnmounted, ref, watch, watchEffect } from 'vue'
import { EyeOff, ImageOff, X } from 'lucide-vue-next'
import { artifactURL } from '../api'

const props = defineProps({
  runId: String,
  artifacts: Array,
  // issues：文件名 → 不可用原因（来自截图质检、机器体检或 UI 阶段自检），用来打标。
  issues: { type: Object, default: () => ({}) },
  // hidden：文件名 → 被遮挡原因。这些截图不再展示（自动清理），只留一行文字说明。
  hidden: { type: Object, default: () => ({}) },
})
const urls = ref({})
const failed = ref({})
const open = ref(null)

const isImage = (a) => a.content_type.startsWith('image/')
const visible = computed(() => props.artifacts.filter((a) => isImage(a) && !props.hidden[a.name]))
const hiddenList = computed(() =>
  props.artifacts
    .filter((a) => isImage(a) && props.hidden[a.name])
    .map((a) => ({ name: a.name, reason: props.hidden[a.name] })),
)
const files = computed(() => props.artifacts.filter((a) => !isImage(a)))

watchEffect(() => {
  for (const a of visible.value) {
    if (urls.value[a.name] || failed.value[a.name]) continue
    artifactURL(props.runId, a.name)
      .then((u) => (urls.value = { ...urls.value, [a.name]: u }))
      .catch(() => (failed.value = { ...failed.value, [a.name]: true }))
  }
})

// 放大层打开时锁住页面滚动，关闭时恢复。
watch(open, (v) => {
  document.body.style.overflow = v ? 'hidden' : ''
})
function onKey(e) {
  if (e.key === 'Escape' && open.value) open.value = null
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  document.body.style.overflow = ''
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
    <figure v-for="a in visible" :key="a.name" class="shot" @click="urls[a.name] && (open = a)"
      :class="{ 'shot-bad': issues[a.name] }" :title="issues[a.name] || ''">
      <img v-if="urls[a.name]" :src="urls[a.name]" :alt="a.name" loading="lazy" />
      <div v-else class="shot-ph"><ImageOff v-if="failed[a.name]" :size="18" /><span v-else class="spin-dot" /></div>
      <span v-if="issues[a.name]" class="shot-badge">不可用</span>
      <figcaption class="mono">{{ a.name }}</figcaption>
      <span v-if="issues[a.name]" class="shot-why">{{ issues[a.name] }}</span>
    </figure>
  </div>
  <details v-if="hiddenList.length" class="shot-hidden">
    <summary><EyeOff :size="13" /> 已自动隐藏 {{ hiddenList.length }} 张被遮挡的截图（不展示）</summary>
    <p v-for="h in hiddenList" :key="h.name" class="mono">{{ h.name }} — {{ h.reason }}</p>
  </details>
  <div v-if="files.length" class="row" style="margin-top: 10px; gap: 6px">
    <button v-for="a in files" :key="a.name" class="chip" @click="download(a)">📎 {{ a.name }}</button>
  </div>
  <!-- 放大层挂到 body：卡片上的 backdrop-filter 会让 position:fixed 相对卡片定位，导致图片跑出视口 -->
  <Teleport to="body">
    <div v-if="open" class="lightbox" role="dialog" aria-modal="true" :aria-label="open.name" @click="open = null">
      <button class="btn ghost icon lb-close" aria-label="关闭" @click.stop="open = null"><X :size="18" /></button>
      <img :src="urls[open.name]" :alt="open.name" @click.stop />
      <div class="mono lb-cap">{{ open.name }}</div>
    </div>
  </Teleport>
</template>
