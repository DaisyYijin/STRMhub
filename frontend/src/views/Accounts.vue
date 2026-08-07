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

const statusLabel = (s) => (s === 'ok' ? '正常' : s === 'error' ? '异常' : s === 'expired' ? '登录过期' : s || '-')

const driverLabel = (t) => drivers.value.find((d) => d.name === t)?.label || t

// 登录设备 key -> 中文名(兼容历史数据存的 key; 新数据已存中文名)
const DEVICE_LABELS = {
  web: '网页版', desktop: '桌面客户端', ios: '苹果端', android: '安卓端',
  harmony: '鸿蒙端', alipaymini: '支付宝小程序', wechatmini: '微信小程序端',
  tv: '安卓电视端', apple_tv: '苹果电视端', qandroid: '115管理_安卓端',
  os_windows: 'Windows端', os_mac: 'macOS端', os_linux: 'Linux端',
  ipad: '苹果平板端', qios: '115管理_苹果端', qipad: '115管理_平板端',
  '115ios': '115_苹果端', '115android': '115_安卓端', '115ipad': '115_平板端',
}
const deviceLabel = (d) => (d && DEVICE_LABELS[d] ? DEVICE_LABELS[d] : d || '')

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
  { id: 'vars', label: '变量说明' },
  { id: 'syntax', label: '语法说明' },
]
const accTab = ref('info')

// ---- 变量说明 / 语法说明(静态文档) ----
const VAR_DOCS = [
  ['{original_name}', '原文件名', '钢铁侠.2008.2160p.UHD.BluRay.x265.10bit.HDR.TrueHD.7.1-TnT.mkv'],
  ['{ext}', '扩展名', 'iso'],
  ['{title}', 'TMDB中的标题', '钢铁侠'],
  ['{en_title}', 'TMDB中的英文标题 (tmdb为空时，会将中文标题转换为拼音)', 'Iron Man'],
  ['{first_letter}', '标题的大写拼音首字母', 'G'],
  ['{year}', 'TMDB中的年份', '2008'],
  ['{tmdb_id}', 'TMDB ID', '1726'],
  ['{resource_pix}', '分辨率', '2160p'],
  ['{resource_version}', '资源版本', 'IMAX、HQ、3D、CC、DC'],
  ['{resource_source}', '资源来源', 'USA.UHD、NF、DSNP'],
  ['{resource_type}', '资源质量', 'BluRay、WEB-DL、HDTV'],
  ['{resource_effect}', '特效', 'DV.HDR、DV、HDR、SDR'],
  ['{video_encode}', '视频编码', 'H265.10bit、REMUX'],
  ['{audio_encode}', '音频编码', 'TrueHD.7.1'],
  ['{resource_team}', '发布组', 'TnT'],
  ['{fps}', '帧率', '60FPS'],
  ['{season_episode}', '季集 SxxExx', 'S01E01'],
  ['{season_num}', '季号', '1'],
  ['{episode_num}', '集号', '1'],
  ['{disc_num}', '盘号', '1'],
  ['{season_name}', '季名', '东海篇'],
  ['{season_year}', '季年份 (可能为空，不建议使用)', '1999'],
  ['{episode_name}', '集名', '我是路飞！将要成为海贼王的男人！'],
  ['{custom_regex_match}', '自定义匹配', '自定义匹配'],
]

const SYNTAX_DOCS = [
  ['{变量名}', '取这个变量的值'],
  ['<...>', '用尖括号包围的字符串称为 块，块里 {变量名} 表示当 {变量名}不为空时，取块里的内容。简单来说重命名规则就是写多个块，然后拼在一起'],
  ['<{{name}}...>', '给块取个名字，类似于临时变量，之后可以用 {name} 反复引用该块的值'],
  ['<?{{name}}...>', '有名字的块可以只取名不输出，便于后续引用'],
  ['<{title}> 和 {title} 的区别', '<{title}> 会先判断 title 是否为空，后者是直接取 title 的值；也就是说如果你用的变量可能为空，则必须用 < > 把变量包起来'],
  ['[[ ]]', '如果想用 { }，由于和语法冲突，可以用 [[ ]] 代替，最终会替换为 { }'],
]

