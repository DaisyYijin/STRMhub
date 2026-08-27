const API = '';

// HTML 转义（防 XSS）
function esc(s) {
  if (s === null || s === undefined) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#x27;');
}

// ==================== 工具 ====================
let authRedirecting = false; // 401 只引导一次，避免多个轮询同时触发重复跳转
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
  // 登录过期：清令牌回登录页（一次），后续调用自然静默
  if (res.status === 401) {
    if (!authRedirecting) {
      authRedirecting = true;
      localStorage.removeItem('token');
      stopLogPoll();
      showAuth('login');
      toast('登录已过期，请重新登录');
      setTimeout(() => { authRedirecting = false; }, 1000);
    }
    throw new Error('登录已过期');
  }
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
  'dashboard': ['仪表盘', '容量 / STRM / 整理 / 任务总览'],
  'config-accounts': ['账号管理', '管理各云盘账号配置'],
  'config-system': ['系统配置', 'STRM / TMDB / 代理 / EMBY 配置'],
  'config-message': ['消息配置', '企业微信与 TG 机器人'],
  'config-extension': ['扩展功能', 'ALIST 同步等插件'],
  'logs': ['实时日志', '同步与整理操作的服务端与本地日志'],
};

// ==================== 前端路由（真实路径，与后端 NoRoute 回退 index.html 配合） ====================
// 切换页面时地址栏变为 /sync、/plugins 等真实路径；直接访问/刷新/收藏/前进后退均可恢复。
const PAGE_PATHS = {
  'dashboard': '/',
  'sync': '/sync',
  'organize': '/organize',
  'upload-download': '/upload-download',
  'config-accounts': '/accounts',
  'config-system': '/settings',
  'config-message': '/message',
  'logs': '/logs',
  'config-extension': '/plugins',
};
const PATH_PAGES = Object.fromEntries(Object.entries(PAGE_PATHS).map(([p, path]) => [path, p]));

function pathToPageId(path) { return PATH_PAGES[path] || null; }

function syncRoute(id) {
  const want = PAGE_PATHS[id] || '/';
  if (location.pathname !== want) history.pushState(null, '', want);
}

// 浏览器前进/后退时按当前路径切页（showPage 内 syncRoute 不会再压栈）
window.addEventListener('popstate', () => {
  const id = pathToPageId(location.pathname);
  if (id) showPage(id);
});

function showPage(id) {
  if (id === 'logs') {
    startLogPoll();
    // 立即加载一次（不等到 interval）
    loadSystemLogs();
  } else { stopLogPoll(); }
  if (id === 'dashboard') loadDashboard();
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
  // 地址栏同步为真实路径（/sync、/plugins 等），可刷新/收藏/前进后退
  syncRoute(id);
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
  if (id === 'sync') { loadConfigs(); previewCron(); }
  if (id === 'upload-download') { loadConfigs(); }
  if (id === 'config-message') loadConfigs();
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
  // 地址栏显示 /login；但保留深链接路径（如直接访问 /plugins）以便登录后跳回目标页
  if (location.pathname === '/' || location.pathname === '/login') {
    history.replaceState(null, '', '/login');
  }
  const isReg = mode === 'register';
  document.getElementById('auth-heading').textContent = isReg ? '首次部署，注册管理员' : '请输入账号密码登录';
  document.getElementById('auth-submit').textContent = isReg ? '注册' : '登录';
  document.getElementById('auth-confirm-row').style.display = isReg ? 'block' : 'none';
}

