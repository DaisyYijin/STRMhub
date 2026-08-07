<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { accountApi, authApi, isAuthed, qrcodeApi, setToken } from './api'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Accounts from './views/Accounts.vue'
import Tasks from './views/Tasks.vue'
import Scrape from './views/Scrape.vue'
import Organize from './views/Organize.vue'
import Logs from './views/Logs.vue'
import Automation from './views/Automation.vue'

const view = ref(localStorage.getItem('strmhub_view') || 'dashboard')
const driverFilter = ref(localStorage.getItem('strmhub_driver') || '')
const authed = ref(isAuthed())
const health = ref('...')
// 驱动列表: 用 localStorage 缓存, 刷新页面立即渲染菜单(后台再刷新)
const drivers = ref(JSON.parse(localStorage.getItem('strmhub_drivers') || '[]'))
const driversError = ref('')
const netpanOpen = ref(localStorage.getItem('strmhub_netpan_open') !== '0')

// 基础菜单(与网盘无关)
const baseViews = [
  { id: 'dashboard', label: '总览', comp: Dashboard },
  { id: 'tasks', label: 'STRM 任务', comp: Tasks },
  { id: 'scrape', label: '刮削与海报墙', comp: Scrape },
  { id: 'organize', label: '目录整理', comp: Organize },
  { id: 'automation', label: 'Webhook 联动', comp: Automation },
  { id: 'logs', label: '实时日志', comp: Logs },  // 日志页(不进菜单, 由右上角按钮进入)
]

// 菜单项(排除日志页)
const menuViews = computed(() => baseViews.filter((v) => v.id !== 'logs'))

// 网盘管理菜单(按驱动类型动态生成, 如 115 网盘管理 / 123 云盘管理)
const accountViews = computed(() => drivers.value.map((d) => ({
  id: `accounts:${d.name}`,
  label: `${d.label}管理`,
  comp: Accounts,
  driver: d.name,
})))

// 兜底: 驱动列表加载失败时仍提供"全部账户"入口
const fallbackAccountView = {
  id: 'accounts:all',
  label: '全部账户',
  comp: Accounts,
  driver: '',
}

const current = computed(() => {
  if (view.value.startsWith('accounts:')) {
    const found = accountViews.value.find((v) => v.id === view.value)
    if (found) return found
    if (view.value === 'accounts:all') return fallbackAccountView
    return { ...fallbackAccountView, driver: driverFilter.value }
  }
  return baseViews.find((v) => v.id === view.value) || baseViews[0]
})

async function loadDrivers() {
  if (!authed.value) return
  driversError.value = ''
  try {
    drivers.value = await accountApi.drivers()
    localStorage.setItem('strmhub_drivers', JSON.stringify(drivers.value))
    if (!drivers.value.length) {
      driversError.value = '驱动列表为空'
    }
  } catch (e) {
    // 有缓存时正常显示菜单, 仅提示刷新失败; 无缓存才报错
    if (!drivers.value.length) {
      driversError.value = `驱动列表加载失败: ${e.message}`
    }
    console.error('[STRMhub]', `驱动列表刷新失败: ${e.message}`)
  }
}

function switchView(id) {
  view.value = id
  if (id.startsWith('accounts:')) {
    driverFilter.value = id.split(':')[1]
    localStorage.setItem('strmhub_driver', driverFilter.value)
  }
  localStorage.setItem('strmhub_view', id)
}


function toggleNetpan() {
  netpanOpen.value = !netpanOpen.value
  localStorage.setItem('strmhub_netpan_open', netpanOpen.value ? '1' : '0')
}

async function logout() {
  setToken('')
  authed.value = false
  drivers.value = []
  view.value = 'dashboard'
}

watch(authed, (v) => { if (v) loadDrivers() })

onMounted(async () => {
  // token 过期: 自动回到登录页
  window.addEventListener('strmhub-unauthorized', () => { authed.value = false })
  try {
    const h = await fetch('/api/health').then((r) => r.json())
    health.value = h.status
  } catch { health.value = 'offline' }
  if (authed.value) loadDrivers()
})
</script>

<template>
  <div v-if="!authed">
    <Login @login="authed = true" />
  </div>
  <div v-else class="layout">
    <aside class="side">
      <div class="logo">STRMhub</div>
      <nav>
        <a v-for="v in menuViews" :key="v.id" :class="{ active: view === v.id }"
           href="#" @click.prevent="switchView(v.id)">{{ v.label }}</a>
        <div v-if="accountViews.length || driversError" class="nav-group nav-toggle"
             :class="{ open: netpanOpen }" @click="toggleNetpan">
          <span class="arrow"></span> 网盘管理
        </div>
        <template v-if="netpanOpen">
          <div v-if="driversError" class="nav-error">{{ driversError }}</div>
          <a v-for="v in accountViews" :key="v.id" class="nav-sub"
             :class="{ active: view === v.id }"
             href="#" @click.prevent="switchView(v.id)">{{ v.label }}</a>
          <a v-if="driversError" class="nav-sub" :class="{ active: view === 'accounts:all' }"
             href="#" @click.prevent="switchView('accounts:all')">{{ fallbackAccountView.label }}</a>
        </template>
      </nav>
      <div class="side-foot">
        <span class="muted">后端: {{ health }}</span>
        <button @click="logout">退出</button>
      </div>
    </aside>
    <main class="main">
      <component :is="current.comp" :driver-type="current.driver || ''"
                 :key="current.driver || 'all'" />
    </main>

    <!-- 右上角: 实时日志入口 -->
    <button class="log-fab" :class="{ on: view === 'logs' }" title="实时日志"
            @click="switchView('logs')">📄 实时日志</button>
  </div>
</template>

<style scoped>
.layout { display: flex; min-height: 100vh; }
.side {
  width: 190px; background: var(--card); border-right: 1px solid var(--line);
  padding: 16px 10px; display: flex; flex-direction: column; gap: 8px;
}
.logo { font-size: 18px; font-weight: 700; color: var(--accent); padding: 0 8px 10px; }
.side nav { display: flex; flex-direction: column; gap: 2px; flex: 1; }
.side nav a {
  color: var(--muted); padding: 7px 10px; border-radius: 6px; font-size: 13px;
}
.side nav a:hover { background: var(--hover); color: var(--text); }
.side nav a.active { background: var(--accent); color: #fff; }
.side nav .nav-group {
  font-size: 13px; font-weight: 600; color: var(--muted);
  padding: 7px 10px; border-radius: 6px;
  cursor: pointer; user-select: none;
  display: flex; align-items: center; gap: 6px;
}
.side nav .nav-group:hover { background: var(--hover); color: var(--text); }
.side nav .nav-group .arrow {
  width: 0; height: 0;
  border-left: 5px solid currentColor;
  border-top: 4px solid transparent;
  border-bottom: 4px solid transparent;
  transition: transform .15s ease;
}
.side nav .nav-group.open .arrow { transform: rotate(90deg); }
.side nav .nav-sub { padding-left: 22px; font-size: 12.5px; }
.side nav .nav-error { font-size: 11px; color: var(--bad); padding: 4px 10px; word-break: break-all; }
.side-foot { display: flex; flex-direction: column; gap: 8px; padding: 8px; }
.main { flex: 1; padding: 22px 26px; max-width: none; }
</style>
