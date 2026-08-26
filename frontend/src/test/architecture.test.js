import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OverviewView from '@/views/OverviewView.vue'
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
    wrapper.unmount()
  })

  it('keeps a dirty Settings draft across unrelated store updates', async () => {
    const data = useDataStore()
    const realtime = useRealtimeStore()
    data.settings = { mode: 'local', http_bind: '127.0.0.1:9090', socks_bind: '127.0.0.1', socks_port: 1080, virtual_host: 'surge.eb', prefix_provider: false, node_test_url: 'https://example.com', node_test_udp_address: '1.1.1.1:53', node_test_timeout_seconds: 10 }
    data.service = {}
    const router = testRouter(SettingsView, 'settings')
    await router.push('/')
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } })
    const deploymentModes = wrapper.get('select').findAll('option').map((option) => [option.attributes('value'), option.text()])
    expect(deploymentModes).toEqual([['local', '仅本机'], ['gateway', '局域网网关']])
    expect(wrapper.get('input[placeholder="surge.eb"]').element.value).toBe('surge.eb')
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
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    expect(wrapper.emitted('close')).toHaveLength(1)
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
