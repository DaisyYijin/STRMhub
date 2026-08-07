<script setup>
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { http } from '../api'

// ---- 实时日志独立页面(SSE 实时流) ----
const logLines = ref([])
const logPaused = ref(false)
const logAutoScroll = ref(true)
const logError = ref('')
const logBox = ref(null)
let logEs = null

const LEVEL_LABELS = { INFO: '信息', WARNING: '警告', ERROR: '错误', CRITICAL: '严重', DEBUG: '调试' }
const levelLabel = (lv) => LEVEL_LABELS[lv] || lv

function logLevelClass(level) {
  if (level === 'ERROR' || level === 'CRITICAL') return 'log-err'
  if (level === 'WARNING') return 'log-warn'
  return ''
}

// 日志汉化(仅前端展示层, 后端日志保持英文原文)
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
  const m = out.match(/(\d+\.\d+\.\d+\.\d+:\d+) - "(\w+) (\S+) HTTP\/1\.1" (\d+)/)
  if (m) {
    let path = m[3]
    if (path.startsWith('/api/logs/stream')) path = '/api/logs/stream (实时日志流)'
    out = out.replace(m[0], `${m[1]} - 请求: ${m[2]} ${path} → ${m[4]}`)
  }
  // httpx 网络请求: 'HTTP Request: GET https://... "HTTP/2 200 OK"'
  const hm = out.match(/HTTP Request: (\w+) (\S+)(?: "([^"]*)")?/)
  if (hm) {
    const method = hm[1] === 'GET' ? '请求' : hm[1] === 'POST' ? '提交' : hm[1]
    out = out.replace(hm[0], `网络${method}: ${hm[2]}${hm[3] ? ' → ' + hm[3] : ''}`)
  }
  // 隐藏 URL 中的登录令牌
  out = out.replace(/(token=)[A-Za-z0-9_.\-]{20,}/g, '$1[已隐藏]')
  return out
}

function scrollTop() {
  if (logAutoScroll.value) {
    nextTick(() => { if (logBox.value) logBox.value.scrollTop = 0 })
  }
}

async function openStream() {
  logError.value = ''
  try {
    const data = await http.get('/api/logs?tail=200')
    logLines.value = (data.lines || []).reverse()  // 最新在上
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
      logLines.value.unshift(ln)
      if (logLines.value.length > 1000) logLines.value.length = 1000
      scrollTop()
    } catch { /* 心跳忽略 */ }
  }
  logEs.onerror = () => {
    logError.value = 'SSE 连接断开, 自动重连中...'
  }
}

function logClear() {
  logLines.value = []
}

onMounted(openStream)
onUnmounted(() => { if (logEs) logEs.close() })
</script>

<template>
  <h1>实时日志</h1>
  <div class="log-panel log-page">
    <div class="log-head">
      <span class="log-title">实时日志</span>
      <button class="log-toggle" :class="{ on: !logPaused }" @click="logPaused = !logPaused">
        {{ logPaused ? '已暂停' : '实时' }}
      </button>
      <button class="log-toggle" :class="{ on: logAutoScroll }" @click="logAutoScroll = !logAutoScroll">
        自动滚动 {{ logAutoScroll ? '开' : '关' }}
      </button>
      <button class="log-btn" @click="logClear">清空</button>
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
</template>
