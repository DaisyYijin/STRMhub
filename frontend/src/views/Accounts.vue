<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { accountApi, qrcodeApi } from '../api'

const props = defineProps({
  driverType: { type: String, default: '' },
})

const accounts = ref([])
const drivers = ref([])
const msg = ref('')
const form = ref({ name: '', driver_type: '', credential: '', config_json: '' })

// 扫码登录弹窗状态
const qrShow = ref(false)
const qrImg = ref('')
const qrUid = ref('')
const qrTime = ref('')
const qrSign = ref('')
const qrApp = ref('web')
const qrApps = ref([])
const qrStatus = ref('')
const qrTimer = ref(null)
const qrBusy = ref(false)
const qrError = ref('')

// 设备 key -> 中文名(与后端 APP_LABELS 对应)
const DEVICE_LABELS = {
  web: '网页版', desktop: '桌面客户端', android: '安卓', harmony: '鸿蒙',
  alipaymini: '支付宝小程序', qandroid: '安卓(默认)', ios: 'iOS', os_windows: 'Windows',
}

// 115 页面: 扫码专用(不显示手动表单); 其他驱动/全部账户: 保留手动表单
const isP115 = computed(() => props.driverType === 'p115')

async function load() {
  try {
    accounts.value = await accountApi.list()
    drivers.value = await accountApi.drivers()
  } catch { /* 401 已由全局事件处理 */ }
}

onMounted(load)

watch(() => props.driverType, load)

const filtered = computed(() =>
  props.driverType
    ? accounts.value.filter((a) => a.driver_type === props.driverType)
    : accounts.value)

const driverLabel = (t) => drivers.value.find((d) => d.name === t)?.label || t

const fmtSize = (fmt, size) => fmt || (size ? `${(size / 1024 ** 3).toFixed(1)} GB` : '')

const deviceLabel = (key) => DEVICE_LABELS[key] || key || ''

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
    form.value = { ...form.value, name: '', credential: '', config_json: '' }
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

// ---- 115 扫码登录 ----
async function startQrcode() {
  if (qrBusy.value) return
  qrBusy.value = true
  qrError.value = ''
  msg.value = ''
  try {
    const data = await qrcodeApi.start('p115')
    qrUid.value = data.uid
    qrTime.value = data.time
    qrSign.value = data.sign
    qrImg.value = data.qr_image  // SVG data URI
    qrApps.value = data.apps || []
    qrApp.value = data.apps?.find((a) => a.key === 'web')?.key
      || data.apps?.[0]?.key || 'web'
    qrStatus.value = 'waiting'
    qrShow.value = true
    qrTimer.value = setInterval(pollQrcode, 2000)
  } catch (e) {
    // 错误直接显示在按钮旁, 便于发现
    qrError.value = `扫码登录不可用: ${e.message}`
    console.error('[STRMhub] 扫码登录失败:', e)
  } finally {
    qrBusy.value = false
  }
}

async function pollQrcode() {
  try {
    const data = await qrcodeApi.poll('p115', {
      uid: String(qrUid.value || ''),
      time: String(qrTime.value || ''),
      sign: String(qrSign.value || ''),
      app: qrApp.value || 'web',
    })
    qrStatus.value = data.status
    if (data.status === 'confirmed') {
      clearInterval(qrTimer.value)
      qrTimer.value = null
      // 后端已自动创建账户(含账号信息)
      qrShow.value = false
      msg.value = { type: 'ok', text: `扫码登录成功, 账户「${data.account?.name || ''}」已自动创建` }
      await load()
    } else if (data.status === 'expired') {
      clearInterval(qrTimer.value)
      qrTimer.value = null
      qrError.value = '二维码已过期, 请重新生成'
      qrStatus.value = 'expired'
    } else if (data.status === 'cancelled') {
      clearInterval(qrTimer.value)
      qrTimer.value = null
      qrError.value = '已取消扫码, 请重新生成'
      qrStatus.value = 'cancelled'
    }
  } catch (e) {
    // 轮询失败: 停止轮询但保留弹窗, 便于用户看到错误后重新操作
    if (qrTimer.value) { clearInterval(qrTimer.value); qrTimer.value = null }
    qrStatus.value = 'error'
    qrError.value = `轮询失败: ${e.message}`
    console.error('[STRMhub] 扫码轮询失败:', e)
  }
}

function closeQrcode() {
  if (qrTimer.value) { clearInterval(qrTimer.value); qrTimer.value = null }
  qrShow.value = false
}
</script>

