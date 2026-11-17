<script setup>
import { computed } from 'vue'
import { CheckCircle2, XCircle, Loader, MinusCircle, Clock, Container } from 'lucide-vue-next'
import { roleMeta, teamText, stageStatusText, stageTone, fmtMs, harnessName } from '../format'

const props = defineProps({ stages: Array, selected: String })
const emit = defineEmits(['select'])

const icon = { ok: CheckCircle2, error: XCircle, timeout: XCircle, running: Loader, sent: Loader, skipped: MinusCircle, canceled: MinusCircle, pending: Clock }

const items = computed(() =>
  props.stages.map((s) => ({
    ...s,
    meta: roleMeta[s.role] || { name: s.role, team: 'plan' },
    label: (roleMeta[s.role]?.name || s.role) + (s.round ? ` · 第 ${s.round} 轮` : ''),
  })),
)
</script>

<template>
  <ol class="timeline">
    <li
      v-for="s in items"
      :key="s.id"
      class="tl-item"
      :class="['team-' + s.meta.team, 'st-' + s.status, { active: selected === s.id }]"
      @click="emit('select', selected === s.id ? '' : s.id)"
    >
      <span class="tl-node"><component :is="icon[s.status] || Clock" :size="14" :class="{ spin: s.status === 'running' || s.status === 'sent' }" /></span>
      <div class="tl-body">
        <div class="row" style="gap: 8px">
          <span class="tl-team">{{ teamText[s.meta.team] }}</span>
          <b>{{ s.label }}</b>
          <span class="pill" :class="stageTone[s.status]">{{ stageStatusText[s.status] || s.status }}</span>
        </div>
        <div class="tl-meta">
          <span v-if="s.kind === 'service'"><Container :size="11" /> 托管服务</span>
          <span v-else-if="s.harness">{{ harnessName(s.harness) }}</span>
          <span v-if="s.duration_ms">{{ fmtMs(s.duration_ms) }}</span>
          <span v-if="s.cost_usd">${{ s.cost_usd.toFixed(3) }}</span>
          <span v-if="s.artifacts?.length">📎 {{ s.artifacts.length }}</span>
        </div>
        <div v-if="s.note" class="tl-note">{{ s.note }}</div>
        <div v-if="s.error" class="tl-note bad">{{ s.error }}</div>
        <div v-if="s.service" class="tl-note">
          <template v-if="s.service.running">服务已就绪 <span class="mono">{{ s.service.url }}</span></template>
          <template v-else>启动命令 <span class="mono">{{ s.service.start_cmd }}</span></template>
        </div>
      </div>
    </li>
  </ol>
</template>
