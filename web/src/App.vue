<script setup>
import { computed } from 'vue'
import { auth, setToken } from './api'
import { route, go, match } from './router'
import Login from './pages/Login.vue'
import Runs from './pages/Runs.vue'
import NewRun from './pages/NewRun.vue'
import RunDetail from './pages/RunDetail.vue'
import Runtimes from './pages/Runtimes.vue'
import Repos from './pages/Repos.vue'
import Settings from './pages/Settings.vue'

const tabs = [
  { path: '/runs', label: '测试' },
  { path: '/runtimes', label: '运行时' },
  { path: '/repos', label: '仓库' },
  { path: '/settings', label: '设置' },
]

const view = computed(() => {
  if (match('/runs/new')) return { comp: NewRun, props: {} }
  const r = match('/runs/:id')
  if (r) return { comp: RunDetail, props: { id: r.id } }
  if (match('/runtimes')) return { comp: Runtimes, props: {} }
  if (match('/repos')) return { comp: Repos, props: {} }
  if (match('/settings')) return { comp: Settings, props: {} }
  return { comp: Runs, props: {} }
})

const activeTab = computed(() => tabs.find((t) => route.path.startsWith(t.path))?.path || '/runs')
</script>

<template>
  <Login v-if="!auth.token" />
  <template v-else>
    <header>
      <h1><a href="/runs" @click.prevent="go('/runs')">Verdict</a></h1>
      <nav>
        <button
          v-for="t in tabs"
          :key="t.path"
          :class="{ active: activeTab === t.path }"
          @click="go(t.path)"
        >
          {{ t.label }}
        </button>
      </nav>
      <span class="spacer" />
      <button class="link" @click="setToken('')">退出</button>
    </header>
    <main>
      <component :is="view.comp" v-bind="view.props" :key="route.path" />
    </main>
  </template>
</template>
