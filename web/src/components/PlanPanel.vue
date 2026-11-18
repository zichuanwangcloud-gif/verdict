<script setup>
import { ref, computed } from 'vue'
import { ClipboardList, Check, X, Server } from 'lucide-vue-next'
import { methodText } from '../format'

const props = defineProps({ plan: Object, awaiting: Boolean, busy: Boolean })
const emit = defineEmits(['approve', 'cancel'])
const note = ref('')
const svc = computed(() => props.plan?.service || {})
</script>

<template>
  <section class="card stack" :class="{ glow: awaiting }" style="gap: 14px">
    <div class="card-head" style="margin: 0">
      <div class="card-title"><ClipboardList :size="16" /> 测试计划</div>
      <span v-if="awaiting" class="pill warn">等待你确认</span>
    </div>
    <p v-if="plan.summary" class="text-2 body">{{ plan.summary }}</p>
    <div v-if="plan.needs_service && svc.start_cmd" class="chip" style="align-self: flex-start">
      <Server /> 建议启动：<span class="mono">{{ svc.start_cmd }}</span>
      <span v-if="svc.health_url" class="mono muted">· {{ svc.health_url }}</span>
    </div>
    <div v-if="plan.needs_service && svc.teardown_cmd" class="chip" style="align-self: flex-start">
      <Server /> 收尾清理：<span class="mono">{{ svc.teardown_cmd }}</span>
    </div>
    <div class="plan-items">
      <div v-for="it in plan.items" :key="it.id" class="plan-item">
        <div class="row" style="gap: 8px">
          <span class="ac-id">{{ it.id }}</span>
          <span class="chip">{{ methodText[it.method] || it.method }}</span>
          <b>{{ it.criterion }}</b>
        </div>
        <ol v-if="it.steps?.length" class="plan-steps">
          <li v-for="(st, i) in it.steps" :key="i">{{ st }}</li>
        </ol>
        <div v-if="it.expected" class="hint">预期：{{ it.expected }}</div>
      </div>
    </div>
    <template v-if="awaiting">
      <div class="field">
        <label>补充说明（可选，会交给执行验证的 agent）</label>
        <textarea v-model="note" rows="3" placeholder="例如：AC-2 重点测退款金额为 0 的情况；测试账号 admin / admin123" />
      </div>
      <div class="row">
        <button class="btn primary" :disabled="busy" @click="emit('approve', note)"><Check :size="15" /> 确认计划，开始执行</button>
        <button class="btn danger" :disabled="busy" @click="emit('cancel')"><X :size="15" /> 取消测试</button>
      </div>
    </template>
  </section>
</template>