const SYNTAX_EXAMPLES = [
  ["{resource_effect.replace('.', ' ')}", '替换 resource_effect 中的.为空格'],
  ['{resource_effect.lower()}', '将 resource_effect 转换为小写'],
  ['{resource_effect.upper()}', '将 resource_effect 转换为大写'],
  ["{'2160p' if resource_pix=='4k' else resource_pix}", '如果 resource_pix 为 4k，则返回 2160p，否则返回 resource_pix'],
  ['自定义命名规则', '自定义多个块，也是多个 <...>，最终这些块按顺序拼在一块'],
  ['文件夹命名规则示例', '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]'],
  ['电影命名规则示例', '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>'],
]

// ---- 整理归档 tab(三目录 + 目录树选择) ----
const ORG_FIELDS = [
  { key: 'pending', label: '等待整理', hint: '需要整理的影视放在这里, 开始整理后扫描此目录' },
  { key: 'existing', label: '已经存在', hint: '整理完成的影视已存在时, 重复文件移动到此目录' },
  { key: 'redundant', label: '冗余文件', hint: '识别失败或非影视文件移动到此目录' },
]
const orgDirs = ref({ pending: {}, existing: {}, redundant: {} })
const orgMsg = ref('')
const orgBusy = ref(false)
const orgResult = ref(null)
// 目录树弹窗
const picker = ref(null)  // { field, parent, stack: [{id, name}], dirs: [] }
const pickerBusy = ref(false)
const pickerErr = ref('')

async function openPicker(field) {
  picker.value = { field, parent: '', stack: [], dirs: [], current: { id: '', name: '根目录' } }
  await loadPickerDirs()
}
async function loadPickerDirs() {
  if (!picker.value) return
  pickerBusy.value = true
  pickerErr.value = ''
  picker.value.diagnose = null
  try {
    const data = await accountApi.browse(acct.value.id, picker.value.parent)
    picker.value.dirs = data.dirs || []
    picker.value.diagnose = data.diagnose || null
  } catch (e) {
    pickerErr.value = e.message
  } finally {
    pickerBusy.value = false
  }
}
function enterDir(dir) {
  picker.value.stack.push(picker.value.current)
  picker.value.current = { id: dir.id, name: dir.name }
  picker.value.parent = dir.id
  loadPickerDirs()
}
function pickerBack() {
  const prev = picker.value.stack.pop()
  picker.value.current = prev || { id: '', name: '根目录' }
  picker.value.parent = prev?.id || ''
  loadPickerDirs()
}
function selectThisDir() {
  orgDirs.value[picker.value.field] = { ...picker.value.current }
  picker.value = null
  orgMsg.value = ''
}
function closePicker() { picker.value = null }

async function saveOrgDirs() {
  orgMsg.value = ''
  try {
    await accountApi.saveRules(acct.value.id, rules.value)
    orgMsg.value = { type: 'ok', text: '目录与规则已保存' }
  } catch (e) {
    orgMsg.value = { type: 'err', text: e.message }
  }
}

async function startOrganize() {
  orgMsg.value = ''
  if (!orgDirs.value.pending?.id) {
    orgMsg.value = { type: 'err', text: '请先选择等待整理目录' }
    return
  }
  if (!confirm('确认开始整理? 将扫描等待整理目录并移动文件')) return
  orgBusy.value = true
  orgResult.value = null
  try {
    orgResult.value = await organizeApi.run(acct.value.id)
  } catch (e) {
    orgMsg.value = { type: 'err', text: e.message }
  } finally {
    orgBusy.value = false
  }
}

