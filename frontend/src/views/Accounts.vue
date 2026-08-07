<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { accountApi, organizeApi, qrcodeApi } from '../api'

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

// 115 单账号模式: 取第一个(后端 upsert 保证唯一)
const acct = computed(() => filtered.value[0] || {})

const statusLabel = (s) => (s === 'ok' ? '正常' : s === 'error' ? '异常' : s || '-')

const driverLabel = (t) => drivers.value.find((d) => d.name === t)?.label || t

const fmtSize = (fmt, size) => fmt || (size ? `${(size / 1024 ** 3).toFixed(1)} GB` : '')

const usedPct = (info) => {
  const { used_size: used, total_size: total } = info || {}
  if (!used || !total) return 0
  return Math.min(100, Math.round((used / total) * 100))
}

// 识别规则 textarea 绑定(换行列表 <-> 数组)
const blacklistText = computed({
  get: () => (rules.value.blacklist || []).join('\n'),
  set: (v) => { rules.value.blacklist = v.split('\n').map((s) => s.trim()).filter(Boolean) },
})
const customWordsText = computed({
  get: () => (rules.value.custom_words || []).join('\n'),
  set: (v) => { rules.value.custom_words = v.split('\n').map((s) => s.trim()).filter(Boolean) },
})
const customMatchesText = computed({
  get: () => (rules.value.custom_matches || []).join('\n'),
  set: (v) => { rules.value.custom_matches = v.split('\n').map((s) => s.trim()).filter(Boolean) },
})
const releaseGroupsText = computed({
  get: () => (rules.value.release_groups || []).join(', '),
  set: (v) => { rules.value.release_groups = v.split(/[,，]/).map((s) => s.trim()).filter(Boolean) },
})

// 重命名预设
const renamePreset = ref('{title}.{year}.{quality}')
function applyPreset() {
  if (renamePreset.value) rules.value.rename_template = renamePreset.value
}

// ---- 账户页 tab ----
const tabs = [
  { id: 'info', label: '账号信息' },
  { id: 'organize', label: '整理归档' },
  { id: 'identify', label: '识别规则' },
  { id: 'ai', label: 'AI 辅助' },
  { id: 'rename', label: '重命名规则' },
  { id: 'category', label: '二级分类策略' },
]
const accTab = ref('info')

// ---- 整理归档 tab ----
const orgPath = ref('')
const orgPlan = ref(null)
const orgBusy = ref(false)
const orgMsg = ref('')

async function makePlan() {
  orgMsg.value = ''
  if (!orgPath.value.trim()) { orgMsg.value = { type: 'err', text: '请输入目录路径' }; return }
  orgBusy.value = true
  try {
    orgPlan.value = await organizeApi.plan(orgPath.value.trim())
  } catch (e) {
    orgMsg.value = { type: 'err', text: e.message }
  } finally {
    orgBusy.value = false
  }
}

async function execPlan() {
  if (!orgPlan.value) return
  if (!confirm(`确认执行整理归档? 共 ${orgPlan.value.items?.length || 0} 项`)) return
  orgBusy.value = true
  try {
    const res = await organizeApi.execute(orgPlan.value.plan_json)
    orgMsg.value = { type: 'ok', text: `整理完成: 成功 ${res.done || 0} 项` }
    orgPlan.value = null
    orgPath.value = ''
  } catch (e) {
    orgMsg.value = { type: 'err', text: e.message }
  } finally {
    orgBusy.value = false
  }
}

// ---- 规则配置(识别/AI/重命名/分类) ----
const RENAME_PRESETS = {
  '默认': '{title}.{year}.{quality}',
  '电影标准': '{title}.{year}.{quality}.{edition}',
  '剧集标准': '{title}.{year}.S{season:02}E{episode:02}.{quality}',
  '仅标题年份': '{title}.{year}',
  '自定义': '',
}

