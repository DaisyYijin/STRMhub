<script setup>
import { computed, onMounted, ref } from 'vue'
import { accountApi, taskApi } from '../api'

const tasks = ref([])
const accounts = ref([])
const form = ref({
  name: '', account_id: null, remote_path: '', local_output: '',
  scan_mode: 'incremental_missing', extensions: '', base_url: '', token: '',
})
const msg = ref('')
const runningId = ref(null)
const lastResult = ref(null)

const scanModes = [
  ['incremental_missing', '增量补缺(只补缺少的 strm)'],
  ['incremental_update', '增量更新(内容不同才重写)'],
  ['full_sync', '全量覆写'],
]

async function load() {
  tasks.value = await taskApi.list()
  accounts.value = await accountApi.list()
}

onMounted(load)

const accountName = (id) => accounts.value.find((a) => a.id === id)?.name || id

async function create() {
  msg.value = ''
  try {
    const extensions = form.value.extensions
      ? form.value.extensions.split(',').map((s) => s.trim()).filter(Boolean)
      : []
    await taskApi.create({
      account_id: Number(form.value.account_id),
      name: form.value.name,
      remote_path: form.value.remote_path,
      local_output: form.value.local_output,
      scan_mode: form.value.scan_mode,
      extensions,
      base_url: form.value.base_url,
      token: form.value.token,
    })
    form.value = { ...form.value, name: '', extensions: '' }
    await load()
    msg.value = { type: 'ok', text: '任务已创建' }
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}

async function run(task) {
  runningId.value = task.id
  lastResult.value = null
  try {
    lastResult.value = await taskApi.run(task.id)
    await load()
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  } finally {
    runningId.value = null
  }
}

async function remove(id) {
  if (!confirm('确认删除任务?')) return
  await taskApi.remove(id)
  await load()
}

const statusClass = (s) => ({ running: 'run', done: 'ok', error: 'err' }[s] || '')
</script>

<template>
  <h1>STRM 任务</h1>
  <div class="card">
    <h2>新建任务</h2>
    <div class="grid2">
      <div>
        <label>任务名称</label>
        <input v-model="form.name" placeholder="如: 115 电影库" />
      </div>
      <div>
        <label>网盘账户</label>
        <select v-model="form.account_id">
          <option :value="null" disabled>选择账户</option>
          <option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.name }}</option>
        </select>
      </div>
      <div>
        <label>源目录(远端路径)</label>
        <input v-model="form.remote_path" placeholder="115 目录 ID / 本地路径" />
      </div>
      <div>
        <label>STRM 输出目录</label>
        <input v-model="form.local_output" placeholder="如 /strm/media" />
      </div>
      <div>
        <label>扫描模式</label>
        <select v-model="form.scan_mode">
          <option v-for="[v, l] in scanModes" :key="v" :value="v">{{ l }}</option>
        </select>
      </div>
      <div>
        <label>扩展名(逗号分隔, 留空=默认媒体集)</label>
        <input v-model="form.extensions" placeholder="mkv,mp4" />
      </div>
      <div>
        <label>base_url(302 端点地址)</label>
        <input v-model="form.base_url" placeholder="http://hub:6060" />
      </div>
      <div>
        <label>token(留空自动生成)</label>
        <input v-model="form.token" />
      </div>
    </div>
    <div class="row" style="margin-top: 10px">
      <button class="primary" @click="create">创建</button>
      <div v-if="msg" class="msg" :class="msg.type">{{ msg.text }}</div>
    </div>
  </div>

  <div class="card">
    <h2>任务列表</h2>
    <table>
      <tr>
        <th>ID</th><th>名称</th><th>账户</th><th>模式</th><th>状态</th>
        <th>最近运行</th><th>操作</th>
      </tr>
      <tr v-for="t in tasks" :key="t.id">
        <td>{{ t.id }}</td>
        <td>{{ t.name }}</td>
        <td>{{ accountName(t.account_id) }}</td>
        <td><code>{{ t.scan_mode }}</code></td>
        <td><span class="badge" :class="statusClass(t.status)">{{ t.status }}</span></td>
        <td class="muted">
          {{ t.last_run_at || '从未' }}
          <div v-if="t.last_error" class="err">{{ t.last_error }}</div>
        </td>
        <td>
          <div class="row">
            <button class="primary" :disabled="runningId === t.id" @click="run(t)">
              {{ runningId === t.id ? '运行中...' : '运行' }}
            </button>
            <button class="danger" @click="remove(t.id)">删除</button>
          </div>
        </td>
      </tr>
      <tr v-if="!tasks.length"><td colspan="7" class="muted">暂无任务</td></tr>
    </table>
    <div v-if="lastResult" class="card">
      <h2>运行结果</h2>
      <pre>{{ JSON.stringify(lastResult, null, 2) }}</pre>
    </div>
  </div>
</template>
