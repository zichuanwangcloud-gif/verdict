import { reactive } from 'vue'

const KEY = 'verdict.token'

function load() {
  try {
    return localStorage.getItem(KEY) || ''
  } catch {
    return ''
  }
}

export const auth = reactive({ token: load() })

export function setToken(t) {
  auth.token = t
  try {
    if (t) localStorage.setItem(KEY, t)
    else localStorage.removeItem(KEY)
  } catch {
    /* 隐私模式下不可用：只保存在内存里 */
  }
}

async function call(method, path, body) {
  const headers = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (auth.token) headers.Authorization = 'Bearer ' + auth.token
  const res = await fetch('/api' + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  let data = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    throw new Error(`服务返回了无法解析的内容（HTTP ${res.status}）`)
  }
  if (res.status === 401 && path !== '/me') setToken('')
  if (!res.ok) throw new Error(data?.error || `请求失败（HTTP ${res.status}）`)
  return data
}

export const api = {
  me: () => call('GET', '/me'),
  settings: () => call('GET', '/settings'),
  saveSettings: (s) => call('PUT', '/settings', s),

  runtimes: () => call('GET', '/runtimes'),
  createRuntime: (name) => call('POST', '/runtimes', { name }),
  renameRuntime: (id, name) => call('PATCH', `/runtimes/${id}`, { name }),
  setConcurrency: (id, concurrency) => call('PATCH', `/runtimes/${id}`, { concurrency }),
  deleteRuntime: (id) => call('DELETE', `/runtimes/${id}`),
  probeRuntime: (id) => call('POST', `/runtimes/${id}/probe`),
  rotateToken: (id) => call('POST', `/runtimes/${id}/token`),
  tiers: () => call('GET', '/tiers'),

  repos: () => call('GET', '/repos'),
  createRepo: (r) => call('POST', '/repos', r),
  updateRepo: (id, r) => call('PUT', `/repos/${id}`, r),
  deleteRepo: (id) => call('DELETE', `/repos/${id}`),

  versions: (repoId) => call('GET', '/versions' + (repoId ? `?repo_id=${repoId}` : '')),
  createVersion: (v) => call('POST', '/versions', v),
  version: (id) => call('GET', `/versions/${id}`),
  updateVersion: (id, v) => call('PATCH', `/versions/${id}`, v),
  deleteVersion: (id) => call('DELETE', `/versions/${id}`),
  addVersionIssue: (id, issueKey, move) => call('POST', `/versions/${id}/issues`, { issue_key: issueKey, move: !!move }),
  removeVersionIssue: (id, key) => call('DELETE', `/versions/${id}/issues/${encodeURIComponent(key)}`),
  freezeVersion: (id, body) => call('POST', `/versions/${id}/regressions`, body),
  releaseVersion: (id, note) => call('POST', `/versions/${id}/release`, { note }),
  retryRegression: (id) => call('POST', `/regressions/${id}/retry`),
  cancelRegression: (id) => call('POST', `/regressions/${id}/cancel`),

  cases: (repoId) => call('GET', `/repos/${repoId}/cases`),
  createCase: (repoId, runId, level) => call('POST', `/repos/${repoId}/cases`, { run_id: runId, level }),
  caseDetail: (id) => call('GET', `/cases/${id}`),
  updateCase: (id, body) => call('PATCH', `/cases/${id}`, body),
  deleteCase: (id) => call('DELETE', `/cases/${id}`),

  issue: (key) => call('GET', `/issues/${encodeURIComponent(key)}`),
  searchIssues: (q, limit = 20) => call('GET', `/issues?q=${encodeURIComponent(q)}&limit=${limit}`),

  boardTeams: () => call('GET', '/board/teams'),
  boardIssues: ({ team = '', states = [], assignee = 'all', q = '', repoId = 0, limit = 100 } = {}) => {
    const p = new URLSearchParams()
    if (team) p.set('team', team)
    if (states.length) p.set('states', states.join(','))
    if (assignee) p.set('assignee', assignee)
    if (q) p.set('q', q)
    if (repoId) p.set('repo_id', repoId)
    p.set('limit', limit)
    return call('GET', '/board/issues?' + p.toString())
  },
  startBatchRuns: (body) => call('POST', '/board/runs', body),

  runs: () => call('GET', '/runs'),
  createRun: (r) => call('POST', '/runs', r),
  run: (id) => call('GET', `/runs/${id}`),
  logs: (id, after) => call('GET', `/runs/${id}/logs?after=${after}`),
  cancelRun: (id) => call('POST', `/runs/${id}/cancel`),
  approveRun: (id, note) => call('POST', `/runs/${id}/approve`, { note }),
  rerun: (id) => call('POST', `/runs/${id}/rerun`),
  resumeRun: (id) => call('POST', `/runs/${id}/resume`),
  writeBack: (id) => call('POST', `/runs/${id}/linear`),
}

// 产物需要带管理员令牌下载：取成 blob URL 供 <img> 使用。
const blobCache = new Map()
export async function artifactURL(runId, name) {
  const key = runId + '/' + name
  if (blobCache.has(key)) return blobCache.get(key)
  const res = await fetch(`/api/runs/${runId}/artifacts/${encodeURIComponent(name)}`, {
    headers: auth.token ? { Authorization: 'Bearer ' + auth.token } : {},
  })
  if (!res.ok) throw new Error(`产物下载失败（HTTP ${res.status}）`)
  const url = URL.createObjectURL(await res.blob())
  blobCache.set(key, url)
  return url
}
