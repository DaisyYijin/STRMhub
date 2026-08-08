<script setup>
import { computed, onMounted, ref } from 'vue'
import { accountApi, taskApi } from '../api'

const tasks = ref([])
const accounts = ref([])
const form = ref({
  name: '', account_id: null, remote_path: '', local_output: '',
  scan_mode: 'incremental_missing', extensions: '', base_url: '', token: '',
  extra: { subtitle: false, image: false, nfo: false, other_ext: '', concurrency: 4,
           metadata_sync: 'off' },
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
  try {
    tasks.value = await taskApi.list()
    accounts.value = await accountApi.list()
  } catch { /* 401 已由全局事件处理 */ }
  loadLife()
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
      extra: {
        subtitle: form.value.extra.subtitle,
        image: form.value.extra.image,
        nfo: form.value.extra.nfo,
        other_ext: form.value.extra.otherExt
          ? form.value.extra.otherExt.split(',').map((x) => x.trim()).filter(Boolean)
          : [],
        concurrency: form.value.extra.concurrency,
        metadata_sync: form.value.extra.metadata_sync || 'off',
      },
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

// 生活事件监控(115 推送式增量): 开关 + 状态
const lifeMap = ref({})

async function loadLife() {
  const map = {}
  for (const t of tasks.value) {
    try {
      map[t.id] = await taskApi.life(t.id)
    } catch { /* 忽略 */ }
  }
  lifeMap.value = map
}

async function toggleLife(t) {
  const cur = lifeMap.value[t.id]
  const next = !cur?.monitor_life
  try {
    await taskApi.setLife(t.id, { monitor_life: next, interval: 10 })
    lifeMap.value[t.id] = await taskApi.life(t.id)
    msg.value = { type: 'ok', text: next ? '已开启生活事件监控(秒级增量)' : '已关闭生活事件监控' }
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}
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
      <div>
        <label>伴生文件下载(AutoFilm 方案): 与视频同名的字幕/海报/nfo 一并下载到本地</label>
        <div class="row" style="gap: 14px; flex-wrap: wrap">
          <label style="display:flex; align-items:center; gap:4px">
            <input type="checkbox" v-model="form.extra.subtitle" /> 字幕(.srt/.ass/.vtt)
          </label>
          <label style="display:flex; align-items:center; gap:4px">
            <input type="checkbox" v-model="form.extra.image" /> 图片(.jpg/.png)
          </label>
          <label style="display:flex; align-items:center; gap:4px">
            <input type="checkbox" v-model="form.extra.nfo" /> nfo
          </label>
          <label style="display:flex; align-items:center; gap:4px">
            <input type="checkbox" v-model="form.extra.other_ext" /> 自定义扩展名
          </label>
        </div>
      </div>
      <div v-if="form.extra.other_ext">
        <label>自定义扩展名(逗号分隔, 如 zip,md)</label>
        <input v-model="form.extra.otherExt" placeholder="zip,md" />
      </div>
      <div>
        <label>伴生下载并发(独立限流, 建议 2-5)</label>
        <input type="number" v-model.number="form.extra.concurrency" min="1" max="10" />
      </div>
      <div>
        <label>元数据同步(LitePan 模式): nfo/图片与网盘双向补缺, 均不覆盖已有</label>
        <select v-model="form.extra.metadata_sync">
          <option value="off">关闭</option>
          <option value="local_primary">local_primary(本地为主, 双向补缺)</option>
          <option value="cloud_primary">cloud_primary(网盘为主, 只下载补齐)</option>
          <option value="bidirectional">bidirectional(对等双向补缺)</option>
        </select>
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
        <th>事件监控</th><th>最近运行</th><th>操作</th>
      </tr>
      <tr v-for="t in tasks" :key="t.id">
        <td>{{ t.id }}</td>
        <td>{{ t.name }}</td>
        <td>{{ accountName(t.account_id) }}</td>
        <td><code>{{ t.scan_mode }}</code></td>
        <td><span class="badge" :class="statusClass(t.status)">{{ t.status }}</span></td>
        <td>
          <button class="primary" :class="{ 'tab-on': lifeMap[t.id]?.monitor_life }"
                  @click="toggleLife(t)">
            {{ lifeMap[t.id]?.monitor_life ? '监控中' : '未监控' }}
          </button>
          <div class="muted" v-if="lifeMap[t.id]?.monitor_life">
            已处理 {{ lifeMap[t.id]?.processed || 0 }} 事件
          </div>
        </td>
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
      <tr v-if="!tasks.length"><td colspan="8" class="muted">暂无任务</td></tr>
    </table>
    <div v-if="lastResult" class="card">
      <h2>运行结果</h2>
      <pre>{{ JSON.stringify(lastResult, null, 2) }}</pre>
    </div>
  </div>
</template>