<template>
  <h1>{{ driverLabel(props.driverType) || '网盘' }}管理</h1>

  <!-- 115 页面: 扫码专用 -->
  <div v-if="isP115" class="card">
    <h2>115 账号登录</h2>
    <div class="row" style="margin-bottom: 10px">
      <button class="primary" :disabled="qrBusy" @click="startQrcode">
        {{ qrBusy ? '生成二维码中...' : '115 扫码登录' }}
      </button>
      <span class="muted">使用 115 手机 App 扫码, 登录后自动创建账号并获取账号信息</span>
    </div>
    <div v-if="qrError" class="msg err" style="margin-top: 0">{{ qrError }}</div>
  </div>

  <!-- 其他驱动/全部账户: 手动表单 -->
  <div v-else class="card">
    <h2>新增账户</h2>
    <div class="grid2">
      <div>
        <label>名称</label>
        <input v-model="form.name" :placeholder="`如: 我的${driverLabel(props.driverType) || '网盘'}`" />
      </div>
      <div>
        <label>驱动类型</label>
        <select v-model="form.driver_type" :disabled="!!props.driverType">
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
    <h2>{{ props.driverType ? `${driverLabel(props.driverType)}账户列表` : '账户列表' }}(凭据已加密存储)</h2>
    <table>
      <tr><th>ID</th><th>账号</th><th>驱动</th><th>信息</th><th>状态</th><th>操作</th></tr>
      <tr v-for="a in filtered" :key="a.id">
        <td>{{ a.id }}</td>
        <td>
          <div class="acc-cell">
            <img v-if="a.info?.avatar" :src="a.info.avatar" class="acc-avatar" alt="头像" />
            <span class="acc-name">{{ a.name }}</span>
          </div>
        </td>
        <td><code>{{ a.driver_type }}</code></td>
        <td>
          <template v-if="a.info && Object.keys(a.info).length">
            <div class="muted" style="white-space: nowrap">
              <span v-if="a.info.nickname">昵称: {{ a.info.nickname }}</span>
              <span v-if="a.info.vip" class="badge ok" style="margin-left: 4px">{{ a.info.vip }}</span>
            </div>
            <div class="muted" v-if="a.info.used_size_fmt || a.info.total_size_fmt">
              容量: {{ fmtSize(a.info.used_size_fmt, a.info.used_size) }} / {{ fmtSize(a.info.total_size_fmt, a.info.total_size) }}
            </div>
            <div class="muted" v-if="a.info.device">登录设备: {{ deviceLabel(a.info.device) }}</div>
          </template>
          <span v-else class="muted">-</span>
        </td>
        <td><span class="badge" :class="a.status === 'ok' ? 'ok' : 'err'">{{ a.status }}</span></td>
        <td><button class="danger" @click="remove(a.id)">删除</button></td>
      </tr>
      <tr v-if="!filtered.length"><td colspan="6" class="muted">暂无账户</td></tr>
    </table>
  </div>

  <!-- 扫码登录弹窗 -->
  <div v-if="qrShow" class="modal-mask" @click.self="closeQrcode">
    <div class="modal">
      <h2 style="margin-top: 0">115 扫码登录</h2>
      <div style="text-align: center">
        <img :src="qrImg" alt="二维码" style="width: 220px; height: 220px; border: 1px solid var(--line); border-radius: 8px" />
        <p class="muted">打开 115 手机 App → 扫一扫 登录</p>
        <p style="margin: 6px 0">
          <label for="qr-app">登录设备:</label>
          <select id="qr-app" v-model="qrApp" style="margin-left: 8px">
            <option v-for="a in qrApps" :key="a.key" :value="a.key">{{ a.label }}</option>
          </select>
        </p>
        <p>
          <span v-if="qrStatus === 'waiting'" class="warn-c">等待扫码...</span>
          <span v-else-if="qrStatus === 'scanned'" class="ok">已扫码, 请在手机上确认</span>
          <span v-else-if="qrStatus === 'confirmed'" class="ok">登录成功</span>
          <span v-else-if="qrStatus === 'expired'" class="err">二维码已过期, 请重新生成</span>
          <span v-else-if="qrStatus === 'cancelled'" class="err">已取消扫码</span>
          <span v-else-if="qrStatus === 'error'" class="err">轮询失败(网络/服务异常)</span>
        </p>
        <p v-if="qrError" class="msg err" style="margin-top: 4px">{{ qrError }}</p>
        <div class="row" style="justify-content: center; margin-top: 10px">
          <button @click="closeQrcode">关闭</button>
          <button v-if="qrStatus === 'expired' || qrStatus === 'error' || qrStatus === 'cancelled'"
                  class="primary" style="margin-left: 8px" @click="startQrcode">重新生成</button>
        </div>
      </div>
    </div>
  </div>
</template>
