<script setup>
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { Search, Loader, X, Inbox, CornerDownLeft } from 'lucide-vue-next'
import { api } from '../api'

const props = defineProps({ modelValue: String })
const emit = defineEmits(['update:modelValue', 'select', 'open'])

const query = ref(props.modelValue || '')
const open = ref(false)
const loading = ref(false)
const err = ref('')
const list = ref([])
const active = ref(0)
const box = ref(null)
const input = ref(null)
let timer
let seq = 0

watch(
  () => props.modelValue,
  (v) => {
    if (!open.value && v !== query.value) query.value = v || ''
  },
)

async function load() {
  const mine = ++seq
  loading.value = true
  err.value = ''
  try {
    const res = await api.searchIssues(query.value.trim())
    if (mine !== seq) return
    list.value = res || []
    active.value = 0
  } catch (e) {
    if (mine !== seq) return
    err.value = e.message
    list.value = []
  } finally {
    if (mine === seq) loading.value = false
  }
}

function focus() {
  open.value = true
  if (!list.value.length && !loading.value) load()
}

// 输入即搜，防抖 250ms
watch(open, (v) => emit('open', v))

watch(query, () => {
  if (!open.value) return
  clearTimeout(timer)
  timer = setTimeout(load, 250)
})

function choose(it) {
  emit('update:modelValue', it.identifier)
  emit('select', it)
  query.value = it.identifier
  open.value = false
  input.value?.blur()
}

function clear() {
  query.value = ''
  emit('update:modelValue', '')
  emit('select', null)
  list.value = []
  focus()
}

function move(d) {
  if (!list.value.length) return
  active.value = (active.value + d + list.value.length) % list.value.length
  nextTick(() => box.value?.querySelector('.ip-item.active')?.scrollIntoView({ block: 'nearest' }))
}

function onKey(e) {
  if (e.key === 'ArrowDown') return (e.preventDefault(), move(1))
  if (e.key === 'ArrowUp') return (e.preventDefault(), move(-1))
  if (e.key === 'Enter') {
    e.preventDefault()
    if (open.value && list.value[active.value]) choose(list.value[active.value])
    return
  }
  if (e.key === 'Escape') {
    open.value = false
    input.value?.blur()
  }
}

function onDocClick(e) {
  if (box.value && !box.value.contains(e.target)) open.value = false
}
onMounted(() => document.addEventListener('mousedown', onDocClick))
onUnmounted(() => {
  document.removeEventListener('mousedown', onDocClick)
  clearTimeout(timer)
})

const shown = computed(() => (query.value.trim() ? list.value : list.value))
const emptyHint = computed(() => (loading.value ? '搜索中…' : query.value.trim() ? '匹配结果' : '最近更新的 issue'))
</script>

<template>
  <div ref="box" class="ip">
    <div class="ip-field" :class="{ open }">
      <Search :size="15" class="ip-icon" />
      <input
        ref="input"
        v-model="query"
        class="ip-input"
        placeholder="点击搜索或输入编号，如 ENG-123"
        autocomplete="off"
        @focus="focus"
        @click="focus"
        @keydown="onKey"
        @input="open = true"
      />
      <Loader v-if="loading" :size="14" class="spin ip-icon" />
      <button v-else-if="query" type="button" class="ip-clear" title="清除" @click="clear"><X :size="14" /></button>
    </div>
    <div v-if="open" class="ip-pop">
      <div v-if="err" class="ip-err">{{ err }}</div>
      <template v-else>
        <div class="ip-hint">{{ emptyHint }}</div>
        <button
          v-for="(it, i) in shown"
          :key="it.id"
          type="button"
          class="ip-item"
          :class="{ active: i === active }"
          @mouseenter="active = i"
          @click="choose(it)"
        >
          <span class="ip-id mono">{{ it.identifier }}</span>
          <span class="ip-title">{{ it.title }}</span>
          <span class="ip-meta">
            <span v-if="it.state" class="chip">{{ it.state }}</span>
            <span v-if="it.assignee" class="muted">{{ it.assignee }}</span>
          </span>
        </button>
        <div v-if="!loading && !shown.length" class="ip-empty">
          <Inbox :size="14" />
          <span v-if="query.trim()">没有找到匹配「{{ query.trim() }}」的 issue</span>
          <span v-else>还没有 issue 可以选</span>
        </div>
        <div class="ip-foot"><CornerDownLeft :size="11" /> 回车选择 · ↑↓ 切换</div>
      </template>
    </div>
  </div>
</template>
