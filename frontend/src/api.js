// API 封装: token 管理 + 统一请求
const TOKEN_KEY = 'strmhub_token'

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(token) {
  if (token) localStorage.setItem(TOKEN_KEY, token)
  else localStorage.removeItem(TOKEN_KEY)
}

export function isAuthed() {
  return !!getToken()
}

export function normalizeError(resp, data) {
  if (data && data.detail) return String(data.detail)
  return `HTTP ${resp.status}`
}

export async function api(method, path, body) {
  const headers = {}
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const resp = await fetch(path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await resp.text()
  let data = null
  try { data = text ? JSON.parse(text) : null } catch { data = null }
  if (!resp.ok) {
    // token 失效: 清除本地凭据并通知 UI 回到登录页(登录接口本身除外)
    if (resp.status === 401 && !path.startsWith('/api/auth/')) {
      setToken('')
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new Event('strmhub-unauthorized'))
      }
      const err = new Error('登录已过期, 请重新登录')
      err.status = 401
      throw err
    }
    const err = new Error(normalizeError(resp, data))
    err.status = resp.status
    throw err
  }
  return data
}

export const http = {
  get: (p) => api('GET', p),
  post: (p, b) => api('POST', p, b),
  put: (p, b) => api('PUT', p, b),
  del: (p) => api('DELETE', p),
}

// ---- 业务接口 ----
export const authApi = {
  login: (password) => http.post('/api/auth/login', { password }),
  me: () => http.get('/api/me'),
}

export const accountApi = {
  list: () => http.get('/api/accounts'),
  create: (body) => http.post('/api/accounts', body),
  remove: (id) => http.del(`/api/accounts/${id}`),
  drivers: () => http.get('/api/accounts/drivers'),
  rules: (id) => http.get(`/api/accounts/${id}/rules`),
  saveRules: (id, rules) => http.put(`/api/accounts/${id}/rules`, { rules }),
  browse: (id, parent) => http.get(
    `/api/accounts/${id}/browse${parent ? `?parent=${encodeURIComponent(parent)}` : ''}`),
}

export const qrcodeApi = {
  start: (driverType) => http.post('/api/accounts/qrcode/start', { driver_type: driverType }),
  poll: (driverType, { uid, time, sign, app }) => http.post('/api/accounts/qrcode/poll', {
    driver_type: driverType, uid, time, sign, app,
  }),
}

export const taskApi = {
  list: () => http.get('/api/tasks'),
  create: (body) => http.post('/api/tasks', body),
  remove: (id) => http.del(`/api/tasks/${id}`),
  run: (id) => http.post(`/api/tasks/${id}/run`),
}

export const scrapeApi = {
  run: (strmDir) => http.post('/api/scrape/run', { strm_dir: strmDir }),
  items: (taskId) => http.get(`/api/scrape/items?task_id=${taskId}`),
}

export const organizeApi = {
  plan: (path) => http.post('/api/organize/plan', { path }),
  execute: (planJson) => http.post('/api/organize/execute', { plan_json: planJson }),
  run: (accountId) => http.post('/api/organize/run', { account_id: accountId }),
  render: (template, sample) => http.post('/api/organize/render', { template, sample }),
}

export const automationApi = {
  list: () => http.get('/api/automation/rules'),
  create: (body) => http.post('/api/automation/rules', body),
  remove: (id) => http.del(`/api/automation/rules/${id}`),
}

export const systemApi = {
  health: () => http.get('/api/health'),
}
