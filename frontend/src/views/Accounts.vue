<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { accountApi, qrcodeApi } from '../api'

const props = defineProps({
  driverType: { type: String, default: '' },
})

const accounts = ref([])
const drivers = ref([])
const form = ref({ name: '', driver_type: '', credential: '', config_json: '' })
const msg = ref('')

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

async function load() {
  try {
    accounts.value = await accountApi.list()
    drivers.value = await accountApi.drivers()
  } catch { /* 401 已由全局事件处理 */ }
}

onMounted(() => { form.value.driver_type = props.driverType; load() })

watch(() => props.driverType, (t) => {
  form.value.driver_type = t
  load()
})

const filtered = computed(() =>
  props.driverType
    ? accounts.value.filter((a) => a.driver_type === props.driverType)
    : accounts.value)

const driverLabel = (t) => drivers.value.find((d) => d.name === t)?.label || t

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
      uid: qrUid.value, time: qrTime.value, sign: qrSign.value, app: qrApp.value,
    })
    qrStatus.value = data.status
    if (data.status === 'confirmed') {
      clearInterval(qrTimer.value)
      qrTimer.value = null
      // 自动创建账户
      await accountApi.create({
        name: `115-${Date.now().toString().slice(-6)}`,
        driver_type: 'p115',
        credential: data.cookies,
      })
      qrShow.value = false
      msg.value = { type: 'ok', text: '扫码登录成功, 账户已创建' }
      await load()
    } else if (data.status === 'expired' || data.status === 'error') {
      clearInterval(qrTimer.value)
      qrTimer.value = null
      msg.value = { type: 'err', text: data.status === 'expired' ? '二维码已过期, 请重新生成' : '登录失败' }
      qrShow.value = false
    }
  } catch (e) {
    clearInterval(qrTimer.value)
    qrTimer.value = null
    msg.value = { type: 'err', text: e.message }
    qrShow.value = false
  }
}

function closeQrcode() {
  if (qrTimer.value) { clearInterval(qrTimer.value); qrTimer.value = null }
  qrShow.value = false
}
</script>

<template>
  <h1>{{ driverLabel(props.driverType) || '网盘' }}管理</h1>

  <div class="card">
    <h2>新增账户</h2>
    <div class="row" style="margin-bottom: 10px">
      <button v-if="props.driverType === 'p115'" class="primary"
              :disabled="qrBusy" @click="startQrcode">
        {{ qrBusy ? '生成二维码中...' : '115 扫码登录' }}
      </button>
      <span class="muted">也可手动填写凭据:</span>
    </div>
    <div v-if="qrError" class="msg err" style="margin-top: 0">{{ qrError }}</div>
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
      <tr><th>ID</th><th>名称</th><th>驱动</th><th>状态</th><th>操作</th></tr>
      <tr v-for="a in filtered" :key="a.id">
        <td>{{ a.id }}</td>
        <td>{{ a.name }}</td>
        <td><code>{{ a.driver_type }}</code></td>
        <td><span class="badge" :class="a.status === 'ok' ? 'ok' : 'err'">{{ a.status }}</span></td>
        <td><button class="danger" @click="remove(a.id)">删除</button></td>
      </tr>
      <tr v-if="!filtered.length"><td colspan="5" class="muted">暂无账户</td></tr>
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
        </p>
      </div>
      <div class="row" style="justify-content: center; margin-top: 10px">
        <button @click="closeQrcode">关闭</button>
      </div>
    </div>
  </div>
</template>
