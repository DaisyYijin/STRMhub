// 回归: Accounts.vue 各驱动挂载/登录态渲染不抛错(曾因模板误用 acct.value.id 崩溃)
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('./api', () => ({
  accountApi: {
    list: vi.fn().mockResolvedValue([]),
    drivers: vi.fn().mockResolvedValue([{ name: 'p115', label: '115 网盘' }]),
    create: vi.fn(), remove: vi.fn(), rules: vi.fn(), saveRules: vi.fn(), browse: vi.fn(),
  },
  driverRulesApi: { rules: vi.fn().mockResolvedValue({ rules: {} }), save: vi.fn() },
  organizeApi: { plan: vi.fn(), execute: vi.fn(), run: vi.fn() },
  qrcodeApi: { start: vi.fn(), poll: vi.fn() },
}))

import Accounts from './views/Accounts.vue'
import { accountApi } from './api'

const loggedAccount = {
  id: 1, name: '扫码用户', driver_type: 'p115', status: 'ok',
  info: { avatar: '', nickname: '测试昵称', vip: 'vip3', device: 'harmony',
          total_size: 1024 ** 4, used_size: 1024 ** 3,
          total_size_fmt: '1 TB', used_size_fmt: '1 GB' },
}

describe('Accounts.vue mount repro', () => {
  it('p115 未登录挂载不报错', async () => {
    const w = mount(Accounts, { props: { driverType: 'p115' } })
    await new Promise((r) => setTimeout(r, 50))
    expect(w.exists()).toBe(true)
  })
  it('p123 未登录挂载不报错', async () => {
    const w = mount(Accounts, { props: { driverType: 'p123' } })
    await new Promise((r) => setTimeout(r, 50))
    expect(w.exists()).toBe(true)
  })
  it('local 未登录挂载不报错', async () => {
    const w = mount(Accounts, { props: { driverType: 'local' } })
    await new Promise((r) => setTimeout(r, 50))
    expect(w.exists()).toBe(true)
  })
  it('无 driverType(全部账户)挂载不报错', async () => {
    const w = mount(Accounts, { props: { driverType: '' } })
    await new Promise((r) => setTimeout(r, 50))
    expect(w.exists()).toBe(true)
  })
  it('已登录(p115 有账户)渲染不报错', async () => {
    accountApi.list.mockResolvedValue([loggedAccount])
    const w = mount(Accounts, { props: { driverType: 'p115' } })
    await new Promise((r) => setTimeout(r, 50))
    expect(w.text()).toContain('测试昵称')
    expect(w.text()).toContain('重新扫码登录')
    // 账号信息内容必须出现在 tab 栏之后(曾平铺在 tab 上方)
    const html = w.html()
    const tabIdx = html.indexOf('整理归档')
    const infoIdx = html.indexOf('测试昵称')
    expect(infoIdx).toBeGreaterThan(tabIdx)
  })
  it('驱动切换重置本地状态(123 的提示不串到 115)', async () => {
    accountApi.list.mockResolvedValue([])
    const w = mount(Accounts, { props: { driverType: 'p123' } })
    await new Promise((r) => setTimeout(r, 30))
    // 模拟 123 页面保存成功提示
    w.vm.msg = { type: 'ok', text: '规则已保存' }
    await w.setProps({ driverType: 'p115' })
    await new Promise((r) => setTimeout(r, 30))
    expect(w.vm.msg).toBe('')
    expect(w.vm.qrError).toBe('')
  })
  it('已登录(local 有账户)渲染不报错', async () => {
    accountApi.list.mockResolvedValue([{ ...loggedAccount, driver_type: 'local' }])
    const w = mount(Accounts, { props: { driverType: 'local' } })
    await new Promise((r) => setTimeout(r, 50))
    expect(w.exists()).toBe(true)
  })
})
