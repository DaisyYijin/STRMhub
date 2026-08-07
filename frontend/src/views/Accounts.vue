<script setup>
import { onMounted, ref } from 'vue'
import { accountApi } from '../api'

const accounts = ref([])
const drivers = ref([])
const form = ref({ name: '', driver_type: '', credential: '', config_json: '' })
const msg = ref('')

async function load() {
  accounts.value = await accountApi.list()
  drivers.value = await accountApi.drivers()
}

onMounted(load)

async function create() {
  msg.value = ''
  try {
    let config = {}
    if (form.value.config_json.trim()) {
      config = JSON.parse(form.value.config_json)
    }
    await accountApi.create({
      name: form.value.name,
      driver_type: form.value.driver_type,
      credential: form.value.credential,
      config,
    })
    form.value = { name: '', driver_type: drivers.value[0]?.name || '', credential: '', config_json: '' }
    await load()
    msg.value = { type: 'ok', text: '账户已创建' }
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}

async function remove(id) {
  if (!confirm('确认删除该账户?')) return
  await accountApi.remove(id)
  await load()
}
</script>

<template>
  <h1>网盘账户</h1>
  <div class="card">
    <h2>新增账户</h2>
    <div class="grid2">
      <div>
        <label>名称</label>
        <input v-model="form.name" placeholder="如: 我的115" />
      </div>
      <div>
        <label>驱动类型</label>
        <select v-model="form.driver_type">
          <option v-for="d in drivers" :key="d.name" :value="d.name">{{ d.label }} ({{ d.name }})</option>
        </select>
      </div>
      <div>
        <label>凭据(115 Cookie / 123 账号密码 / WebDAV user:pass, 按驱动)</label>
        <input v-model="form.credential" placeholder="留空表示无需登录" />
      </div>
      <div>
        <label>配置 JSON(如 WebDAV 的 base_url)</label>
        <input v-model="form.config_json" placeholder='{"base_url":"https://dav.example.com"}' />
      </div>
    </div>
    <div class="row" style="margin-top: 10px">
      <button class="primary" @click="create">创建</button>
      <div v-if="msg" class="msg" :class="msg.type">{{ msg.text }}</div>
    </div>
  </div>
  <div class="card">
    <h2>账户列表(凭据已加密存储)</h2>
    <table>
      <tr><th>ID</th><th>名称</th><th>驱动</th><th>状态</th><th>操作</th></tr>
      <tr v-for="a in accounts" :key="a.id">
        <td>{{ a.id }}</td>
        <td>{{ a.name }}</td>
        <td><code>{{ a.driver_type }}</code></td>
        <td><span class="badge" :class="a.status === 'ok' ? 'ok' : 'err'">{{ a.status }}</span></td>
        <td><button class="danger" @click="remove(a.id)">删除</button></td>
      </tr>
      <tr v-if="!accounts.length"><td colspan="5" class="muted">暂无账户</td></tr>
    </table>
  </div>
</template>
