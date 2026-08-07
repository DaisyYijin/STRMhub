// API 封装单测(node 环境, mock localStorage/fetch)
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  api, getToken, isAuthed, normalizeError, setToken,
} from '../src/api.js'

function mockStorage() {
  const store = {}
  globalThis.localStorage = {
    getItem: (k) => store[k] ?? null,
    setItem: (k, v) => { store[k] = String(v) },
    removeItem: (k) => { delete store[k] },
  }
}

beforeEach(() => {
  mockStorage()
  globalThis.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('token 管理', () => {
  it('set/get/isAuthed', () => {
    expect(isAuthed()).toBe(false)
    setToken('tok-1')
    expect(getToken()).toBe('tok-1')
    expect(isAuthed()).toBe(true)
    setToken('')
    expect(isAuthed()).toBe(false)
  })
})

describe('normalizeError', () => {
  it('优先 detail 字段', () => {
    expect(normalizeError({ status: 400 }, { detail: '密码错误' })).toBe('密码错误')
  })
  it('回退 HTTP 状态码', () => {
    expect(normalizeError({ status: 500 }, null)).toBe('HTTP 500')
  })
})

describe('api()', () => {
  it('GET 带 Authorization 头', async () => {
    setToken('tok-2')
    globalThis.fetch.mockResolvedValue({
      ok: true, status: 200,
      text: async () => JSON.stringify({ ok: 1 }),
    })
    const data = await api('GET', '/api/health')
    expect(data.ok).toBe(1)
    const [, init] = globalThis.fetch.mock.calls[0]
    expect(init.headers.Authorization).toBe('Bearer tok-2')
  })

  it('POST 序列化 body 与 Content-Type', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true, status: 200, text: async () => 'null',
    })
    await api('POST', '/api/auth/login', { password: 'p' })
    const [, init] = globalThis.fetch.mock.calls[0]
    expect(init.body).toBe('{"password":"p"}')
    expect(init.headers['Content-Type']).toBe('application/json')
  })

  it('非 2xx 抛出带 detail 的错误', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: false, status: 401,
      text: async () => JSON.stringify({ detail: '凭据无效' }),
    })
    await expect(api('GET', '/api/me')).rejects.toThrow('凭据无效')
  })

  it('非 JSON 响应容错', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true, status: 200, text: async () => 'plain text',
    })
    const data = await api('GET', '/api/x')
    expect(data).toBeNull()
  })
})
