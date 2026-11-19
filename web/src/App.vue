<script setup>
import { computed } from 'vue'
import { FlaskConical, Server, GitBranch, Settings as SettingsIcon, LogOut, Sun, Moon, ScanSearch, PackageCheck, ShieldCheck, Inbox, BarChart3 } from 'lucide-vue-next'
import { auth, setToken } from './api'
import { route, go, match } from './router'
import { theme, toggleTheme } from './theme'
import Login from './pages/Login.vue'
import Runs from './pages/Runs.vue'
import NewRun from './pages/NewRun.vue'
import RunDetail from './pages/RunDetail.vue'
import Runtimes from './pages/Runtimes.vue'
import Repos from './pages/Repos.vue'
import Settings from './pages/Settings.vue'
import Versions from './pages/Versions.vue'
import VersionDetail from './pages/VersionDetail.vue'
import Cases from './pages/Cases.vue'
import Board from './pages/Board.vue'
import Review from './pages/Review.vue'

const tabs = [
  { path: '/board', label: '待测试', icon: Inbox },
  { path: '/runs', label: '验收测试', icon: FlaskConical },
  { path: '/versions', label: '版本', icon: PackageCheck },
  { path: '/cases', label: '回归基线', icon: ShieldCheck },
  { path: '/review', label: '复盘', icon: BarChart3 },
  { path: '/runtimes', label: '运行时', icon: Server },
  { path: '/repos', label: '仓库', icon: GitBranch },
  { path: '/settings', label: '设置', icon: SettingsIcon },
]

const view = computed(() => {
  if (match('/board')) return { comp: Board, props: {} }
  if (match('/runs/new')) return { comp: NewRun, props: {} }
  const r = match('/runs/:id')
  if (r) return { comp: RunDetail, props: { id: r.id } }
  if (match('/versions')) return { comp: Versions, props: {} }
  const v = match('/versions/:id')
  if (v) return { comp: VersionDetail, props: { id: v.id } }
  if (match('/cases')) return { comp: Cases, props: {} }
  if (match('/review')) return { comp: Review, props: {} }
  if (match('/runtimes')) return { comp: Runtimes, props: {} }
  if (match('/repos')) return { comp: Repos, props: {} }
  if (match('/settings')) return { comp: Settings, props: {} }
  return { comp: Runs, props: {} }
})

const activeTab = computed(() => tabs.find((t) => route.path.startsWith(t.path))?.path || '/runs')
</script>

<template>
  <Login v-if="!auth.token" />
  <div v-else class="shell">
    <aside class="sidebar">
      <a href="/runs" class="brand" @click.prevent="go('/runs')">
        <span class="brand-mark"><ScanSearch :size="18" /></span>
        <span>
          <div class="brand-name">Verdict</div>
          <div class="brand-sub">AI 验收测试</div>
        </span>
      </a>
      <button
        v-for="t in tabs"
        :key="t.path"
        class="nav-item"
        :class="{ active: activeTab === t.path }"
        @click="go(t.path)"
      >
        <component :is="t.icon" :size="17" />
        {{ t.label }}
      </button>
      <div class="sidebar-foot">
        <button class="nav-item" @click="toggleTheme">
          <component :is="theme === 'dark' ? Sun : Moon" :size="17" />
          {{ theme === 'dark' ? '浅色模式' : '深色模式' }}
        </button>
        <button class="nav-item" @click="setToken('')">
          <LogOut :size="17" />
          退出登录
        </button>
      </div>
    </aside>

    <div class="topbar">
      <div class="row between">
        <a href="/runs" class="brand" @click.prevent="go('/runs')">
          <span class="brand-mark"><ScanSearch :size="18" /></span>
          <span class="brand-name">Verdict</span>
        </a>
        <div class="row">
          <button class="btn ghost icon" :aria-label="theme === 'dark' ? '浅色模式' : '深色模式'" @click="toggleTheme">
            <component :is="theme === 'dark' ? Sun : Moon" :size="17" />
          </button>
          <button class="btn ghost icon" aria-label="退出登录" @click="setToken('')"><LogOut :size="17" /></button>
        </div>
      </div>
      <nav>
        <button
          v-for="t in tabs"
          :key="t.path"
          class="nav-item"
          :class="{ active: activeTab === t.path }"
          @click="go(t.path)"
        >
          <component :is="t.icon" :size="15" />
          {{ t.label }}
        </button>
      </nav>
    </div>

    <main class="content">
      <component :is="view.comp" v-bind="view.props" :key="route.path" />
    </main>
  </div>
</template>
