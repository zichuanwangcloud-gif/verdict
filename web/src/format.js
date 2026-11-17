export const statusText = {
  queued: '排队中',
  dispatched: '已下发',
  preparing: '准备中',
  running: '执行中',
  awaiting_approval: '待确认计划',
  finalizing: '收尾中',
  completed: '已完成',
  error: '出错',
  canceled: '已取消',
}

export const verdictText = { pass: '通过', fail: '未通过', partial: '部分无法验证' }
export const acText = { pass: '通过', fail: '未通过', unverifiable: '无法验证', disputed: '存疑' }

// 状态 → 标签颜色
export function runTone(run) {
  if (run.status === 'completed') return { pass: 'ok', fail: 'bad', partial: 'warn' }[run.verdict] || ''
  if (run.status === 'error') return 'bad'
  if (run.status === 'canceled') return ''
  if (run.status === 'awaiting_approval') return 'warn'
  return 'info'
}

export function runLabel(run) {
  return run.status === 'completed' ? verdictText[run.verdict] || '已完成' : statusText[run.status] || run.status
}

export const acTone = { pass: 'ok', fail: 'bad', unverifiable: 'warn', disputed: 'dispute' }

export function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function fmtDuration(a, b) {
  if (!a) return '—'
  const ms = (b ? new Date(b) : new Date()) - new Date(a)
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分 ${s % 60} 秒`
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`
}

export function ago(t) {
  if (!t) return '从未'
  const s = Math.round((Date.now() - new Date(t)) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  return `${Math.floor(s / 86400)} 天前`
}

export function acCounts(acs = []) {
  const c = { pass: 0, fail: 0, unverifiable: 0, total: acs.length }
  for (const a of acs) {
    if (a.status === 'pass') c.pass++
    else if (a.status === 'fail') c.fail++
    else if (a.status === 'disputed') c.disputed = (c.disputed || 0) + 1
    else c.unverifiable++
  }
  return c
}

export const isLive = (run) =>
  ['queued', 'dispatched', 'preparing', 'running', 'awaiting_approval', 'finalizing'].includes(run?.status)

export const harnessName = (h) => ({ claude: 'Claude Code', codex: 'Codex' })[h] || h

export const tierMeta = {
  S: { name: 'S 对抗', short: 'S', tagline: '红蓝多轮对抗 + 全量 UI 走查 + 主动探索', tone: 's' },
  A: { name: 'A 深度', short: 'A', tagline: '一轮红队交叉复核 + 定向 UI 走查 + 裁决', tone: 'a' },
  B: { name: 'B 标准', short: 'B', tagline: '计划 + 托管服务 + 实测，输出前自查', tone: 'b' },
  C: { name: 'C 快速', short: 'C', tagline: '只用命令行、单测和读代码，几分钟出结果', tone: 'c' },
  custom: { name: '自定义', short: '✦', tagline: '自己组合流程开关', tone: 'custom' },
}

export const roleMeta = {
  plan: { name: '测试计划', team: 'plan' },
  service: { name: '启动服务', team: 'env' },
  verify: { name: '执行验证', team: 'blue' },
  ui: { name: 'UI 走查', team: 'blue' },
  red: { name: '红队质疑', team: 'red' },
  recheck: { name: '蓝队复测', team: 'blue' },
  explore: { name: '主动探索', team: 'red' },
  judge: { name: '裁决', team: 'judge' },
}

export const teamText = { plan: '计划', env: '环境', blue: '蓝队', red: '红队', judge: '裁决' }

export const stageStatusText = {
  pending: '待执行',
  sent: '已下发',
  running: '执行中',
  ok: '完成',
  error: '失败',
  timeout: '超时',
  canceled: '已取消',
  skipped: '已跳过',
}

export const stageTone = { ok: 'ok', error: 'bad', timeout: 'bad', canceled: '', skipped: '', running: 'live', sent: 'live', pending: '' }

export const confidenceText = { high: '高', medium: '中', low: '低' }
export const severityText = { critical: '严重', high: '高', medium: '中', low: '低' }
export const methodText = { cli: '命令行', api: '接口', ui: '界面', code: '读代码' }
export const challengeKindText = {
  insufficient_evidence: '证据不足',
  counterexample: '反例',
  wrong_status: '结论有误',
  flaky: '结果不稳定',
  other: '其他',
}

export function fmtMs(ms) {
  if (!ms) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  return m < 60 ? `${m} 分 ${s % 60} 秒` : `${Math.floor(m / 60)} 小时 ${m % 60} 分`
}