function showMain() {
  document.getElementById('auth-page').style.display = 'none';
  document.getElementById('main-app').style.display = 'flex';
  loadVersion();
  loadLogLevel();
  // 优先按地址栏路径恢复（深链接 / 刷新），其次上次停留页面，默认仪表盘
  const fromPath = pathToPageId(location.pathname);
  const saved = localStorage.getItem('current-page');
  const target = fromPath || (saved && document.getElementById('page-' + saved) ? saved : 'dashboard');
  showPage(target);
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
      const catTa = document.getElementById('category-yaml');
      catTa.value = data.config;
      catTa._yamlRender && catTa._yamlRender();
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
      const washTa = document.getElementById('wash-yaml');
      washTa.value = data.config;
      washTa._yamlRender && washTa._yamlRender();
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

// 全量同步确认气泡（全量重建耗时长且仅手动触发）
function confirmFullSync(btn) {
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定全量同步？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok cb-danger">确定同步</button></div>';
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    pollTaskStatus();
    startFullSync();
  };
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// 增量同步确认气泡
function confirmIncrementalSync(btn) {
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定增量同步？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok cb-danger">确定同步</button></div>';
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    pollTaskStatus();
    startIncrementalSync();
  };
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// 重置整理记录（清空去重数据库，误判"已存在"时使用）
function resetOrgRecords(btn) {
  closeConfirmBubble();
  document.removeEventListener('click', closeConfirmBubbleOnOutside);
  const bubble = document.createElement('div');
  bubble.id = 'confirm-bubble';
  bubble.className = 'confirm-bubble';
  bubble.innerHTML = '<div class="cb-text">确定清空整理记录？</div><div class="cb-actions"><button class="cb-cancel">取消</button><button class="cb-ok cb-danger">确定清空</button></div>';
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
  bubble.classList.add('show');
  bubble.querySelector('.cb-cancel').onclick = (e) => { e.stopPropagation(); closeConfirmBubble(); };
  bubble.querySelector('.cb-ok').onclick = async (e) => {
    e.stopPropagation();
    closeConfirmBubble();
    try {
      const data = await api('/organize/reset-records', { method: 'POST' });
      toast(data.message || '已重置');
      appendLog('已重置整理记录: ' + (data.message || ''));
    } catch (err) { toast('重置失败: ' + err.message); }
  };
  setTimeout(() => {
    document.addEventListener('click', closeConfirmBubbleOnOutside, { once: true });
  }, 0);
}

// ==================== 增量同步 ====================
async function startIncrementalSync() {
  await resolveInputCID('full-cid');
  const cid = resolveCID('full-cid');
  if (!cid || cid === '0') { toast('无法识别 115 目录：请点「选择目录」重新选择，或直接输入纯数字 cid'); return; }
  try {
    toast('增量同步进行中（受 API 间隔限制可能持续数分钟）...');
    appendLog('开始增量同步（拉取 115 生活事件，定向同步受影响目录）...');
    const data = await api('/sync/incremental', { method: 'POST', body: JSON.stringify({
      cid: cid,
      local_path: document.getElementById('full-local').value,
      video_ext: getTags('video-ext'),
      image_ext: getTags('image-ext'),
      data_ext: getTags('data-ext'),
    }) });
    toast(data.message || '增量同步完成');
    const sm = data.summary || {};
    appendLog(`任务完成: 增量同步, 耗时 ${sm.elapsed || '-'} · 事件 ${sm.events_total}（新 ${sm.events_fresh}，媒体相关 ${sm.relevant}，结构性 ${sm.structural}：删 ${sm.deleted} 移/改 ${sm.moved}），目录 ${sm.dirs}（跳过 ${sm.dirs_skipped}），视频 ${sm.videos}（STRM ${sm.strm_created}），附属下载 ${sm.assets_downloaded}`);
  } catch (e) {
    appendLog('✗ 增量同步失败: ' + e.message);
    toast('增量同步失败：' + e.message);
  }
}

// 分享链接转存
async function receiveShare() {
  const link = document.getElementById('share-url').value.trim();
  const code = document.getElementById('share-code').value.trim();
  if (!link || !code) { toast('请填写分享链接和提取码'); return; }
  try {
    toast('转存进行中...');
    const data = await api('/share/receive', { method: 'POST', body: JSON.stringify({ url: link, code }) });
    toast(data.message || '转存完成');
    appendLog('分享转存: ' + (data.message || ''));
  } catch (e) { toast('转存失败: ' + e.message); }
}

async function testEmbyConnection() {
  const url = val('emby-server-url').trim();
  const key = val('emby-api-key').trim();
  const el = document.getElementById('emby-test-result');
  if (!url) { toast('请填写 Emby 服务器地址'); return; }
  el.textContent = '测试中...'; el.style.color = '';
  try {
    const d = await api('/config/test-emby', { method: 'POST', body: JSON.stringify({ server_url: url, api_key: key }) });
    if (d.ok) {
      el.textContent = '✓ 已连接（' + (d.server_name || 'Emby') + '，' + (d.library_count || 0) + ' 个媒体库）';
      el.style.color = 'var(--primary)';
    } else {
      el.textContent = '✗ ' + (d.error || '连接失败');
      el.style.color = '#e74c3c';
    }
  } catch (e) {
    el.textContent = '✗ ' + e.message;
    el.style.color = '#e74c3c';
  }
}

// ==================== 仪表盘 ====================
async function loadDashboard() {
  try {
    const d = await api('/dashboard');
    const p = d.pan115 || {};
    document.getElementById('dash-capacity').textContent = (p.used_h || '-') + ' / ' + (p.total_h || '-');
    const total = Number(p.total || 0), used = Number(p.used || 0);
    if (total > 0) {
      const pct = Math.min(100, Math.round(used / total * 1000) / 10);
      document.getElementById('dash-cap-bar').style.width = pct + '%';
      document.getElementById('dash-cap-text').textContent = '已用 ' + pct + '%';
    }
    const strm = d.strm || {};
    document.getElementById('dash-strm-count').textContent = strm.total || 0;
    document.getElementById('dash-strm-active').textContent = strm.active || 0;
    document.getElementById('dash-strm-invalid').textContent = strm.invalid || 0;
    document.getElementById('dash-synced').textContent = d.synced_files || 0;
    document.getElementById('dash-organized').textContent = d.organized || 0;
    document.getElementById('dash-task').textContent = d.task_running ? '⏳ 任务进行中' : '✓ 空闲';
    document.getElementById('dash-task-detail').textContent = '待处理事件 ' + (d.pending_events || 0) + ' 条';
    const recent = d.recent_media || [];
    if (recent.length > 0) {
      document.getElementById('dash-recent').innerHTML = recent.map(m =>
        `<div>★ ${esc(m.title)} (${esc(m.year || '?')}) [${esc(m.category || m.type || '-')}] <span style="color:var(--text-3);font-size:12px">${esc(m.at)}</span></div>`
      ).join('');
    } else {
      document.getElementById('dash-recent').textContent = '暂无整理记录';
    }
  } catch (e) {}
}

// ==================== STRM 管理 ====================


// Cron 未来运行时间预览（防抖 500ms）
let cronPreviewTimer = null;
let cronPreviewLast = ''; // 上次已展示的表达式（相同则跳过，避免闪烁）
async function previewCron() {
  clearTimeout(cronPreviewTimer);
  cronPreviewTimer = setTimeout(async () => {
    const expr = document.getElementById('incr-cron').value.trim();
    const el = document.getElementById('incr-cron-preview');
    if (!expr) { el.textContent = '请填写 cron 表达式'; cronPreviewLast = ''; return; }
    if (expr === cronPreviewLast) return; // 未变化不重绘
    try {
      const d = await api('/sync/cron-preview', { method: 'POST', body: JSON.stringify({ cron: expr }) });
      const times = d.next || [];
      if (times.length === 0) {
        el.textContent = '○ 未来一年内不会触发，请检查表达式';
      } else {
        el.innerHTML = times.map((t, i) => `第 ${i + 1} 次: ${esc(t)}`).join('<br>');
      }
      cronPreviewLast = expr;
    } catch (e) {
      el.textContent = '✗ ' + (e.message || '表达式无效');
      cronPreviewLast = '';
    }
  }, 500);
}

// ==================== 任务状态（同步/整理互斥提示） ====================
let taskPollTimer = null;
async function pollTaskStatus() {
  const bar = document.getElementById('task-status-bar');
  const btns = ['btn-fullsync', 'btn-incrsync', 'btn-organize'].map(id => document.getElementById(id)).filter(Boolean);
  try {
    const st = await api('/sync/status');
    if (st.running) {
      bar.style.display = 'block';
      bar.innerHTML = '⏳ ' + esc(st.task || '任务') + ' 正在执行（已运行 ' + esc(st.elapsed || '-') + '，开始于 ' + esc(st.since || '-') + '），其他同步/整理操作已暂不可用';
      btns.forEach(b => { b.disabled = true; b.style.opacity = '.5'; });
    } else {
      bar.style.display = 'none';
      btns.forEach(b => { b.disabled = false; b.style.opacity = ''; });
    }
  } catch (e) { /* 静默 */ }
}
function startTaskPoll() {
  stopTaskPoll();
  pollTaskStatus();
  taskPollTimer = setInterval(pollTaskStatus, 5000);
}
function stopTaskPoll() {
  if (taskPollTimer) { clearInterval(taskPollTimer); taskPollTimer = null; }
}

// ==================== 目录选择器 ====================
let dirPicker = { mode: '115', cid: '0', path: '', trail: [], history: [] }; // trail: 115 逐级目录名
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
  dirPicker = { mode: '115', cid: '0', path: '', trail: [], history: [] };
  showDirPicker('选择 115 目录');
  load115Dirs('0');
}
function openLocalDirPicker(targetId) {
  dirPickerTarget = targetId || 'full-local';
  dirPicker = { mode: 'local', cid: '0', path: '', history: [] };
  showDirPicker('选择本地目录');
  loadLocalDirs('');
}

// load115Dirs 加载 115 目录；opts.enter = 进入的目录名，opts.restore = 上级恢复的 {cid, trail}
async function load115Dirs(cid, opts) {
  const list = document.getElementById('dir-picker-list');
  list.innerHTML = '<div class="dir-empty">加载中...</div>';
  try {
    if (opts && opts.enter) {
      dirPicker.history.push({ cid: dirPicker.cid, trail: [...dirPicker.trail] });
      dirPicker.trail.push(opts.enter);
    } else if (opts && opts.restore) {
      dirPicker.trail = opts.restore;
    } else {
      dirPicker.trail = []; // 根目录 / 手动跳转
    }
    const data = await api('/storage/115/dirs?cid=' + encodeURIComponent(cid));
    dirPicker.cid = cid;
    document.getElementById('dir-picker-path').textContent = dirPicker.trail.length ? '/' + dirPicker.trail.join('/') : '根目录';
    const items = data.data || [];
    let note = '';
    if (!items.length && data.count > 0) {
      note = '目录共有 ' + data.count + ' 个条目，但没有识别到文件夹（通道: ' + (data.channel || '?') + '，来源: ' + (data.origin || '?') + '）';
    }
    renderDirList(items, cid, note);
  } catch (e) {
    list.innerHTML = '<div class="dir-empty">' + esc(e.message || '加载失败') + '</div>';
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
    list.innerHTML = '<div class="dir-empty">' + esc(e.message || '加载失败') + '</div>';
  }
}

// 手动输入路径/cid 直接跳转（115 支持 "/路径/跳转" 和纯数字 cid 两种写法）
async function dirPickerJump() {
  const input = document.getElementById('dir-picker-input');
  const v = (input.value || '').trim();
  if (!v) return;
  if (dirPicker.mode === '115') {
    if (/^\d+$/.test(v)) {
      load115Dirs(v, {});
      return;
    }
    // 路径写法：后端逐段解析成 cid 再跳转，并把面包屑设为该路径
    try {
      const data = await api('/storage/115/resolve?path=' + encodeURIComponent(v));
      if (data && data.cid) {
        dirPicker.trail = v.replace(/^\/+|\/+$/g, '').split('/').filter(Boolean);
        load115Dirs(data.cid, { restore: dirPicker.trail });
      }
    } catch (e) {
      document.getElementById('dir-picker-list').innerHTML =
        '<div class="dir-empty">' + esc(e.message || '路径无法解析') + '</div>';
    }
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
    return `<div class="dir-item" data-index="${i}"><span class="dir-icon">▸</span><span>${esc(name)}</span></div>`;
  }).join('');
  list.querySelectorAll('.dir-item').forEach(el => {
    el.addEventListener('click', () => {
      const it = items[parseInt(el.dataset.index)];
      if (dirPicker.mode === '115') {
        load115Dirs(it.cid, { enter: it.name });
      } else {
        loadLocalDirs(it.path);
      }
    });
  });
}

