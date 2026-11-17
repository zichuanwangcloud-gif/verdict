<script setup>
defineProps({ name: String })
</script>

<template>
  <span class="harness-logo" :class="name">{{ name === 'claude' ? 'C' : name === 'codex' ? '◎' : '?' }}</span>
</template>
