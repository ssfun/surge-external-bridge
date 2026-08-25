import { defineStore } from 'pinia'
import { api, encodeID } from '@/api.js'

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
    async loadResource(name, { background = false } = {}) {
      if (background && Date.now() - (this.loadedAt[name] || 0) < 5000) return false
      if (requests.has(name)) return requests.get(name)
      const request = (async () => {
        let value
        try {
          value = await api(resourcePaths[name])
        } catch (error) {
          if (name === 'service') value = null
          else throw error
        }
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
      })().finally(() => requests.delete(name))
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
      const request = api(`/api/providers/${encodeID(id)}/runtime`).then((runtime) => {
        const provider = this.providers.find((item) => item.stable_id === id)
        if (!provider) return
        provider.runtime = runtime
        provider.runtimeError = ''
      }).catch((error) => {
        const provider = this.providers.find((item) => item.stable_id === id)
        if (provider) provider.runtimeError = error.message
        if (!quiet) throw error
      }).finally(() => requests.delete(key))
      requests.set(key, request)
      return request
    },
    async saveProvider(provider, id = '') {
      await api(id ? `/api/providers/${encodeID(id)}` : '/api/providers', {
        method: id ? 'PUT' : 'POST',
        body: JSON.stringify(provider),
      })
      await this.reloadResources(['providers', 'nodes', 'overview'])
    },
    async deleteProvider(id) {
      await api(`/api/providers/${encodeID(id)}`, { method: 'DELETE' })
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
      if (body.management_token) localStorage.setItem('surgeeb-management-token', body.management_token)
      if (!result.reconnect) await this.reloadResources(['settings', 'overview', 'providers', 'nodes'])
      return result
    },
  },
})