function dirPickerUp() {
  if (dirPicker.mode === '115') {
    const prev = dirPicker.history.pop();
    if (!prev) return; // 已在根目录
    load115Dirs(prev.cid, { restore: prev.trail });
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
    if (target) {
      // 输入框显示可读路径，真实 cid 存 dataset 供同步/保存使用；
      // dataset.path 记录 cid 对应的路径，用于检测用户手改路径后的 cid 失配
      target.dataset.cid = dirPicker.cid;
      target.dataset.path = dirPicker.trail.length ? '/' + dirPicker.trail.join('/') : '';
      target.value = dirPicker.trail.length ? '/' + dirPicker.trail.join('/') : '';
      target.placeholder = '根目录';
    }
  } else {
    if (target) target.value = dirPicker.path || '/media';
    // 本地目录选择即时生效：监控目录这类"选完即用"的配置自动保存，
    // 避免忘记点保存导致刷新后丢失
    if (dirPickerTarget === 'monitor-dir') {
      saveConfig('monitor').then(() => toast('监控目录已保存')).catch(() => {});
    }
  }
  closeDirPicker();
}

// resolveInputCID 把输入框里手填的路径解析成 cid（调后端逐段匹配），
// 成功后写回 dataset.cid/dataset.path。防抖后由输入事件触发，
// 也在同步/保存前主动 await 一次兜底
async function resolveInputCID(inputId) {
  const el = document.getElementById(inputId);
  if (!el) return;
  const v = (el.value || '').trim();
  if (!v || /^\d+$/.test(v)) return;
  if (el.dataset.cid && el.dataset.cid !== '0' && el.dataset.path === v) return; // cid 与路径已对上号
  try {
    const data = await api('/storage/115/resolve?path=' + encodeURIComponent(v));
    if (data && data.cid) {
      el.dataset.cid = data.cid;
      el.dataset.path = v;
    }
  } catch (e) { /* 解析失败保留原 dataset.cid，提交时由 resolveCID 拦截 */ }
}

// attachCIDResolvers 给 115 目录输入框挂防抖解析（手填路径自动换算 cid）
const CID_INPUTS = ['full-cid', 'share-folder', 'org-pending', 'org-existing', 'org-redundant'];
function attachCIDResolvers() {
  CID_INPUTS.forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    let timer = null;
    el.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(() => resolveInputCID(id), 600);
    });
  });
}

// resolveCID 取输入框对应的 115 cid。
// 优先级：纯数字输入 > 与当前路径匹配的 dataset.cid；都不满足返回 ''。
// 关键：dataset.cid 只有在记录的 dataset.path 等于当前输入值时才可信，
// 否则用户手改过路径而 cid 还是旧目录的——静默用旧 cid 会同步错目录
function resolveCID(inputId) {
  const el = document.getElementById(inputId);
  const v = (el.value || '').trim();
  if (/^\d+$/.test(v)) return v;
  if (el.dataset && el.dataset.cid && el.dataset.cid !== '0') {
    if (el.dataset.path === undefined || el.dataset.path === v) return el.dataset.cid;
  }
  return '';
}

// ==================== 全量同步 ====================
async function startFullSync() {
  // 手填路径先解析成 cid（防抖解析可能还没触发），解析不了就拒绝执行，
  // 绝不能拿旧 dataset.cid 静默同步错误的目录
  await resolveInputCID('full-cid');
  const cid = resolveCID('full-cid');
  if (!cid || cid === '0') { toast('无法识别 115 目录：请点「选择目录」重新选择，或直接输入纯数字 cid'); return; }
  const videoExt = getTags('video-ext');
  if (!videoExt.length) { toast('请至少保留一个视频文件后缀'); return; }
  try {
    toast('全量同步进行中（受 API 间隔限制可能持续数分钟）...');
    appendLog('开始全量同步（视频生成 STRM，图片/字幕/NFO 落盘）...');
    const data = await api('/sync/full', { method: 'POST', body: JSON.stringify({
      cid: cid,
      local_path: document.getElementById('full-local').value,
      video_ext: videoExt,
      image_ext: getTags('image-ext'),
      data_ext: getTags('data-ext'),
    }) });
    toast(data.message || '全量同步完成');
    appendLog(`任务完成: 全量同步 · 视频 ${data.total} 个（生成 STRM ${data.created}），附属文件 ${data.assets_total} 个（下载 ${data.assets_downloaded}，跳过 ${data.assets_skipped}，失败 ${data.assets_failed}）；详细耗时见服务端日志`);
  } catch (e) { toast(e.message); }
}

