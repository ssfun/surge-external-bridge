import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { expect, it, vi } from 'vitest'
import NodesView from '@/views/NodesView.vue'
import { useDataStore } from '@/stores/data.js'

async function setupNodes() {
  const pinia = createPinia()
  setActivePinia(pinia)
  useDataStore().nodes = [
    { id: 'hk', name: '香港 01', provider_name: '主力订阅', type: 'vless', alive: true, udp: true, history: [{ delay: 42 }] },
    { id: 'jp', name: '日本 02', provider_name: '备用订阅', type: 'trojan', alive: false, history: [] },
  ]
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: NodesView }] })
  await router.push('/')
  return () => mount(NodesView, { global: { plugins: [pinia, router] } })
}

it('switches node layouts without losing filters, diagnostic results or node actions', async () => {
  const render = await setupNodes()
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ tcp: { success: true, latency_ms: 48 }, udp: { success: false, detail: 'UDP timeout' } }) })))
  const wrapper = render()
  const grid = wrapper.get('button[aria-label="网格卡片"]')
  const list = wrapper.get('button[aria-label="列表卡片"]')
  expect(grid.attributes('aria-pressed')).toBe('true')
  await wrapper.get('input[aria-label="按节点名称过滤"]').setValue('香港')
  await list.trigger('click')
  expect(list.attributes('aria-pressed')).toBe('true')
  expect(grid.attributes('aria-pressed')).toBe('false')
  expect(wrapper.findAll('article')).toHaveLength(1)
  expect(wrapper.get('article').text()).toContain('42 ms')
  await wrapper.get('article').findAll('button').find(button => button.text() === '端到端诊断').trigger('click')
  await flushPromises()
  expect(fetch).toHaveBeenCalledWith('/api/nodes/hk/diagnose', expect.objectContaining({ method: 'POST' }))
  expect(wrapper.get('article').text()).toContain('TCP 通过')
  await grid.trigger('click')
  expect(wrapper.get('input[aria-label="按节点名称过滤"]').element.value).toBe('香港')
  expect(wrapper.get('article').text()).toContain('UDP timeout')
  await list.trigger('click')
  await wrapper.get('.node-more').trigger('click')
  expect(wrapper.findAll('[role="menuitem"]').map((button) => button.text())).toEqual(['节点 DNS 诊断', '复制 Surge 节点行'])
  wrapper.unmount()
})

it('remembers the selected node layout when the page is reopened', async () => {
  const render = await setupNodes()
  let wrapper = render()
  await wrapper.get('button[aria-label="列表卡片"]').trigger('click')
  wrapper.unmount()
  wrapper = render()
  expect(wrapper.get('button[aria-label="列表卡片"]').attributes('aria-pressed')).toBe('true')
  await wrapper.get('button[aria-label="网格卡片"]').trigger('click')
  wrapper.unmount()
  wrapper = render()
  expect(wrapper.get('button[aria-label="网格卡片"]').attributes('aria-pressed')).toBe('true')
  wrapper.unmount()
})
