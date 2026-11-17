<script setup>
import { ref, computed, watch, nextTick } from 'vue'
import { MessageSquareText, SquareTerminal, CornerDownRight, Cpu, CheckCheck, TriangleAlert, CircleX, Brain, FileJson } from 'lucide-vue-next'

const props = defineProps({ logs: Array, live: Boolean })
const box = ref(null)
const follow = ref(true)
const opened = ref(new Set())

const rules = [
  ['💬', 'say'], ['🔧', 'tool'], ['✏️', 'tool'], ['🔎', 'tool'], ['↳', 'out'], ['[stderr]', 'warn'],
  ['▶', 'sys'], ['■', 'done'], ['📝', 'done'], ['⚠', 'warn'], ['❌', 'err'], ['💭', 'think'],
]
const icons = { say: MessageSquareText, tool: SquareTerminal, out: CornerDownRight, sys: Cpu, done: CheckCheck, warn: TriangleAlert, err: CircleX, think: Brain }

function parse(text) {
  let t = text
  let sub = false
  if (t.trimStart().startsWith('[子任务]')) {
    sub = true
    t = t.trimStart().slice('[子任务]'.length)
  }
  t = t.trimStart()
  for (const [p, kind] of rules) {
    if (t.startsWith(p)) {
      let rest = t.slice(p.length).trimStart()
      let k = kind
      if (kind === 'out' && rest.startsWith('⚠')) {
        k = 'out'
        rest = rest.slice(1).trimStart()
      }
      if (kind === 'done' && p === '📝') return { kind: 'done', text: rest, sub, icon: FileJson }
      return { kind: k, text: rest, sub, icon: icons[k] }
    }
  }
  return { kind: 'sys', text: t, sub, icon: icons.sys }
}

const items = computed(() => props.logs.map((l) => ({ ...l, ...parse(l.text) })))

function fmt(ts) {
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
}

function toggle(seq) {
  const s = new Set(opened.value)
  s.has(seq) ? s.delete(seq) : s.add(seq)
  opened.value = s
}

function onScroll() {
  const el = box.value
  if (el) follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

watch(
  () => props.logs.length,
  async () => {
    if (!follow.value) return
    await nextTick()
    if (box.value) box.value.scrollTop = box.value.scrollHeight
  },
)
</script>

<template>
  <div ref="box" class="stream" @scroll="onScroll">
    <div v-if="!logs.length && !live" class="muted" style="padding: 4px">没有日志</div>
    <div
      v-for="l in items"
      :key="l.seq"
      class="ln"
      :class="[l.kind, { open: opened.has(l.seq) }]"
      :style="l.sub ? 'padding-left: 20px' : ''"
      @click="l.kind === 'out' && toggle(l.seq)"
    >
      <span class="ln-time">{{ fmt(l.ts) }}</span>
      <span class="ln-icon"><component :is="l.icon" /></span>
      <span class="ln-text">{{ l.text }}</span>
    </div>
    <div v-if="live" class="thinking"><i /><i /><i /> agent 正在工作</div>
  </div>
</template>
