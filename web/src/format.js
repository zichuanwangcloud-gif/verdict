export const statusText = {
  queued: '排队中',
  dispatched: '已下发',
  preparing: '准备中',
  running: '执行中',
  completed: '已完成',
  error: '出错',
  canceled: '已取消',
}

export const verdictText = { pass: '通过', fail: '未通过', partial: '部分无法验证' }
export const acText = { pass: '通过', fail: '未通过', unverifiable: '无法验证' }

// 状态 → 标签颜色
export function runTone(run) {
  if (run.status === 'completed') return { pass: 'ok', fail: 'bad', partial: 'warn' }[run.verdict] || ''
  if (run.status === 'error') return 'bad'
  if (run.status === 'canceled') return ''
  return 'info'
}

export function runLabel(run) {
  return run.status === 'completed' ? verdictText[run.verdict] || '已完成' : statusText[run.status] || run.status
}

export const acTone = { pass: 'ok', fail: 'bad', unverifiable: 'warn' }

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
    else c.unverifiable++
  }
  return c
}

export const isLive = (run) => ['queued', 'dispatched', 'preparing', 'running'].includes(run?.status)

export const harnessName = (h) => ({ claude: 'Claude Code', codex: 'Codex' })[h] || h
