const API = '';

// ==================== 工具 ====================
async function api(path, options = {}) {
  const token = localStorage.getItem('token');
  const res = await fetch(API + '/api' + path, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: 'Bearer ' + token } : {}),
      ...options.headers,
    },
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.message || data.error || '请求失败');
  return data;
}

function toast(msg) {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    el.style.cssText = 'position:fixed;top:20px;left:50%;transform:translateX(-50%);background:#1d2129;color:#fff;padding:8px 20px;border-radius:4px;font-size:14px;z-index:2000;box-shadow:0 2px 8px rgba(0,0,0,.2);transition:opacity .3s';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.style.opacity = '1';
  clearTimeout(el._t);
  el._t = setTimeout(() => { el.style.opacity = '0'; }, 2000);
}

// ==================== 页面标题映射 ====================
const PAGE_TITLES = {
  'sync': ['115 账号同步', '全量 / 增量 / 分享同步'],
  'organize': ['自动整理', '基础配置 / 识别规则 / 分类策略 / 洗版 / 重命名'],
  'monitor-upload': ['上传下载', '上传 emby 生成的媒体图片 / 转存下载'],
  'upload-download': ['上传下载', '监控上传 / 转存下载'],
  'transfer': ['上传下载', '监控上传 / 转存下载'],
  'config-accounts': ['账号管理', '管理各云盘账号配置'],
  'config-system': ['系统配置', 'STRM / TMDB / 代理 / EMBY 配置'],
  'config-message': ['消息配置', '企业微信与 TG 机器人'],
  'config-extension': ['扩展功能', 'ALIST 同步等插件'],
};

function showPage(id) {
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.menu-item').forEach(n => n.classList.remove('active'));
  const page = document.getElementById('page-' + id);
  const nav = document.querySelector(`.menu-item[data-page="${id}"]`);
  if (page) page.classList.add('active');
  if (nav) nav.classList.add('active');
  const title = PAGE_TITLES[id] || [id, ''];
  document.getElementById('page-title').textContent = title[0];
  document.getElementById('page-desc').textContent = title[1];
  const mobileTitle = document.getElementById('mobile-page-title');
  if (mobileTitle) mobileTitle.textContent = title[0];
  // 记住当前页面，刷新后恢复
  localStorage.setItem('current-page', id);
  // 移动端切页面后关闭侧边栏
  if (window.innerWidth <= 768) closeSidebar();
  // 加载对应数据
  if (id === 'config-system') {
    loadTmdb();
    loadConfigs();
    updateStrmExample();
  }
  if (id === 'config-accounts') loadAccount();
  if (id === 'organize') {
    loadConfigs();
    loadCategory();
    loadWash();
  }
  if (id === 'sync') loadConfigs();
  // 恢复上次停留的 Tab（所有含 tab 的页面通用）
  const savedTab = localStorage.getItem('current-tab-page-' + id);
  if (savedTab) switchTab('page-' + id, savedTab);
}

// ==================== 移动端侧边栏 ====================
function toggleSidebar(forceOpen) {
  const sidebar = document.querySelector('.sidebar');
  const overlay = document.getElementById('sidebar-overlay');
  const isOpen = sidebar.classList.contains('open');
  if (forceOpen === true || !isOpen) {
    sidebar.classList.add('open');
    overlay.classList.add('show');
  } else {
    closeSidebar();
  }
}
function closeSidebar() {
  document.querySelector('.sidebar')?.classList.remove('open');
  document.getElementById('sidebar-overlay')?.classList.remove('show');
}

// Tab 切换（通用，作用于指定页面容器）
function switchTab(pageId, tabName) {
  const page = document.getElementById(pageId);
  if (!page) return;
  page.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  page.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));
  const tab = page.querySelector(`.tab[data-tab="${tabName}"]`);
  const panel = page.querySelector(`.tab-panel[data-panel="${tabName}"]`);
  if (tab) tab.classList.add('active');
  if (panel) {
    panel.classList.add('active');
    // 元素可见后重新计算自适应高度
    panel.querySelectorAll('textarea.auto-resize').forEach(autoResizeTextarea);
  }
  // 记住当前 Tab（所有页面通用）
  localStorage.setItem('current-tab-' + pageId, tabName);
}

// ==================== 认证 ====================
async function checkAuth() {
  try {
    const data = await api('/auth/status');
    if (!data.initialized) {
      showAuth('register');
    } else if (!localStorage.getItem('token')) {
      showAuth('login');
    } else {
      showMain();
    }
  } catch (e) {
    showAuth('login');
  }
}

function showAuth(mode) {
  document.getElementById('auth-page').style.display = 'flex';
  document.getElementById('main-app').style.display = 'none';
  document.getElementById('auth-mode').value = mode;
  const isReg = mode === 'register';
  document.getElementById('auth-heading').textContent = isReg ? '首次部署，注册管理员' : '请输入账号密码登录';
  document.getElementById('auth-submit').textContent = isReg ? '注册' : '登录';
  document.getElementById('auth-confirm-row').style.display = isReg ? 'block' : 'none';
}

function showMain() {
  document.getElementById('auth-page').style.display = 'none';
  document.getElementById('main-app').style.display = 'flex';
  // 恢复上次停留的页面
  const saved = localStorage.getItem('current-page');
  if (saved) showPage(saved);
}

async function handleAuth(e) {
  e.preventDefault();
  const mode = document.getElementById('auth-mode').value;
  const username = document.getElementById('auth-username').value;
  const password = document.getElementById('auth-password').value;
  if (!username || !password) { toast('请输入账号和密码'); return; }
  if (mode === 'register') {
    const confirm = document.getElementById('auth-confirm').value;
    if (password !== confirm) { toast('两次密码不一致'); return; }
  }
  try {
    const data = await api('/auth/' + mode, { method: 'POST', body: JSON.stringify({ username, password }) });
    localStorage.setItem('token', data.token);
    localStorage.setItem('username', data.username);
    showMain();
    toast(mode === 'register' ? '注册成功' : '登录成功');
  } catch (e) { toast(e.message); }
}

