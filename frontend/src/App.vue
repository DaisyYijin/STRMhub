<script setup>
import { computed, onMounted, ref } from 'vue'
import { authApi, isAuthed, setToken } from './api'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Accounts from './views/Accounts.vue'
import Tasks from './views/Tasks.vue'
import Scrape from './views/Scrape.vue'
import Organize from './views/Organize.vue'
import Automation from './views/Automation.vue'

const view = ref(localStorage.getItem('strmhub_view') || 'dashboard')
const authed = ref(isAuthed())
const health = ref('...')

const views = [
  { id: 'dashboard', label: '总览', comp: Dashboard },
  { id: 'accounts', label: '网盘账户', comp: Accounts },
  { id: 'tasks', label: 'STRM 任务', comp: Tasks },
  { id: 'scrape', label: '刮削与海报墙', comp: Scrape },
  { id: 'organize', label: '目录整理', comp: Organize },
  { id: 'automation', label: 'Webhook 联动', comp: Automation },
]

const current = computed(() => views.find((v) => v.id === view.value) || views[0])

function switchView(id) {
  view.value = id
  localStorage.setItem('strmhub_view', id)
}

async function logout() {
  setToken('')
  authed.value = false
  view.value = 'dashboard'
}

onMounted(async () => {
  try {
    const h = await fetch('/api/health').then((r) => r.json())
    health.value = h.status
  } catch { health.value = 'offline' }
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
        <a v-for="v in views" :key="v.id" :class="{ active: view === v.id }"
           href="#" @click.prevent="switchView(v.id)">{{ v.label }}</a>
      </nav>
      <div class="side-foot">
        <span class="muted">后端: {{ health }}</span>
        <button @click="logout">退出</button>
      </div>
    </aside>
    <main class="main">
      <component :is="current.comp" />
    </main>
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
.side nav a:hover { background: #0b1220; color: var(--text); }
.side nav a.active { background: #0e7490; color: #fff; }
.side-foot { display: flex; flex-direction: column; gap: 8px; padding: 8px; }
.main { flex: 1; padding: 22px 26px; max-width: 1100px; }
</style>