// ==================== 同步记录 ====================
async function loadRecords() {
  try {
    const data = await api('/strm?page=1&page_size=30');
    const tbody = document.getElementById('record-tbody');
    if (data.data && data.data.length) {
      tbody.innerHTML = data.data.map(f => `<tr>
        <td><input type="checkbox"></td><td>${esc(f.rel_path || '-')}</td><td>${f.kind === 'video' ? 'STRM' : '附属'}</td><td>${esc(f.pick_code || '-')}</td><td>${fmtSize(f.size)}</td>
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
let transferOrganizeVal = localStorage.getItem('transfer-organize') !== 'false';
function setTransferOrganize(v, silent) {
  transferOrganizeVal = v;
  localStorage.setItem('transfer-organize', v ? 'true' : 'false');
  if (silent) return;
  document.querySelectorAll('#transfer-organize-switch .seg-item').forEach(el => {
    el.classList.toggle('active', String(el.dataset.value) === String(v));
  });
}

async function transfer() {
  const url = document.getElementById('transfer-link').value.trim();
  if (!url) { toast('请填写链接'); return; }

  // 自动判断链接类型
  const isShare = url.includes('115.com/s/');
  const endpoint = isShare ? '/share/receive' : '/offline/add';

  // 提取码：行内输入框优先；其次从 URL 提取（?password=xxx 或 #xxx）；再试链接后附加的字段
  let code = (document.getElementById('transfer-code')?.value || '').trim();
  let cleanUrl = url;
  if (isShare) {
    const m = url.match(/[?#](?:password=)?([a-zA-Z0-9]{4,})/);
    if (m && !code) { code = m[1]; cleanUrl = url.split(/[?#]/)[0]; }
    if (!code) {
      // "链接 提取码" 同框写法
      const parts = url.split(/\s+/);
      if (parts.length >= 2) { cleanUrl = parts[0]; code = parts[1]; }
    }
    if (!code) {
      toast('115 分享链接需要提取码，请填写后重试');
      document.getElementById('transfer-code')?.focus();
      return;
    }
  }

  const body = {
    url: cleanUrl,
    code: code,
    target_cid: '',
    organize: transferOrganizeVal,
  };

  toast(isShare ? '转存进行中...' : '离线下载提交中...');
  try {
    const d = await api(endpoint, { method: 'POST', body: JSON.stringify(body) });
    toast(d.message || '完成');
    appendLog(`✓ ${isShare ? '转存' : '离线下载'}: ${d.message || ''}`);
    // 清空输入框
    document.getElementById('transfer-link').value = '';
    if (transferOrganizeVal) {
      appendLog('⏳ 自动整理+增量同步将在转存完成后触发...');
    }
  } catch (e) {
    toast('失败: ' + e.message);
    appendLog(`✗ ${isShare ? '转存' : '离线下载'} 失败: ${e.message}`);
  }
}
async function testProxy() {
  const url = document.getElementById('proxy-url').value.trim();
  if (!url) { toast('请先填写代理地址'); return; }
  const el = document.getElementById('proxy-test-result');
  if (el) el.textContent = '测试中...';
  try {
    const d = await api('/proxy/test', { method: 'POST', body: JSON.stringify({ url }) });
    if (d.ok) { if (el) { el.textContent = '✓ 延迟 ' + d.latency_ms + 'ms'; el.style.color = 'var(--primary)'; } }
    else { if (el) { el.textContent = '✗ ' + (d.error || '连接失败'); el.style.color = '#e74c3c'; } }
  } catch (e) { if (el) { el.textContent = '✗ ' + e.message; el.style.color = '#e74c3c'; } }
}
function genDirTree() { toast('目录树生成工具开发中'); }

// ==================== 115 扫码登录 ====================
let qrcodeTimer = null;
let qrPollApi = '/storage/qrcode';  // 当前轮询接口（OpenAPI 启用时切换）
let qrAutoRefresh = 0;              // 二维码过期自动刷新次数（手动打开弹窗时清零）
async function openQrCode(isAuto) {
  if (!isAuto) qrAutoRefresh = 0;
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
        setTimeout(() => { closeQrCode(); toast('115 账号绑定成功'); checkCookie(); }, 1200);
      } else if (status === 'expired' || status === 'cancelled') {
        if (status === 'expired' && qrAutoRefresh < 3) {
          // 二维码有效期约 2 分钟，过期自动换新码重试（最多 3 次），不用手动关开弹窗
          qrAutoRefresh++;
          document.getElementById('qrcode-status').textContent = '二维码已过期，正在自动刷新（第 ' + qrAutoRefresh + '/3 次），请稍候重新扫码...';
          openQrCode(true);
          return;
        }
        document.getElementById('qrcode-status').textContent = status === 'expired' ? '二维码已过期，请关闭后重新获取' : '已取消登录';
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
      updateAccCard(data);
      toast('Cookie 检测成功');
    })
    .catch(e => toast('Cookie 检测失败：' + (e.message || '无效')));
}

// 填充账号信息卡片：头像/会员/容量进度条/UID/通道
function updateAccCard(data) {
  const box = document.getElementById('acc-status-box');
  box.style.display = 'flex';
  const avatar = document.getElementById('acc-avatar');
  const ph = avatar.nextElementSibling;
  if (data.avatar) {
    avatar.src = data.avatar; avatar.style.display = ''; ph.style.display = 'none';
  } else { avatar.style.display = 'none'; ph.style.display = 'flex'; }
  document.getElementById('acc-username').textContent = data.username || '-';
  document.getElementById('acc-channel').textContent = '通道：' + (data.channel || '-');
  document.getElementById('acc-uid').textContent = data.user_id || '-';
  // 会员：0=非会员；forever=终身；expire=到期时间戳（秒）
  const badge = document.getElementById('acc-vip-badge');
  const vipText = document.getElementById('acc-vip');
  if (data.vip_forever === 1 || data.vip_forever === true) {
    badge.style.display = ''; badge.textContent = '终身VIP';
    vipText.textContent = '终身会员';
  } else if (data.vip > 0) {
    badge.style.display = ''; badge.textContent = 'VIP';
    vipText.textContent = data.vip_expire > 0 ? '会员，到期 ' + new Date(data.vip_expire * 1000).toLocaleDateString() : '会员';
  } else {
    badge.style.display = 'none';
    vipText.textContent = '非会员';
  }
  document.getElementById('acc-capacity').textContent = data.capacity || '-';
  const used = Number(data.used_size || 0), total = Number(data.total_size || 0);
  if (total > 0) {
    const pct = Math.min(100, Math.round(used / total * 1000) / 10);
    document.getElementById('acc-bar').style.width = pct + '%';
    document.getElementById('acc-bar-text').textContent = '已用 ' + pct + '%';
  }
  // 登录设备列表（可折叠，排查风控/异常登录）
  const devBox = document.getElementById('acc-devices');
  if (Array.isArray(data.devices) && data.devices.length) {
    const rows = data.devices.map(d => {
      const t = d.utime > 0 ? new Date(d.utime * 1000).toLocaleString() : '';
      return '<div style="padding:2px 0">' + (d.is_current ? '🟢 ' : '· ') +
        (d.name || d.device || '未知设备') + '　' + (d.ip || '') + (d.city ? '（' + d.city + '）' : '') +
        (t ? '　' + t : '') + '</div>';
    }).join('');
    devBox.innerHTML = '<summary style="cursor:pointer;color:var(--text-2)">登录设备（' + data.devices.length + '）</summary>' + rows;
    devBox.style.display = '';
  } else {
    devBox.style.display = 'none';
  }
}



async function loadAccount() {
  try {
    const data = await api('/storage');
    const acc = (data.data || []).find(s => s.type === '115');
    if (!acc) return;
    document.getElementById('acc-cookie-path').value = acc.cookie_path || '/config/115-cookies.txt';
    // 回显用户保存的设备选择（值不在选项中时保持默认网页端）
    const devSel = document.getElementById('acc-device');
    if (devSel && acc.device && [...devSel.options].some(o => o.value === acc.device)) devSel.value = acc.device;
    document.getElementById('acc-interval').value = acc.interval || 3.0;
    document.getElementById('acc-appid').value = acc.app_id || '';
    setOpenapi(!!acc.openapi_enabled);
    // 静默刷新账号卡片（真实容量/会员/头像）
    api('/storage/check', { method: 'POST', body: JSON.stringify({ type: '115' }) })
      .then(d => { if (d.valid) updateAccCard(d); })
      .catch(() => {});
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
let migratedEmbyEnabled = false;
let migratedEmbyStyle = false;
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
  const rule = document.getElementById('emby-path-mapping')?.value || '';
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

// Emby Webhook 接收地址：token 首次自动生成并落库，直接展示可复制地址（推送什么事件由 Emby 侧决定）
let embyNotifyToken = '';
function applyEmbyNotify(v) {
  embyNotifyToken = (v && v.token) || '';
  // 兼容旧版：token 存在旧 webhook 地址里的场景
  if (!embyNotifyToken && v && v.webhook) {
    const m = v.webhook.match(/[?&]token=([^&]+)/);
    if (m) embyNotifyToken = m[1];
  }
  if (!embyNotifyToken) {
    saveEmbyNotifyToken(genEmbyNotifyToken());
  }
  renderEmbyWebhookUrl();
}
function genEmbyNotifyToken() {
  const buf = new Uint8Array(8);
  crypto.getRandomValues(buf);
  return [...buf].map(b => b.toString(16).padStart(2, '0')).join('');
}
function saveEmbyNotifyToken(t) {
  embyNotifyToken = t;
  api('/config/setting', { method: 'POST', body: JSON.stringify({ key: 'emby-notify', value: JSON.stringify({ token: t }) }) })
    .then(() => { renderEmbyWebhookUrl(); })
    .catch(() => { toast('token 保存失败'); });
}
function renderEmbyWebhookUrl() {
  const el = document.getElementById('emby-webhook-url');
  if (el) el.textContent = location.origin + '/api/emby/webhook?token=' + embyNotifyToken;
}
function rotateEmbyWebhookToken() {
  saveEmbyNotifyToken(genEmbyNotifyToken());
  toast('已生成新 token，请到 Emby 更新回调地址');
}
function copyEmbyWebhook(btn) {
  const addr = location.origin + '/api/emby/webhook?token=' + (embyNotifyToken || '');
  navigator.clipboard?.writeText(addr).then(() => toast('已复制：' + addr)).catch(() => toast(addr));
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

async function testMessage(btn) {
  const result = btn ? btn.closest('.control')?.querySelector('.test-result') : null;
  btn.disabled = true;
  btn.textContent = '发送中...';
  if (result) { result.textContent = ''; result.style.color = ''; }
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
    const t0 = Date.now();
    const data = await api('/organize/pipeline', { method: 'POST', body: JSON.stringify({ sync_after: true }) });
    toast(data.message || '整理完成');
    if (data.steps) {
      data.steps.forEach(s => {
        const icon = s.status === '完成' ? '✓' : s.status === '失败' ? '✗' : '○';
        appendLog(`${icon} ${esc(s.step)}: ${esc(s.message)}`);
      });
    }
    // 按部汇总：一行一部
    (data.shows || []).forEach(sh => {
      appendLog(`★ 入库: ${esc(sh.title)} (${esc(sh.year || '-')} → ${esc(sh.target)}`);
    });
    // 逐项详情：识别出的标题/年份/分类/目标路径
    (data.details || []).forEach(d => {
      const icon = d.status === 'success' ? '✓' : d.status === 'exists' ? '○' : '✗';
      appendLog(`${icon} ${esc(d.file_name)} ${esc(d.message)}`);
    });
    appendLog(`任务完成: 自动整理, 耗时 ${((Date.now() - t0) / 1000).toFixed(1)} 秒`);
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
  const pickcode = 'abchrb6gnrw0hhh80';
  const name = '钢铁侠.2008.1080p.BluRay.X264.DTS-TnT.mkv';
  const id = strmExtVal ? pickcode + '.mkv' : pickcode;
  // pick_code:      /d/{pickcode}[.ext]
  // pick_code_name: /d/{pickcode}[.ext]?/{原文件名}（?/ 后的名字供播放器识别）
  ex.textContent = strmFormatVal === 'pick_code'
    ? `${domain}/d/${id}`
    : `${domain}/d/${id}?/${name}`;
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
  if (key === 'emby-notify') {
    return { token: embyNotifyToken };
  }
  if (key === 'message') {
    return {
      wecom: { corp_id: val('msg-wecom-corp-id'), secret: val('msg-wecom-secret'), agent_id: val('msg-wecom-agent-id'), api_url: val('msg-wecom-api-url'), token: val('msg-wecom-token'), encoding_aes_key: val('msg-wecom-aes-key'), enabled: msgWecomEnabled },
      tg: { token: val('msg-tg-token'), chat_id: val('msg-tg-chat-id'), enabled: msgTgEnabled },
    };
  }
  if (key === 'org-basic') {
    // 影视库不再单独配置，直接使用全量同步的媒体库
    return {
      pending: resolveCID('org-pending'),
      pending_path: val('org-pending'),
      existing: resolveCID('org-existing'),
      existing_path: val('org-existing'),
      redundant: resolveCID('org-redundant'),
      redundant_path: val('org-redundant'),
      enrich: {
        enabled: enrichOpts.enabled, mode: enrichOpts.mode,
        missing: enrichOpts.missing, conflict_low: enrichOpts.clow,
        conflict_high: enrichOpts.chigh, full_named: enrichOpts.full
      }
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
  if (key === 'emby') {
    return {
      server_url: val('emby-server-url'),
      api_key: val('emby-api-key'),
      path_mapping: val('emby-path-mapping'),
      style: embyStyleVal,
      refresh_enabled: embyEnabledVal,
    };
  }
  if (key === 'full') {
    return {
      cid: resolveCID('full-cid'),
      path: val('full-cid'),
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
    return { folder: resolveCID('share-folder'), folder_path: val('share-folder') };
  }
  if (key === 'monitor') {
    return { dir: val('monitor-dir') }; // 目标固定用全量同步的媒体库（target 字段已废弃）
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
  } else if (key === 'emby-notify') {
    applyEmbyNotify(v);
  } else if (key === 'message') {
    if (v.wecom) {
      setVal('msg-wecom-corp-id', v.wecom.corp_id);
      setVal('msg-wecom-secret', v.wecom.secret);
      setVal('msg-wecom-agent-id', v.wecom.agent_id);
      setVal('msg-wecom-api-url', v.wecom.api_url || 'https://qyapi.weixin.qq.com');
      setVal('msg-wecom-token', v.wecom.token);
      setVal('msg-wecom-aes-key', v.wecom.encoding_aes_key);
      setMsgEnabled('wecom', v.wecom.enabled === true || v.wecom.enabled === 'true');
    }
    if (v.tg) {
      setVal('msg-tg-token', v.tg.token);
      setVal('msg-tg-chat-id', v.tg.chat_id);
      setMsgEnabled('tg', v.tg.enabled === true || v.tg.enabled === 'true');
    }
	} else if (key === 'org-basic') {
    // 输入框显示可读路径，cid 存 dataset（兼容旧数据：值本身是数字 cid）；
    // 同步记录 dataset.path，用于检测用户手改路径后的 cid 失配
    const pairs = [['org-pending', 'pending'], ['org-existing', 'existing'], ['org-redundant', 'redundant']];
    pairs.forEach(([id, k]) => {
      const el = document.getElementById(id);
      if (!el) return;
      el.dataset.cid = v[k] || '';
      const shown = v[k + '_path'] !== undefined ? v[k + '_path'] : (v[k] || '');
      el.dataset.path = shown;
      setVal(id, shown);
    });
    if (v.enrich) loadEnrichOpts(v.enrich);
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
  } else if (key === 'emby') {
    setVal('emby-server-url', v.server_url);
    setVal('emby-api-key', v.api_key);
    setVal('emby-path-mapping', v.path_mapping);
    if (v.style) {
      setEmbyStyle(v.style === 'windows' || v.style === 'Windows风格' ? 'windows' : 'unix');
      migratedEmbyStyle = true; // 新版已有值时不再被旧 emby-refresh.style 覆盖
    }
    if (v.refresh_enabled !== undefined) {
      setEmbyEnabled(v.refresh_enabled === true || v.refresh_enabled === 'true');
      migratedEmbyEnabled = true; // 新版已有值时不再被旧 emby-refresh.enabled 覆盖
    }
    testEmbyPath();
	} else if (key === 'full') {
    const cidEl = document.getElementById('full-cid');
    if (cidEl) {
      cidEl.dataset.cid = v.cid || '';
      const shown = v.path !== undefined ? v.path : (v.cid || '');
      cidEl.dataset.path = shown;
      setVal('full-cid', shown);
    }
    setVal('full-local', v.local_path);
    if (v.video_ext) setTags('video-ext', v.video_ext);
    if (v.image_ext) setTags('image-ext', v.image_ext);
    if (v.data_ext) setTags('data-ext', v.data_ext);
  } else if (key === 'incr') {
    setVal('incr-cron', v.cron);
	} else if (key === 'share') {
    const el = document.getElementById('share-folder');
    if (el) {
      el.dataset.cid = v.folder || '';
      const shown = v.folder_path !== undefined ? v.folder_path : (v.folder || '');
      el.dataset.path = shown;
      setVal('share-folder', shown);
    }
  } else if (key === 'monitor') {
    setVal('monitor-dir', v.dir);
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

// warnDirOverlap 检查路径与媒体库目录的包含关系（相等或互为前缀），
// 提示用户自动整理可能误处理库内文件（如把库里的分类目录搬进冗余）
function dirOverlapWarn(a, b) {
  const norm = p => (p || '').trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase();
  const na = norm(a), nb = norm(b);
  if (!na || !nb || na === '/') return false;
  return na === nb || na.startsWith(nb + '/') || nb.startsWith(na + '/');
}
function warnShareDirOverlap() {
  const lib = document.getElementById('full-cid');
  const share = document.getElementById('share-folder');
  if (lib && share && dirOverlapWarn(lib.value, share.value)) {
    toast('⚠ 转存目录与媒体库目录存在包含关系，自动整理会跳过以防误搬库内容，请修正目录配置');
    appendLog('⚠ 转存目录与媒体库存在包含关系：' + share.value + ' vs ' + lib.value);
  }
}
function warnPendingDirOverlap() {
  const lib = document.getElementById('full-cid');
  const pending = document.getElementById('org-pending');
  // 待整理目录本就允许建在媒体库内部（常见布局），只警告"覆盖整个库"的危险方向
  if (lib && pending && pending.value) {
    const norm = p => (p || '').trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase();
    const np = norm(pending.value), nl = norm(lib.value);
    if (np && nl && np !== '/' && (np === nl || nl.startsWith(np + '/'))) {
      toast('⚠ 待整理目录覆盖到媒体库，库内条目会被跳过（不会被误整理），建议待整理与媒体库平级或建在库内');
    }
  }
}

// firstUnresolvedCID 返回第一个"有输入文本但解析不出 cid"的输入框 id（全解析成功返回 ''）
function firstUnresolvedCID(ids) {
  for (const id of ids) {
    const el = document.getElementById(id);
    if (!el) continue;
    if ((el.value || '').trim() && !resolveCID(id)) return id;
  }
  return '';
}

async function saveConfig(key) {
  // 含 115 目录的配置：先把手填路径解析成 cid；解析失败直接拦截，
  // 避免存入空 cid 或与显示路径不符的旧 cid
  let cidFields = [];
  if (key === 'full') cidFields = ['full-cid'];
  if (key === 'share') cidFields = ['share-folder'];
  if (key === 'org-basic') cidFields = ['org-pending', 'org-existing', 'org-redundant'];
  if (cidFields.length) {
    await Promise.all(cidFields.map(resolveInputCID));
    if (firstUnresolvedCID(cidFields)) {
      toast('目录路径无法识别：请点「选择目录」重新选择，或输入纯数字 cid');
      return;
    }
  }
  const value = collectConfig(key);
  if (value === null) { toast('该配置暂未支持保存'); return; }
  try {
    await api('/config/setting', { method: 'POST', body: JSON.stringify({ key, value: JSON.stringify(value) }) });
    toast('保存成功');
    if (key === 'share') warnShareDirOverlap();
    if (key === 'org-basic') warnPendingDirOverlap();
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
  document.body.appendChild(bubble);
  // fixed 定位到按钮上方
  const rect = btn.getBoundingClientRect();
  bubble.style.position = 'fixed';
  bubble.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  bubble.style.left = 'auto';
  bubble.style.bottom = (window.innerHeight - rect.top + 8) + 'px';
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
  'emby': { server_url: '', api_key: '', path_mapping: '', style: 'unix', refresh_enabled: true },
  'strm': { domain: '', format: 'p', keep_ext: 'true', skip_exist: 'overwrite' },
  'proxy': { url: '' },
  'emby-notify': { token: '' },
  'org-basic': { pending: '', pending_path: '', existing: '', existing_path: '', redundant: '', redundant_path: '' },
  'org-recognize': { replace_rules: '', release_groups: '', min_size: '0' },
  'org-gpt': { url: 'https://api.siliconflow.cn/v1', key: '', model: '' },
  'org-rename': {
    movie_folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
    movie_file: '{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    tv_folder: '{first_letter}-{title}-{year}-[tmdb={tmdb_id}]',
    tv_file: '{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}',
    av_folder: '{first_letter}-{title}',
    av_file: '{title}{ext}',
  },
  'monitor': { dir: '', target: '' },
  'message': { wecom: { corp_id: '', secret: '', agent_id: '', api_url: 'https://qyapi.weixin.qq.com', enabled: false }, tg: { token: '', chat_id: '', enabled: false } },
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
      const rstTa = document.getElementById('category-yaml');
      rstTa.value = DEFAULT_CATEGORY_YAML;
      rstTa._yamlRender && rstTa._yamlRender();
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
  migratedEmbyEnabled = false;
  migratedEmbyStyle = false;
  const keys = ['emby', 'full', 'strm', 'proxy', 'emby-notify', 'org-basic', 'org-recognize', 'org-gpt', 'org-rename', 'message', 'incr', 'share', 'monitor'];
  // 并行拉取，避免逐个等待导致 cid 等字段迟迟不回填
  await Promise.all(keys.map(async (key) => {
    try {
      const data = await api('/config/setting?key=' + key);
      if (!data.value) return;
      applyConfig(key, JSON.parse(data.value));
    } catch (e) {}
  }));
  // emby-notify 可能尚无配置（首次使用），走同一逻辑生成 token 并展示地址
  if (!document.getElementById('emby-webhook-url')?.textContent) applyEmbyNotify({});
  // 旧版 emby-refresh 配置迁移：在 emby 配置应用完成后统一回填，避免并行竞态
  try {
    const data = await api('/config/setting?key=emby-refresh');
    if (data.value) {
      const v = JSON.parse(data.value);
      const mapEl = document.getElementById('emby-path-mapping');
      if (v.path_rule && mapEl && !mapEl.value.trim()) mapEl.value = v.path_rule;
      if (v.style && !migratedEmbyStyle) setEmbyStyle(v.style === 'windows' || v.style === 'Windows风格' ? 'windows' : 'unix');
      if (v.enabled !== undefined && !migratedEmbyEnabled) setEmbyEnabled(v.enabled === true || v.enabled === 'true');
      testEmbyPath();
    }
  } catch (e) {}
}

// ==================== 媒体信息补全配置 ====================
const enrichOpts = { enabled: false, mode: 'standard', missing: 'rename', clow: 'rename', chigh: 'rename', full: 'keep' };
function setEnrichOpt(key, v) {
  enrichOpts[key] = v;
  const map = { enabled: 'enrich-enabled-switch', mode: 'enrich-mode-switch', missing: 'enrich-missing-switch', clow: 'enrich-clow-switch', chigh: 'enrich-chigh-switch', full: 'enrich-full-switch' };
  document.querySelectorAll('#' + map[key] + ' .seg-item').forEach(el => {
    el.classList.toggle('active', el.dataset.value === String(v));
  });
}
function loadEnrichOpts(v) {
  if (!v) return;
  if (v.enabled !== undefined) setEnrichOpt('enabled', v.enabled === true || v.enabled === 'true');
  ['mode', 'missing', 'clow', 'chigh', 'full'].forEach(k => { if (v[k]) setEnrichOpt(k, v[k]); });
}
// org-basic 保存时附带补全配置
const _origCollectOrgBasic = null;

// ==================== 插件：一键创建 Emby 媒体库 ====================
let embyLibItems = [];
function renderEmbyLibTable(data, box, withCreateButton) {
  const existsCount = data.data.length - embyLibItems.length;
  const rows = data.data.map(x => `<tr>
    <td>${esc(x.name)}</td>
    <td>${x.type_label}</td>
    <td style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(x.emby_path)}">${esc(x.emby_path)}</td>
    <td>${x.exists ? '<span style="color:#f0ad4e">已存在</span>' : '<span style="color:#27ae60">将创建</span>'}</td>
  </tr>`).join('');
  box.innerHTML = `
    <table>
      <thead><tr><th>库名</th><th>类型</th><th>Emby 路径</th><th>状态</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    <div style="margin-top:8px;display:flex;align-items:center;gap:10px">
      ${withCreateButton ? `<button class="btn btn-primary btn-sm" ${embyLibItems.length ? '' : 'disabled'} onclick="embyLibCreate()">创建 ${embyLibItems.length} 个库</button>` : ''}
      <span style="color:var(--text-3)">${existsCount ? existsCount + ' 个已存在将跳过' : ''}</span>
    </div>`;
  const mbtn = document.getElementById('emby-lib-create-btn');
  if (mbtn) { mbtn.disabled = !embyLibItems.length; mbtn.textContent = `创建 ${embyLibItems.length} 个库`; }
}
// kind: 'modal'（配置规则弹窗）| 'panel'（立即运行结果面板）
async function embyLibScanTo(kind) {
  const box = document.getElementById(kind === 'modal' ? 'emby-lib-modal-body' : 'emby-lib-result');
  box.innerHTML = '扫描中...';
  try {
    const data = await api('/plugin/emby-libraries');
    if (!data.emby_configured) {
      box.innerHTML = '<span style="color:#e74c3c">未配置 Emby 服务器，请先在「系统配置 → EMBY 管理」填写地址与 API 密钥</span>';
      return false;
    }
    if (!data.data || !data.data.length) {
      box.innerHTML = '未在媒体库根目录下发现分类目录（需要 根/分类/子目录 或 根/分类 结构）';
      return false;
    }
    embyLibItems = data.data.filter(x => !x.exists);
    renderEmbyLibTable(data, box, kind !== 'modal');
    return true;
  } catch (e) {
    box.innerHTML = '<span style="color:#e74c3c">' + esc(e.message) + '</span>';
    return false;
  }
}
function embyLibConfig() {
  document.getElementById('emby-lib-modal').style.display = 'flex';
  embyLibScanTo('modal');
}
function embyLibScanAgain() { embyLibScanTo('modal'); }
function closeEmbyLibModal() { document.getElementById('emby-lib-modal').style.display = 'none'; }
async function embyLibPreview() {
  document.getElementById('emby-lib-result').style.display = 'block';
  await embyLibScanTo('panel');
}
async function embyLibRun() {
  const box = document.getElementById('emby-lib-result');
  const btn = document.getElementById('emby-lib-run-btn');
  box.style.display = 'block';
  box.innerHTML = '扫描中...';
  if (btn) btn.disabled = true;
  try {
    const ok = await embyLibScanTo('panel');
    if (!ok) return;
    if (!embyLibItems.length) { toast('没有需要创建的媒体库，全部已存在'); return; }
    if (!confirm(`将创建 ${embyLibItems.length} 个 Emby 媒体库（${embyLibItems.map(x => x.name).join('、')}），确认执行？`)) return;
    await embyLibCreate();
  } catch (e) {
    box.innerHTML = '<span style="color:#e74c3c">' + esc(e.message) + '</span>';
  } finally {
    if (btn) btn.disabled = false;
  }
}
async function embyLibCreate() {
  const inModal = document.getElementById('emby-lib-modal').style.display !== 'none';
  const target = document.getElementById(inModal ? 'emby-lib-modal-body' : 'emby-lib-result');
  target.innerHTML = '创建中...';
  const mbtn = document.getElementById('emby-lib-create-btn');
  if (mbtn) mbtn.disabled = true;
  try {
    const data = await api('/plugin/emby-libraries', { method: 'POST', body: JSON.stringify({ items: embyLibItems }) });
    toast(data.message || '完成');
  } catch (e) {
    target.innerHTML = '<span style="color:#e74c3c">' + esc(e.message) + '</span>';
    return;
  }
  setTimeout(() => embyLibScanTo(inModal ? 'modal' : 'panel'), 600);
}

// ==================== 媒体信息补全 ====================
async function enrichScan(btn) {
  if (btn) btn.disabled = true;
  try {
    const data = await api('/enrich/scan', { method: 'POST' });
    toast(`扫描完成：共 ${data.total_videos} 个视频，${data.queued} 个缺画质信息已入队探测`);
  } catch (e) { toast(e.message); }
  if (btn) btn.disabled = false;
}

// ==================== 115 离线任务面板 ====================
function fmtSize(n) {
  if (!n || n <= 0) return '-';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + u[i];
}


// ==================== YAML 编辑器高亮（透明文本域 + 着色层） ====================
function escHtml(t) {
  return String(t).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// 值部分着色：字符串/数字/布尔/行内注释
function hlYamlValue(v) {
  if (v === '') return '';
  if (/^\s*#/.test(v)) return '<span class="y-c">' + escHtml(v) + '</span>';
  const hashIdx = v.search(/\s#/);
  let val = v, comment = '';
  if (hashIdx > 0 && !/["']/.test(v.slice(0, hashIdx))) {
    val = v.slice(0, hashIdx);
    comment = '<span class="y-c">' + escHtml(v.slice(hashIdx)) + '</span>';
  }
  let vh;
  if (/^\s*["']/.test(val)) vh = '<span class="y-s">' + escHtml(val) + '</span>';
  else if (/^\s*(true|false|null)\s*$/i.test(val)) vh = '<span class="y-n">' + escHtml(val) + '</span>';
  else if (/^\s*-?\d+(\.\d+)?\s*$/.test(val)) vh = '<span class="y-n">' + escHtml(val) + '</span>';
  else vh = escHtml(val);
  return vh + comment;
}

function hlYAML(src) {
  return src.split('\n').map(line => {
    if (/^\s*#/.test(line)) return '<span class="y-c">' + escHtml(line) + '</span>';
    // 键: 值（支持中文键、带引号键、列表项前缀 "- "）
    let m = line.match(/^(\s*)(-\s+)?("[^"]*"|[^:#\s][^:]*?)(\s*:)(.*)$/);
    if (m) {
      return escHtml(m[1]) +
        (m[2] ? '<span class="y-d">-</span>' + escHtml(m[2].slice(1)) : '') +
        '<span class="y-k">' + escHtml(m[3]) + '</span>' +
        '<span class="y-p">' + escHtml(m[4]) + '</span>' +
        hlYamlValue(m[5]);
    }
    // 纯列表项（无键值）
    m = line.match(/^(\s*)(-)(\s+)(\S.*)$/);
    if (m) {
      return escHtml(m[1]) + '<span class="y-d">-</span>' + escHtml(m[3]) + hlYamlValue(' ' + m[4]).replace(/^ /, '');
    }
    return escHtml(line);
  }).join('\n');
}

// 把已有 textarea 升级为高亮编辑器：包一层 wrap，加着色 pre，输入/滚动同步
function attachYamlHighlight(id) {
  const ta = document.getElementById(id);
  if (!ta || ta.dataset.yamlHl) return;
  const wrap = document.createElement('div');
  wrap.className = 'yaml-wrap';
  ta.parentNode.insertBefore(wrap, ta);
  const pre = document.createElement('pre');
  pre.className = 'yaml-hl';
  const code = document.createElement('code');
  pre.appendChild(code);
  wrap.appendChild(pre);
  wrap.appendChild(ta);
  ta.classList.add('yaml-ta');
  const render = () => { code.innerHTML = hlYAML(ta.value) + '\n'; };
  const sync = () => { pre.scrollTop = ta.scrollTop; pre.scrollLeft = ta.scrollLeft; };
  ta.addEventListener('input', render);
  ta.addEventListener('scroll', sync);
  ta._yamlRender = () => { render(); sync(); };
  ta.dataset.yamlHl = '1';
  render();
}

// ==================== 孤儿 STRM 清理 ====================
async function cleanupOrphanStrm() {
  try {
    const dry = await api('/strm/cleanup', { method: 'POST', body: JSON.stringify({ confirm: false }) });
    if (!dry.count) { toast('没有发现孤儿文件'); return; }
    const sample = (dry.sample || []).slice(0, 5).join('\n');
    if (!confirm(`发现 ${dry.count} 个孤儿文件（云端已删/移走的本地残留）\n\n${sample}${dry.count > 5 ? '\n...' : ''}\n\n确定删除？`)) return;
    const res = await api('/strm/cleanup', { method: 'POST', body: JSON.stringify({ confirm: true }) });
    toast(`已删除 ${res.deleted} 个孤儿文件`);
    appendLog && appendLog(`孤儿清理: 删除 ${res.deleted} 个文件`);
  } catch (e) { toast(e.message); }
}

// ==================== 日志级别 ====================
let logLevelVal = 'verbose';
function setLogLevel(v) {
  logLevelVal = v;
  document.querySelectorAll('#log-level-switch .seg-item').forEach(el => {
    el.classList.toggle('active', el.dataset.value === v);
  });
}
async function saveLogLevel() {
  try {
    await api('/system/log-level', { method: 'POST', body: JSON.stringify({ level: logLevelVal }) });
    toast('保存成功');
  } catch (e) { toast(e.message); }
}
async function loadLogLevel() {
  try {
    const data = await api('/config/setting?key=log-level');
    const v = data.value || 'verbose';
    setLogLevel(v === 'simple' ? 'simple' : 'verbose');
  } catch (e) {}
}

// 版本号（左下角 footer 显示）+ 刷新时自动检查 GitHub 是否有新版本
async function loadVersion() {
  let local = '';
  try {
    const data = await api('/version');
    local = String(data.version || '');
  } catch (e) {}
  const el = document.getElementById('footer-version');
  if (el && local) {
    el.textContent = 'StrmHub v' + local.slice(0, 7);
    // 版本号可点击：随时手动检查更新（不依赖"有新版本"提示出现）
    el.style.cursor = 'pointer';
    el.title = '点击检查更新';
    el.onclick = openUpdateModal;
  }
  // 本地 dev 构建无版本可比，跳过更新检查
  if (!el || !local || local === 'dev') return;
  try {
    const d = await api('/version/latest');
    if (d.latest && d.latest.slice(0, 7) !== local.slice(0, 7)) {
      el.innerHTML = 'StrmHub v' + local.slice(0, 7) +
        ' <a href="javascript:void(0)" onclick="openUpdateModal()" style="color:var(--warning);text-decoration:none;font-weight:600">有新版本 ↗</a>';
    }
  } catch (e) {}
}

// ==================== 账号（下拉菜单 / 修改用户名密码 / 退出登录） ====================
function toggleAccountMenu(ev) {
  if (ev) ev.stopPropagation();
  const m = document.getElementById('account-menu');
  m.style.display = m.style.display === 'none' ? '' : 'none';
  if (m.style.display === '') {
    document.getElementById('account-menu-user').textContent = localStorage.getItem('username') || '-';
  }
}
document.addEventListener('click', e => {
  const m = document.getElementById('account-menu');
  if (m && m.style.display !== 'none' && !e.target.closest('#account-menu') && !e.target.textContent.includes('账号')) {
    m.style.display = 'none';
  }
});

function openAccountModal() {
  document.getElementById('account-modal').style.display = '';
  document.getElementById('acc-username').value = localStorage.getItem('username') || '';
  document.getElementById('acc-old-pwd').value = '';
  document.getElementById('acc-new-pwd').value = '';
  document.getElementById('acc-new-pwd2').value = '';
  const msg = document.getElementById('account-msg');
  msg.textContent = ''; msg.style.color = '';
}
function closeAccountModal() { document.getElementById('account-modal').style.display = 'none'; }

async function saveAccount(btn) {
  const newUsername = document.getElementById('acc-username').value.trim();
  const oldPwd = document.getElementById('acc-old-pwd').value;
  const newPwd = document.getElementById('acc-new-pwd').value;
  const newPwd2 = document.getElementById('acc-new-pwd2').value;
  const msg = document.getElementById('account-msg');
  msg.style.color = '';
  const curUsername = localStorage.getItem('username') || '';
  const changeUser = newUsername && newUsername !== curUsername;
  const changePwd = !!newPwd;
  if (!oldPwd) { msg.textContent = '请填写原密码'; return; }
  if (!changeUser && !changePwd) { msg.textContent = '没有修改任何内容'; return; }
  if (changeUser && newUsername.length < 2) { msg.textContent = '用户名至少 2 个字符'; return; }
  if (changePwd) {
    if (newPwd.length < 6) { msg.textContent = '新密码至少 6 位'; return; }
    if (newPwd !== newPwd2) { msg.textContent = '两次输入的新密码不一致'; return; }
  }
  btn.disabled = true; btn.textContent = '提交中...';
  try {
    const d = await api('/auth/update-account', { method: 'POST', body: JSON.stringify({
      old_password: oldPwd, new_username: changeUser ? newUsername : '', new_password: newPwd,
    }) });
    // 后端签发了新 token（用户名可能已变），更新本地会话
    if (d.token) localStorage.setItem('token', d.token);
    if (d.username) localStorage.setItem('username', d.username);
    msg.style.color = 'var(--success)';
    msg.textContent = '✓ ' + (d.message || '修改成功');
    document.getElementById('acc-old-pwd').value = '';
    document.getElementById('acc-new-pwd').value = '';
    document.getElementById('acc-new-pwd2').value = '';
  } catch (e) {
    msg.style.color = 'var(--danger)';
    msg.textContent = '✗ ' + e.message;
  } finally {
    btn.disabled = false; btn.textContent = '保存修改';
  }
}

function logoutNow() {
  localStorage.clear();
  history.pushState(null, '', '/login');
  location.reload();
}

// ==================== 应用内更新面板 ====================
async function openUpdateModal() {
  const mask = document.getElementById('update-modal');
  const body = document.getElementById('update-modal-body');
  const btn = document.getElementById('update-apply-btn');
  mask.style.display = '';
  body.textContent = '获取更新内容中...';
  btn.disabled = false; btn.textContent = '立即更新';
  try {
    const d = await api('/version/changes');
    document.getElementById('update-modal-title').textContent =
      '发现新版本 v' + String(d.latest || '').slice(0, 7) + '（当前 v' + String(d.current || '').slice(0, 7) + '）';
    if (!d.commits || !d.commits.length) {
      body.innerHTML = d.error
        ? '<p>更新内容获取失败：' + escHtml(d.error) + '</p><p>可点「查看 GitHub」直接查看提交记录。</p>'
        : '<p>未获取到提交记录，可点「查看 GitHub」查看。</p>';
      return;
    }
    body.innerHTML = '<p style="margin-bottom:6px"><b>更新内容（' + d.commits.length + ' 个提交）：</b></p>' +
      d.commits.map(cm =>
        '<div style="display:flex;gap:8px;padding:5px 0;border-bottom:1px solid var(--border);align-items:baseline">' +
        '<code style="color:var(--text-3);flex:none">' + String(cm.sha).slice(0, 7) + '</code>' +
        '<span>' + escHtml(cm.message) + '</span></div>').join('');
  } catch (e) {
    body.innerHTML = '<p>获取失败：' + escHtml(e.message) + '</p>';
  }
}
function closeUpdateModal() { document.getElementById('update-modal').style.display = 'none'; }

async function applyUpdate(btn) {
  btn.disabled = true; btn.textContent = '更新中...';
  const body = document.getElementById('update-modal-body');
  let latest = '';
  try {
    const d = await api('/update/apply', { method: 'POST' });
    if (d.message && d.message.includes('已是最新')) {
      btn.textContent = '已是最新'; body.insertAdjacentHTML('afterbegin', '<p>✓ ' + escHtml(d.message) + '</p>');
      return;
    }
    latest = d.latest || '';
    body.insertAdjacentHTML('afterbegin',
      '<p>✓ 镜像已拉取，容器重启中（预计 10~30 秒）…页面将自动刷新。</p>');
    btn.textContent = '重启中...';
  } catch (e) {
    // 无 docker.sock 等场景：错误信息自带配置指引
    btn.disabled = false; btn.textContent = '立即更新';
    body.insertAdjacentHTML('afterbegin',
      '<p style="color:var(--danger);white-space:pre-line">✗ ' + escHtml(e.message || '更新失败') + '</p>');
    return;
  }
  // 轮询服务恢复：版本变化或服务可达即刷新
  let tries = 0;
  const timer = setInterval(async () => {
    tries++;
    try {
      const d = await api('/version');
      const v = String(d.version || '');
      if (v && v !== 'dev' && (!latest || v.slice(0, 7) === latest.slice(0, 7))) {
        clearInterval(timer); toast('✓ 已更新到 v' + v.slice(0, 7));
        setTimeout(() => location.reload(), 800);
      }
    } catch (e) { /* 重启期间不可达，继续等 */ }
    if (tries > 60) { clearInterval(timer); btn.textContent = '请手动刷新'; }
  }, 3000);
}

// ==================== 日志 ====================
let logPollTimer = null;
// 操作日志面板已移除（日志页只保留服务端任务日志）；
// appendLog 保留为空操作，历史调用点无需逐一清理
function appendLog(line) {}
function clearLogViewer() {
  const sv = document.getElementById('server-log-viewer');
  if (sv) sv.textContent = '暂无日志...';
}
function openLog() {
  showPage('logs');
  startLogPoll();
}
// 服务端日志轮询：日志页可见时每 3 秒刷新，离开自动停止
function startLogPoll() {
  stopLogPoll();
  loadSystemLogs();
  logPollTimer = setInterval(() => {
    const page = document.getElementById('page-logs');
    if (!page || !page.classList.contains('active')) { stopLogPoll(); return; }
    loadSystemLogs();
  }, 3000);
}
function stopLogPoll() {
  if (logPollTimer) { clearInterval(logPollTimer); logPollTimer = null; }
}
async function loadSystemLogs() {
  const viewer = document.getElementById('server-log-viewer');
  if (!viewer) return;
  if (viewer.textContent === '暂无日志...') viewer.textContent = '加载中...';
  try {
    const data = await api('/system/logs');
    // 最新在最上面显示
    const lines = String(data.logs || '暂无日志').split('\n').filter(l => l.trim() !== '');
    viewer.textContent = lines.reverse().join('\n');
  } catch (e) {
    console.error('[日志] 加载失败:', e);
    viewer.textContent = '加载失败: ' + (e.message || '未知错误');
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
  document.getElementById('btn-logout')?.addEventListener('click', () => {
    localStorage.clear();
    // 跳到 /login：地址栏立即反映登出状态，登录后回到仪表盘
    history.pushState(null, '', '/login');
    location.reload();
  });
  // 输入框自适应高度
  document.querySelectorAll('textarea.auto-resize').forEach(ta => {
    ta.addEventListener('input', () => autoResizeTextarea(ta));
    autoResizeTextarea(ta);
  });
  updateRenameExample();
  attachCIDResolvers();
  attachYamlHighlight('category-yaml');
  attachYamlHighlight('wash-yaml');
  startTaskPoll();
  checkAuth();
});