// ---- 重命名规则(5 段模板) ----
const RENAME_FIELDS = [
  { key: 'movie_folder', label: '电影文件夹命名规则', sample: 'movie_folder' },
  { key: 'movie_file', label: '电影文件命名规则', sample: 'movie_file' },
  { key: 'tv_folder', label: '剧集文件夹命名规则', sample: 'tv_folder' },
  { key: 'season_folder', label: '季文件夹命名规则', sample: 'season_folder' },
  { key: 'episode_file', label: '集文件命名规则', sample: 'episode_file' },
]
const RENAME_DEFAULTS = {
  movie_folder: '{first_letter}-{title}-{year}-[tmdb=[[tmdb_id]]]',
  movie_file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>',
  tv_folder: '{first_letter}-{title}-{year}-[tmdb=[[tmdb_id]]]',
  season_folder: 'Season {season_num:02d}',
  episode_file: '{title}.{year}.{season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>',
}
// 三种档位预设: 完整 / 常规 / 精简
const RENAME_MODES = {
  full: { label: '完整', templates: { ...RENAME_DEFAULTS } },
  normal: {
    label: '常规',
    templates: {
      movie_folder: '{title}-{year}',
      movie_file: '{title}.{year}<.{resource_pix}><.{resource_type}><-{resource_team}>',
      tv_folder: '{title}-{year}',
      season_folder: 'Season {season_num:02d}',
      episode_file: '{title}.{year}.{season_episode}<.{resource_pix}>',
    },
  },
  minimal: {
    label: '精简',
    templates: {
      movie_folder: '{title}-{year}',
      movie_file: '{title}.{year}',
      tv_folder: '{title}-{year}',
      season_folder: '{season_episode}',
      episode_file: '{title}.{season_episode}',
    },
  },
}
const renameMode = ref('full')
// 实时示例预览结果: {key: rendered}
const renamePreviews = ref({})

function switchRenameMode(mode) {
  renameMode.value = mode
  const tpls = RENAME_MODES[mode].templates
  for (const f of RENAME_FIELDS) {
    rules.value[`rename_${f.key}`] = tpls[f.key]
  }
  refreshAllPreviews()
}

const fullPreview = ref({ movieFolder: '', movieFile: '', tvFolder: '', seasonFolder: '', episodeFile: '' })
async function refreshFullPreview() {
  try {
    const [mf, mfl, tf, sf, efl] = await Promise.all([
      organizeApi.render(rules.value.rename_movie_folder, 'movie_folder'),
      organizeApi.render(rules.value.rename_movie_file, 'movie_file'),
      organizeApi.render(rules.value.rename_tv_folder, 'tv_folder'),
      organizeApi.render(rules.value.rename_season_folder, 'season_folder'),
      organizeApi.render(rules.value.rename_episode_file, 'episode_file'),
    ])
    fullPreview.value = {
      movieFolder: mf.rendered, movieFile: mfl.rendered,
      tvFolder: tf.rendered, seasonFolder: sf.rendered, episodeFile: efl.rendered,
    }
  } catch { /* 忽略 */ }
}

let renderTimer = null
async function refreshPreview(key) {
  const sample = RENAME_FIELDS.find((f) => f.key === key)?.sample
  try {
    const data = await organizeApi.render(rules.value[`rename_${key}`], sample)
    renamePreviews.value[key] = data.rendered
  } catch { renamePreviews.value[key] = '' }
  refreshFullPreview()
}
function refreshAllPreviews() {
  for (const f of RENAME_FIELDS) refreshPreview(f.key)
}
function onTemplateInput(key) {
  clearTimeout(renderTimer)
  renderTimer = setTimeout(() => refreshPreview(key), 300)  // 防抖
}

