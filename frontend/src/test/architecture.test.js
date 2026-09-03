import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { DOMWrapper, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import OverviewView from '@/views/OverviewView.vue'
import ProvidersView from '@/views/ProvidersView.vue'
import PolicyPathsView from '@/views/PolicyPathsView.vue'
import NodesView from '@/views/NodesView.vue'
import SettingsView from '@/views/SettingsView.vue'
import LogsView from '@/views/LogsView.vue'
import ProviderDialog from '@/components/ProviderDialog.vue'
import LoginPage from '@/components/LoginPage.vue'
import { authState, logout } from '@/api.js'
import appRouter, { finalizeNavigation } from '@/router.js'
import { backgroundResources, routeResources, useDataStore } from '@/stores/data.js'
import { routeStreams, useRealtimeStore } from '@/stores/realtime.js'
import { shouldRefreshInBackground } from '@/refreshPolicy.js'
import { nodeConnectionStats } from '@/utils.js'

function sampleOverview() {
  return { version: '0.3.1', core_version: 'test', provider_count: 1, projection_count: 1, policy_url: 'http://127.0.0.1/proxies', process_rule: 'PROCESS-NAME,SurgeEB,DIRECT', gateway: { state: 'running', socks_address: '127.0.0.1:1080', projection_hash: 'abcdef', projection_count: 1 } }
}

function testRouter(component, name = 'overview') {
  return createRouter({ history: createMemoryHistory(), routes: [{ path: '/', name, component }] })
}

describe('component update boundaries', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('wraps long Policy Path descriptions inside their cards', () => {
    const styles = readFileSync('public/styles.css', 'utf8')
    expect(styles).toMatch(/\.policy-path-head p\{[^}]*overflow-wrap:anywhere/)
  })

  it('uses the concise SurgeEB title in the left navigation', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.overview = sampleOverview()
    data.providers = []
    data.nodes = []
    const router = testRouter(OverviewView)
    await router.push('/')
    vi.stubGlobal('fetch', vi.fn(async (path) => ({
      ok: true,
      status: 200,
      json: async () => path === '/api/session' ? { required: false, authenticated: true } : path === '/api/overview' ? sampleOverview() : [],
    })))
    const wrapper = mount(App, { global: { plugins: [pinia, router] } })
    await vi.waitFor(() => expect(wrapper.get('[data-testid="brand-title"]').text()).toBe('SurgeEB'))
    wrapper.unmount()
  })

  it('retries session initialization after a transient authentication check failure', async () => {
    authState.checking = false
    authState.required = false
    authState.authenticated = false
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    const router = testRouter(OverviewView)
    await router.push('/')
    let sessionAttempts = 0
    const retryOverview = { ...sampleOverview(), version: 'retry-session' }
    vi.stubGlobal('fetch', vi.fn(async (path) => {
      if (path === '/api/session') {
        sessionAttempts += 1
        if (sessionAttempts === 1) throw new Error('temporary session failure')
        return { ok: true, status: 200, json: async () => ({ required: false, authenticated: true }) }
      }
      return { ok: true, status: 200, json: async () => path === '/api/overview' ? retryOverview : [] }
    }))

    const wrapper = mount(App, { global: { plugins: [pinia, router] } })
    await vi.waitFor(() => expect(wrapper.text()).toContain('temporary session failure'))
    await wrapper.get('.empty-state button').trigger('click')
    await vi.waitFor(() => expect(data.overview?.version).toBe('retry-session'))
    expect(sessionAttempts).toBe(2)
    expect(authState.authenticated).toBe(true)
    wrapper.unmount()
  })

  it('keeps selected Overview text and its DOM node while realtime metrics change', async () => {
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.overview = sampleOverview()
    data.providers = [{ stable_id: 'p1', name: 'Provider', enabled: true }]
    data.nodes = [{ id: 'n1', alive: true }]
    const router = testRouter(OverviewView)
    await router.push('/')
    const wrapper = mount(OverviewView, { attachTo: document.body, global: { plugins: [router] } })
    const staticNode = wrapper.get('[data-testid="overview-static-copy"]').element
    const range = document.createRange()
    range.selectNodeContents(staticNode.querySelector('span'))
    const selection = window.getSelection()
    selection.removeAllRanges()
    selection.addRange(range)
    const selectedText = selection.toString()

    realtime.traffic = { up: 4096, down: 2048 }
    realtime.connections = { connections: [{ id: 'c1' }] }
    data.overview = { ...data.overview, projection_count: 2, gateway: { ...data.overview.gateway, projection_count: 2 } }
    data.nodes = [{ id: 'n1', alive: true }, { id: 'n2', alive: false }]
    await nextTick()

    expect(wrapper.get('[data-testid="overview-static-copy"]').element).toBe(staticNode)
    expect(window.getSelection().toString()).toBe(selectedText)
    expect(wrapper.text()).toContain('6.0 KiB/s')
    expect(wrapper.text()).toContain('1 / 2 / 1')
    expect(wrapper.text()).not.toContain('Projection')
    expect(wrapper.text()).not.toContain('一个强制认证 SOCKS5 TCP/UDP 端口')
    expect(wrapper.text()).not.toContain('abcdef')
    wrapper.unmount()
  })

  it('keeps a dirty Settings draft across unrelated store updates', async () => {
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.settings = { mode: 'local', http_bind: '127.0.0.1:9090', socks_bind: '127.0.0.1', socks_port: 1080, socks_host: 'socks.surge.eb', policy_host: 'policy.surge.eb', projection_key: 'shared-projection-key-for-devices', projection_hash: 'abcdef', projection_count: 3, prefix_provider: false, node_test_url: 'https://example.com', node_test_udp_address: '1.1.1.1:53', node_test_timeout_seconds: 10 }
    data.service = { platform: 'darwin', installed: true, active: false, repair_needed: false }
    const router = testRouter(SettingsView, 'settings')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } })
    const deploymentModes = wrapper.get('select').findAll('option').map((option) => [option.attributes('value'), option.text()])
    expect(deploymentModes).toEqual([['local', '仅本机'], ['gateway', '局域网网关']])
    expect(wrapper.get('[data-testid="settings-socks-host"]').element.value).toBe('socks.surge.eb')
    expect(wrapper.get('[data-testid="settings-policy-host"]').element.value).toBe('policy.surge.eb')
    expect(wrapper.get('[data-testid="projection-key"]').element.value).toBe('shared-projection-key-for-devices')
    expect(wrapper.get('[data-testid="settings-deployment"]').text()).toContain('使用范围与地址')
    expect(wrapper.get('[data-testid="settings-security"]').text()).toContain('访问 Token')
    expect(wrapper.get('[data-testid="settings-identity"]').text()).toContain('节点凭据')
    expect(wrapper.get('[data-testid="settings-diagnostics"]').element.open).toBe(false)
    expect(wrapper.get('[data-testid="settings-service"] > .settings-card-head .pill').text()).toBe('已开启')
    expect(wrapper.get('[data-testid="settings-service"]').text()).not.toContain('未运行')
    expect(wrapper.find('[data-testid="settings-save"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('可用节点3 个')
    expect(wrapper.text()).not.toContain('abcdef')
    expect(wrapper.text()).not.toContain('投影协议范围')
    expect(wrapper.text()).not.toContain('确定性投影身份')
    const input = wrapper.get('[data-testid="settings-http-bind"]')
    await input.setValue('127.0.0.1:9191')
    realtime.memory = { inuse: 123456 }
    data.overview = sampleOverview()
    await nextTick()
    expect(input.element.value).toBe('127.0.0.1:9191')
    expect(wrapper.get('[data-testid="settings-save"]').text()).toContain('有未保存的修改')
    expect(wrapper.get('[data-testid="settings-save"]').text()).toContain('新的设置会立即生效')
    expect(wrapper.text()).not.toContain('校验边界并原子应用')
    data.service = { platform: 'darwin', installed: true, active: true, repair_needed: true }
    await nextTick()
    expect(wrapper.get('[data-testid="settings-service"] > .settings-card-head .pill').text()).toBe('需要修复')
    expect(wrapper.get('[data-testid="settings-service"]').text()).toContain('旧定义需要迁移')
    const repairButton = wrapper.get('[data-testid="settings-service"]').findAll('button').find((button) => button.text() === '修复自动启动')
    expect(repairButton.attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('keeps gateway token generation and the settings save contract intact', async () => {
    const data = useDataStore()
    data.settings = {
      mode: 'local', http_bind: '127.0.0.1:9090', socks_bind: '127.0.0.1', socks_port: 1080,
      socks_host: '127.0.0.1', policy_host: '127.0.0.1', projection_key: 'shared-projection-key-for-devices', prefix_provider: false,
      management_token_configured: false, policy_token_configured: false, policy_token: '',
      suggested_gateway_host: '192.168.50.10', node_test_url: 'https://example.com', node_test_udp_address: '1.1.1.1:53', node_test_timeout_seconds: 10,
    }
    data.service = {}
    data.updateSettings = vi.fn(async () => ({ reconnect: false }))
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const router = testRouter(SettingsView, 'settings')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } })

    await wrapper.get('[data-testid="settings-security"]').findAll('button').find((button) => button.text() === '生成').trigger('click')
    const management = wrapper.get('[data-testid="management-token"]')
    expect(management.element.value).toMatch(/^[A-Za-z0-9_-]{24}$/)
    expect(wrapper.get('[data-testid="generated-token-note"]').text()).toContain('新的 24 位 Management Token')
    expect(wrapper.get('[data-testid="generated-token-note"]').text()).not.toContain('两个独立的 24 位 Token')

    const mode = wrapper.get('[data-testid="settings-mode"]')
    await mode.setValue('gateway')
    expect(mode.element.value).toBe('gateway')
    expect(wrapper.get('[data-testid="settings-http-bind"]').element.value).toBe('0.0.0.0:9090')
    expect(wrapper.get('[data-testid="settings-socks-bind"]').element.value).toBe('0.0.0.0')
    expect(wrapper.get('[data-testid="settings-socks-host"]').element.value).toBe('192.168.50.10')
    expect(wrapper.get('[data-testid="settings-policy-host"]').element.value).toBe('192.168.50.10')
    expect(wrapper.get('[data-testid="settings-deployment"] > .settings-card-head .pill').text()).toBe('局域网')
    expect(wrapper.get('[data-testid="settings-deployment"]').text()).toContain('不能填写 peer 地址')
    const policy = wrapper.get('[data-testid="policy-token"]')
    const tokens = [management.element.value, policy.element.value]
    expect(tokens[0]).toMatch(/^[A-Za-z0-9_-]{24}$/)
    expect(tokens[1]).toMatch(/^[A-Za-z0-9_-]{24}$/)
    expect(tokens[0]).not.toBe(tokens[1])
    expect(management.attributes('type')).toBe('text')
    expect(policy.attributes('type')).toBe('text')
    expect(wrapper.get('[data-testid="generated-token-note"]').text()).toContain('两个独立的 24 位 Token')
    expect(wrapper.get('[data-testid="settings-security"]').findAll('button').map((button) => button.text())).toEqual(['隐藏', '复制', '重新生成', '复制', '重新生成'])

    await wrapper.get('[data-testid="settings-socks-host"]').setValue('192.168.99.9')
    await wrapper.get('[data-testid="settings-policy-host"]').setValue('policy.surge.eb')
    await mode.setValue('local')
    expect(wrapper.get('[data-testid="settings-socks-host"]').element.value).toBe('127.0.0.1')
    expect(wrapper.get('[data-testid="settings-policy-host"]').element.value).toBe('127.0.0.1')
    expect(wrapper.get('[data-testid="settings-http-bind"]').element.value).toBe('127.0.0.1:9090')
    expect(wrapper.get('[data-testid="settings-socks-bind"]').element.value).toBe('127.0.0.1')
    await mode.setValue('gateway')
    expect(wrapper.get('[data-testid="settings-socks-host"]').element.value).toBe('192.168.50.10')
    expect(wrapper.get('[data-testid="settings-policy-host"]').element.value).toBe('192.168.50.10')
    await wrapper.get('[data-testid="settings-socks-host"]').setValue('192.168.99.9')
    await wrapper.get('[data-testid="settings-policy-host"]').setValue('policy.surge.eb')

    await wrapper.get('[data-testid="settings-identity"]').findAll('button').find((button) => button.text() === '重新生成').trigger('click')
    expect(wrapper.get('[data-testid="projection-key"]').element.value).toMatch(/^[A-Za-z0-9_-]{24}$/)

    await wrapper.get('form').trigger('submit')
    expect(data.updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      mode: 'gateway',
      management_token: tokens[0],
      policy_token: tokens[1],
      socks_host: '192.168.99.9',
      policy_host: 'policy.surge.eb',
      projection_types: ['*'],
      node_test_timeout_seconds: 10,
    }))
    expect(wrapper.find('[data-testid="settings-save"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows service status failures instead of reporting autostart as disabled', async () => {
    const data = useDataStore()
    data.settings = { mode: 'local', http_bind: '127.0.0.1:9090', socks_bind: '127.0.0.1', socks_port: 1080, socks_host: '127.0.0.1', policy_host: '127.0.0.1', projection_key: 'shared-projection-key-for-devices' }
    data.service = { error: 'HOME is not defined' }
    const router = testRouter(SettingsView, 'settings')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } })

    const serviceCard = wrapper.get('[data-testid="settings-service"]')
    expect(serviceCard.get('.pill').text()).toBe('检测失败')
    expect(serviceCard.text()).toContain('无法检测系统服务状态：HOME is not defined')
    expect(serviceCard.text()).not.toContain('自动启动未开启')
    expect(serviceCard.findAll('button').every((button) => button.attributes('disabled') !== undefined)).toBe(true)
    wrapper.unmount()
  })

  it('retains the service API error for the Settings status card', async () => {
    const data = useDataStore()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 501, json: async () => ({ error: 'HOME is not defined' }) })))

    await data.loadResource('service')
    expect(data.service).toEqual({ error: 'HOME is not defined' })
  })

  it('logs in through the dedicated page without persisting the token', async () => {
    localStorage.setItem('surgeeb-management-token', 'stale-token-value')
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) })))
    const wrapper = mount(LoginPage)
    await wrapper.get('[data-testid="login-token"]').setValue('management-token-1234567890')
    await wrapper.get('form').trigger('submit')
    await vi.waitFor(() => expect(wrapper.emitted('authenticated')).toHaveLength(1))
    expect(fetch).toHaveBeenCalledWith('/api/session', expect.objectContaining({ method: 'POST' }))
    expect(localStorage.getItem('surgeeb-management-token')).toBeNull()
    expect(authState.authenticated).toBe(true)
    wrapper.unmount()
  })

  it('keeps the authenticated UI state when logout is not confirmed by the server', async () => {
    authState.authenticated = true
    localStorage.setItem('surgeeb-management-token', 'legacy-token-value')
    vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('network unavailable') }))

    await expect(logout()).rejects.toThrow('network unavailable')
    expect(authState.authenticated).toBe(true)
    expect(localStorage.getItem('surgeeb-management-token')).toBe('legacy-token-value')
  })

  it('clears response snapshots so a new session reloads unchanged server data', async () => {
    const data = useDataStore()
    const overview = { ...sampleOverview(), version: 'session-reset-cache' }
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => overview })))

    await data.loadResource('overview')
    expect(data.overview?.version).toBe('session-reset-cache')
    data.resetSession()
    expect(data.overview).toBeNull()
    await data.loadResource('overview')
    expect(data.overview?.version).toBe('session-reset-cache')
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('ignores an earlier session response that finishes after logout reset', async () => {
    const data = useDataStore()
    data.resetSession()
    let resolveFirst
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls += 1
      if (calls === 1) return new Promise((resolve) => { resolveFirst = resolve })
      return { ok: true, status: 200, json: async () => ({ ...sampleOverview(), version: 'fresh-session' }) }
    }))

    const staleLoad = data.loadResource('overview')
    await vi.waitFor(() => expect(resolveFirst).toBeTypeOf('function'))
    data.resetSession()
    resolveFirst({ ok: true, status: 200, json: async () => ({ ...sampleOverview(), version: 'stale-session' }) })
    await staleLoad
    expect(data.overview).toBeNull()
    await data.loadResource('overview')
    expect(data.overview?.version).toBe('fresh-session')
  })

  it('keeps the Provider dialog accessible and keyboard-dismissible', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(ProviderDialog, { props: { open: true }, global: { plugins: [pinia] } })
    await nextTick()
    const dialog = document.querySelector('[role="dialog"]')
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.getAttribute('aria-labelledby')).toBe('provider-dialog-title')
    expect(dialog.getAttribute('aria-describedby')).toBe('provider-dialog-description')
    expect(document.activeElement).toBe(dialog.querySelector('input'))
    const primary = new DOMWrapper(document.querySelector('[data-testid="provider-primary"]'))
    expect(primary.text()).toContain('名称')
    expect(primary.text()).toContain('节点名 Provider 前缀')
    expect(primary.text()).toContain('订阅 URL')
    expect(primary.text()).toContain('节点筛选')
    expect(primary.text()).not.toContain('请求 Header')
    const options = new DOMWrapper(document.querySelector('[data-testid="provider-options"]'))
    expect(options.element.open).toBe(false)
    expect(options.text()).toContain('请求 Header')
    expect(options.text()).toContain('刷新间隔')
    expect(options.text()).toContain('健康检查')
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('keeps same-type edit secrets optional but requires a source after changing type', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const provider = {
      stable_id: 'provider-1', name: '机场', type: 'http', enabled: true,
      refresh_seconds: 21600, size_limit: 16777216, include_name: '香港', exclude_name: '过期',
      health_check: true, health_check_seconds: 300, health_check_timeout: 5000,
      health_check_lazy: true, expected_status: '200-399',
    }
    const wrapper = mount(ProviderDialog, { props: { open: true, provider }, global: { plugins: [pinia] } })
    await nextTick()
    const field = (selector) => new DOMWrapper(document.querySelector(selector))
    expect(field('[data-testid="provider-url"]').attributes('required')).toBeUndefined()
    expect(field('[data-testid="provider-include"]').element.value).toBe('香港')
    expect(field('[data-testid="provider-exclude"]').element.value).toBe('过期')
    await field('[data-testid="provider-type"]').setValue('file')
    expect(field('[data-testid="provider-file"]').attributes()).toHaveProperty('required')
    expect(field('[data-testid="provider-http-options"]').isVisible()).toBe(false)
    wrapper.unmount()
  })

  it('submits a new HTTP Provider without hidden required fields blocking the form', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.saveProvider = vi.fn(async () => {})
    const wrapper = mount(ProviderDialog, { props: { open: true }, global: { plugins: [pinia] } })
    await nextTick()
    const dialog = new DOMWrapper(document.querySelector('[role="dialog"]'))
    await new DOMWrapper(document.querySelector('[data-testid="provider-primary"] input')).setValue('新订阅')
    await new DOMWrapper(document.querySelector('[data-testid="provider-prefix"]')).setValue('机场前缀')
    await new DOMWrapper(document.querySelector('[data-testid="provider-url"]')).setValue('https://example.com/subscription')
    expect(document.querySelector('[data-testid="provider-file"]')).toBeNull()
    expect(document.querySelector('[data-testid="provider-payload"]')).toBeNull()
    expect(dialog.element.checkValidity()).toBe(true)
    await dialog.trigger('submit')
    await vi.waitFor(() => expect(data.saveProvider).toHaveBeenCalledWith(expect.objectContaining({ name: '新订阅', prefix: '机场前缀', type: 'http', url: 'https://example.com/subscription' }), ''))
    wrapper.unmount()
  })

  it('submits an existing private file Provider after switching it to HTTP', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.saveProvider = vi.fn(async () => {})
    const provider = {
      stable_id: 'private-file', name: '私有订阅', type: 'file', enabled: true,
      refresh_seconds: 0, size_limit: 0, health_check: false,
      health_check_seconds: 0, health_check_timeout: 0,
    }
    const wrapper = mount(ProviderDialog, { props: { open: true, provider }, global: { plugins: [pinia] } })
    await nextTick()
    await new DOMWrapper(document.querySelector('[data-testid="provider-type"]')).setValue('http')
    await new DOMWrapper(document.querySelector('[data-testid="provider-url"]')).setValue('https://example.com/subscription')
    const dialog = new DOMWrapper(document.querySelector('[role="dialog"]'))
    const numbers = [...dialog.element.querySelectorAll('input[type="number"]')]
    expect(numbers.find((input) => input.min === '60' && !input.disabled).value).toBe('21600')
    expect(numbers.find((input) => input.min === '1024').value).toBe('16777216')
    expect(dialog.element.checkValidity()).toBe(true)
    await dialog.trigger('submit')
    await vi.waitFor(() => expect(data.saveProvider).toHaveBeenCalledWith(expect.objectContaining({
      name: '私有订阅', type: 'http', url: 'https://example.com/subscription',
      refresh_seconds: 21600, size_limit: 16777216,
    }), 'private-file'))
    wrapper.unmount()
  })

  it('opens advanced options and shows an error for an invalid hidden control', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(ProviderDialog, { props: { open: true }, global: { plugins: [pinia] } })
    await nextTick()
    const details = document.querySelector('[data-testid="provider-options"]')
    const sizeLimit = [...details.querySelectorAll('input[type="number"]')].find((input) => input.min === '1024')
    sizeLimit.value = '1'
    sizeLimit.dispatchEvent(new Event('input', { bubbles: true }))
    expect(details.open).toBe(false)
    expect(document.querySelector('[role="dialog"]').checkValidity()).toBe(false)
    await nextTick()
    expect(details.open).toBe(true)
    expect(document.querySelector('[role="alert"]').textContent).toContain('请检查表单')
    wrapper.unmount()
  })

  it('uploads a selected private Provider file instead of submitting a server path', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.saveProvider = vi.fn(async () => {})
    const wrapper = mount(ProviderDialog, { props: { open: true }, global: { plugins: [pinia] } })
    await nextTick()
    await new DOMWrapper(document.querySelector('[data-testid="provider-primary"] input')).setValue('上传订阅')
    await new DOMWrapper(document.querySelector('[data-testid="provider-type"]')).setValue('file')
    const input = document.querySelector('[data-testid="provider-file"]')
    const file = new File(['proxies: []\n'], 'provider.yaml', { type: 'text/yaml' })
    Object.defineProperty(input, 'files', { configurable: true, value: [file] })
    await new DOMWrapper(input).trigger('change')
    await new DOMWrapper(document.querySelector('[role="dialog"]')).trigger('submit')
    await vi.waitFor(() => expect(data.saveProvider).toHaveBeenCalledWith(expect.objectContaining({
      name: '上传订阅', type: 'file',
    }), '', file))
    expect(data.saveProvider.mock.calls[0][0]).not.toHaveProperty('file_path')
    wrapper.unmount()
  })

  it('submits Inline payload as Mihomo Provider YAML', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.saveProvider = vi.fn(async () => {})
    const wrapper = mount(ProviderDialog, { props: { open: true }, global: { plugins: [pinia] } })
    await nextTick()
    const yaml = 'proxies:\n  - name: YAML Node\n    type: vless\n    server: example.com\n    port: 443\n'
    await new DOMWrapper(document.querySelector('[data-testid="provider-primary"] input')).setValue('Inline YAML')
    await new DOMWrapper(document.querySelector('[data-testid="provider-type"]')).setValue('inline')
    const payload = new DOMWrapper(document.querySelector('[data-testid="provider-payload"]'))
    expect(payload.element.previousElementSibling.textContent).toContain('Mihomo Provider YAML')
    await payload.setValue(yaml)
    await new DOMWrapper(document.querySelector('[role="dialog"]')).trigger('submit')
    await vi.waitFor(() => expect(data.saveProvider).toHaveBeenCalledWith(expect.objectContaining({
      name: 'Inline YAML', type: 'inline', payload: yaml.trim(),
    }), ''))
    wrapper.unmount()
  })

  it('requires confirmation before a Provider rename rotates node credentials', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.saveProvider = vi.fn(async () => {})
    const provider = {
      stable_id: 'provider-1', name: '原名称', type: 'http', enabled: true,
      refresh_seconds: 21600, size_limit: 16777216, health_check: false,
    }
    window.confirm.mockReturnValue(false)
    const wrapper = mount(ProviderDialog, { props: { open: true, provider }, global: { plugins: [pinia] } })
    await nextTick()
    await new DOMWrapper(document.querySelector('[data-testid="provider-primary"] input')).setValue('新名称')
    document.querySelector('[role="dialog"]').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('用户名和密码'))
    expect(data.saveProvider).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('uses Provider IDs to disambiguate same-name node connection totals', () => {
    const connections = [
      { chains: ['香港 01', 'GLOBAL'], providerIDs: ['provider-a'], upload: 10, download: 20 },
      { chains: ['香港 01', 'GLOBAL'], providerIDs: ['provider-b'], upload: 30, download: 40 },
    ]
    expect(nodeConnectionStats({ name: '香港 01', proxy_name: '香港 01', provider_id: 'provider-a' }, connections)).toMatchObject({ count: 1, upload: 10, download: 20 })
    expect(nodeConnectionStats({ name: '香港 01', proxy_name: '香港 01', provider_id: 'provider-b' }, connections)).toMatchObject({ count: 1, upload: 30, download: 40 })
  })

  it('keeps Provider cards focused on actionable state and gates source-specific actions', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    data.providers = [
      {
        stable_id: 'http-off', name: '主力订阅', type: 'http', url: 'https://example.com/…', enabled: false,
        refresh_seconds: 21600, size_limit: 16777216, include_name: '香港', exclude_name: '过期',
        header_names: ['Authorization'], health_check: true,
      },
      {
        stable_id: 'inline-on', name: '本地节点', type: 'inline', enabled: true, health_check: false,
        filtered_count: 2, filtered_nodes: ['WARP-A', 'WARP-B'], hosts_count: 4,
        runtime: { proxies: [
          { name: '香港 01', type: 'Vless', alive: true, history: [{ delay: 42 }] },
          { name: '新加坡 02', type: 'Trojan', alive: false, history: [] },
        ] },
      },
    ]
    data.nodes = [{ id: 'node-1', provider_id: 'inline-on' }]
    const router = testRouter(ProvidersView, 'providers')
    await router.push('/')
    data.reorderProviders = vi.fn(async () => {})
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [pinia, router] } })
    const http = wrapper.get('[data-provider-id="http-off"]')
    const inline = wrapper.get('[data-provider-id="inline-on"]')

    expect(wrapper.get('[data-testid="provider-summary"]').text()).toContain('1 / 2 已启用')
    expect(http.text()).toContain('6 小时')
    expect(http.text()).toContain('筛选包含 香港 · 排除 过期')
    expect(http.text()).not.toContain('最近更新 —')
    expect(http.text()).not.toContain('暂无订阅流量信息')
    expect(http.findAll('button').map((button) => button.text())).toEqual(['↑', '↓', '编辑', '更多'])
    expect(http.get('[aria-label="上移 Provider“主力订阅”"]').attributes('disabled')).toBeDefined()
    expect(http.get('[aria-label="下移 Provider“主力订阅”"]').attributes('disabled')).toBeUndefined()
    await http.get('[aria-label="下移 Provider“主力订阅”"]').trigger('click')
    await vi.waitFor(() => expect(data.reorderProviders).toHaveBeenCalledWith(['inline-on', 'http-off']))
    expect(wrapper.text()).toContain('Provider 从上到下决定最终节点的排列顺序')

    await http.get('.provider-more').trigger('click')
    expect(wrapper.get('.provider-menu-panel').text()).toContain('复制 URL / Header')
    await inline.get('.provider-more').trigger('click')
    expect(wrapper.findAll('.provider-menu-panel')).toHaveLength(1)
    expect(wrapper.get('.provider-menu-panel').text()).not.toContain('复制 URL / Header')

    await inline.get('.provider-disclosure').trigger('click')
    const nodeCards = inline.findAll('[data-testid="provider-node-card"]')
    expect(nodeCards).toHaveLength(2)
    expect(nodeCards[0].text()).toContain('香港 01')
    expect(nodeCards[0].text()).toContain('Vless')
    expect(nodeCards[0].text()).toContain('42 ms')
    expect(nodeCards[1].text()).toContain('新加坡 02')
    expect(nodeCards[1].text()).toContain('未知 / 失败')
    expect(nodeCards[1].text()).toContain('暂无数据')
    expect(inline.get('.provider-filter-warning').text()).toContain('已过滤 2 个链式节点')
    expect(inline.get('.provider-filter-warning').text()).toContain('WARP-A、WARP-B')
    expect(inline.text()).toContain('已应用 4 条代理服务器映射')
    wrapper.unmount()
  })

  it('manages independent Policy Paths with explicit Provider selection', async () => {
    const data = useDataStore()
    data.providers = [
      { stable_id: 'first', name: '第一订阅', enabled: true },
      { stable_id: 'second', name: '第二订阅', enabled: false },
    ]
    data.policyPaths = [{
      id: 'default', name: '全部节点', include_all: true, provider_ids: [], token: 'default-token',
      include_name: '香港|新加坡', exclude_name: '过期',
      url: 'http://127.0.0.1:18080/proxies?token=default-token', default: true, provider_count: 2, projection_count: 3,
    }]
    data.savePolicyPath = vi.fn(async () => {})
    data.regeneratePolicyPathToken = vi.fn(async () => {})
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const router = testRouter(PolicyPathsView, 'policyPaths')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { attachTo: document.body, global: { plugins: [router] } })

    expect(wrapper.get('[data-testid="policy-path-card"]').text()).toContain('全部 Provider')
    expect(wrapper.get('[data-testid="policy-path-card"]').text()).toContain('3当前节点')
    expect(wrapper.get('[data-testid="policy-path-card"]').text()).toContain('包含 /香港|新加坡/ · 排除 /过期/')
    await wrapper.findAll('button').find((button) => button.text() === '添加 Policy Path').trigger('click')
    await wrapper.get('[data-testid="policy-path-name"]').setValue('指定节点')
    await wrapper.get('[data-testid="policy-path-token"]').setValue('manual-policy-token-1234')
    await wrapper.get('[data-testid="policy-path-include"]').setValue('Node$')
    await wrapper.get('[data-testid="policy-path-exclude"]').setValue('Deprecated')
    const scopeLabels = wrapper.findAll('.policy-path-scope .check-row')
    await scopeLabels[1].get('input').setValue(true)
    const providerRows = wrapper.findAll('[data-testid="policy-path-provider-list"] .check-row')
    expect(providerRows[1].text()).toContain('已停用，启用后自动恢复输出')
    await providerRows[0].get('input').setValue(true)
    await wrapper.get('.policy-path-modal').trigger('submit')
    await vi.waitFor(() => expect(data.savePolicyPath).toHaveBeenCalledWith({
      name: '指定节点', token: 'manual-policy-token-1234', include_all: false, provider_ids: ['first'],
      include_name: 'Node$', exclude_name: 'Deprecated',
    }, ''))

    await wrapper.get('[data-testid="policy-path-card"]').findAll('button').find((button) => button.text() === '编辑').trigger('click')
    expect(wrapper.get('[data-testid="policy-path-token"]').element.value).toBe('')
    expect(wrapper.get('[data-testid="policy-path-include"]').element.value).toBe('香港|新加坡')
    expect(wrapper.get('[data-testid="policy-path-exclude"]').element.value).toBe('过期')
    await wrapper.get('.policy-path-modal').trigger('submit')
    await vi.waitFor(() => expect(data.savePolicyPath).toHaveBeenLastCalledWith({
      name: '全部节点', token: '', include_all: true, provider_ids: [],
      include_name: '香港|新加坡', exclude_name: '过期',
    }, 'default'))

    await wrapper.get('[data-testid="policy-path-card"]').findAll('button').find((button) => button.text() === '编辑').trigger('click')
    await wrapper.get('[data-testid="policy-path-token"]').setValue('updated-default-token-1234')
    await wrapper.get('.policy-path-modal').trigger('submit')
    await vi.waitFor(() => expect(data.savePolicyPath).toHaveBeenLastCalledWith({
      name: '全部节点', token: 'updated-default-token-1234', include_all: true, provider_ids: [],
      include_name: '香港|新加坡', exclude_name: '过期',
    }, 'default'))

    await wrapper.get('[data-testid="policy-path-card"]').findAll('button').find((button) => button.text() === '重新生成 Token').trigger('click')
    await vi.waitFor(() => expect(data.regeneratePolicyPathToken).toHaveBeenCalledWith('default'))
    wrapper.unmount()
  })

  it('presents node health and activity as layered cards and keeps credential copy secondary', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.providers = [{ stable_id: 'main', name: '主力订阅', enabled: true }]
    data.nodes = [
      {
        id: 'node-1', name: '香港 HKG 01', provider_id: 'main', provider_name: '主力订阅',
        type: 'vless', alive: true, udp: true, uot: true, history: [{ delay: 50 }, { delay: 42 }],
      },
      {
        id: 'node-2', name: '日本 Tokyo 02', provider_id: 'main', provider_name: '主力订阅',
        type: 'trojan', alive: false, history: [],
      },
    ]
    realtime.connections = { connections: [{ id: 'connection-1', upload: 2048, download: 4096, chains: ['香港 HKG 01', 'GLOBAL'] }] }
    const router = testRouter(NodesView, 'nodes')
    await router.push('/')
    const wrapper = mount(NodesView, { global: { plugins: [pinia, router] } })
    const cards = wrapper.findAll('[data-testid="node-card"]')

    expect(wrapper.get('[data-testid="node-summary"]').text()).toContain('2 个节点')
    expect(wrapper.get('[data-testid="node-summary"]').text()).toContain('1 个可用')
    expect(wrapper.get('[data-testid="node-summary"]').text()).toContain('1 个需检查')
    expect(wrapper.get('[data-testid="node-summary"]').text()).toContain('1 个实时连接')
    expect(cards).toHaveLength(2)
    expect(cards[0].text()).toContain('香港 HKG 01')
    expect(cards[0].text()).toContain('主力订阅')
    expect(cards[0].text()).toContain('VLESS')
    expect(cards[0].text()).toContain('42 ms')
    expect(cards[0].text()).toContain('历史 50 ms')
    expect(cards[0].text()).toContain('↑ 2.0 KiB · ↓ 4.0 KiB')
    expect(cards[0].text()).toContain('代理链GLOBAL')
    expect(cards[0].text()).not.toContain('node-1')
    expect(cards[0].text()).not.toContain('尚未运行端到端诊断')
    expect(cards[0].findAll('button').map((button) => button.text())).toEqual(['Mihomo 测速', '端到端诊断', '更多'])

    await cards[0].get('.node-more').trigger('click')
    expect(wrapper.get('.node-menu-panel').text()).toBe('复制 Surge 节点行')
    await wrapper.get('select[aria-label="按存活状态过滤"]').setValue('false')
    expect(wrapper.findAll('[data-testid="node-card"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="node-card"]').text()).toContain('日本 Tokyo 02')
    await wrapper.get('.node-filter-summary button').trigger('click')
    expect(wrapper.findAll('[data-testid="node-card"]')).toHaveLength(2)
    wrapper.unmount()
  })

  it('gives product events priority and keeps log filtering understandable', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.overview = sampleOverview()
    data.providers = [{ stable_id: 'provider-1', name: '主力订阅', enabled: true }]
    data.nodes = [{ id: 'node-1', name: '香港 HKG 01', proxy_name: 'HKG 01' }]
    data.events = [{ time: '2026-08-26T10:00:00Z', level: 'info', message: '网关已启动，加载 1 个节点' }]
    realtime.applyPayload('logs', { time: '10:00:00', level: 'info', message: 'provider ready' })
    realtime.applyPayload('logs', { time: '10:00:01', level: 'warn', message: '主力订阅 HKG 01 latency high' })
    realtime.applyPayload('logs', { time: '10:00:02', level: 'error', message: 'connection failed' })
    const wrapper = mount(LogsView, { global: { plugins: [pinia] } })

    expect(wrapper.get('h1').text()).toBe('日志')
    expect(wrapper.get('[data-testid="log-summary"]').text()).toContain('3 条运行日志')
    expect(wrapper.get('[data-testid="log-summary"]').text()).toContain('1 条错误')
    expect(wrapper.get('[data-testid="log-summary"]').text()).toContain('1 条警告')
    expect(wrapper.text()).toContain('最近事件')
    expect(wrapper.text()).toContain('网关已启动，加载 1 个节点')
    expect(wrapper.text()).not.toContain('结构化日志与产品事件')

    await wrapper.get('select[aria-label="按日志级别过滤"]').setValue('warning')
    expect(wrapper.findAll('[data-testid="log-row"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="log-row"]').text()).toContain('警告')
    await wrapper.get('select[aria-label="按 Provider 过滤"]').setValue('provider-1')
    expect(wrapper.get('[data-testid="log-row"]').text()).toContain('主力订阅')
    await wrapper.get('.log-filter-summary button').trigger('click')
    expect(wrapper.findAll('[data-testid="log-row"]')).toHaveLength(3)

    await wrapper.findAll('button').find((button) => button.text() === '清空当前日志').trigger('click')
    expect(realtime.logs).toHaveLength(0)
    expect(wrapper.text()).toContain('等待运行日志')
    wrapper.unmount()
  })

  it('keeps an existing log row and text selection when a new log arrives', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.overview = sampleOverview()
    data.events = []
    realtime.applyPayload('logs', { time: '10:00:00', level: 'info', message: 'first stable log' })
    const wrapper = mount(LogsView, { attachTo: document.body, global: { plugins: [pinia] } })
    const oldRow = wrapper.get('[data-testid="log-row"]').element
    const message = oldRow.querySelector('.log-row-content p')
    const range = document.createRange()
    range.selectNodeContents(message)
    const selection = window.getSelection()
    selection.removeAllRanges()
    selection.addRange(range)
    const selectedText = selection.toString()

    realtime.applyPayload('logs', { time: '10:00:01', level: 'info', message: 'second log' })
    await nextTick()

    const retained = wrapper.findAll('[data-testid="log-row"]').find((row) => row.text().includes('first stable log'))
    expect(retained.element).toBe(oldRow)
    expect(window.getSelection().toString()).toBe(selectedText)
    wrapper.unmount()
  })

  it('preserves the viewed log position and offers a return to the latest entry', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.overview = sampleOverview()
    data.events = []
    realtime.applyPayload('logs', { time: '10:00:00', level: 'info', message: 'first visible log' })
    const wrapper = mount(LogsView, { global: { plugins: [pinia] } })
    const viewport = wrapper.get('[data-testid="log-stream"]').element
    Object.defineProperty(viewport, 'scrollHeight', { configurable: true, get: () => wrapper.findAll('[data-testid="log-row"]').length * 40 })
    viewport.scrollTop = 20

    realtime.applyPayload('logs', { time: '10:00:01', level: 'info', message: 'new unseen log' })
    await nextTick()
    await vi.waitFor(() => expect(wrapper.find('.log-new-indicator').exists()).toBe(true))

    expect(viewport.scrollTop).toBe(60)
    expect(wrapper.get('.log-new-indicator').text()).toContain('1 条新日志')
    await wrapper.get('.log-new-indicator').trigger('click')
    expect(viewport.scrollTop).toBe(0)
    expect(wrapper.find('.log-new-indicator').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('route lifecycle and request ownership', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('clears the browser Selection after navigation', async () => {
    const removeAllRanges = vi.spyOn(window.getSelection(), 'removeAllRanges')
    await appRouter.push('/')
    removeAllRanges.mockClear()
    await appRouter.push('/providers')
    expect(removeAllRanges).toHaveBeenCalled()
  })

  it('does not clear Selection or scroll when navigation is cancelled', () => {
    const removeAllRanges = vi.spyOn(window.getSelection(), 'removeAllRanges')
    finalizeNavigation({ type: 4 })
    expect(removeAllRanges).not.toHaveBeenCalled()
    expect(window.scrollTo).not.toHaveBeenCalled()
  })

  it('refreshes only Overview-owned modules in the background', async () => {
    const data = useDataStore()
    data.loaded.overview = true
    const fetchMock = vi.fn(async (path) => ({
      ok: true,
      status: 200,
      json: async () => path === '/api/overview' ? sampleOverview() : [],
    }))
    vi.stubGlobal('fetch', fetchMock)
    await data.refreshRoute('overview', { background: true })
    expect(backgroundResources.overview).toEqual(['overview', 'providers', 'nodes'])
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(['/api/overview', '/api/providers', '/api/nodes'])
  })

  it('blocks background refresh policy while hidden or paused', () => {
    expect(shouldRefreshInBackground(false, false)).toBe(true)
    expect(shouldRefreshInBackground(true, false)).toBe(false)
    expect(shouldRefreshInBackground(false, true)).toBe(false)
  })

  it('discards queued low-frequency payloads when paused', () => {
    vi.useFakeTimers()
    const realtime = useRealtimeStore()
    realtime.setReduced(true)
    realtime.queuePayload('traffic', { up: 100, down: 200 })
    realtime.setPaused(true)
    vi.advanceTimersByTime(2000)
    realtime.setReduced(false)
    expect(realtime.traffic).toEqual({ up: 0, down: 0 })
    realtime.stop()
    vi.useRealTimers()
  })

  it('requests only resources owned by the active route', async () => {
    const data = useDataStore()
    data.loaded.overview = true
    vi.stubGlobal('fetch', vi.fn(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/nodes' ? [] : {} })))
    await data.refreshRoute('nodes')
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith('/api/nodes', expect.any(Object))
    expect(routeResources.nodes).toEqual(['nodes'])
  })

  it('opens and closes WebSockets by route', () => {
    const opened = []
    class FakeWebSocket {
      constructor(url) { this.url = url; this.readyState = 0; this.closed = false; opened.push(this) }
      close() { this.readyState = 3; this.closed = true }
    }
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const realtime = useRealtimeStore()
    realtime.sync('logs')
    expect(opened.map((socket) => socket.url)).toEqual(['ws://localhost:3000/api/mihomo/logs?level=info'])
    realtime.sync('providers')
    expect(opened[0].closed).toBe(true)
    expect(routeStreams.providers).toEqual([])
    realtime.stop()
  })
})
