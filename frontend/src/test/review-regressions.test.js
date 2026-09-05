import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { latestHealth } from '@/utils.js'
import { api, authState } from '@/api.js'
import LiveControls from '@/components/LiveControls.vue'
import PolicyPathsView from '@/views/PolicyPathsView.vue'
import NodesView from '@/views/NodesView.vue'

const response = (value, status = 200) => ({ ok: status < 400, status, json: async () => value })

beforeEach(() => {
  setActivePinia(createPinia())
  useDataStore().resetSession()
})
afterEach(() => {
  useRealtimeStore().resetSession()
  vi.useRealTimers()
})

describe('confirmed writes and read recovery', () => {
  it('times out an unresponsive read without leaving a permanently pending refresh', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_, options) => new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new Error('aborted')))
    })))
    const request = expect(api('/api/overview')).rejects.toThrow('读取超时')
    await vi.advanceTimersByTimeAsync(15000)
    await request
  })

  it('keeps a token change committed even if establishing the new session fails', async () => {
    authState.authenticated = true
    vi.stubGlobal('fetch', vi.fn(async (path) => {
      if (path === '/api/settings') return response({ reconnect: false })
      throw new Error('session disconnected')
    }))
    const result = await useDataStore().updateSettings({ management_token: 'new-management-token' })
    expect(result.sessionExpired).toBe(true)
    expect(result.refreshed).toBe(false)
    expect(authState.authenticated).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('keeps a successful save successful and retries only failed reads', async () => {
    let failing = true
    const fetchMock = vi.fn(async (path, options) => {
      if (options.method === 'POST') return response({ stable_id: 'saved' }, 201)
      if (path === '/api/providers' && failing) throw new Error('read disconnected')
      return response(path === '/api/overview' ? {} : [])
    })
    vi.stubGlobal('fetch', fetchMock)
    const data = useDataStore()
    await expect(data.saveProvider({ name: 'saved' })).resolves.toBeUndefined()
    expect(data.pendingRefresh).toEqual(['providers'])
    failing = false
    await data.retryPendingRefresh()
    expect(data.pendingRefresh).toEqual([])
    expect(fetchMock.mock.calls.filter(([, options]) => options.method === 'POST')).toHaveLength(1)
  })

  it('still rejects a failed write without scheduling read retries', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ error: 'invalid source' }, 400)))
    const data = useDataStore()
    await expect(data.saveProvider({})).rejects.toThrow('invalid source')
    expect(data.pendingRefresh).toEqual([])
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('checks shared gateway status on the connections page and clears a recovered error', async () => {
    const data = useDataStore()
    vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('offline') }))
    await expect(data.refreshRoute('connections')).rejects.toThrow('offline')
    expect(data.resourceErrors.overview).toBe('offline')
    vi.stubGlobal('fetch', vi.fn(async () => response({ gateway: { state: 'running' } })))
    await data.refreshRoute('connections')
    expect(data.resourceErrors.overview).toBeUndefined()
  })
})

describe('stream lifecycle', () => {
  function sockets() {
    const opened = []
    vi.stubGlobal('WebSocket', class {
      constructor(url) { this.url = url; this.readyState = 0; opened.push(this) }
      close() { this.readyState = 3; this.onclose?.() }
    })
    return opened
  }
  it('labels lost streams and recovers without discarding the last received data', async () => {
    vi.useFakeTimers()
    const opened = sockets()
    const realtime = useRealtimeStore()
    realtime.sync('connections')
    const wrapper = mount(LiveControls)
    expect(wrapper.text()).toContain('连接中')
    for (const socket of opened) { socket.readyState = 1; socket.onopen() }
    opened[1].onmessage({ data: JSON.stringify({ up: 42, down: 0 }) })
    opened[1].close()
    await nextTick()
    expect(wrapper.text()).toContain('连接中断')
    expect(wrapper.text()).toContain('最后更新')
    expect(realtime.traffic.up).toBe(42)
    vi.advanceTimersByTime(3000)
    const replacement = opened.at(-1)
    replacement.readyState = 1
    replacement.onopen()
    await nextTick()
    expect(realtime.streamStatus).toBe('connected')
    wrapper.unmount()
  })

  it('ignores late callbacks from closed routes and clears data at logout', () => {
    vi.useFakeTimers()
    const opened = sockets()
    const realtime = useRealtimeStore()
    realtime.sync('logs')
    const old = opened[0]
    realtime.sync('connections')
    old.onmessage({ data: JSON.stringify({ message: 'old session' }) })
    old.onclose()
    vi.advanceTimersByTime(3000)
    expect(opened).toHaveLength(4)
    expect(realtime.logs).toEqual([])
    realtime.applyPayload('traffic', { up: 999, down: 0 })
    realtime.resetSession()
    expect(realtime.traffic.up).toBe(0)
    vi.advanceTimersByTime(6000)
    expect(opened).toHaveLength(4)
  })
})

describe('health and dialog UX', () => {
  it('shows the latest failed result instead of an older successful latency', async () => {
    const data = useDataStore()
    const history = [{ delay: 42, time: '2026-09-05T10:00:00Z' }, { delay: 0, time: '2026-09-05T10:01:00Z' }]
    expect(latestHealth(history).label).toBe('测速失败')
    expect(latestHealth([]).label).toBe('尚未测速')
    data.nodes = [{ id: 'n', name: 'Test', alive: false, history }]
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: NodesView }] })
    await router.push('/')
    const wrapper = mount(NodesView, { global: { plugins: [router] } })
    expect(wrapper.get('.node-card-metrics').text()).toContain('最近测速测速失败')
    expect(wrapper.get('.node-card-metrics').text()).toContain('历史 42 ms')
    wrapper.unmount()
  })

  it('moves focus into Policy Path editing, traps Tab, and restores focus on Escape', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: PolicyPathsView }] })
    await router.push('/')
    const wrapper = mount(PolicyPathsView, { attachTo: document.body, global: { plugins: [router] } })
    const opener = wrapper.findAll('button').find((button) => button.text() === '添加 Policy Path')
    opener.element.focus()
    await opener.trigger('click')
    await nextTick()
    expect(document.activeElement).toBe(wrapper.get('[data-testid="policy-path-name"]').element)
    const first = wrapper.get('.modal-close').element
    first.focus()
    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement.textContent).toBe('取消')
    document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await nextTick()
    await nextTick()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(opener.element)
    wrapper.unmount()
  })
})
