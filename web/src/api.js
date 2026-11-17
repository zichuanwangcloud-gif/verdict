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
  deleteRuntime: (id) => call('DELETE', `/runtimes/${id}`),
  probeRuntime: (id) => call('POST', `/runtimes/${id}/probe`),

  repos: () => call('GET', '/repos'),
  createRepo: (r) => call('POST', '/repos', r),
  updateRepo: (id, r) => call('PUT', `/repos/${id}`, r),
  deleteRepo: (id) => call('DELETE', `/repos/${id}`),

  issue: (key) => call('GET', `/issues/${encodeURIComponent(key)}`),

  runs: () => call('GET', '/runs'),
  createRun: (r) => call('POST', '/runs', r),
  run: (id) => call('GET', `/runs/${id}`),
  logs: (id, after) => call('GET', `/runs/${id}/logs?after=${after}`),
  cancelRun: (id) => call('POST', `/runs/${id}/cancel`),
  rerun: (id) => call('POST', `/runs/${id}/rerun`),
  writeBack: (id) => call('POST', `/runs/${id}/linear`),
}
