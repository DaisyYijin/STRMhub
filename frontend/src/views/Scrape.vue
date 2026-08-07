<script setup>
import { onMounted, ref } from 'vue'
import { scrapeApi } from '../api'

const strmDir = ref('')
const taskId = ref('')
const result = ref(null)
const items = ref([])
const msg = ref('')

async function run() {
  msg.value = ''
  try {
    result.value = await scrapeApi.run(strmDir.value)
    taskId.value = result.value.task_id
    await loadItems()
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}

async function loadItems() {
  if (!taskId.value) return
  items.value = await scrapeApi.items(taskId.value)
}

onMounted(async () => {
  const saved = localStorage.getItem('strmhub_scrape_task')
  if (saved) {
    taskId.value = saved
    await loadItems()
  }
})

async function loadSaved() {
  if (!taskId.value) return
  localStorage.setItem('strmhub_scrape_task', taskId.value)
  await loadItems()
}

const statusText = (s) => ({ matched: '已匹配', doubt: '存疑', none: '未匹配' }[s] || s)
</script>

<template>
  <h1>刮削与海报墙</h1>
  <div class="card">
    <h2>刮削 STRM 目录(需 TMDB_API_KEY)</h2>
    <div class="grid2">
      <div>
        <label>STRM 目录</label>
        <input v-model="strmDir" placeholder="如 /strm/media" />
      </div>
      <div>
        <label>查询已有任务(海报墙)</label>
        <div class="row">
          <input v-model="taskId" placeholder="task_id" />
          <button @click="loadSaved">查询</button>
        </div>
      </div>
    </div>
    <div class="row" style="margin-top: 10px">
      <button class="primary" @click="run">开始刮削</button>
      <div v-if="msg" class="msg" :class="msg.type">{{ msg.text }}</div>
    </div>
    <div v-if="result" class="muted" style="margin-top: 8px">
      作品组 {{ result.groups }} · 匹配 {{ result.matched }} · 存疑 {{ result.doubt }} ·
      未匹配 {{ result.none }} · 海报 {{ result.posters }}
    </div>
  </div>

  <div class="card">
    <h2>海报墙(追更状态)</h2>
    <table>
      <tr>
        <th>标题</th><th>年份</th><th>类型</th><th>状态</th>
        <th>TMDB ID</th><th>集数(本地/TMDB)</th>
      </tr>
      <tr v-for="it in items" :key="it.title">
        <td>{{ it.title }}</td>
        <td>{{ it.year || '-' }}</td>
        <td>{{ it.media_type === 'tv' ? '剧集' : '电影' }}</td>
        <td><span class="badge" :class="{ ok: it.status === 'matched', 'warn-c': it.status === 'doubt', err: it.status === 'none' }">
          {{ statusText(it.status) }}</span></td>
        <td>{{ it.tmdb_id || '-' }}</td>
        <td>{{ it.ep_local }}/{{ it.ep_tmdb || '?' }}</td>
      </tr>
      <tr v-if="!items.length"><td colspan="6" class="muted">暂无条目</td></tr>
    </table>
  </div>
</template>
