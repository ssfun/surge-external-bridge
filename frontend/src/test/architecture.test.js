import { createPinia, setActivePinia } from 'pinia'
import { DOMWrapper, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import OverviewView from '@/views/OverviewView.vue'
import ProvidersView from '@/views/ProvidersView.vue'
import SettingsView from '@/views/SettingsView.vue'
import LogsView from '@/views/LogsView.vue'
import ProviderDialog from '@/components/ProviderDialog.vue'
import appRouter, { finalizeNavigation } from '@/router.js'
import { backgroundResources, routeResources, useDataStore } from '@/stores/data.js'
import { routeStreams, useRealtimeStore } from '@/stores/realtime.js'
import { shouldRefreshInBackground } from '@/refreshPolicy.js'

function sampleOverview() {
  return { version: '0.3.1', core_version: 'test', provider_count: 1, projection_count: 1, policy_url: 'http://127.0.0.1/proxies', process_rule: 'PROCESS-NAME,SurgeEB,DIRECT', gateway: { state: 'running', socks_address: '127.0.0.1:1080', projection_hash: 'abcdef', projection_count: 1 } }
}

function testRouter(component, name = 'overview') {
  return createRouter({ history: createMemoryHistory(), routes: [{ path: '/', name, component }] })
}

describe('component update boundaries', () => {
  beforeEach(() => setActivePinia(createPinia()))

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
      json: async () => path === '/api/overview' ? sampleOverview() : [],
    })))
    const wrapper = mount(App, { global: { plugins: [pinia, router] } })
    expect(wrapper.get('[data-testid="brand-title"]').text()).toBe('SurgeEB')
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
    data.settings = { mode: 'local', http_bind: '127.0.0.1:9090', socks_bind: '127.0.0.1', socks_port: 1080, virtual_host: 'surge.eb', projection_key: 'shared-projection-key-for-devices', projection_hash: 'abcdef', projection_count: 3, prefix_provider: false, node_test_url: 'https://example.com', node_test_udp_address: '1.1.1.1:53', node_test_timeout_seconds: 10 }
    data.service = {}
    const router = testRouter(SettingsView, 'settings')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } })
    const deploymentModes = wrapper.get('select').findAll('option').map((option) => [option.attributes('value'), option.text()])
    expect(deploymentModes).toEqual([['local', '仅本机'], ['gateway', '局域网网关']])
    expect(wrapper.get('input[placeholder="surge.eb"]').element.value).toBe('surge.eb')
    expect(wrapper.get('[data-testid="projection-key"]').element.value).toBe('shared-projection-key-for-devices')
    expect(wrapper.text()).toContain('可用节点3 个')
    expect(wrapper.text()).not.toContain('abcdef')
    const input = wrapper.find('input[spellcheck="false"]')
    await input.setValue('127.0.0.1:9191')
    realtime.memory = { inuse: 123456 }
    data.overview = sampleOverview()
    await nextTick()
    expect(input.element.value).toBe('127.0.0.1:9191')
    expect(wrapper.text()).toContain('有未保存的设置')
    wrapper.unmount()
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
    expect(field('[data-testid="provider-file-path"]').attributes()).toHaveProperty('required')
    expect(field('[data-testid="provider-http-options"]').isVisible()).toBe(false)
    wrapper.unmount()
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
        runtime: { proxies: [
          { name: '香港 01', type: 'Vless', alive: true, history: [{ delay: 42 }] },
          { name: '新加坡 02', type: 'Trojan', alive: false, history: [] },
        ] },
      },
    ]
    data.nodes = [{ id: 'node-1', provider_id: 'inline-on' }]
    const router = testRouter(ProvidersView, 'providers')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [pinia, router] } })
    const http = wrapper.get('[data-provider-id="http-off"]')
    const inline = wrapper.get('[data-provider-id="inline-on"]')

    expect(wrapper.get('[data-testid="provider-summary"]').text()).toContain('1 / 2 已启用')
    expect(http.text()).toContain('6 小时')
    expect(http.text()).toContain('筛选包含 香港 · 排除 过期')
    expect(http.text()).not.toContain('最近更新 —')
    expect(http.text()).not.toContain('暂无订阅流量信息')
    expect(http.findAll('button').map((button) => button.text())).toEqual(['编辑', '更多'])

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
    const oldRow = wrapper.get('.event').element
    const message = oldRow.querySelector('span:last-child')
    const range = document.createRange()
    range.selectNodeContents(message)
    const selection = window.getSelection()
    selection.removeAllRanges()
    selection.addRange(range)
    const selectedText = selection.toString()

    realtime.applyPayload('logs', { time: '10:00:01', level: 'info', message: 'second log' })
    await nextTick()

    const retained = wrapper.findAll('.event').find((row) => row.text().includes('first stable log'))
    expect(retained.element).toBe(oldRow)
    expect(window.getSelection().toString()).toBe(selectedText)
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