// ---- 规则配置(识别/AI/重命名/分类) ----
const rules = ref({
  min_video_size_mb: 0,
  blacklist: [],        // string[] 关键词
  custom_words: [],     // "原始|替换"
  custom_matches: [],   // "关键词|tmdb_id|movie/tv"
  release_groups: [],
  rename_template: '',
  rename_movie_folder: RENAME_DEFAULTS.movie_folder,
  rename_movie_file: RENAME_DEFAULTS.movie_file,
  rename_tv_folder: RENAME_DEFAULTS.tv_folder,
  rename_season_folder: RENAME_DEFAULTS.season_folder,
  rename_episode_file: RENAME_DEFAULTS.episode_file,
  organize_dirs: {},
  category_rules: [],
  category_yaml: '',
  ai: { enabled: false, mode: 'off', api_base: '', api_key: '', model: '' },
})
const rulesBusy = ref(false)
// 每 tab 独立的保存提示(一个页面对应一个)
const tabMsg = ref({ identify: '', ai: '', rename: '', category: '', organize: '' })

// 保存当前 tab 的字段子集(拉取最新规则合并, 不影响其他 tab 数据)
async function saveRules(key, fields) {
  if (!acct.value.id) return
  rulesBusy.value = true
  tabMsg.value[key] = ''
  try {
    if (key === 'category') rules.value.category_yaml = categoryYaml.value
    const cur = await accountApi.rules(acct.value.id)
    const merged = { ...(cur.rules || {}) }
    for (const f of fields) merged[f] = rules.value[f]
    await accountApi.saveRules(acct.value.id, merged)
    tabMsg.value[key] = { type: 'ok', text: '规则已保存' }
  } catch (e) {
    tabMsg.value[key] = { type: 'err', text: e.message }
  } finally {
    rulesBusy.value = false
  }
}

// textarea 自适应高度(回车增高)
function autoGrow(e) {
  const el = e.target
  el.style.height = 'auto'
  el.style.height = `${el.scrollHeight}px`
}

// ---- 二级分类策略 YAML ----
const categoryYaml = ref('')
const CATEGORY_YAML_DEFAULT = `# 配置说明:
# 1. movie/tv 为固定一级分类; 二级名称即目录名, 按顺序匹配, 匹配后建立二级目录
# 2. 条件: original_language 语种 / origin_country|production_countries 地区 /
#    genre_ids 内容类型 / release_year 年份(YYYY 或 YYYY-YYYY) / TMDB 其它一级字段
# 3. 多项条件需同时满足; 一个条件多个值用逗号分隔; 前缀 ! 表示排除该值
# 4. 无任何条件的分类为兜底项(如 外语电影/未分类)

movie:
  动画电影:
    genre_ids: '16'
  华语电影:
    original_language: 'zh,cn,bo,za'
  外语电影:

tv:
  国漫:
    genre_ids: '16'
    origin_country: 'CN,TW,HK'
  日番:
    genre_ids: '16'
    origin_country: 'JP'
  纪录片:
    genre_ids: '99'
  儿童:
    genre_ids: '10762'
  综艺:
    genre_ids: '10764,10767'
  国产剧:
    origin_country: 'CN,TW,HK'
  欧美剧:
    origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU,UK'
  日韩剧:
    origin_country: 'JP,KP,KR,TH,IN,SG'
  未分类:
`
function loadCategorySample() {
  categoryYaml.value = CATEGORY_YAML_DEFAULT
}

