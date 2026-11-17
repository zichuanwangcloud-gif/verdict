<script setup>
import { ref } from 'vue'
import Requirements from './pages/Requirements.vue'
import ChangeSets from './pages/ChangeSets.vue'

const tabs = [
  { key: 'requirements', label: '需求与验收条件', comp: Requirements },
  { key: 'changesets', label: '提测与裁决', comp: ChangeSets },
]
const current = ref(tabs[0])
</script>

<template>
  <header>
    <h1>Verdict</h1>
    <nav>
      <button
        v-for="t in tabs"
        :key="t.key"
        :class="{ active: current.key === t.key }"
        @click="current = t"
      >
        {{ t.label }}
      </button>
    </nav>
  </header>
  <main>
    <component :is="current.comp" />
  </main>
</template>
