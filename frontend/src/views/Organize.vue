<script setup>
import { ref } from 'vue'
import { organizeApi } from '../api'

const path = ref('')
const plan = ref(null)
const msg = ref('')

async function makePlan() {
  msg.value = ''
  plan.value = null
  try {
    plan.value = await organizeApi.plan(path.value)
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}

async function execute() {
  if (!plan.value) return
  if (!confirm(`确认执行 ${plan.value.preview.length} 条重命名?`)) return
  try {
    const res = await organizeApi.execute(plan.value.plan_json)
    msg.value = { type: 'ok', text: `执行完成: 成功 ${res.done}, 跳过 ${res.skipped}` }
    plan.value = null
  } catch (e) {
    msg.value = { type: 'err', text: e.message }
  }
}
</script>

<template>
  <h1>目录整理(计划-预览-执行)</h1>
  <div class="card">
    <div>
      <label>扫描目录</label>
      <div class="row">
        <input v-model="path" placeholder="如 /strm/media" />
        <button class="primary" @click="makePlan">生成计划</button>
      </div>
    </div>
    <div v-if="msg" class="msg" :class="msg.type">{{ msg.text }}</div>
  </div>

  <div v-if="plan" class="card">
    <h2>计划预览({{ plan.preview.length }} 条) <span class="muted">plan_id: {{ plan.plan_id }}</span></h2>
    <table>
      <tr><th>源文件</th><th>目标文件</th></tr>
      <tr v-for="(a, i) in plan.preview" :key="i">
        <td class="muted">{{ a.source }}</td>
        <td>{{ a.target }}</td>
      </tr>
      <tr v-if="!plan.preview.length"><td colspan="2" class="muted">无需整理(文件名已规范)</td></tr>
    </table>
    <div class="row" style="margin-top: 10px">
      <button class="primary" :disabled="!plan.preview.length" @click="execute">执行重命名</button>
    </div>
  </div>
</template>
