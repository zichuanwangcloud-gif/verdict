<script setup>
import { computed } from 'vue'
import { CircleCheck, CircleX, CircleAlert, Loader, Ban, Clock, TriangleAlert } from 'lucide-vue-next'
import { runTone, runLabel } from '../format'

const props = defineProps({ run: Object, large: Boolean })

const icon = computed(() => {
  const r = props.run
  if (r.status === 'completed') return { pass: CircleCheck, fail: CircleX, partial: CircleAlert }[r.verdict] || CircleCheck
  if (r.status === 'error') return TriangleAlert
  if (r.status === 'canceled') return Ban
  if (r.status === 'queued') return Clock
  return Loader
})
const live = computed(() => ['dispatched', 'preparing', 'running'].includes(props.run.status))
</script>

<template>
  <span class="pill" :class="[live ? 'live' : runTone(run), { lg: large }]">
    <component :is="icon" :class="{ spin: live }" />
    {{ runLabel(run) }}
  </span>
</template>
