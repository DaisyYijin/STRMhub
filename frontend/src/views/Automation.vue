<script setup>
import { onMounted, ref } from 'vue'
import { automationApi } from '../api'

const rules = ref([])
const form = ref({ name: '', trigger: 'webhook', action_chain: '', token: '' })
const msg = ref('')

async function load() {
  rules.value = await automationApi.list()
}

onMounted(load)

async function create() {
  msg.value = ''
  try {
    const chain = form.value.action_chain
      .split('\n').map((s) => s.trim()).filter(Boolean)
    await automationApi.create({
      name: form.value.name,
      trigger: form.value.trigger,
      action_chain: chain,
      token: form.value.token,
    })
    form.value = { name: '', trigger: 'webhook', action_chain: '', token: '' }
    await load()
    msg.value = { type: 'ok', text: '规则已创建' }
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}

async function remove(id) {
  if (!confirm('确认删除规则?')) return
  await automationApi.remove(id)
  await load()
}
</script>

<template>
  <h1>Webhook 联动</h1>
  <div class="card">
    <h2>新建规则</h2>
    <div class="grid2">
      <div>
        <label>规则名称</label>
        <input v-model="form.name" placeholder="如: 转存即触发" />
      </div>
      <div>
        <label>触发类型</label>
        <select v-model="form.trigger">
          <option value="webhook">webhook(通用)</option>
          <option value="qas_strm">qas_strm(Quark-Auto-Save)</option>
          <option value="cs_strm">cs_strm(CloudSaver)</option>
        </select>
      </div>
      <div class="span2">
        <label>动作链(每行一个: strm_scan:任务ID / scrape:目录 / emby_refresh)</label>
        <textarea v-model="form.action_chain" rows="3"
                  placeholder="strm_scan:1&#10;scrape:/strm/media&#10;emby_refresh"></textarea>
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
    <h2>规则列表</h2>
    <table>
      <tr><th>ID</th><th>名称</th><th>触发</th><th>动作链</th><th>触发地址</th><th>操作</th></tr>
      <tr v-for="r in rules" :key="r.id">
        <td>{{ r.id }}</td>
        <td>{{ r.name }}</td>
        <td>{{ r.trigger }}</td>
        <td>
          <code v-for="a in r.action_chain" :key="a" style="display:block">{{ a }}</code>
        </td>
        <td class="muted"><code>/api/automation/webhook/{{ r.token }}</code></td>
        <td><button class="danger" @click="remove(r.id)">删除</button></td>
      </tr>
      <tr v-if="!rules.length"><td colspan="6" class="muted">暂无规则</td></tr>
    </table>
  </div>
</template>

<style scoped>
.span2 { grid-column: span 2; }
</style>
