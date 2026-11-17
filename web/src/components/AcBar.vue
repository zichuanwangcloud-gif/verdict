<script setup>
import { computed } from 'vue'

const props = defineProps({ acs: { type: Array, default: () => [] } })
const tone = { pass: 'ok', fail: 'bad', disputed: 'dispute' }
const segs = computed(() => props.acs.map((a) => tone[a.status] || 'warn'))
</script>

<template>
  <div v-if="segs.length" class="acbar" :title="`${acs.length} 条验收标准`">
    <span v-for="(s, i) in segs" :key="i" :class="s" />
  </div>
</template>
