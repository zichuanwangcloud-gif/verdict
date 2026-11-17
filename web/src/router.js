import { reactive } from 'vue'

// 极简 history 路由：报告链接形如 /runs/<id>，需要能直接打开。
export const route = reactive({ path: location.pathname })

export function go(path) {
  if (path === route.path) return
  history.pushState(null, '', path)
  route.path = path
  window.scrollTo(0, 0)
}

window.addEventListener('popstate', () => {
  route.path = location.pathname
})

export function match(pattern) {
  const p = pattern.split('/')
  const a = route.path.replace(/\/+$/, '').split('/')
  if (p.length !== a.length) return null
  const params = {}
  for (let i = 0; i < p.length; i++) {
    if (p[i].startsWith(':')) params[p[i].slice(1)] = decodeURIComponent(a[i])
    else if (p[i] !== a[i]) return null
  }
  return params
}
