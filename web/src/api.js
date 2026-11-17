async function call(method, path, body) {
  const res = await fetch('/api' + path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  let data = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    throw new Error(`服务返回了无法解析的内容（HTTP ${res.status}）`)
  }
  if (!res.ok) throw new Error(data?.error || `请求失败（HTTP ${res.status}）`)
  return data
}

export const api = {
  verifiers: () => call('GET', '/verifiers'),
  areas: () => call('GET', '/areas'),
  requirements: () => call('GET', '/requirements'),
  createRequirement: (r) => call('POST', '/requirements', r),
  draftACs: (id) => call('POST', `/requirements/${id}/draft-acs`),
  saveACs: (id, acs) => call('PUT', `/requirements/${id}/acs`, acs),
  setDisposition: (id, disposition) => call('PUT', `/requirements/${id}/disposition`, { disposition }),
  changesets: () => call('GET', '/changesets'),
  createChangeSet: (c) => call('POST', '/changesets', c),
  verdict: (id) => call('GET', `/changesets/${id}/verdict`),
  run: (id) => call('POST', `/changesets/${id}/run`),
}