const rules = ref({
  min_video_size_mb: 0,
  blacklist: [],        // string[] 关键词
  custom_words: [],     // "原始|替换"
  custom_matches: [],   // "关键词|tmdb_id|movie/tv"
  release_groups: [],
  rename_template: '{title}.{year}.{quality}',
  category_rules: [],   // [{kind, match, target}]
  ai: { enabled: false, api_base: '', api_key: '', model: '' },
})
const rulesMsg = ref('')
const rulesBusy = ref(false)

const strList = (v) => (v || []).join('\n')
const setStrList = (key, text) => { rules.value[key] = text.split('\n').map((s) => s.trim()).filter(Boolean) }

async function loadRules() {
  if (!acct.value.id) return
  try {
    const data = await accountApi.rules(acct.value.id)
    const r = data.rules || {}
    rules.value = {
      min_video_size_mb: r.min_video_size_mb || 0,
      blacklist: r.blacklist || [],
      custom_words: r.custom_words || [],
      custom_matches: r.custom_matches || [],
      release_groups: r.release_groups || [],
      rename_template: r.rename_template || RENAME_PRESETS['默认'],
      category_rules: r.category_rules || [],
      ai: { enabled: false, api_base: '', api_key: '', model: '', ...(r.ai || {}) },
    }
  } catch { /* 忽略 */ }
}

async function saveRules() {
  if (!acct.value.id) return
  rulesBusy.value = true
  rulesMsg.value = ''
  try {
    await accountApi.saveRules(acct.value.id, rules.value)
    rulesMsg.value = { type: 'ok', text: '规则已保存' }
  } catch (e) {
    rulesMsg.value = { type: 'err', text: e.message }
  } finally {
    rulesBusy.value = false
  }
}

function addCategoryRule() {
  rules.value.category_rules.push({ kind: 'movie', match: '', target: '' })
}
function delCategoryRule(i) {
  rules.value.category_rules.splice(i, 1)
}

watch(acct, (a) => { if (a.id) loadRules() })