function resetCategory() {
  if (!confirm('确定重置为默认分类策略? 当前内容将被覆盖')) return
  categoryYaml.value = CATEGORY_YAML_DEFAULT
  tabMsg.value.category = { type: 'ok', text: '已重置为默认策略(记得保存)' }
}

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
      rename_template: r.rename_template || '',
      rename_movie_folder: r.rename_movie_folder || RENAME_DEFAULTS.movie_folder,
      rename_movie_file: r.rename_movie_file || RENAME_DEFAULTS.movie_file,
      rename_tv_folder: r.rename_tv_folder || RENAME_DEFAULTS.tv_folder,
      rename_season_folder: r.rename_season_folder || RENAME_DEFAULTS.season_folder,
      rename_episode_file: r.rename_episode_file || RENAME_DEFAULTS.episode_file,
      organize_dirs: r.organize_dirs || {},
      category_rules: r.category_rules || [],
      category_yaml: r.category_yaml || '',
      ai: { enabled: false, api_base: '', api_key: '', model: '', ...(r.ai || {}) },
    }
    // 兼容旧版 enabled 布尔 -> mode
    if (!rules.value.ai.mode) {
      rules.value.ai.mode = rules.value.ai.enabled ? 'assist' : 'off'
    }
    categoryYaml.value = r.category_yaml || ''
    orgDirs.value = {
      pending: { ...(r.organize_dirs?.pending || {}) },
      existing: { ...(r.organize_dirs?.existing || {}) },
      redundant: { ...(r.organize_dirs?.redundant || {}) },
    }
  } catch { /* 忽略 */ }
}



function addCategoryRule() {
  rules.value.category_rules.push({ kind: 'movie', match: '', target: '' })
}
function delCategoryRule(i) {
  rules.value.category_rules.splice(i, 1)
}

watch(acct, (a) => { if (a.id) loadRules() })

