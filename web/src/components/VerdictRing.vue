<script setup>
import { computed } from 'vue'
import { acCounts } from '../format'

const props = defineProps({ run: Object })
const R = 48
const C = 2 * Math.PI * R

const c = computed(() => acCounts(props.run.acs))
const segs = computed(() => {
  const t = c.value.total
  if (!t || props.run.status !== 'completed') return []
  const gap = t > 1 ? 3 : 0
  let off = 0
  const out = []
  for (const [k, color] of [['pass', 'var(--ok)'], ['fail', 'var(--bad)'], ['disputed', 'var(--dispute)'], ['unverifiable', 'var(--warn)']]) {
    const len = ((c.value[k] || 0) / t) * C
    if (len > 0) out.push({ color, dash: `${Math.max(len - gap, 1)} ${C}`, offset: -off })
    off += len
  }
  return out
})
const live = computed(() => ['queued', 'dispatched', 'preparing', 'running', 'finalizing'].includes(props.run.status))
</script>

<template>
  <div class="ring">
    <svg viewBox="0 0 112 112">
      <defs>
        <linearGradient id="ring-grad" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0%" stop-color="#a78bfa" />
          <stop offset="100%" stop-color="#22d3ee" />
        </linearGradient>
      </defs>
      <circle cx="56" cy="56" :r="R" fill="none" stroke="var(--surface-2)" stroke-width="8" />
      <circle
        v-for="(s, i) in segs"
        :key="i"
        cx="56" cy="56" :r="R" fill="none" :stroke="s.color" stroke-width="8" stroke-linecap="round"
        :stroke-dasharray="s.dash" :stroke-dashoffset="s.offset"
      />
      <circle
        v-if="live"
        class="spin" style="transform-origin: 56px 56px"
        cx="56" cy="56" :r="R" fill="none" stroke="url(#ring-grad)" stroke-width="8" stroke-linecap="round"
        :stroke-dasharray="`${C * 0.28} ${C}`"
      />
    </svg>
    <div class="ring-label">
      <div v-if="run.status === 'completed' && c.total">
        <div class="ring-num">{{ c.pass }}<span class="muted" style="font-size: 16px">/{{ c.total }}</span></div>
        <div class="ring-sub">条通过</div>
      </div>
      <div v-else-if="live">
        <div class="ring-num grad-text" style="font-size: 15px">AI</div>
        <div class="ring-sub">测试中</div>
      </div>
      <div v-else>
        <div class="ring-num muted" style="font-size: 20px">—</div>
        <div class="ring-sub">无结论</div>
      </div>
    </div>
  </div>
</template>
