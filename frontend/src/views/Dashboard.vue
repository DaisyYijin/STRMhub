<script setup>
import { onMounted, ref } from 'vue'
import { accountApi, systemApi, taskApi } from '../api'

const accounts = ref([])
const tasks = ref([])
const health = ref(null)
const drivers = ref([])

onMounted(async () => {
  try { health.value = await systemApi.health() } catch { health.value = { status: 'offline' } }
  try { accounts.value = await accountApi.list() } catch { /* 401 等 */ }
  try { tasks.value = await taskApi.list() } catch { /* ignore */ }
  try { drivers.value = await accountApi.drivers() } catch { /* ignore */ }
})

const running = () => tasks.value.filter((t) => t.status === 'running').length
const done = () => tasks.value.filter((t) => t.status === 'done').length
const failed = () => tasks.value.filter((t) => t.status === 'error').length
</script>

<template>
  <h1>总览</h1>
  <div class="grid2">
    <div class="card">
      <h2>系统</h2>
      <p>后端状态: <span class="ok">{{ health?.status || '未知' }}</span></p>
      <p class="muted">管理 API 端口 6060 · Emby 302 反代端口 6086</p>
    </div>
    <div class="card">
      <h2>统计</h2>
      <p>网盘账户: <b>{{ accounts.length }}</b></p>
      <p>STRM 任务: <b>{{ tasks.length }}</b>
        (运行中 {{ running() }} · 成功 {{ done() }} · 失败 {{ failed() }})</p>
    </div>
  </div>
  <div class="card">
    <h2>可用驱动</h2>
    <table>
      <tr><th>类型</th><th>名称</th><th>认证方式</th></tr>
      <tr v-for="d in drivers" :key="d.name">
        <td><code>{{ d.name }}</code></td>
        <td>{{ d.label }}</td>
        <td>{{ d.auth_type }}</td>
      </tr>
    </table>
  </div>
</template>
