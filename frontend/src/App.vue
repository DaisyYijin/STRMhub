<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { accountApi, authApi, http, isAuthed, qrcodeApi, setToken } from './api'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Accounts from './views/Accounts.vue'
import Tasks from './views/Tasks.vue'
import Scrape from './views/Scrape.vue'
import Organize from './views/Organize.vue'
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
]

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

// ---- 实时日志面板(右上角) ----
const logShow = ref(false)
const logLines = ref([])
const logPaused = ref(false)
const logAutoScroll = ref(true)
const logError = ref('')
const logBox = ref(null)
let logEs = null

function logLevelClass(level) {
  if (level === 'ERROR' || level === 'CRITICAL') return 'log-err'
  if (level === 'WARNING') return 'log-warn'
  return ''
}

// ---- 日志汉化(仅前端展示层, 后端日志保持英文原文) ----
const LOG_TRANSLATIONS = [
  [/Application startup complete\./, '应用启动完成'],
  [/Uvicorn running on (http:\/\/[^ )]+)/, '服务已启动并监听 $1'],
  [/Waiting for application startup\./, '等待应用启动...'],
  [/Shutting down/, '正在关闭服务'],
  [/Invalid HTTP request received\./, '收到无效 HTTP 请求(通常是端口扫描探测, 可忽略)'],
  [/Connection lost/, '客户端连接断开'],
  [/Started server process \[\d+\]/, '服务进程已启动'],
  [/Finished server process \[\d+\]/, '服务进程已结束'],
  [/Press CTRL\+C to quit/, '按 Ctrl+C 退出'],
]

function translateMsg(msg) {
  let out = msg || ''
  for (const [re, rep] of LOG_TRANSLATIONS) out = out.replace(re, rep)
  // 访问日志: '1.2.3.4:5678 - "GET /api/x HTTP/1.1" 200'
  // (消息开头带时间戳, 不能锚定 ^)
  const m = out.match(/(\d+\.\d+\.\d+\.\d+:\d+) - "(\w+) (\S+) HTTP\/1\.1" (\d+)/)
  if (m) {
    let path = m[3]
    if (path.startsWith('/api/logs/stream')) path = '/api/logs/stream (实时日志流)'
    out = out.replace(m[0], `${m[1]} - 请求: ${m[2]} ${path} → ${m[4]}`)
  }
  // 隐藏 URL 中的登录令牌
  out = out.replace(/(token=)[A-Za-z0-9_.\-]{20,}/g, '$1[已隐藏]')
  return out
}

// 日志级别汉化
const LEVEL_LABELS = { INFO: '信息', WARNING: '警告', ERROR: '错误', CRITICAL: '严重', DEBUG: '调试' }
const levelLabel = (lv) => LEVEL_LABELS[lv] || lv

async function logOpen() {
  logShow.value = true
  logError.value = ''
  try {
    const data = await http.get('/api/logs?tail=200')
    // 最新日志显示在第一行
    logLines.value = (data.lines || []).reverse()
  } catch (e) {
    logError.value = `历史日志加载失败: ${e.message}`
  }
  const token = localStorage.getItem('strmhub_token') || ''
  if (logEs) logEs.close()
  logEs = new EventSource(`/api/logs/stream?token=${encodeURIComponent(token)}`)
  logEs.onmessage = (e) => {
    if (logPaused.value) return
    try {
      const ln = JSON.parse(e.data)
      if (ln.error) { logError.value = `SSE 连接失败: ${ln.error}`; return }
      logLines.value.unshift(ln)  // 新日志插入第一行
      if (logLines.value.length > 1000) logLines.value.length = 1000
      if (logAutoScroll.value) {
        nextTick(() => { if (logBox.value) logBox.value.scrollTop = 0 })
      }
    } catch { /* 心跳忽略 */ }
  }
  logEs.onerror = () => {
    logError.value = 'SSE 连接断开, 自动重连中...'
  }
}

function logClose() {
  logShow.value = false
  if (logEs) { logEs.close(); logEs = null }
}

function logClear() {
  logLines.value = []
}

onUnmounted(() => { if (logEs) logEs.close() })

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
        <a v-for="v in baseViews" :key="v.id" :class="{ active: view === v.id }"
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
      <!-- 页面内容(logShow 时被日志面板覆盖) -->
      <div v-if="!logShow">
        <component :is="current.comp" :driver-type="current.driver || ''" />
      </div>

      <!-- 实时日志(点击📄覆盖整个页面区域) -->
      <div v-else class="log-panel">
        <div class="log-head">
          <span class="log-title">实时日志</span>
          <button class="log-toggle" :class="{ on: !logPaused }" @click="logPaused = !logPaused">
            {{ logPaused ? '已暂停' : '实时' }}
          </button>
          <button class="log-toggle" :class="{ on: logAutoScroll }" @click="logAutoScroll = !logAutoScroll">
            自动滚动 {{ logAutoScroll ? '开' : '关' }}
          </button>
          <button class="log-btn" @click="logClear">清空</button>
          <button class="log-btn" @click="logClose">返回</button>
        </div>
        <div v-if="logError" class="log-errbar">{{ logError }}</div>
        <div ref="logBox" class="log-body">
          <div v-for="(ln, i) in logLines" :key="i" class="log-line" :class="logLevelClass(ln.level)">
            <span class="log-ts">{{ new Date(ln.ts * 1000).toLocaleTimeString() }}</span>
            <span class="log-lv">[{{ levelLabel(ln.level) }}]</span>
            <span class="log-msg">{{ translateMsg(ln.msg) }}</span>
          </div>
          <div v-if="!logLines.length" class="muted" style="padding: 10px">暂无日志...</div>
        </div>
      </div>
    </main>

    <!-- 右上角: 实时日志开关 -->
    <button class="log-fab" :class="{ on: logShow }" title="实时日志" @click="logShow ? logClose() : logOpen()">📄</button>
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
