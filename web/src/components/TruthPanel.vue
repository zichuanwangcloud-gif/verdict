<script setup>
import { ref, computed, watch } from 'vue'
import { CircleCheck, CircleX, CircleSlash, ClipboardCheck, Ticket, Trash2, Loader } from 'lucide-vue-next'
import { api } from '../api'
import { fmtTime, truthSourceText } from '../format'

const props = defineProps({ run: Object })
const emit = defineEmits(['saved'])

const truth = ref(props.run?.truth || null)
const draft = ref({ verdict: '', source: 'manual', note: '', ticket: '', ticket_ref: '' })
const err = ref('')
const busy = ref(false)
const open = ref(false)

watch(
  () => props.run?.truth,
  (v) => {
    truth.value = v || null
  },
)

// 只有跑完的测试才谈得上人工复核。
const reviewable = computed(() => props.run?.status === 'completed')

// agent 的结论：三档；人工真值只有两档 + 无效。
const agentText = computed(() => {
  const v = props.run?.verdict
  return { pass: '通过', fail: '未通过', partial: '部分无法验证' }[v] || '未给出结论'
})

// 标记后与 agent 的结论是否一致，用来提示「这条对不上」。
const mismatch = computed(() => {
  const t = truth.value
  if (!t || !reviewable.value) return ''
  const a = props.run.verdict
  if (t.verdict === 'invalid') return ''
  if (t.verdict === 'fail' && a === 'pass') return 'escaped'
  if (t.verdict === 'pass' && a === 'fail') return 'alarm'
  return ''
})

function start(v) {
  draft.value = {
    verdict: v,
    source: truth.value?.source || 'manual',
    note: truth.value?.note || '',
    ticket: truth.value?.ticket || '',
    ticket_ref: truth.value?.ticket_ref || '',
  }
  open.value = true
  err.value = ''
}

async function save() {
  err.value = ''
  busy.value = true
  try {
    truth.value = await api.setTruth(props.run.id, draft.value)
    open.value = false
    emit('saved', truth.value)
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}

async function clear() {
  if (!confirm('撤销这条人工结论？')) return
  busy.value = true
  try {
    await api.clearTruth(props.run.id)
    truth.value = null
    open.value = false
    emit('saved', null)
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section v-if="reviewable" class="card stack review" style="gap: 12px" :class="{ unmarked: !truth }">
    <div class="card-head" style="margin: 0">
      <div class="card-title"><ClipboardCheck :size="16" /> 人工复核</div>
      <span class="hint">AI 判：{{ agentText }}</span>
    </div>

    <template v-if="truth && !open">
      <div class="row" style="gap: 10px; flex-wrap: wrap">
        <span class="pill lg" :class="{ ok: truth.verdict === 'pass', bad: truth.verdict === 'fail' }">
          <component :is="{ pass: CircleCheck, fail: CircleX, invalid: CircleSlash }[truth.verdict]" />
          {{ { pass: '人工：通过', fail: '人工：未通过', invalid: '人工：无效' }[truth.verdict] }}
        </span>
        <span class="chip">{{ truthSourceText[truth.source] || truth.source }}</span>
        <span v-if="truth.ticket_ref" class="chip"><Ticket /> {{ truth.ticket_ref }}</span>
        <span class="muted" style="font-size: 12px" :title="fmtTime(truth.updated_at)">标记于 {{ fmtTime(truth.updated_at) }}</span>
      </div>
      <p v-if="truth.note" class="body text-2">{{ truth.note }}</p>
      <details v-if="truth.ticket">
        <summary>查看单子内容</summary>
        <pre>{{ truth.ticket }}</pre>
      </details>
      <div v-if="mismatch" class="alert" :class="mismatch === 'escaped' ? 'warn' : 'bad'">
        <template v-if="mismatch === 'escaped'">AI 判通过，人工测出问题——这是一次<b>漏报</b>。</template>
        <template v-else>AI 判未通过，人工确认没问题——这是一次<b>误报</b>。</template>
      </div>
      <div class="row">
        <button class="btn sm" :disabled="busy" @click="start(truth.verdict)">修改</button>
        <button class="btn sm danger" :disabled="busy" @click="clear"><Trash2 :size="13" /> 撤销</button>
      </div>
    </template>

    <template v-else>
      <p class="hint" style="margin: 0">
        人工测试完这一版之后，标一下结果。这是评判 AI 测得准不准的唯一依据；判为未通过时请附上说明或单子。
      </p>
      <div class="row" style="gap: 8px; flex-wrap: wrap">
        <button class="btn sm" :class="{ primary: draft.verdict === 'pass' && open }" :disabled="busy" @click="start('pass')">
          <CircleCheck :size="14" /> 通过
        </button>
        <button class="btn sm" :class="{ primary: draft.verdict === 'fail' && open }" :disabled="busy" @click="start('fail')">
          <CircleX :size="14" /> 未通过
        </button>
        <button class="btn sm" :class="{ primary: draft.verdict === 'invalid' && open }" :disabled="busy" @click="start('invalid')">
          <CircleSlash :size="14" /> 这次没意义
        </button>
      </div>
    </template>

    <form v-if="open" class="stack" style="gap: 10px" @submit.prevent="save">
      <div class="grid-2">
        <div class="field">
          <label>来源</label>
          <select v-model="draft.source">
            <option value="manual">人工复测</option>
            <option value="ticket">测试人员开单</option>
          </select>
        </div>
        <div class="field">
          <label>单号 / 链接</label>
          <input v-model="draft.ticket_ref" placeholder="BUG-123" class="mono" />
        </div>
      </div>
      <div class="field">
        <label>说明{{ draft.verdict === 'pass' ? '（可选）' : '' }}</label>
        <input v-model="draft.note" :placeholder="draft.verdict === 'pass' ? '例如：手工回归通过' : '人工发现的问题，一两句说清'" />
      </div>
      <div class="field">
        <label>单子内容{{ draft.verdict === 'pass' ? '（可选）' : '' }}</label>
        <textarea v-model="draft.ticket" rows="5" placeholder="把测试人员开的单子粘贴进来：复现步骤、现象、预期" />
      </div>
      <div v-if="err" class="alert bad">{{ err }}</div>
      <div class="row">
        <button class="btn primary sm" :disabled="busy">{{ busy ? '保存中…' : '保存' }}</button>
        <button type="button" class="btn ghost sm" :disabled="busy" @click="open = false">取消</button>
      </div>
    </form>
  </section>
</template>