watch(accTab, (t) => {
  if (t === 'rename') {
    refreshAllPreviews()   // 进入 tab 直接显示示例
    refreshFullPreview()
  }
})

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

    <!-- 已登录: 账户管理卡(八 tab) -->
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
            <div v-if="acct.status === 'expired'" class="msg err" style="margin: 4px 0 0">
              凭据已过期, 请点击下方「重新扫码登录」更新登录状态
            </div>
            <div class="muted" v-if="acct.info?.nickname && acct.info.nickname !== acct.name">昵称: {{ acct.info.nickname }}</div>
            <div class="muted" v-if="acct.info?.device">登录设备: {{ deviceLabel(acct.info.device) }}</div>
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
        <p class="muted" style="margin-top: 0">选择三个目录: 点击"选择"浏览网盘目录(无需填 cid)。开始整理后, 扫描等待整理目录并识别分类。</p>
        <div v-for="f in ORG_FIELDS" :key="f.key" class="org-dir-row">
          <div class="org-dir-label">
            <span>{{ f.label }}</span>
            <span class="help" :data-tip="f.hint">?</span>
          </div>
          <input class="org-dir-value" :class="{ 'muted': !orgDirs[f.key]?.id }"
                 readonly :value="orgDirs[f.key]?.name || '点击选择目录...'"
                 @click="openPicker(f.key)" />
          <button v-if="orgDirs[f.key]?.id" class="danger" @click="orgDirs[f.key] = {}">清除</button>
        </div>
        <div class="row" style="margin-top: 12px">
          <button class="primary" @click="saveRules('organize', ['organize_dirs'])">保存目录</button>
          <button class="primary" :disabled="orgBusy" @click="startOrganize">
            {{ orgBusy ? '整理中...' : '开始整理' }}
          </button>
          <div v-if="tabMsg.organize" class="msg" :class="tabMsg.organize.type">{{ tabMsg.organize.text }}</div>
        </div>
        <div v-if="orgResult" class="org-result" style="margin-top: 12px">
          <div class="row">
            <span class="badge ok">整理成功 {{ orgResult.counts?.ok || 0 }}</span>
            <span class="badge" style="color: var(--warn)">已存在 {{ orgResult.counts?.existing || 0 }}</span>
            <span class="badge err">冗余 {{ orgResult.counts?.redundant || 0 }}</span>
          </div>
          <table style="margin-top: 8px">
            <tr><th>文件</th><th>结果</th></tr>
            <tr v-for="(it, i) in orgResult.ok" :key="'ok' + i">
              <td class="muted">{{ it.name }}</td><td><code>{{ it.target }}</code></td>
            </tr>
            <tr v-for="(it, i) in orgResult.existing" :key="'ex' + i">
              <td class="muted">{{ it.name }}</td><td>已存在 → {{ it.reason }}</td>
            </tr>
            <tr v-for="(it, i) in orgResult.redundant" :key="'rd' + i">
              <td class="muted">{{ it.name }}</td><td class="err">{{ it.reason }}</td>
            </tr>
          </table>
        </div>
      </template>

      <!-- 识别规则 -->
      <template v-else-if="accTab === 'identify'">
        <h2 style="margin-top: 0">识别规则</h2>
        <div>
          <label>最小视频大小(MB)<span class="help" data-tip="低于此大小的视频文件不纳入整理识别; 填写 0 表示不限制">?</span></label>
          <input type="number" min="0" v-model.number="rules.min_video_size_mb" class="rules-input" style="max-width: 320px" />
        </div>
        <div>
          <label>发布组扩展<span class="help" data-tip="追加识别发布组; 逗号分隔多个, 如 FRDS, NEWCINE">?</span></label>
          <input v-model="releaseGroupsText" class="rules-input" placeholder="例如: FRDS, 蓝光组, NEWCINE" />
        </div>
        <div>
          <label>整理黑名单<span class="help" data-tip="命中关键词的文件跳过整理; 一行是一条规则, 如 trailer / sample">?</span></label>
          <textarea v-model="blacklistText" class="rules-input" rows="4" @input="autoGrow" placeholder="例如: trailer&#10;sample&#10;xxx" />
        </div>
        <div>
          <label>自定义识别词<span class="help" data-tip="识别时将原始词替换为替换词; 一行是一条规则, 格式: 原始词|替换词, 如 蜘蛛侠3|Spider-Man 3">?</span></label>
          <textarea v-model="customWordsText" class="rules-input" rows="3" @input="autoGrow" placeholder="例如: 蜘蛛侠3|Spider-Man 3&#10;SW|Star Wars" />
        </div>
        <div>
          <label>自定义匹配<span class="help" data-tip="文件名命中关键词时直接指定为对应作品; 一行是一条规则, 格式: 关键词|TMDB_ID|movie或tv">?</span></label>
          <textarea v-model="customMatchesText" class="rules-input" rows="3" @input="autoGrow" placeholder="例如: 星际穿越|157336|movie&#10;三体|457433|tv" />
        </div>
        <div class="row" style="margin-top: 12px">
          <button class="primary" :disabled="rulesBusy" @click="saveRules('identify', ['min_video_size_mb', 'blacklist', 'custom_words', 'custom_matches', 'release_groups'])">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="tabMsg.identify" class="msg" :class="tabMsg.identify.type">{{ tabMsg.identify.text }}</div>
        </div>
      </template>

      <!-- AI 辅助 -->
      <template v-else-if="accTab === 'ai'">
        <h2 style="margin-top: 0">AI 辅助识别</h2>
        <p class="muted" style="margin-top: 0">使用大模型辅助识别文件名(OpenAI 兼容接口)。</p>
        <div class="row" style="margin-bottom: 12px">
          <button class="ai-mode" :class="{ on: rules.ai.mode === 'off' }" @click="rules.ai.mode = 'off'">禁用</button>
          <button class="ai-mode" :class="{ on: rules.ai.mode === 'assist' }" @click="rules.ai.mode = 'assist'">辅助识别</button>
          <button class="ai-mode" :class="{ on: rules.ai.mode === 'force' }" @click="rules.ai.mode = 'force'">强制识别</button>
        </div>
        <p class="muted" style="margin-top: -6px">
          <template v-if="rules.ai.mode === 'off'">不使用 AI, 仅用内置识别规则。</template>
          <template v-else-if="rules.ai.mode === 'assist'">内置识别结果不准确时, 使用 AI 辅助识别文件名。</template>
          <template v-else>不使用内置识别规则, 直接由 AI 识别文件名或目录名。</template>
        </p>
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
          <button class="primary" :disabled="rulesBusy" @click="saveRules('ai', ['ai'])">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="tabMsg.ai" class="msg" :class="tabMsg.ai.type">{{ tabMsg.ai.text }}</div>
        </div>
      </template>

      <!-- 重命名规则(5 段模板) -->
      <template v-else-if="accTab === 'rename'">
        <h2 style="margin-top: 0">重命名规则</h2>
        <div class="row" style="margin-bottom: 14px">
          <span class="muted" style="margin-right: 6px">模板方式:</span>
          <button v-for="(m, key) in RENAME_MODES" :key="key" class="ai-mode"
                  :class="{ on: renameMode === key }" @click="switchRenameMode(key)">
            {{ m.label }}
          </button>
          <span class="help" data-tip="完整: 全变量模板(推荐); 常规: 常用变量; 精简: 最简文件名; 切换后仍可手动修改任意模板">?</span>
        </div>
        <div v-for="f in RENAME_FIELDS" :key="f.key" style="margin-bottom: 14px">
          <label>{{ f.label }}</label>
          <input v-model="rules[`rename_${f.key}`]" @input="onTemplateInput(f.key)" />
          <div class="rename-preview" v-if="renamePreviews[f.key]">
            示例: <code>{{ renamePreviews[f.key] }}</code>
          </div>
        </div>
        <div class="full-preview">
          <h3 style="font-size: 14px; margin: 10px 0 6px">🎬 电影完整示例(目录结构)</h3>
          <pre class="tree-example">📁 {{ fullPreview.movieFolder || '...' }}/
   └ {{ fullPreview.movieFile || '...' }}</pre>
          <h3 style="font-size: 14px; margin: 10px 0 6px">📺 剧集完整示例(目录结构)</h3>
          <pre class="tree-example">📁 {{ fullPreview.tvFolder || '...' }}/
   📁 {{ fullPreview.seasonFolder || '...' }}/
      └ {{ fullPreview.episodeFile || '...' }}</pre>
          <p class="muted" style="margin-top: 8px">变量与语法详见"变量说明"/"语法说明" tab(<code>&lt;...&gt;</code> 块 / <code>[[ ]]</code> 转义)。</p>
        </div>
        <div class="row" style="margin-top: 12px">
          <button class="primary" :disabled="rulesBusy" @click="saveRules('rename', ['rename_movie_folder', 'rename_movie_file', 'rename_tv_folder', 'rename_season_folder', 'rename_episode_file'])">{{ rulesBusy ? '保存中...' : '保存规则' }}</button>
          <div v-if="tabMsg.rename" class="msg" :class="tabMsg.rename.type">{{ tabMsg.rename.text }}</div>
        </div>
      </template>

      <!-- 二级分类策略(YAML) -->
      <template v-else-if="accTab === 'category'">
        <h2 style="margin-top: 0">二级分类策略</h2>
        <p class="muted" style="margin-top: 0">YAML 方式配置(优先级从上到下): 分类名 → 目标目录 cid(115 用 cid, 123 用 cid123); 整理时按 TMDB 类型/地区匹配分类并<strong>自动创建目录结构</strong>。</p>
        <textarea v-model="categoryYaml" rows="18" class="yaml-editor"
                  placeholder="movie:&#10;  动画电影:&#10;    genre_ids: '16'&#10;  ...&#10;tv:&#10;  ..." />
        <p class="muted" style="margin-top: 6px">字段: <code>cid</code> 115 目标目录 · <code>cid123</code> 123 目标目录 · <code>genre_ids</code> 类型 · <code>origin_country</code>/<code>production_countries</code> 地区 · <code>original_language</code> 语种 · <code>release_year</code> 年份(支持 YYYY-YYYY); 多值逗号分隔, <code>!值</code> 排除; 无条件的分类为兜底项。</p>
        <div class="row" style="margin-top: 12px">
          <button class="primary" :disabled="rulesBusy" @click="saveRules('category', ['category_yaml'])">{{ rulesBusy ? '保存中...' : '保存策略' }}</button>
          <button @click="resetCategory">重置策略</button>
          <button @click="loadCategorySample">加载示例</button>
          <div v-if="tabMsg.category" class="msg" :class="tabMsg.category.type">{{ tabMsg.category.text }}</div>
        </div>
      </template>

      <!-- 变量说明 -->
      <template v-else-if="accTab === 'vars'">
        <h2 style="margin-top: 0">变量说明</h2>
        <p class="muted" style="margin-top: 0">重命名规则中可用的变量(识别结果)。</p>
        <table class="doc-table">
          <tr><th>变量名</th><th>说明</th><th>示例值</th></tr>
          <tr v-for="(row, i) in VAR_DOCS" :key="i">
            <td><code>{{ row[0] }}</code></td>
            <td>{{ row[1] }}</td>
            <td class="muted">{{ row[2] }}</td>
          </tr>
        </table>
      </template>

      <!-- 语法说明 -->
      <template v-else-if="accTab === 'syntax'">
        <h2 style="margin-top: 0">语法说明</h2>
        <table class="doc-table" style="margin-bottom: 12px">
          <tr><th>语法</th><th>说明</th></tr>
          <tr v-for="(row, i) in SYNTAX_DOCS" :key="i">
            <td><code>{{ row[0] }}</code></td>
            <td>{{ row[1] }}</td>
          </tr>
        </table>
        <h3 style="font-size: 14px; margin: 10px 0 6px">示例</h3>
        <p v-for="(ex, i) in SYNTAX_EXAMPLES" :key="i" class="doc-example">
          <code>{{ ex[0] }}</code><br />
          <span class="muted">{{ ex[1] }}</span>
        </p>
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
            <div class="muted" v-if="a.info.device">登录设备: {{ deviceLabel(a.info.device) }}</div>
          </template>
          <span v-else class="muted">-</span>
        </td>
        <td><span class="badge" :class="a.status === 'ok' ? 'ok' : 'err'">{{ statusLabel(a.status) }}</span></td>
        <td><button class="danger" @click="remove(a.id)">删除</button></td>
      </tr>
      <tr v-if="!filtered.length"><td colspan="6" class="muted">暂无账户</td></tr>
    </table>
  </div>

  <!-- 目录树选择弹窗 -->
  <div v-if="picker" class="modal-mask" @click.self="closePicker">
    <div class="modal" style="width: 420px">
      <h2 style="margin-top: 0">选择目录 — {{ ORG_FIELDS.find((f) => f.key === picker.field)?.label }}</h2>
      <div class="picker-path">
        <button @click="pickerBack" :disabled="!picker.stack.length">← 返回</button>
        <span class="muted" style="margin-left: 8px">{{ picker.current.name || '根目录' }}</span>
      </div>
      <div v-if="pickerErr" class="msg err">{{ pickerErr }}</div>
      <div class="picker-list">
        <div v-if="!pickerBusy && !picker.dirs.length" class="muted" style="padding: 10px">无子目录</div>
        <div v-if="!pickerBusy && picker.diagnose" class="picker-diag">
          诊断(rows={{ picker.diagnose.rows }}, 文件={{ picker.diagnose.all_files?.length || 0 }}):
          <pre>{{ JSON.stringify(picker.diagnose, null, 1) }}</pre>
        </div>
        <button v-for="d in picker.dirs" :key="d.id" class="picker-dir" @click="enterDir(d)">📁 {{ d.name }}</button>
      </div>
      <div class="row" style="justify-content: space-between; margin-top: 10px">
        <button class="primary" @click="selectThisDir">选择当前目录</button>
        <button @click="closePicker">取消</button>
      </div>
    </div>
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
