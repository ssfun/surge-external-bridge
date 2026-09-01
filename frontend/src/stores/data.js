import { defineStore } from 'pinia'
import { api, encodeID, login } from '@/api.js'

const resourcePaths = {
  overview: '/api/overview',
  providers: '/api/providers',
  nodes: '/api/nodes',
  events: '/api/events',
  settings: '/api/settings',
  service: '/api/service',
}

export const routeResources = {
  overview: ['overview', 'providers', 'nodes'],
  providers: ['providers', 'nodes'],
  nodes: ['nodes'],
  connections: [],
  logs: ['events', 'providers', 'nodes'],
  settings: ['settings', 'service'],
}

export const backgroundResources = {
  overview: ['overview', 'providers', 'nodes'],
  providers: ['providers', 'nodes'],
  nodes: ['nodes'],
  connections: [],
  logs: ['events'],
  settings: [],
}

const requests = new Map()
const snapshots = new Map()
let requestGeneration = 0

export const useDataStore = defineStore('data', {
  state: () => ({
    overview: null,
    providers: [],
    nodes: [],
    events: [],
    settings: null,
    service: null,
    loaded: {},
    loadedAt: {},
    initialError: '',
  }),
  actions: {
    resetSession() {
      requestGeneration += 1
      requests.clear()
      snapshots.clear()
      this.$reset()
    },
    async loadResource(name, { background = false } = {}) {
      if (background && Date.now() - (this.loadedAt[name] || 0) < 5000) return false
      if (requests.has(name)) return requests.get(name)
      const generation = requestGeneration
      const request = (async () => {
        let value
        try {
          value = await api(resourcePaths[name])
        } catch (error) {
          if (generation !== requestGeneration) return false
          if (name === 'service') value = { error: error.message || '服务状态检测失败' }
          else throw error
        }
        if (generation !== requestGeneration) return false
        const snapshot = JSON.stringify(value)
        const changed = snapshots.get(name) !== snapshot
        snapshots.set(name, snapshot)
        this.loaded[name] = true
        this.loadedAt[name] = Date.now()
        if (!changed) return false
        if (name === 'providers') {
          const runtime = new Map(this.providers.map((provider) => [provider.stable_id, {
            runtime: provider.runtime,
            runtimeError: provider.runtimeError,
          }]))
          value = value.map((provider) => ({ ...provider, ...runtime.get(provider.stable_id) }))
        }
        this[name] = value
        return true
      })().finally(() => { if (requests.get(name) === request) requests.delete(name) })
      requests.set(name, request)
      return request
    },
    async refreshRoute(routeName, { background = false } = {}) {
      const names = [...(background ? backgroundResources[routeName] || [] : routeResources[routeName] || [])]
      if (!this.loaded.overview && !names.includes('overview')) names.unshift('overview')
      const results = await Promise.allSettled(names.map((name) => this.loadResource(name, { background })))
      const failure = results.find((result) => result.status === 'rejected')
      if (failure) throw failure.reason
    },
    async reloadResources(names) {
      await Promise.all(names.map(async (name) => {
        if (requests.has(name)) await requests.get(name).catch(() => {})
        return this.loadResource(name)
      }))
    },
    async loadProviderRuntime(id, { quiet = false } = {}) {
      const key = `provider-runtime:${id}`
      if (requests.has(key)) return requests.get(key)
      const generation = requestGeneration
      const request = api(`/api/providers/${encodeID(id)}/runtime`).then((runtime) => {
        if (generation !== requestGeneration) return
        const provider = this.providers.find((item) => item.stable_id === id)
        if (!provider) return
        provider.runtime = runtime
        provider.runtimeError = ''
      }).catch((error) => {
        if (generation !== requestGeneration) return
        const provider = this.providers.find((item) => item.stable_id === id)
        if (provider) provider.runtimeError = error.message
        if (!quiet) throw error
      }).finally(() => { if (requests.get(key) === request) requests.delete(key) })
      requests.set(key, request)
      return request
    },
    async saveProvider(provider, id = '', file = null) {
      const body = file ? new FormData() : JSON.stringify(provider)
      if (file) {
        body.append('provider', JSON.stringify(provider))
        body.append('file', file)
      }
      await api(id ? `/api/providers/${encodeID(id)}` : '/api/providers', {
        method: id ? 'PUT' : 'POST',
        body,
      })
      await this.reloadResources(['providers', 'nodes', 'overview'])
    },
    async deleteProvider(id) {
      await api(`/api/providers/${encodeID(id)}`, { method: 'DELETE' })
      await this.reloadResources(['providers', 'nodes', 'overview'])
    },
    async reorderProviders(providerIDs) {
      await api('/api/providers/order', {
        method: 'PUT',
        body: JSON.stringify({ provider_ids: providerIDs }),
      })
      await this.reloadResources(['providers', 'nodes', 'overview'])
    },
    async refreshProvider(id) {
      await api(`/api/providers/${encodeID(id)}/refresh`, { method: 'POST' })
      await this.reloadResources(['providers', 'nodes', 'overview'])
      await this.loadProviderRuntime(id, { quiet: true })
    },
    async healthCheckProvider(id) {
      await api(`/api/providers/${encodeID(id)}/healthcheck`, { method: 'POST' })
      await this.reloadResources(['providers', 'nodes', 'overview'])
      await this.loadProviderRuntime(id, { quiet: true })
    },
    async updateSettings(body) {
      const result = await api('/api/settings', { method: 'PUT', body: JSON.stringify(body) })
      if (body.management_token && !result.reconnect) await login(body.management_token)
      if (!result.reconnect) await this.reloadResources(['settings', 'overview', 'providers', 'nodes'])
      return result
    },
  },
})