// ---- 手动表单(其他驱动) ----
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
      // 后端已自动建户/更新(单账号模式)
      qrShow.value = false
      const action = data.account?.action === 'updated' ? '已更新' : '已自动创建'
      msg.value = { type: 'ok', text: `扫码登录成功, 账户「${data.account?.name || ''}」${action}` }
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

  <!-- 115 页面: 单账号卡片 -->
  <div v-if="isP115">
    <!-- 未登录: 引导登录卡 -->
    <div v-if="!filtered.length" class="card">
      <h2>115 账号登录</h2>
      <p class="muted" style="margin-top: 0">使用 115 手机 App 扫码登录, 登录后自动创建账号并获取账号信息(容量/头像/昵称等)。</p>
      <div class="row" style="margin-bottom: 10px">
        <button class="primary" :disabled="qrBusy" @click="startQrcode">
          {{ qrBusy ? '生成二维码中...' : '115 扫码登录' }}
        </button>
      </div>
      <div v-if="qrError" class="msg err" style="margin-top: 0">{{ qrError }}</div>
    </div>

    <!-- 已登录: 账户管理卡(六 tab) -->
    <div v-else class="card acc-card">
      <div class="acc-tabs">
        <button v-for="t in tabs" :key="t.id" :class="{ 'tab-on': accTab === t.id }"
                @click="accTab = t.id">{{ t.label }}</button>
      </div>

      <!-- 账号信息 -->
      <template v-if="accTab === 'info'">
        <div class="acc-head">
          <img v-if="acct.info?.avatar" :src="acct.info.avatar" class="acc-big-avatar" alt="头像" />
          <div class="acc-head-info">
            <div class="acc-title">
              <span class="acc-name">{{ acct.name }}</span>
              <span v-if="acct.info?.vip" class="badge ok">{{ acct.info.vip }}</span>
              <span class="badge" :class="acct.status === 'ok' ? 'ok' : 'err'">{{ statusLabel(acct.status) }}</span>
            </div>
            <div class="muted" v-if="acct.info?.nickname && acct.info.nickname !== acct.name">昵称: {{ acct.info.nickname }}</div>
            <div class="muted" v-if="acct.info?.device">登录设备: {{ acct.info.device }}</div>
          </div>
        </div>
        <div class="acc-space" v-if="acct.info?.total_size">
          <div class="space-row">
            <span>容量</span>
            <span class="muted">
              已用 {{ fmtSize(acct.info.used_size_fmt, acct.info.used_size) }}
              / 总 {{ fmtSize(acct.info.total_size_fmt, acct.info.total_size) }}
              ({{ usedPct(acct.info) }}%)
            </span>
          </div>
          <div class="space-bar"><div class="space-fill" :style="{ width: usedPct(acct.info) + '%' }"></div></div>
        </div>
        <div class="row" style="margin-top: 14px">
          <button class="primary" :disabled="qrBusy" @click="startQrcode">
            {{ qrBusy ? '生成二维码中...' : '重新扫码登录(换号)' }}
          </button>
          <button class="danger" @click="remove(acct.id)">删除账户</button>
          <div v-if="msg" class="msg" :class="msg.type">{{ msg.text }}</div>
        </div>
        <div v-if="qrError" class="msg err" style="margin-top: 8px">{{ qrError }}</div>
      </template>

      <!-- 整理归档 -->
      <template v-else-if="accTab === 'organize'">
        <h2 style="margin-top: 0">整理归档</h2>
        <p class="muted" style="margin-top: 0">扫描影视目录, 识别影视信息并归类重命名(计划-预览-执行)。</p>
        <div class="row" style="margin-bottom: 10px">
          <input v-model="orgPath" placeholder="输入影视目录路径, 如 /media/movies" style="flex: 1" />
          <button class="primary" :disabled="orgBusy" @click="makePlan">{{ orgBusy ? '分析中...' : '生成整理计划' }}</button>
        </div>
        <div v-if="orgMsg" class="msg" :class="orgMsg.type">{{ orgMsg.text }}</div>
        <div v-if="orgPlan" class="org-plan">
          <table>
            <tr><th>原文件</th><th>→ 整理后</th></tr>
            <tr v-for="(it, i) in orgPlan.items" :key="i">
              <td class="muted">{{ it.original }}</td>
              <td><code>{{ it.target }}</code></td>
            </tr>
            <tr v-if="!orgPlan.items?.length"><td colspan="2" class="muted">未发现可整理的文件</td></tr>
          </table>
          <div class="row" style="margin-top: 10px">
            <button class="primary" :disabled="orgBusy" @click="execPlan">执行整理</button>
          </div>
        </div>
      </template>

      <!-- 识别规则 -->
      <template v-else-if="accTab === 'identify'">
        <h2 style="margin-top: 0">识别规则</h2>
        <div class="grid2">
          <div>
            <label>最小视频大小(MB) — 低于此大小的视频文件不纳入整理</label>
            <input type="number" min="0" v-model.number="rules.min_video_size_mb" />
          </div>
          <div>
            <label>发布组扩展(逗号分隔) — 识别发布组</label>
            <input v-model="releaseGroupsText" placeholder="例如: FRDS, 蓝光组, NEWCINE" />
          </div>
        </div>
        <div>
          <label>整理黑名单(每行一个关键词) — 命中关键词的文件跳过整理</label>
          <textarea v-model="blacklistText" rows="4" placeholder="例如: trailer&#10;sample&#10;xxx" />
        </div>
        <div>
          <label>自定义识别词(每行: 原始词|替换词) — 识别时将原始词替换</label>
          <textarea v-model="customWordsText" rows="3" placeholder="例如: 蜘蛛侠3|Spider-Man 3&#10;SW|Star Wars" />
        </div>
        <div>
          <label>自定义匹配(每行: 关键词|TMDB_ID|movie或tv) — 直接指定作品</label>
          <textarea v-model="customMatchesText" rows="3" placeholder="例如: 星际穿越|157336|movie&#10;三体|457433|tv" />
        </div>
        <div class="row" style="margin-top: 12px">
          <button class="primary" :disabled="rulesBusy" @click="saveRules">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="rulesMsg" class="msg" :class="rulesMsg.type">{{ rulesMsg.text }}</div>
        </div>
      </template>

      <!-- AI 辅助 -->
      <template v-else-if="accTab === 'ai'">
        <h2 style="margin-top: 0">AI 辅助识别</h2>
        <p class="muted" style="margin-top: 0">使用大模型辅助识别文件名(OpenAI 兼容接口)。</p>
        <div class="row" style="margin-bottom: 10px">
          <label style="display:flex; align-items:center; gap:6px">
            <input type="checkbox" v-model="rules.ai.enabled" /> 启用 AI 辅助
          </label>
        </div>
        <div class="grid2">
          <div>
            <label>API Base</label>
            <input v-model="rules.ai.api_base" placeholder="https://api.openai.com/v1" />
          </div>
          <div>
            <label>模型</label>
            <input v-model="rules.ai.model" placeholder="gpt-4o-mini / deepseek-chat" />
          </div>
        </div>
        <div>
          <label>API Key</label>
          <input v-model="rules.ai.api_key" type="password" placeholder="sk-..." />
        </div>
        <div class="row" style="margin-top: 12px">
          <button class="primary" :disabled="rulesBusy" @click="saveRules">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="rulesMsg" class="msg" :class="rulesMsg.type">{{ rulesMsg.text }}</div>
        </div>
      </template>

      <!-- 重命名规则 -->
      <template v-else-if="accTab === 'rename'">
        <h2 style="margin-top: 0">重命名规则</h2>
        <div class="grid2">
          <div>
            <label>预设模板</label>
            <select v-model="renamePreset" @change="applyPreset">
              <option v-for="(tpl, name) in RENAME_PRESETS" :key="name" :value="tpl">{{ name }}</option>
            </select>
          </div>
          <div>
            <label>自定义模板</label>
            <input v-model="rules.rename_template" placeholder="{title}.{year}.{quality}" />
          </div>
        </div>
        <p class="muted">可用变量: <code>{title}</code> 标题 · <code>{year}</code> 年份 · <code>{quality}</code> 画质 · <code>{edition}</code> 版本 · <code>{season:02}</code> 季 · <code>{episode:02}</code> 集</p>
        <div class="row" style="margin-top: 12px">
          <button class="primary" :disabled="rulesBusy" @click="saveRules">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="rulesMsg" class="msg" :class="rulesMsg.type">{{ rulesMsg.text }}</div>
        </div>
      </template>

      <!-- 二级分类策略 -->
      <template v-else-if="accTab === 'category'">
        <h2 style="margin-top: 0">二级分类策略</h2>
        <p class="muted" style="margin-top: 0">按匹配词将作品归类到指定子目录(如 电影/动作 → 动作片目录)。</p>
        <table>
          <tr><th>类型</th><th>匹配词(命中即归类)</th><th>分类目录</th><th></th></tr>
          <tr v-for="(r, i) in rules.category_rules" :key="i">
            <td>
              <select v-model="r.kind">
                <option value="movie">电影</option>
                <option value="tv">剧集</option>
              </select>
            </td>
            <td><input v-model="r.match" placeholder="动作 / 爱情 / 科幻" /></td>
            <td><input v-model="r.target" placeholder="动作片" /></td>
            <td><button class="danger" @click="delCategoryRule(i)">删除</button></td>
          </tr>
          <tr v-if="!rules.category_rules.length"><td colspan="4" class="muted">暂无分类规则</td></tr>
        </table>
        <div class="row" style="margin-top: 12px">
          <button @click="addCategoryRule">+ 添加分类规则</button>
          <button class="primary" :disabled="rulesBusy" @click="saveRules">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="rulesMsg" class="msg" :class="rulesMsg.type">{{ rulesMsg.text }}</div>
        </div>
      </template>
    </div>
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

  <div class="card" v-if="!isP115">
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
            <div class="muted" v-if="a.info.device">登录设备: {{ a.info.device }}</div>
          </template>
          <span v-else class="muted">-</span>
        </td>
        <td><span class="badge" :class="a.status === 'ok' ? 'ok' : 'err'">{{ statusLabel(a.status) }}</span></td>
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