// ==================== TMDB 配置 ====================
let tmdbLangVal = 'zh-CN';
function setTmdbLang(val) {
  tmdbLangVal = val;
  document.querySelectorAll('#tmdb-language-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
}

async function loadTmdb() {
  try {
    const data = await api('/config/tmdb');
    const c = data.data || data;
    document.getElementById('tmdb-api-url').value = c.api_url || 'https://api.tmdb.org';
    document.getElementById('tmdb-image-url').value = c.image_api_url || 'https://image.tmdb.org';
    document.getElementById('tmdb-api-key').value = c.api_key || '';
    setTmdbLang(c.language || 'zh-CN');
  } catch (e) {}
}

async function saveTmdb() {
  try {
    await api('/config/tmdb', { method: 'POST', body: JSON.stringify({
      api_url: document.getElementById('tmdb-api-url').value,
      image_api_url: document.getElementById('tmdb-image-url').value,
      api_key: document.getElementById('tmdb-api-key').value,
      language: tmdbLangVal,
    }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// ==================== 二级分类 / 洗版 ====================
async function loadCategory() {
  try {
    const data = await api('/scrape/categories');
    if (data.config) {
      document.getElementById('category-yaml').value = data.config;
    }
  } catch (e) {}
}

async function saveCategory() {
  const yaml = document.getElementById('category-yaml').value;
  try {
    await api('/scrape/categories', { method: 'POST', body: JSON.stringify({ yaml }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

const DEFAULT_CATEGORY_YAML = `# 配置电影的分类策略
# 分类名即 115 目录名（支持多级，用 / 分隔）
# 整理后路径：{已存在目录}/{分类名}/{重命名文件}
movie:
  # 如已存在目录为「影视库」，整理结果为：影视库/电影/动画电影/xxx
  电影/动画电影:
    # 匹配 genre_ids 内容类型，16是动漫
    genre_ids: '16'
  电影/华语电影:
    # 匹配语种
    original_language: 'zh,cn,bo,za'
  # 未匹配以上条件时，返回最后一个
  电影/外语电影:

# 配置电视剧的分类策略
tv:
  电视剧/国漫:
    genre_ids: '16'
    origin_country: 'CN,TW,HK'
  电视剧/日番:
    genre_ids: '16'
    origin_country: 'JP'
  电视剧/国产剧:
    origin_country: 'CN,TW,HK'
  电视剧/欧美剧:
    origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU'
  电视剧/日韩剧:
    origin_country: 'JP,KP,KR,TH,IN,SG'
  # 未匹配以上分类，则命名为未分类
  电视剧/未分类:

# 配置AV的分类策略（按番号前缀或制作商分类）
av:
  无码:
    # 匹配番号前缀（无码番号通常含这些前缀）
    num_prefix: 'ABC,DEF'
  有码:
    # 兜底分类
    num_prefix: ''
  # 未匹配以上分类
  未分类:`;

function resetCategory(btn) {
  resetConfig('category', btn);
}

async function loadWash() {
  try {
    const data = await api('/scrape/wash');
    if (data.config) {
      document.getElementById('wash-yaml').value = data.config;
    }
  } catch (e) {}
}
async function saveWash() {
  const yaml = document.getElementById('wash-yaml').value;
  try {
    await api('/scrape/wash', { method: 'POST', body: JSON.stringify({ yaml }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// ==================== 后缀标签输入 ====================
function addTag(e) {
  if (e.key !== 'Enter') return;
  e.preventDefault();
  const containerId = e.target.dataset.tags;
  const container = document.getElementById(containerId);
  const val = e.target.value.trim().replace(/^\.+/, '').toLowerCase();
  if (!val) return;
  if (container.querySelector(`.tag[data-val="${val}"]`)) { e.target.value = ''; return; }
  const tag = document.createElement('span');
  tag.className = 'tag';
  tag.dataset.val = val;
  tag.innerHTML = val + '<span class="tag-close" onclick="removeTag(this)">×</span>';
  container.insertBefore(tag, e.target);
  e.target.value = '';
}
function removeTag(el) {
  el.closest('.tag').remove();
}
function getTags(containerId) {
  return Array.from(document.querySelectorAll(`#${containerId} .tag`)).map(t => t.dataset.val);
}

// ==================== 目录选择器 ====================
let dirPicker = { mode: '115', cid: '0', path: '', history: [] };
let dirPickerTarget = 'full-cid'; // 选择后回填的输入框 id

function showDirPicker(title) {
  document.getElementById('dir-picker-title').textContent = title;
  document.getElementById('dir-picker-modal').style.display = 'flex';
}
function closeDirPicker() {
  document.getElementById('dir-picker-modal').style.display = 'none';
}

function open115DirPicker(targetId) {
  dirPickerTarget = targetId || 'full-cid';
  dirPicker = { mode: '115', cid: '0', path: '', history: [] };
  showDirPicker('选择 115 目录');
  load115Dirs('0');
}
function openLocalDirPicker(targetId) {
  dirPickerTarget = targetId || 'full-local';
  dirPicker = { mode: 'local', cid: '0', path: '', history: [] };
  showDirPicker('选择本地目录');
  loadLocalDirs('');
}

async function load115Dirs(cid, pushHistory) {
  const list = document.getElementById('dir-picker-list');
  list.innerHTML = '<div class="dir-empty">加载中...</div>';
  try {
    if (pushHistory && dirPicker.cid !== cid) dirPicker.history.push(dirPicker.cid);
    const data = await api('/storage/115/dirs?cid=' + encodeURIComponent(cid));
    dirPicker.cid = cid;
    document.getElementById('dir-picker-path').textContent = (cid === '0' || cid === '') ? '根目录' : 'cid: ' + cid;
    const items = data.data || [];
    let note = '';
    if (!items.length && data.count > 0) {
      note = '目录共有 ' + data.count + ' 个条目，但没有识别到文件夹（通道: ' + (data.channel || '?') + '，来源: ' + (data.origin || '?') + '）';
    }
    renderDirList(items, cid, note);
  } catch (e) {
    list.innerHTML = '<div class="dir-empty">' + (e.message || '加载失败') + '</div>';
  }
}
async function loadLocalDirs(path) {
  const list = document.getElementById('dir-picker-list');
  list.innerHTML = '<div class="dir-empty">加载中...</div>';
  try {
    const data = await api('/storage/local/dirs?path=' + encodeURIComponent(path));
    dirPicker.path = path;
    document.getElementById('dir-picker-path').textContent = path || '计算机';
    const note = data.truncated ? '目录较大，仅显示前 1000 个文件夹，可直接在上方输入路径' : '';
    renderDirList(data.data || [], path, note);
  } catch (e) {
    list.innerHTML = '<div class="dir-empty">' + (e.message || '加载失败') + '</div>';
  }
}

// 手动输入路径/cid 直接跳转
function dirPickerJump() {
  const input = document.getElementById('dir-picker-input');
  const v = (input.value || '').trim();
  if (!v) return;
  if (dirPicker.mode === '115') {
    const cid = v.replace(/\D/g, '');
    load115Dirs(cid || '0', true);
  } else {
    loadLocalDirs(v);
  }
}

function renderDirList(items, current, note) {
  const list = document.getElementById('dir-picker-list');
  if (!items.length) {
    list.innerHTML = '<div class="dir-empty">' + (note || '该目录下没有子文件夹') + '</div>';
    return;
  }
  const noteHtml = note ? '<div class="dir-empty" style="opacity:.7">' + note + '</div>' : '';
  list.innerHTML = noteHtml + items.map((it, i) => {
    const name = it.name || it.path;
    return `<div class="dir-item" data-index="${i}"><span class="dir-icon">▸</span><span>${name}</span></div>`;
  }).join('');
  list.querySelectorAll('.dir-item').forEach(el => {
    el.addEventListener('click', () => {
      const it = items[parseInt(el.dataset.index)];
      if (dirPicker.mode === '115') {
        load115Dirs(it.cid, true);
      } else {
        loadLocalDirs(it.path);
      }
    });
  });
}

function dirPickerUp() {
  if (dirPicker.mode === '115') {
    const prev = dirPicker.history.pop();
    load115Dirs(prev !== undefined ? prev : '0');
  } else {
    loadLocalDirs(parentPath(dirPicker.path || ''));
  }
}

function parentPath(p) {
  p = p.replace(/[\\/]+$/, '');
  const idx = Math.max(p.lastIndexOf('\\'), p.lastIndexOf('/'));
  if (idx <= 0) return '';
  return p.slice(0, idx + 1);
}

function confirmDirPicker() {
  const target = document.getElementById(dirPickerTarget);
  if (dirPicker.mode === '115') {
    if (target) target.value = (dirPicker.cid === '0' || dirPicker.cid === '') ? '' : dirPicker.cid;
  } else {
    if (target) target.value = dirPicker.path || '/media';
  }
  closeDirPicker();
}

// ==================== 全量同步 ====================
async function startFullSync() {
  const cid = document.getElementById('full-cid').value;
  if (!cid) { toast('请先选择或填写 115 媒体库 cid'); return; }
  const videoExt = getTags('video-ext');
  if (!videoExt.length) { toast('请至少保留一个视频文件后缀'); return; }
  try {
    appendLog('开始全量同步（视频生成 STRM，图片/字幕/NFO 落盘）...');
    const data = await api('/sync/full', { method: 'POST', body: JSON.stringify({
      cid: cid,
      local_path: document.getElementById('full-local').value,
      video_ext: videoExt,
      image_ext: getTags('image-ext'),
      data_ext: getTags('data-ext'),
    }) });
    toast(data.message || '全量同步完成');
    appendLog(`全量同步完成：视频 ${data.total} 个（生成 STRM ${data.created}），附属文件 ${data.assets_total} 个（下载 ${data.assets_downloaded}，跳过 ${data.assets_skipped}，失败 ${data.assets_failed}）`);
  } catch (e) { toast(e.message); }
}

// ==================== 同步记录 ====================
async function loadRecords() {
  try {
    const data = await api('/strm');
    const tbody = document.getElementById('record-tbody');
    if (data.data && data.data.length) {
      tbody.innerHTML = data.data.slice(0, 30).map(f => `<tr>
        <td><input type="checkbox"></td><td>${f.local_path || '-'}</td><td>文件</td><td>${f.remote_path || '-'}</td><td>-</td>
        <td><button class="btn btn-outline btn-sm">删除</button></td></tr>`).join('');
    } else {
      tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--text-3)">暂无同步记录</td></tr>';
    }
  } catch (e) {}
}

// ==================== 转存 / 订阅（占位） ====================
function switchUDTab(tab) {
  document.querySelectorAll('#page-upload-download .tab').forEach(t => t.classList.toggle('active', t.dataset.tab === tab));
  document.querySelectorAll('#page-upload-download .tab-panel').forEach(p => p.classList.toggle('active', p.dataset.panel === tab));
}
function transfer() { toast('转存功能开发中'); }
function testProxy() { toast('代理延迟测试中...'); }
function genDirTree() { toast('目录树生成工具开发中'); }

// ==================== 115 扫码登录 ====================
let qrcodeTimer = null;
let qrPollApi = '/storage/qrcode';  // 当前轮询接口（OpenAPI 启用时切换）
async function openQrCode() {
  document.getElementById('qrcode-modal').style.display = 'flex';
  document.getElementById('qrcode-img').innerHTML = '二维码加载中...';
  // OPENAPI 启用且填了 AppID → 走开放平台授权；否则走 Cookie 扫码
  const appid = (document.getElementById('acc-appid') || {}).value || '';
  if (openapiEnabled && appid.trim()) {
    qrPollApi = '/storage/open/qrcode';
    document.getElementById('qrcode-status').textContent = '正在获取开放平台授权二维码...';
    try {
      const data = await api('/storage/open/qrcode', { method: 'POST', body: JSON.stringify({ type: '115', app_id: appid.trim() }) });
      if (data.qrcode) {
        document.getElementById('qrcode-img').innerHTML = `<img src="${data.qrcode}" style="width:170px;height:170px">`;
        document.getElementById('qrcode-status').textContent = '请使用 115 手机 App 扫码（开放平台授权）';
        startQrCodePolling(data.uid, data.time, data.sign);
      } else {
        document.getElementById('qrcode-img').innerHTML = '获取二维码失败';
        document.getElementById('qrcode-status').textContent = data.error || '请稍后重试';
      }
    } catch (e) {
      document.getElementById('qrcode-img').innerHTML = '<div style="padding:40px 0;color:var(--text-3)">二维码获取失败</div>';
      document.getElementById('qrcode-status').textContent = e.message || '请稍后重试';
    }
    return;
  }
  qrPollApi = '/storage/qrcode';
  document.getElementById('qrcode-status').textContent = '正在获取登录二维码...';
  try {
    const device = document.getElementById('acc-device').value;
    const data = await api('/storage/qrcode', { method: 'POST', body: JSON.stringify({ type: '115', device }) });
    if (data.qrcode) {
      document.getElementById('qrcode-img').innerHTML = `<img src="${data.qrcode}" style="width:170px;height:170px">`;
      document.getElementById('qrcode-status').textContent = '请使用 115 手机 App 扫描二维码';
      // 开始轮询扫码状态
      startQrCodePolling(data.uid, data.time, data.sign);
    } else {
      document.getElementById('qrcode-img').innerHTML = '获取二维码失败';
      document.getElementById('qrcode-status').textContent = data.error || '请稍后重试';
    }
  } catch (e) {
    document.getElementById('qrcode-img').innerHTML = '<div style="padding:40px 0;color:var(--text-3)">二维码获取失败</div>';
    document.getElementById('qrcode-status').textContent = e.message || '请稍后重试';
  }
}

function startQrCodePolling(uid, time, sign) {
  const session = {};          // 本次轮询的会话令牌
  qrcodeTimer = session;       // 记录当前会话，closeQrCode 会置空
  let errCount = 0;            // 连续失败计数，用于指数退避
  (async function poll() {
    if (qrcodeTimer !== session) return;   // 已关闭或新会话
    try {
      const data = await api(qrPollApi + '/status', { method: 'POST', body: JSON.stringify({ uid, time, sign }) });
      if (qrcodeTimer !== session) return;
      errCount = 0;  // 成功则重置退避
      const status = data.status;
      if (status === 'scanned') {
        document.getElementById('qrcode-status').textContent = '已扫码，请在手机上确认登录...';
        poll();
      } else if (status === 'success') {
        document.getElementById('qrcode-status').textContent = '登录成功！';
        // 更新账号信息显示
        const box = document.getElementById('acc-status-box');
        box.style.display = 'block';
        document.getElementById('acc-username').textContent = data.username || '-';
        document.getElementById('acc-capacity').textContent = '已绑定';
        if (data.warning) {
          // 设备槽位不匹配等可用性警告，提示用户改用网页端重新扫码
          document.getElementById('qrcode-status').textContent = '警告：' + data.warning;
          setTimeout(() => { closeQrCode(); toast('扫码设备类型不配套，请改用「115浏览器_网页端」重新扫码'); }, 3500);
        } else {
          setTimeout(() => { closeQrCode(); toast('115 账号绑定成功'); }, 1000);
        }
      } else if (status === 'expired' || status === 'cancelled') {
        document.getElementById('qrcode-status').textContent = status === 'expired' ? '二维码已过期，请重新获取' : '已取消登录';
      } else {
        poll();  // waiting，继续长轮询
      }
    } catch (e) {
      if (qrcodeTimer !== session) return;
      // 网络故障（fetch 抛 TypeError）才退避重试；服务端返回的错误（如 115 拒绝登录/IP风控）
      // 属于确定性失败，重试只会加重风控，直接展示并停止
      if (e instanceof TypeError) {
        errCount++;
        const delay = Math.min(30000, 1000 * Math.pow(2, errCount - 1));
        document.getElementById('qrcode-status').textContent = '网络波动，' + (delay / 1000) + ' 秒后重试...';
        setTimeout(poll, delay);
      } else {
        qrcodeTimer = null;  // 停止轮询
        document.getElementById('qrcode-status').textContent = e.message || '登录失败';
      }
    }
  })();
}

function closeQrCode() {
  qrcodeTimer = null;  // 停止轮询
  document.getElementById('qrcode-modal').style.display = 'none';
}

let openapiEnabled = false;
function setOpenapi(val) {
  openapiEnabled = val;
  document.querySelectorAll('#openapi-switch .seg-item').forEach(el => {
    el.classList.toggle('active', String(el.dataset.value) === String(val));
  });
}

function checkCookie() {
  // 后端使用扫码登录后保存的 Cookie 进行检测，无需 cookie 文件路径
  toast('正在检测 Cookie 可用性...');
  api('/storage/check', { method: 'POST', body: JSON.stringify({ type: '115' }) })
    .then(data => {
      if (!data.valid) {
        toast('Cookie 无效：' + (data.message || '未知原因'));
        return;
      }
      const box = document.getElementById('acc-status-box');
      box.style.display = 'block';
      document.getElementById('acc-username').textContent = data.username || '-';
      document.getElementById('acc-capacity').textContent = data.capacity || '-';
      toast('Cookie 检测成功');
    })
    .catch(e => toast('Cookie 检测失败：' + (e.message || '无效')));
}

// 手动导入 Cookie（绕过被 IP 风控的扫码登录），检测通过后后端自动保存
function importCookie() {
  const ck = document.getElementById('acc-cookie-paste').value.trim();
  if (!ck) { toast('请先粘贴 Cookie'); return; }
  if (!ck.includes('UID=') || !ck.includes('SEID=')) { toast('Cookie 格式不正确，缺少 UID/SEID 字段'); return; }
  toast('正在验证并导入 Cookie...');
  api('/storage/check', { method: 'POST', body: JSON.stringify({ type: '115', cookie: ck }) })
    .then(data => {
      if (!data.valid) { toast('导入失败：' + (data.message || 'Cookie 无效')); return; }
      const box = document.getElementById('acc-status-box');
      box.style.display = 'block';
      document.getElementById('acc-username').textContent = data.username || '-';
      document.getElementById('acc-capacity').textContent = data.capacity || '-';
      toast('Cookie 导入成功，可直接使用目录选择与同步功能');
    })
    .catch(e => toast('导入失败：' + (e.message || '无效')));
}

// 诊断 115 连接：会话/域名风控/UA 配对全矩阵
async function diagnose115() {
  toast('正在诊断 115 连接（约 15-30 秒）...');
  try {
    const data = await api('/storage/115/diagnose');
    let report = '诊断结果 (Cookie长度=' + data.cookie_len + '):\n\n';
    let anyOk = false;
    Object.entries(data.results || {}).forEach(([name, r]) => {
      report += (r.ok ? '✓' : '✗') + ' ' + name + ': ' + (r.info || '失败') + '\n';
      if (r.ok) anyOk = true;
    });
    report += '\n' + (data.hint || '');
    alert(report);
    console.log('[115诊断]', data);
  } catch (e) {
    alert('诊断失败: ' + (e.message || ''));
  }
}

async function loadAccount() {
  try {
    const data = await api('/storage');
    const acc = (data.data || []).find(s => s.type === '115');
    if (!acc) return;
    document.getElementById('acc-cookie-path').value = acc.cookie_path || '/config/115-cookies.txt';
    // 设备下拉保持默认"网页端"（App 槽位 Cookie 与 webapi 不配套），不恢复历史保存值
    document.getElementById('acc-interval').value = acc.interval || 3.0;
    document.getElementById('acc-appid').value = acc.app_id || '';
    setOpenapi(!!acc.openapi_enabled);
    if (acc.name && acc.name !== '115主号') {
      document.getElementById('acc-status-box').style.display = 'block';
      document.getElementById('acc-username').textContent = acc.name;
      document.getElementById('acc-capacity').textContent = acc.status === 'online' ? '已绑定' : '-';
    }
  } catch (e) {}
}

function resetAccount() {
  ['acc-cookie-path', 'acc-appid', 'acc-interval'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.value = '';
  });
  document.getElementById('acc-cookie-path').value = '/config/115-cookies.txt';
  document.getElementById('acc-device').value = 'web';
  document.getElementById('acc-interval').value = '3.0';
  setOpenapi(false);
  document.getElementById('acc-status-box').style.display = 'none';
  toast('配置已重置');
}

async function saveAccount() {
  try {
    await api('/storage', { method: 'POST', body: JSON.stringify({
      name: '115主号',
      type: '115',
      device: document.getElementById('acc-device').value,
      cookie_path: document.getElementById('acc-cookie-path').value,
      interval: parseFloat(document.getElementById('acc-interval').value) || 3.0,
      openapi_enabled: openapiEnabled,
      app_id: document.getElementById('acc-appid').value,
    }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// ==================== 重命名规则实时示例 ====================
const RENAME_VARS = {
  '{title}': '钢铁侠',
  '{en_title}': 'Iron Man',
  '{original_name}': '钢铁侠.2008.2160p.UHD.BluRay.mkv',
  '{year}': '2008',
  '{tmdb_id}': '1726',
  '{first_letter}': 'G',
  '{ext}': 'mkv',
  '{custom_regex_match}': '自定义',
  '{season_episode}': 'S01E01',
  '{season_num}': '1',
  '{episode_num}': '1',
  '{season_name}': '东海篇',
  '{episode_name}': '我是路飞',
  '{season_year}': '1999',
  '{disc_num}': '1',
  '{resource_pix}': '2160p',
  '{fps}': '60FPS',
  '{resource_version}': 'IMAX',
  '{resource_source}': 'NF',
  '{resource_type}': 'BluRay',
  '{resource_effect}': 'DV.HDR',
  '{video_encode}': 'H265.10bit',
  '{audio_encode}': 'TrueHD.7.1',
  '{resource_team}': 'TnT',
  '{num}': 'ABC-123',
};

function renderRenameExample(rule) {
  let s = rule || '';
  // 按变量名长度降序替换，避免 {season} 先于 {season_episode} 被误替换
  const keys = Object.keys(RENAME_VARS).sort((a, b) => b.length - a.length);
  // 第一步：处理 <...> 块语法（块内变量非空则输出块内容，否则丢弃整个块）
  // 支持 <{name}...>、<?{name}...>、<...{var}...> 等形式
  // 简化处理：遍历替换 <...> 块
  let prev;
  do {
    prev = s;
    // 匹配最内层的 <...>
    const m = s.match(/<([^<>]*)>/);
    if (!m) break;
    const blockContent = m[1];
    // 检查块内是否有非空变量
    let hasNonEmpty = false;
    let rendered = blockContent;
    for (const k of keys) {
      if (blockContent.includes(k)) {
        hasNonEmpty = true;
        rendered = rendered.split(k).join(RENAME_VARS[k]);
      }
    }
    // 块内有变量且至少一个被替换 → 输出替换后的内容；否则丢弃
    s = s.replace(m[0], hasNonEmpty ? rendered : '');
  } while (s !== prev);
  // 第二步：替换裸 {变量名}
  for (const k of keys) {
    s = s.split(k).join(RENAME_VARS[k]);
  }
  // 第三步：[[ ]] → { }
  s = s.replace(/\[\[/g, '{').replace(/\]\]/g, '}');
  return s;
}

function updateRenameExample() {
  const pairs = [
    ['rename-movie-folder', 'ex-movie-folder'],
    ['rename-movie-file', 'ex-movie-file'],
    ['rename-tv-folder', 'ex-tv-folder'],
    ['rename-tv-file', 'ex-tv-file'],
    ['rename-av-folder', 'ex-av-folder'],
    ['rename-av-file', 'ex-av-file'],
  ];
  pairs.forEach(([inputId, exId]) => {
    const input = document.getElementById(inputId);
    const ex = document.getElementById(exId);
    if (input && ex) ex.textContent = renderRenameExample(input.value);
  });
}

// 输入框自适应高度
function autoResizeTextarea(el) {
  el.style.height = 'auto';
  el.style.height = el.scrollHeight + 'px';
}

// ==================== GPT 测试连接 ====================
async function testGPT() {
  const url = val('org-gpt-url');
  const key = val('org-gpt-key');
  const model = val('org-gpt-model');
  const btn = document.getElementById('gpt-test-btn');
  const result = document.getElementById('gpt-test-result');
  if (!url || !model) { toast('请先填写 API 地址和模型名称'); return; }
  btn.disabled = true;
  btn.textContent = '测试中...';
  result.textContent = '';
  result.style.color = '';
  try {
    const data = await api('/config/test-gpt', { method: 'POST', body: JSON.stringify({ url, key, model }) });
    if (data.success) {
      result.textContent = '✓ ' + data.message;
      result.style.color = 'var(--success)';
    } else {
      result.textContent = '✗ ' + (data.error || '连接失败');
      result.style.color = 'var(--danger)';
    }
  } catch (e) {
    result.textContent = '✗ ' + e.message;
    result.style.color = 'var(--danger)';
  } finally {
    btn.disabled = false;
    btn.textContent = '测试连接';
  }
}

async function testTmdb() {
  const btn = document.getElementById('tmdb-test-btn');
  const result = document.getElementById('tmdb-test-result');
  btn.disabled = true;
  btn.textContent = '测试中...';
  result.textContent = '';
  result.style.color = '';
  try {
    const data = await api('/config/test-tmdb', { method: 'POST', body: JSON.stringify({
      api_url: val('tmdb-api-url'),
      api_key: val('tmdb-api-key'),
      language: tmdbLangVal,
    }) });
    if (data.success) {
      result.textContent = '✓ 连接成功' + (data.message ? '：' + data.message : '');
      result.style.color = 'var(--success)';
    } else {
      result.textContent = '✗ ' + (data.error || '连接失败');
      result.style.color = 'var(--danger)';
    }
  } catch (e) {
    result.textContent = '✗ ' + e.message;
    result.style.color = 'var(--danger)';
  } finally {
    btn.disabled = false;
    btn.textContent = '测试连接';
  }
}

// EMBY 入库刷新
let embyStyleVal = 'unix';
let embyEnabledVal = true;
function setEmbyStyle(val) {
  embyStyleVal = val;
  document.querySelectorAll('#emby-refresh-style-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
  testEmbyPath();
}
function setEmbyEnabled(val) {
  embyEnabledVal = val;
  document.querySelectorAll('#emby-refresh-enabled-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === String(val)));
}
function testEmbyPath() {
  const input = document.getElementById('emby-path-test-input')?.value || '';
  const rule = document.getElementById('emby-refresh-path')?.value || '';
  const out = document.getElementById('emby-path-test-output');
  if (!out) return;
  if (!input) { out.textContent = ''; return; }
  let result = input;
  if (rule && rule.includes('#')) {
    const [src, dst] = rule.split('#');
    if (src && input.startsWith(src)) result = dst + input.slice(src.length);
  }
  if (embyStyleVal === 'windows') result = result.replace(/\//g, '\\');
  out.textContent = result;
}

// 消息通知
let msgWecomEnabled = false;
let msgTgEnabled = false;
function switchMsgTab(tab) {
  document.querySelectorAll('#page-config-message .tab').forEach(t => t.classList.toggle('active', t.dataset.tab === tab));
  document.querySelectorAll('#page-config-message .tab-panel').forEach(p => p.classList.toggle('active', p.dataset.panel === tab));
}
function setMsgEnabled(type, val) {
  if (type === 'wecom') msgWecomEnabled = val;
  if (type === 'tg') msgTgEnabled = val;
  document.querySelectorAll('#msg-' + type + '-enabled-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === String(val)));
}

async function testMessage() {
  const btn = document.getElementById('msg-test-btn');
  const result = document.getElementById('msg-test-result');
  btn.disabled = true;
  btn.textContent = '发送中...';
  result.textContent = '';
  result.style.color = '';
  try {
    const data = await api('/message/test', { method: 'POST' });
    if (data.success) {
      result.textContent = '✓ 测试消息已发送';
      result.style.color = 'var(--success)';
    } else {
      result.textContent = '✗ ' + (data.error || '发送失败');
      result.style.color = 'var(--danger)';
    }
  } catch (e) {
    result.textContent = '✗ ' + e.message;
    result.style.color = 'var(--danger)';
  } finally {
    btn.disabled = false;
    btn.textContent = '测试通知';
  }
}

// ==================== 整理 ====================
async function startOrganize() {
  const btn = event?.target;
  if (btn) { btn.disabled = true; btn.textContent = '执行中...'; }
  toast('整理任务执行中...');
  appendLog('开始执行整理任务');
  try {
    const data = await api('/organize/pipeline', { method: 'POST', body: JSON.stringify({ sync_after: true }) });
    toast(data.message || '整理完成');
    if (data.steps) {
      data.steps.forEach(s => {
        const icon = s.status === '完成' ? '✓' : s.status === '失败' ? '✗' : '○';
        appendLog(`${icon} ${s.step}: ${s.message}`);
      });
    }
  } catch (e) {
    toast(e.message);
    appendLog('✗ 整理执行失败: ' + e.message);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '开始整理'; }
  }
}

// ==================== 通用配置保存/回显（STRM / 代理 / EMBY） ====================
// 读取输入框/textarea 的值（安全）
function val(id) {
  const el = document.getElementById(id);
  return el ? el.value : '';
}
function setVal(id, v) {
  const el = document.getElementById(id);
  if (el && v !== undefined && v !== null) el.value = v;
}

// STRM 分段开关
let strmFormatVal = 'pick_code_name';
let strmExistVal = 'overwrite';
let strmExtVal = true;

function updateStrmExample() {
  const ex = document.getElementById('strm-format-example');
  if (!ex) return;
  const domain = (document.getElementById('strm-domain')?.value || '').replace(/\/+$/, '');
  if (!domain) { ex.textContent = ''; return; }
  const fid = 'abc123';
  const name = '钢铁侠';
  const ext = strmExtVal ? '.mkv' : '';
  ex.textContent = strmFormatVal === 'pick_code'
    ? `${domain}/d/${fid}`
    : `${domain}/d/${fid}/${name}${ext}`;
}

function setStrmFormat(val) {
  strmFormatVal = val;
  document.querySelectorAll('#strm-format-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
  updateStrmExample();
}
function setStrmExist(val) {
  strmExistVal = val;
  document.querySelectorAll('#strm-exist-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === val));
}
function setStrmExt(val) {
  strmExtVal = val;
  document.querySelectorAll('#strm-ext-switch .seg-item').forEach(i => i.classList.toggle('active', i.dataset.value === String(val)));
  updateStrmExample();
}

function collectConfig(key) {
  if (key === 'strm') {
    return {
      domain: document.getElementById('strm-domain').value,
      format: strmFormatVal,
      exist: strmExistVal,
      keep_ext: strmExtVal,
    };
  }
  if (key === 'proxy') {
    return { url: document.getElementById('proxy-url').value };
  }
  if (key === 'emby-refresh') {
    return {
      path_rule: document.getElementById('emby-refresh-path').value,
      style: embyStyleVal,
      enabled: embyEnabledVal,
    };
  }
  if (key === 'emby-notify') {
    return { webhook: document.getElementById('emby-notify-webhook').value };
  }
  if (key === 'message') {
    return {
      wecom: { corp_id: val('msg-wecom-corp-id'), secret: val('msg-wecom-secret'), agent_id: val('msg-wecom-agent-id'), enabled: msgWecomEnabled },
      tg: { token: val('msg-tg-token'), chat_id: val('msg-tg-chat-id'), enabled: msgTgEnabled },
    };
  }
  if (key === 'org-basic') {
    return {
      pending: val('org-pending'),
      library: val('org-library'),
      existing: val('org-existing'),
      redundant: val('org-redundant'),
    };
  }
  if (key === 'org-recognize') {
    return {
      replace_rules: val('org-replace-rules'),
      min_size: val('org-min-size'),
      release_groups: val('org-release-groups'),
    };
  }
  if (key === 'org-gpt') {
    return {
      url: val('org-gpt-url'),
      key: val('org-gpt-key'),
      model: val('org-gpt-model'),
    };
  }
  if (key === 'org-rename') {
    return {
      movie_folder: val('rename-movie-folder'),
      movie_file: val('rename-movie-file'),
      tv_folder: val('rename-tv-folder'),
      tv_file: val('rename-tv-file'),
      av_folder: val('rename-av-folder'),
      av_file: val('rename-av-file'),
    };
  }
  if (key === 'full') {
    return {
      cid: val('full-cid'),
      local_path: val('full-local'),
      video_ext: getTags('video-ext'),
      image_ext: getTags('image-ext'),
      data_ext: getTags('data-ext'),
    };
  }
  if (key === 'incr') {
    return { cron: val('incr-cron') };
  }
  if (key === 'share') {
    return { folder: val('share-folder') };
  }
  if (key === 'monitor') {
    return { dir: val('monitor-dir'), target: val('monitor-target') };
  }
  return null;
}

function applyConfig(key, v) {
  if (!v) return;
  if (key === 'strm') {
    if (v.domain !== undefined) document.getElementById('strm-domain').value = v.domain;
    if (v.format) setStrmFormat(v.format);
    if (v.exist) setStrmExist(v.exist);
    if (v.keep_ext !== undefined) setStrmExt(v.keep_ext === true || v.keep_ext === 'true');
    updateStrmExample();
  } else if (key === 'proxy') {
    if (v.url !== undefined) document.getElementById('proxy-url').value = v.url;
  } else if (key === 'emby-refresh') {
    if (v.path_rule !== undefined) document.getElementById('emby-refresh-path').value = v.path_rule;
    if (v.style) setEmbyStyle(v.style === 'windows' || v.style === 'Windows风格' ? 'windows' : 'unix');
    if (v.enabled !== undefined) setEmbyEnabled(v.enabled === true || v.enabled === 'true');
    testEmbyPath();
  } else if (key === 'emby-notify') {
    if (v.webhook !== undefined) document.getElementById('emby-notify-webhook').value = v.webhook;
  } else if (key === 'message') {
    if (v.wecom) {
      setVal('msg-wecom-corp-id', v.wecom.corp_id);
      setVal('msg-wecom-secret', v.wecom.secret);
      setVal('msg-wecom-agent-id', v.wecom.agent_id);
      setMsgEnabled('wecom', v.wecom.enabled === true || v.wecom.enabled === 'true');
    }
    if (v.tg) {
      setVal('msg-tg-token', v.tg.token);
      setVal('msg-tg-chat-id', v.tg.chat_id);
      setMsgEnabled('tg', v.tg.enabled === true || v.tg.enabled === 'true');
    }
  } else if (key === 'org-basic') {
    setVal('org-pending', v.pending);
    setVal('org-library', v.library);
    setVal('org-existing', v.existing);
    setVal('org-redundant', v.redundant);
  } else if (key === 'org-recognize') {
    setVal('org-replace-rules', v.replace_rules);
    setVal('org-min-size', v.min_size);
    setVal('org-release-groups', v.release_groups);
  } else if (key === 'org-gpt') {
    setVal('org-gpt-url', v.url);
    setVal('org-gpt-key', v.key);
    setVal('org-gpt-model', v.model);
  } else if (key === 'org-rename') {
    setVal('rename-movie-folder', v.movie_folder);
    setVal('rename-movie-file', v.movie_file);
    setVal('rename-tv-folder', v.tv_folder);
    setVal('rename-tv-file', v.tv_file);
    setVal('rename-av-folder', v.av_folder);
    setVal('rename-av-file', v.av_file);
    updateRenameExample();
  } else if (key === 'full') {
    setVal('full-cid', v.cid);
    setVal('full-local', v.local_path);
    if (v.video_ext) setTags('video-ext', v.video_ext);
    if (v.image_ext) setTags('image-ext', v.image_ext);
    if (v.data_ext) setTags('data-ext', v.data_ext);
  } else if (key === 'incr') {
    setVal('incr-cron', v.cron);
  } else if (key === 'share') {
    setVal('share-folder', v.folder);
  } else if (key === 'monitor') {
    setVal('monitor-dir', v.dir);
    setVal('monitor-target', v.target);
  }
}

// setTags 用数组重建标签输入框的标签集
function setTags(containerId, values) {
  const container = document.getElementById(containerId);
  if (!container) return;
  container.querySelectorAll('.tag').forEach(t => t.remove());
  (values || []).forEach(v => {
    const tag = document.createElement('span');
    tag.className = 'tag';
    tag.dataset.val = v;
    tag.innerHTML = v + '<span class="tag-close" onclick="removeTag(this)">×</span>';
    container.insertBefore(tag, container.querySelector('.tag-add'));
  });
}

async function saveConfig(key) {
  const value = collectConfig(key);
  if (value === null) { toast('该配置暂未支持保存'); return; }
  try {
    await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(value) }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}

// 重置配置（带气泡确认）
function resetConfig(key, btn) {
  // 移除已有气泡和旧的监听器
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  // 创建气泡
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定重置此配置？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok">确定</button></div>';
  btn.appendChild(bubble);
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    doResetConfig(key, btn);
  };
  // 延迟注册点击外部关闭（跳过当前事件循环）
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// 各配置的默认值
const DEFAULT_CONFIGS = {
  'strm': { domain: '', format: 'p', keep_ext: 'true', skip_exist: 'overwrite' },
  'proxy': { url: '' },
  'emby-refresh': { url: '', api_key: '', path_replace: '', enabled: true },
  'emby-notify': { webhook: '' },
  'org-basic': { pending: '', library: '', existing: '', redundant: '' },
  'org-recognize': { replace_rules: '', release_groups: '', min_size: '0' },
  'org-gpt': { url: 'https://api.siliconflow.cn/v1', key: '', model: '' },
  'org-rename': { movie_folder: '{first_letter}/{title} ({year}) [{tmdb_id}]', movie_file: '{title} ({year}) [{tmdb_id}]{ext}', tv_folder: '{first_letter}/{title} ({year}) [{tmdb_id}]', tv_file: '{title} - S{season}E{episode}{ext}' },
  'monitor': { dir: '', target: '' },
  'message': { wecom: { corp_id: '', secret: '', agent_id: '', enabled: false }, tg: { token: '', chat_id: '', enabled: false } },
  'full': { cid: '', local_path: '/media', video_ext: ['mp4','mkv','ts','avi','mov','rmvb','webm','flv','m2ts','wmv','mpg','iso'], image_ext: ['jpg','png','jpeg','webp'], data_ext: ['ass','srt','ssa','sub'] },
  'incr': { cron: '*/10 8-23 * * *' },
  'share': { folder: '' },
  'tmdb': { api_key: '', api_url: 'https://api.tmdb.org', image_url: 'https://image.tmdb.org', language: 'zh-CN' },
};

async function doResetConfig(key, btn) {
  try {
    const defaults = DEFAULT_CONFIGS[key] || {};
    // 特殊处理：使用独立保存接口的配置直接设默认值
    if (key === 'tmdb') {
      await api('/config/tmdb', { method: 'POST', body: JSON.stringify({
        api_url: defaults.api_url, image_api_url: defaults.image_url, api_key: defaults.api_key, language: defaults.language
      }) });
      setVal('tmdb-api-url', defaults.api_url);
      setVal('tmdb-image-url', defaults.image_url);
      setVal('tmdb-api-key', defaults.api_key);
      toast('配置已恢复默认值');
      return;
    }
    if (key === 'category') {
      await api('/scrape/categories', { method: 'POST', body: JSON.stringify({ yaml: DEFAULT_CATEGORY_YAML }) });
      document.getElementById('category-yaml').value = DEFAULT_CATEGORY_YAML;
      toast('配置已恢复默认值');
      return;
    }
    if (['full', 'incr', 'share'].includes(key)) {
      // 这些配置走通用 setting 接口，直接保存默认值
      await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(defaults) }) });
    } else {
      await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(defaults) }) });
    }
    applyConfig(key, defaults);
    toast('配置已恢复默认值');
  } catch (e) { toast(e.message); }
}

function closeConfirmBubble() {
  const b = document.getElementById('confirm-bubble');
  if (b) b.remove();
}

function closeConfirmBubbleOnOutside(e) {
  // 点击气泡内部或重置按钮本身时不关闭
  if (!e.target.closest('#confirm-bubble') && !e.target.closest('[onclick*="resetConfig"]')) {
    closeConfirmBubble();
  }
}

async function loadConfigs() {
  const keys = ['strm', 'proxy', 'emby-refresh', 'emby-notify', 'org-basic', 'org-recognize', 'org-gpt', 'org-rename', 'message', 'full', 'incr', 'share', 'monitor'];
  for (const key of keys) {
    try {
      const data = await api('/config/setting?key=' + key);
      if (!data.value) continue;
      applyConfig(key, JSON.parse(data.value));
    } catch (e) {}
  }
}

// ==================== 日志 ====================
function appendLog(line) {
  const viewer = document.getElementById('log-viewer');
  const time = new Date().toLocaleTimeString();
  viewer.textContent = (viewer.textContent === '暂无日志...' ? '' : viewer.textContent) + `[${time}] ${line}\n`;
  viewer.scrollTop = viewer.scrollHeight;
}
function openLog() {
  document.getElementById('log-modal').style.display = 'flex';
  loadSystemLogs();
}
function closeLog() {
  document.getElementById('log-modal').style.display = 'none';
}
async function loadSystemLogs() {
  const viewer = document.getElementById('server-log-viewer');
  viewer.textContent = '加载中...';
  try {
    const data = await api('/system/logs');
    viewer.textContent = data.logs || '暂无日志';
    viewer.scrollTop = viewer.scrollHeight;
  } catch (e) {
    viewer.textContent = '加载失败: ' + (e.message || '');
  }
}

// ==================== 初始化 ====================
window.addEventListener('DOMContentLoaded', () => {
  document.getElementById('auth-form').addEventListener('submit', handleAuth);
  // auth-switch 已移除
  document.querySelectorAll('.menu-item').forEach(item => {
    item.addEventListener('click', () => showPage(item.dataset.page));
  });
  // Tab 切换（通用）
  document.querySelectorAll('.page .tab').forEach(tab => {
    tab.addEventListener('click', () => switchTab(tab.closest('.page').id, tab.dataset.tab));
  });
  document.getElementById('btn-log').addEventListener('click', openLog);
  const btnLogMobile = document.getElementById('btn-log-mobile');
  if (btnLogMobile) btnLogMobile.addEventListener('click', openLog);
  document.getElementById('btn-logout').addEventListener('click', () => {
    localStorage.clear();
    location.reload();
  });
  // 输入框自适应高度
  document.querySelectorAll('textarea.auto-resize').forEach(ta => {
    ta.addEventListener('input', () => autoResizeTextarea(ta));
    autoResizeTextarea(ta);
  });
  updateRenameExample();
  checkAuth();
});
